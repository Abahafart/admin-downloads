package netx

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	socks5 "github.com/things-go/go-socks5"
)

// fakeResolver records the names a SOCKS server is asked to resolve and
// maps every name to 127.0.0.1 (proving resolution happens proxy-side).
type fakeResolver struct {
	mu    sync.Mutex
	names []string
}

func (r *fakeResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	r.mu.Lock()
	r.names = append(r.names, name)
	r.mu.Unlock()
	return ctx, net.IPv4(127, 0, 0, 1), nil
}

func (r *fakeResolver) seen(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.names {
		if n == name {
			return true
		}
	}
	return false
}

// startSOCKS starts a go-socks5 server. creds nil = no auth. users receives
// the username of every authenticated request.
func startSOCKS(t *testing.T, creds socks5.CredentialStore, res socks5.NameResolver, users *[]string, mu *sync.Mutex) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	opts := []socks5.Option{socks5.WithLogger(nopLogger{})}
	if creds != nil {
		opts = append(opts, socks5.WithCredential(creds))
	}
	if res != nil {
		opts = append(opts, socks5.WithResolver(res))
	}
	if users != nil {
		opts = append(opts, socks5.WithDialAndRequest(func(ctx context.Context, network, addr string, req *socks5.Request) (net.Conn, error) {
			if req.AuthContext != nil {
				mu.Lock()
				*users = append(*users, req.AuthContext.Payload["username"])
				mu.Unlock()
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}))
	}
	srv := socks5.NewServer(opts...)
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

type nopLogger struct{}

func (nopLogger) Errorf(string, ...any) {}

// anyCreds accepts every username/password (like tor's SOCKS port).
type anyCreds struct{}

func (anyCreds) Valid(user, password, _ string) bool { return user != "" }

func hostServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello "+r.Host)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return srv, port
}

