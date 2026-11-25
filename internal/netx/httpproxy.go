package netx

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"
)

// Labeler is implemented by egresses with a human readable, secret-free
// label for the UI.
type Labeler interface {
	Label() string
}

// ErrProxyAuth is returned (wrapped) when a proxy rejects the credentials.
var ErrProxyAuth = errors.New("netx: proxy authentication failed")

// HTTPProxy is an HTTP or HTTPS (TLS to the proxy) proxy egress. Plain HTTP
// clients use it through ProxyURL (absolute-form requests and CONNECT done
// by net/http); DialContext opens a CONNECT tunnel for everything else.
type HTTPProxy struct {
	healthState
	id     string
	spec   ProxySpec
	dialer net.Dialer

	mu    sync.Mutex
	creds proxyCreds
}

var (
	_ Egress      = (*HTTPProxy)(nil)
	_ HTTPProxier = (*HTTPProxy)(nil)
)

// NewHTTPProxy returns an HTTP(S) proxy egress with the given stable id.
func NewHTTPProxy(id string, spec ProxySpec) (*HTTPProxy, error) {
	if spec.Scheme != "http" && spec.Scheme != "https" {
		return nil, fmt.Errorf("netx: http proxy: unsupported scheme %q", spec.Scheme)
	}
	if spec.Host == "" || spec.Port == "" {
		return nil, errors.New("netx: http proxy: missing host or port")
	}
	return &HTTPProxy{
		id:     id,
		spec:   spec,
		dialer: net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second},
		creds:  newProxyCreds(spec),
	}, nil
}

// ID implements Egress.
func (p *HTTPProxy) ID() string { return p.id }

// Kind implements Egress.
func (p *HTTPProxy) Kind() Kind { return KindHTTPProxy }

// Scope implements Egress.
func (p *HTTPProxy) Scope() Scope { return ScopePerConnection }

// Label implements Labeler (no password).
func (p *HTTPProxy) Label() string { return p.spec.Redacted() }

// Spec returns the parsed entry (it contains the password: never log it).
func (p *HTTPProxy) Spec() ProxySpec { return p.spec }

// ProxyURL implements HTTPProxier. The URL carries the credentials (net/http
// turns them into Proxy-Authorization); never log it.
func (p *HTTPProxy) ProxyURL() *url.URL {
	u := &url.URL{Scheme: p.spec.Scheme, Host: p.spec.Addr()}
	p.mu.Lock()
	c := p.creds
	p.mu.Unlock()
	if c.user != "" {
		u.User = url.UserPassword(c.username(), c.password)
	}
	return u
}

// DialContext implements Egress with an HTTP CONNECT tunnel. The target
// host name is sent to the proxy unresolved (no local DNS).
func (p *HTTPProxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("netx: http proxy: network %q not supported", network)
	}
	conn, err := p.dialProxy(ctx)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	br, err := p.connect(conn, addr)
	stopped := stop()
	if err != nil {
		conn.Close()
		if !stopped || ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

func (p *HTTPProxy) dialProxy(ctx context.Context) (net.Conn, error) {
	conn, err := p.dialer.DialContext(ctx, "tcp", p.spec.Addr())
	if err != nil {
		return nil, fmt.Errorf("netx: dial proxy %s: %w", p.spec.Addr(), err)
	}
	if p.spec.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: p.spec.Host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("netx: TLS to proxy %s: %w", p.spec.Addr(), err)
		}
		conn = tc
	}
	return conn, nil
}

func (p *HTTPProxy) connect(conn net.Conn, addr string) (*bufio.Reader, error) {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	p.mu.Lock()
	c := p.creds
	p.mu.Unlock()
	if c.user != "" {
		raw := c.username() + ":" + c.password
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(raw)))
	}
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("netx: CONNECT via %s: %w", p.spec.Addr(), err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("netx: CONNECT via %s: %w", p.spec.Addr(), err)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusProxyAuthRequired:
		return nil, fmt.Errorf("%w: %s", ErrProxyAuth, p.spec.Addr())
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("netx: CONNECT via %s: %s", p.spec.Addr(), resp.Status)
	}
	return br, nil
}

// Rotate implements Egress: with a sticky-session username it draws a new
// session (new exit IP); otherwise ErrRotateUnsupported.
func (p *HTTPProxy) Rotate(context.Context) error {
	if !p.spec.HasSessionTemplate() {
		return ErrRotateUnsupported
	}
	p.mu.Lock()
	p.creds.session = newSessionID()
	p.mu.Unlock()
	return nil
}

// PublicIP implements Egress.
func (p *HTTPProxy) PublicIP(ctx context.Context) (netip.Addr, error) { return PublicIP(ctx, p) }

// Close implements Egress.
func (p *HTTPProxy) Close() error { return nil }

// bufferedConn returns bytes the CONNECT reader read past the response.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
