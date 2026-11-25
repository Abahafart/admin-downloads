//go:build live

package netx

// Live tests against real services. Run with:
//
//	ADESC_TOR=1 go test -tags live -run Live ./internal/netx/            (tor on 127.0.0.1:9050/9051)
//	ADESC_TOR_CONTROL_PASSWORD=... (optional, HashedControlPassword)
//	ADESC_WG_CONF=/path/to/provider.conf go test -tags live -run LiveWireGuard ./internal/netx/
//	ADESC_UPNP=1 go test -tags live -run LiveRouter ./internal/netx/    (reconnects your router!)

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveTorNewnymChangesIP(t *testing.T) {
	if os.Getenv("ADESC_TOR") == "" {
		t.Skip("set ADESC_TOR=1 with a running tor")
	}
	tor, err := NewTor(context.Background(), TorOptions{ControlPassword: os.Getenv("ADESC_TOR_CONTROL_PASSWORD"), Circuits: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer tor.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := tor.Circuits()[0]
	before := CheckHealth(ctx, c, nil)
	if !before.Healthy {
		t.Fatalf("tor circuit: %+v", before)
	}
	if err := tor.NewIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	after := CheckHealth(ctx, c, nil)
	t.Logf("exit before %s after %s", before.PublicIP, after.PublicIP)
	if !after.Healthy || after.PublicIP == before.PublicIP {
		t.Fatalf("IP did not change: %+v", after)
	}
}

func TestLiveWireGuard(t *testing.T) {
	path := os.Getenv("ADESC_WG_CONF")
	if path == "" {
		t.Skip("set ADESC_WG_CONF to a wg-quick .conf")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseWGConfig(string(b))
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWireGuard("wireguard:live", c, WireGuardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	direct := CheckHealth(ctx, NewDirect(), nil)
	h := CheckHealth(ctx, w, nil)
	t.Logf("direct %s, tunnel %s", direct.PublicIP, h.PublicIP)
	if !h.Healthy || h.PublicIP == direct.PublicIP {
		t.Fatalf("tunnel health %+v", h)
	}
}

func TestLiveRouterUPnP(t *testing.T) {
	if os.Getenv("ADESC_UPNP") == "" {
		t.Skip("set ADESC_UPNP=1 to reconnect the router")
	}
	r, err := NewRouter(RouterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := NewHTTPClient(NewDirect(), nil)
	old, _ := ConfirmedPublicIP(ctx, c, nil)
	if err := r.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	ip, err := WaitForIPChange(ctx, c, nil, old, 2*time.Minute, 5*time.Second)
	t.Logf("old %s new %s err %v", old, ip, err)
	if err != nil {
		t.Fatal(err)
	}
}