func get(t *testing.T, e Egress, url string) string {
	t.Helper()
	c := NewHTTPClient(e, nil)
	defer c.CloseIdleConnections()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestSOCKS5RemoteDNSAndAuth(t *testing.T) {
	res := &fakeResolver{}
	var users []string
	var mu sync.Mutex
	addr := startSOCKS(t, socks5.StaticCredentials{"alice": "s3cret"}, res, &users, &mu)
	_, port := hostServer(t)
	host, p, _ := net.SplitHostPort(addr)

	e, err := NewSOCKS5("socks5:test", ProxySpec{Scheme: "socks5", Host: host, Port: p, Username: "alice", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, e, "http://hoster.invalid:"+port+"/"); !strings.HasPrefix(got, "hello hoster.invalid") {
		t.Fatalf("body = %q", got)
	}
	if !res.seen("hoster.invalid") {
		t.Fatal("the name was not resolved by the proxy (DNS leak)")
	}
	if !errors.Is(e.Rotate(context.Background()), ErrRotateUnsupported) {
		t.Fatal("plain SOCKS5 must not rotate")
	}
	if strings.Contains(e.Label(), "s3cret") {
		t.Fatal("label leaks password")
	}

	bad, _ := NewSOCKS5("socks5:bad", ProxySpec{Scheme: "socks5", Host: host, Port: p, Username: "alice", Password: "nope"})
	_, err = bad.DialContext(context.Background(), "tcp", "127.0.0.1:"+port)
	if !errors.Is(err, ErrProxyAuth) {
		t.Fatalf("err = %v, want ErrProxyAuth", err)
	}
	if strings.Contains(fmt.Sprint(err), "nope") {
		t.Fatal("error leaks password")
	}
}

func TestSOCKS5StickySession(t *testing.T) {
	var users []string
	var mu sync.Mutex
	addr := startSOCKS(t, anyCreds{}, nil, &users, &mu)
	_, port := hostServer(t)
	host, p, _ := net.SplitHostPort(addr)
	spec := ProxySpec{Scheme: "socks5", Host: host, Port: p, Username: "cust-session-{session}", Password: "pw", Sessions: 2}
	e0, _ := NewProxyEgress(spec, 0)
	e1, _ := NewProxyEgress(spec, 1)
	if e0.ID() == e1.ID() {
		t.Fatal("instances share an ID")
	}
	get(t, e0, "http://127.0.0.1:"+port+"/")
	get(t, e1, "http://127.0.0.1:"+port+"/")
	if err := e0.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	get(t, e0, "http://127.0.0.1:"+port+"/")
	mu.Lock()
	defer mu.Unlock()
	if len(users) != 3 {
		t.Fatalf("users = %v", users)
	}
	for _, u := range users {
		if !strings.HasPrefix(u, "cust-session-") || strings.Contains(u, "{") {
			t.Fatalf("bad session user %q", u)
		}
	}
	if users[0] == users[1] || users[0] == users[2] {
		t.Fatalf("sessions not distinct: %v", users)
	}
}

// connectProxy is a minimal HTTP proxy: CONNECT tunnels and absolute-form
// requests, with Basic auth.
func connectProxy(t *testing.T, user, pass string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.Host)
		mu.Unlock()
		if user != "" && r.Header.Get("Proxy-Authorization") != want {
			w.Header().Set("Proxy-Authenticate", `Basic realm="x"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if r.Method != http.MethodConnect {
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}
		target := r.Host
		if strings.HasPrefix(target, "hoster.invalid:") {
			target = "127.0.0.1:" + strings.TrimPrefix(target, "hoster.invalid:")
		}
		up, err := net.Dial("tcp", target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, _ := w.(http.Hijacker)
		c, brw, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		// Send the response and the first upstream bytes together to
		// exercise bufferedConn.
		brw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		brw.Flush()
		go func() { io.Copy(up, brw); up.Close() }()
		io.Copy(c, up)
		c.Close()
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestHTTPProxyConnectAndProxyURL(t *testing.T) {
	proxy, seen := connectProxy(t, "bob", "pw1")
	_, port := hostServer(t)
	host, p, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	e, err := NewHTTPProxy("http:test", ProxySpec{Scheme: "http", Host: host, Port: p, Username: "bob", Password: "pw1"})
	if err != nil {
		t.Fatal(err)
	}
	// CONNECT tunnel via DialContext: the name is sent unresolved.
	conn, err := e.DialContext(context.Background(), "tcp", "hoster.invalid:"+port)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: hoster.invalid\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	conn.Close()
	if string(b) != "hello hoster.invalid" {
		t.Fatalf("tunnel body = %q", b)
	}
	// net/http path (absolute-form through ProxyURL).
	if got := get(t, e, "http://127.0.0.1:"+port+"/x"); !strings.HasPrefix(got, "hello 127.0.0.1") {
		t.Fatalf("proxied body = %q", got)
	}
	if len(*seen) < 2 || !strings.HasPrefix((*seen)[0], "CONNECT hoster.invalid:") {
		t.Fatalf("proxy saw %v", *seen)
	}

	bad, _ := NewHTTPProxy("http:bad", ProxySpec{Scheme: "http", Host: host, Port: p, Username: "bob", Password: "wrong"})
	_, err = bad.DialContext(context.Background(), "tcp", "127.0.0.1:"+port)
	if !errors.Is(err, ErrProxyAuth) {
		t.Fatalf("err = %v, want ErrProxyAuth", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("error leaks password")
	}
}

func TestHTTPProxyDialCanceled(t *testing.T) {
	// A proxy that accepts but never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	host, p, _ := net.SplitHostPort(ln.Addr().String())
	e, _ := NewHTTPProxy("http:slow", ProxySpec{Scheme: "http", Host: host, Port: p})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = e.DialContext(ctx, "tcp", "example.invalid:80")
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

func TestCheckHealthRecordsIP(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "198.51.100.9")
	}))
	defer echo.Close()
	addr := startSOCKS(t, nil, nil, nil, nil)
	host, p, _ := net.SplitHostPort(addr)
	e, _ := NewSOCKS5("s", ProxySpec{Scheme: "socks5", Host: host, Port: p})
	h := CheckHealth(context.Background(), e, []string{echo.URL})
	if !h.Healthy || h.PublicIP != netip.MustParseAddr("198.51.100.9") {
		t.Fatalf("health = %+v", h)
	}
	if got := e.Health(); got.PublicIP != h.PublicIP || !e.Healthy() {
		t.Fatalf("not recorded: %+v", got)
	}
	// A dead proxy.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	host, p, _ = net.SplitHostPort(dead)
	d, _ := NewSOCKS5("d", ProxySpec{Scheme: "socks5", Host: host, Port: p})
	if h := CheckHealth(context.Background(), d, []string{echo.URL}); h.Healthy || d.Healthy() || h.LastError == "" {
		t.Fatalf("dead proxy health = %+v", h)
	}
}

func TestConfirmedPublicIPAndWaitForIPChange(t *testing.T) {
	var mu sync.Mutex
	ip := "203.0.113.1"
	echo := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprint(w, ip)
		}))
	}
	a, b := echo(), echo()
	defer a.Close()
	defer b.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "192.0.2.77")
	}))
	defer other.Close()
	c := NewHTTPClient(NewDirect(), nil)
	ctx := context.Background()

	got, err := ConfirmedPublicIP(ctx, c, []string{other.URL, a.URL, b.URL})
	if err != nil || got.String() != "203.0.113.1" {
		t.Fatalf("ConfirmedPublicIP = %v, %v", got, err)
	}
	old := got
	_, err = WaitForIPChange(ctx, c, []string{a.URL, b.URL}, old, 300*time.Millisecond, 50*time.Millisecond)
	if !errors.Is(err, ErrIPUnchanged) {
		t.Fatalf("err = %v, want ErrIPUnchanged", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		ip = "203.0.113.2"
		mu.Unlock()
	}()
	got, err = WaitForIPChange(ctx, c, []string{a.URL, b.URL}, old, 5*time.Second, 30*time.Millisecond)
	if err != nil || got.String() != "203.0.113.2" {
		t.Fatalf("WaitForIPChange = %v, %v", got, err)
	}
}
