package rotation

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"admindescargas/internal/netx"
)

// identities maps the local address of outgoing test connections to the
// name of the egress (or fake proxy) that dialed them, so httptest servers
// can tell "source IPs" apart on a single loopback address.
type identities struct {
	mu     sync.Mutex
	byAddr map[string]string
	ips    map[string]string // identity -> public IP returned by the echo service
}

func newIdentities() *identities {
	return &identities{byAddr: map[string]string{}, ips: map[string]string{}}
}

func (ids *identities) dial(ctx context.Context, who, network, addr string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	ids.mu.Lock()
	ids.byAddr[c.LocalAddr().String()] = who
	ids.mu.Unlock()
	return c, nil
}

func (ids *identities) who(r *http.Request) string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	if w, ok := ids.byAddr[r.RemoteAddr]; ok {
		return w
	}
	return "direct"
}

func (ids *identities) setIP(who, ip string) {
	ids.mu.Lock()
	ids.ips[who] = ip
	ids.mu.Unlock()
}

// echo returns an IP echo service answering the identity's IP.
func (ids *identities) echo(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := ids.who(r)
		ids.mu.Lock()
		ip := ids.ips[who]
		ids.mu.Unlock()
		if ip == "" {
			http.Error(w, "unknown", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, ip)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeEgress is a per-connection (or global) egress for manager tests.
type fakeEgress struct {
	id     string
	kind   netx.Kind
	scope  netx.Scope
	ids    *identities
	rotate func(ctx context.Context) error

	healthy atomic.Bool
	rotated atomic.Int32
}

func newFake(ids *identities, id string) *fakeEgress {
	f := &fakeEgress{id: id, kind: netx.KindSOCKS5, scope: netx.ScopePerConnection, ids: ids}
	f.healthy.Store(true)
	return f
}

func (f *fakeEgress) ID() string        { return f.id }
func (f *fakeEgress) Kind() netx.Kind   { return f.kind }
func (f *fakeEgress) Scope() netx.Scope { return f.scope }
func (f *fakeEgress) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	who := f.id
	if f.scope == netx.ScopeGlobal {
		who = "direct"
	}
	return f.ids.dial(ctx, who, network, addr)
}
func (f *fakeEgress) Rotate(ctx context.Context) error {
	f.rotated.Add(1)
	if f.rotate == nil {
		return netx.ErrRotateUnsupported
	}
	return f.rotate(ctx)
}
func (f *fakeEgress) PublicIP(ctx context.Context) (netip.Addr, error) { return netx.PublicIP(ctx, f) }
func (f *fakeEgress) Healthy() bool                                    { return f.healthy.Load() }
func (f *fakeEgress) Close() error                                     { return nil }

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
