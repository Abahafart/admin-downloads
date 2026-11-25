package netx

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func genKey(t *testing.T) WGPrivateKey {
	t.Helper()
	var k WGPrivateKey
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	return k
}

const sampleConf = `# Mullvad-style config
[Interface]
PrivateKey = %s
Address = 10.66.0.2/32, fd00::2/128
DNS = 10.66.0.1, example.lan
MTU = 1380
PostUp = iptables -A FORWARD -i %%i -j ACCEPT ; never executed
Table = off

[Peer]
PublicKey = %s
PresharedKey = %s
Endpoint = %s
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`

func TestParseWGConfig(t *testing.T) {
	priv, peer, psk := genKey(t), genKey(t), genKey(t)
	text := fmt.Sprintf(sampleConf, WGKey(priv).Base64(), peer.PublicKey().Base64(), WGKey(psk).Base64(), "vpn.example:51820")
	c, err := ParseWGConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	if c.PrivateKey != priv || c.MTU != 1380 || len(c.Addresses) != 2 || c.Addresses[0].String() != "10.66.0.2/32" {
		t.Fatalf("interface = %+v", c)
	}
	if len(c.DNS) != 1 || c.DNS[0].String() != "10.66.0.1" {
		t.Fatalf("dns = %v", c.DNS)
	}
	p := c.Peers[0]
	if p.PublicKey != peer.PublicKey() || p.PresharedKey != psk || p.Endpoint != "vpn.example:51820" || len(p.AllowedIPs) != 2 || p.PersistentKeepalive != 25 {
		t.Fatalf("peer = %+v", p)
	}
	if s := fmt.Sprintf("%v %+v %#v", c.PrivateKey, c, p.PresharedKey); strings.Contains(s, WGKey(priv).Base64()) || strings.Contains(s, WGKey(psk).hex()) {
		t.Fatal("keys printed")
	}

	res := resolverFunc(func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if host != "vpn.example" {
			return nil, errors.New("unexpected host")
		}
		return []netip.Addr{netip.MustParseAddr("2001:db8::9"), netip.MustParseAddr("198.51.100.4")}, nil
	})
	ipc, err := c.IpcConfig(context.Background(), res)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"private_key=" + WGKey(priv).hex() + "\n",
		"public_key=" + peer.PublicKey().hex() + "\n",
		"preshared_key=" + WGKey(psk).hex() + "\n",
		"endpoint=198.51.100.4:51820\n",
		"persistent_keepalive_interval=25\n",
		"allowed_ip=0.0.0.0/0\n",
		"allowed_ip=::/0\n",
		"replace_peers=true\n",
	} {
		if !strings.Contains(ipc, want) {
			t.Errorf("IpcConfig lacks %q:\n%s", want, ipc)
		}
	}
}

type resolverFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestParseWGConfigErrors(t *testing.T) {
	k := WGKey(genKey(t)).Base64()
	tests := []struct{ name, text, want string }{
		{"no private key", "[Interface]\nAddress=10.0.0.2/32\n[Peer]\nPublicKey=" + k + "\nAllowedIPs=0.0.0.0/0\nEndpoint=1.2.3.4:1\n", "PrivateKey"},
		{"no address", "[Interface]\nPrivateKey=" + k + "\n[Peer]\nPublicKey=" + k + "\nAllowedIPs=0.0.0.0/0\nEndpoint=1.2.3.4:1\n", "Address"},
		{"no peer", "[Interface]\nPrivateKey=" + k + "\nAddress=10.0.0.2\n", "[Peer]"},
		{"no endpoint", "[Interface]\nPrivateKey=" + k + "\nAddress=10.0.0.2\n[Peer]\nPublicKey=" + k + "\nAllowedIPs=0.0.0.0/0\n", "Endpoint"},
		{"bad key", "[Interface]\nPrivateKey=abc\n", "invalid key"},
		{"bad section", "[Foo]\n", "unknown section"},
		{"outside", "PrivateKey=" + k + "\n", "outside"},
		{"unknown key", "[Interface]\nBogus=1\n", "unknown key"},
		{"bad endpoint", "[Interface]\nPrivateKey=" + k + "\nAddress=10.0.0.2\n[Peer]\nPublicKey=" + k + "\nEndpoint=nohost\n", "host:port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseWGConfig(tt.text)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), k) {
				t.Fatal("error leaks a key")
			}
		})
	}
}

// wgServer is the "provider side": a netstack WireGuard device listening on
// loopback UDP that serves HTTP (and DNS) inside its tunnel.
type wgServer struct {
	dev      *device.Device
	endpoint string
	pub      WGKey
}

func startWGServer(t *testing.T, addr string, clientPub WGKey) *wgServer {
	t.Helper()
	srvKey := genKey(t)
	local := netip.MustParseAddr(addr)
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{local}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	dev := device.NewDevice(tdev, conn.NewStdNetBind(), device.NewLogger(device.LogLevelSilent, ""))
	ipc := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=10.66.0.0/24\n", WGKey(srvKey).hex(), clientPub.hex())
	if err := dev.IpcSet(ipc); err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dev.Close)
	got, err := dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	port := ""
	for _, l := range strings.Split(got, "\n") {
		if v, ok := strings.CutPrefix(l, "listen_port="); ok {
			port = v
		}
	}
	if port == "" || port == "0" {
		t.Fatalf("no listen port in %q", got)
	}

	ln, err := tnet.ListenTCP(&net.TCPAddr{IP: net.ParseIP(addr), Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.URL.Path == "/ip" {
			fmt.Fprint(w, host)
			return
		}
		fmt.Fprintf(w, "inside %s for %s", addr, r.Host)
	}), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	udp, err := tnet.ListenUDP(&net.UDPAddr{IP: net.ParseIP(addr), Port: 53})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close() })
	go serveDNS(udp, local)
	return &wgServer{dev: dev, endpoint: "127.0.0.1:" + port, pub: srvKey.PublicKey()}
}

