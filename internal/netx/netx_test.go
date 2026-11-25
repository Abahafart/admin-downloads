package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestDirectClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	d := NewDirect()
	if d.ID() != DirectID || d.Kind() != KindDirect || d.Scope() != ScopePerConnection || !d.Healthy() {
		t.Fatal("unexpected Direct metadata")
	}
	if err := d.Rotate(context.Background()); !errors.Is(err, ErrRotateUnsupported) {
		t.Fatalf("Rotate err = %v", err)
	}
	resp, err := NewHTTPClient(d, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// countingEgress records dials so we can check NewHTTPClient uses it.
type countingEgress struct {
	*Direct
	dials atomic.Int32
	proxy *url.URL
}

func (c *countingEgress) DialContext(ctx context.Context, n, a string) (net.Conn, error) {
	c.dials.Add(1)
	return c.Direct.DialContext(ctx, n, a)
}

func (c *countingEgress) ProxyURL() *url.URL { return c.proxy }

func TestNewHTTPClientUsesDialContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	e := &countingEgress{Direct: NewDirect()}
	resp, err := NewHTTPClient(e, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if e.dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1", e.dials.Load())
	}
}

func TestNewHTTPClientUsesProxyURL(t *testing.T) {
	var sawAbsolute atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A proxy receives the absolute-form target.
		if r.URL.Host == "example.invalid" {
			sawAbsolute.Store(true)
		}
		fmt.Fprint(w, "proxied")
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	e := &countingEgress{Direct: NewDirect(), proxy: pu}

	resp, err := NewHTTPClient(e, nil).Get("http://example.invalid/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !sawAbsolute.Load() {
		t.Fatal("request did not go through the proxy")
	}
	if e.dials.Load() != 0 {
		t.Fatal("DialContext must not be used when ProxyURL is set")
	}
}

func TestPublicIPWith(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>")
	}))
	defer garbage.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, " 203.0.113.7\n")
	}))
	defer good.Close()

	c := NewHTTPClient(NewDirect(), nil)
	ip, err := PublicIPWith(context.Background(), c, []string{bad.URL, garbage.URL, good.URL})
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "203.0.113.7" {
		t.Fatalf("ip = %s", ip)
	}

	_, err = PublicIPWith(context.Background(), c, []string{bad.URL, garbage.URL})
	if !errors.Is(err, ErrNoPublicIP) {
		t.Fatalf("err = %v, want ErrNoPublicIP", err)
	}
}
