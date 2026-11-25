package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// SOCKS5 is a SOCKS5 proxy egress. Host names are always resolved by the
// proxy (socks5h semantics), so there are no DNS leaks.
type SOCKS5 struct {
	healthState
	id      string
	spec    ProxySpec
	forward net.Dialer

	mu    sync.Mutex
	creds proxyCreds
}

var _ Egress = (*SOCKS5)(nil)

// NewSOCKS5 returns a SOCKS5 egress with the given stable id.
func NewSOCKS5(id string, spec ProxySpec) (*SOCKS5, error) {
	if spec.Scheme != "socks5" {
		return nil, fmt.Errorf("netx: socks5: unsupported scheme %q", spec.Scheme)
	}
	if spec.Host == "" || spec.Port == "" {
		return nil, errors.New("netx: socks5: missing host or port")
	}
	return &SOCKS5{
		id:      id,
		spec:    spec,
		forward: net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second},
		creds:   newProxyCreds(spec),
	}, nil
}

// ID implements Egress.
func (s *SOCKS5) ID() string { return s.id }

// Kind implements Egress.
func (s *SOCKS5) Kind() Kind { return KindSOCKS5 }

// Scope implements Egress.
func (s *SOCKS5) Scope() Scope { return ScopePerConnection }

// Label implements Labeler (no password).
func (s *SOCKS5) Label() string { return s.spec.Redacted() }

// Spec returns the parsed entry (it contains the password: never log it).
func (s *SOCKS5) Spec() ProxySpec { return s.spec }

// DialContext implements Egress.
func (s *SOCKS5) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	s.mu.Lock()
	c := s.creds
	s.mu.Unlock()
	var auth *proxy.Auth
	if c.user != "" {
		auth = &proxy.Auth{User: c.username(), Password: c.password}
	}
	return dialSOCKS5(ctx, &s.forward, s.spec.Addr(), auth, network, addr)
}

func dialSOCKS5(ctx context.Context, forward *net.Dialer, proxyAddr string, auth *proxy.Auth, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("netx: socks5: network %q not supported", network)
	}
	d, err := proxy.SOCKS5("tcp", proxyAddr, auth, forward)
	if err != nil {
		return nil, fmt.Errorf("netx: socks5 %s: %w", proxyAddr, err)
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("netx: socks5 dialer without context support")
	}
	conn, err := cd.DialContext(ctx, network, addr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// x/net/internal/socks reports auth failures with this text.
		if auth != nil && strings.Contains(err.Error(), "authentication failed") {
			return nil, fmt.Errorf("%w: socks5 %s", ErrProxyAuth, proxyAddr)
		}
		return nil, fmt.Errorf("netx: socks5 via %s: %w", proxyAddr, err)
	}
	return conn, nil
}

// Rotate implements Egress: with a sticky-session username it draws a new
// session (new exit IP); otherwise ErrRotateUnsupported.
func (s *SOCKS5) Rotate(context.Context) error {
	if !s.spec.HasSessionTemplate() {
		return ErrRotateUnsupported
	}
	s.mu.Lock()
	s.creds.session = newSessionID()
	s.mu.Unlock()
	return nil
}

// PublicIP implements Egress.
func (s *SOCKS5) PublicIP(ctx context.Context) (netip.Addr, error) { return PublicIP(ctx, s) }

// Close implements Egress.
func (s *SOCKS5) Close() error { return nil }