// serveDNS answers every A question with ip.
func serveDNS(c net.PacketConn, ip netip.Addr) {
	buf := make([]byte, 1500)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil {
			continue
		}
		q, err := p.Question()
		if err != nil {
			continue
		}
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
		b.EnableCompression()
		_ = b.StartQuestions()
		_ = b.Question(q)
		_ = b.StartAnswers()
		if q.Type == dnsmessage.TypeA {
			_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: ip.As4()})
		}
		out, err := b.Finish()
		if err == nil {
			c.WriteTo(out, from)
		}
	}
}

func clientConf(priv WGPrivateKey, srv *wgServer, dns string) *WGConfig {
	c := &WGConfig{
		PrivateKey: priv,
		Addresses:  []netip.Prefix{netip.MustParsePrefix("10.66.0.2/32")},
		Peers: []WGPeer{{
			PublicKey:  srv.pub,
			Endpoint:   srv.endpoint,
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.66.0.0/24")},
		}},
	}
	if dns != "" {
		c.DNS = []netip.Addr{netip.MustParseAddr(dns)}
	}
	return c
}

func TestWireGuardEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("in-process WireGuard tunnel")
	}
	cliKey := genKey(t)
	srvA := startWGServer(t, "10.66.0.1", cliKey.PublicKey())
	srvB := startWGServer(t, "10.66.0.9", cliKey.PublicKey())

	confA := clientConf(cliKey, srvA, "10.66.0.1")
	confA.Name = "a"
	confB := clientConf(cliKey, srvB, "10.66.0.9")
	confB.Name = "b"
	g, err := NewWireGuardGroup("wg:test", "test", []*WGConfig{confA, confB}, 1, WireGuardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if len(g.Egresses()) != 1 {
		t.Fatalf("MaxSimultaneous=1 should give 1 tunnel, got %d", len(g.Egresses()))
	}
	w := g.Egresses()[0]
	if w.ID() != "wg:test:0" || w.Kind() != KindWireGuard || w.Label() != "test · a" {
		t.Fatalf("egress %s %s %q", w.ID(), w.Kind(), w.Label())
	}

	// Plain IP and a name resolved by the DNS server inside the tunnel.
	if got := get(t, w, "http://10.66.0.1/"); got != "inside 10.66.0.1 for 10.66.0.1" {
		t.Fatalf("body = %q", got)
	}
	if got := get(t, w, "http://files.hoster.test/"); got != "inside 10.66.0.1 for files.hoster.test" {
		t.Fatalf("body via tunnel DNS = %q", got)
	}
	h := CheckHealth(context.Background(), w, []string{"http://10.66.0.1/ip"})
	if !h.Healthy || h.PublicIP.String() != "10.66.0.2" {
		t.Fatalf("health = %+v", h)
	}

	// Rotate switches to conf b (the other provider server).
	if err := w.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Config().Name != "b" {
		t.Fatalf("after Rotate conf = %s", w.Config().Name)
	}
	if got := get(t, w, "http://files.hoster.test/"); got != "inside 10.66.0.9 for files.hoster.test" {
		t.Fatalf("body after rotate = %q", got)
	}
}

func TestWireGuardGroupRotation(t *testing.T) {
	k := genKey(t)
	mk := func(name string) *WGConfig {
		return &WGConfig{Name: name, PrivateKey: k, Addresses: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")},
			Peers: []WGPeer{{PublicKey: k.PublicKey(), Endpoint: "127.0.0.1:9", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}}}}
	}
	g, err := NewWireGuardGroup("wg:p", "p", []*WGConfig{mk("a"), mk("b"), mk("c")}, 2, WireGuardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := g.Egresses()
	if len(e) != 2 || e[0].Config().Name != "a" || e[1].Config().Name != "b" {
		t.Fatal("initial assignment")
	}
	if err := e[0].Rotate(context.Background()); err != nil || e[0].Config().Name != "c" {
		t.Fatalf("rotate 0: %v %s", err, e[0].Config().Name)
	}
	if err := e[1].Rotate(context.Background()); err != nil || e[1].Config().Name != "a" {
		t.Fatalf("rotate 1: %v %s", err, e[1].Config().Name)
	}
	names := map[string]bool{e[0].Config().Name: true, e[1].Config().Name: true}
	if len(names) != 2 {
		t.Fatal("two tunnels share a configuration")
	}

	full, _ := NewWireGuardGroup("wg:f", "f", []*WGConfig{mk("a"), mk("b")}, 0, WireGuardOptions{})
	if err := full.Egresses()[0].Rotate(context.Background()); !errors.Is(err, ErrRotateUnsupported) {
		t.Fatalf("err = %v, want ErrRotateUnsupported", err)
	}
}
