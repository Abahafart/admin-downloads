// Package netx defines the Egress abstraction: the network path through which
// a download leaves the machine (direct, HTTP proxy, SOCKS5, Tor, WireGuard
// userspace tunnel, router reconnect, system VPN).
//
// Every HTTP request of the project must go through an Egress (build clients
// with NewHTTPClient) so that IP rotation works uniformly. This package holds
// the interface plus the Direct egress; the other implementations live in
// their own files (httpproxy.go, socks5.go, tor.go, wireguard.go, router.go,
// systemvpn.go), each owned by the networking agent.
package netx

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
)

// Kind identifies the egress implementation.
type Kind string

// Egress kinds.
const (
	KindDirect    Kind = "direct"
	KindHTTPProxy Kind = "http"   // HTTP or HTTPS proxy (CONNECT)
	KindSOCKS5    Kind = "socks5" // SOCKS5 with remote DNS (socks5h)
	KindTor       Kind = "tor"
	KindWireGuard Kind = "wireguard" // userspace tunnel (wireguard-go + netstack)
	KindRouter    Kind = "router"    // router reconnect (UPnP IGD or script); global
	KindSystemVPN Kind = "systemvpn" // system VPN driven by argv commands; global
)

// Scope tells whether an egress can coexist with others.
type Scope string

const (
	// ScopePerConnection egresses route only the connections dialed through
	// them; many can be active at the same time (proxies, Tor, WireGuard).
	ScopePerConnection Scope = "per-connection"
	// ScopeGlobal egresses change the public IP of the whole machine
	// (router reconnect, system VPN). Rotating one affects every download.
	ScopeGlobal Scope = "global"
)

// Egress is a network exit. Implementations must be safe for concurrent use.
type Egress interface {
	// ID is stable across restarts (it is stored in SQLite, e.g. in
	// quota_marks and downloads.egress_id). Direct uses "direct".
	ID() string
	Kind() Kind
	Scope() Scope
	// DialContext opens a connection to addr *through* the egress. For
	// proxies this means the tunnel to addr, not the connection to the
	// proxy itself. Name resolution of addr must happen on the far side
	// whenever the egress supports it (no DNS leaks).
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	// Rotate asks the egress for a new exit IP (Tor NEWNYM, WireGuard peer
	// switch, router reconnect, VPN reconnect). Egresses that cannot rotate
	// return ErrRotateUnsupported.
	Rotate(ctx context.Context) error
	// PublicIP returns the IP the outside world sees for this egress. Most
	// implementations simply call the package-level PublicIP helper.
	PublicIP(ctx context.Context) (netip.Addr, error)
	// Healthy reports the result of the last health check.
	Healthy() bool
	// Close releases resources (tunnels, control connections).
	Close() error
}

// HTTPProxier is optionally implemented by egresses that are plain HTTP(S)
// proxies. When ProxyURL returns a non-nil URL, NewHTTPClient configures
// http.Transport.Proxy with it (so the proxy sees absolute-form requests for
// http:// and CONNECT for https://) instead of using DialContext. DialContext
// must still work (as a CONNECT tunnel) for non-HTTP users.
type HTTPProxier interface {
	ProxyURL() *url.URL
}

// ProxyDialer is optionally implemented by HTTP proxy egresses that must
// reach the proxy itself through something other than a plain net.Dialer
// (for example a proxy behind a WireGuard tunnel). It is only consulted when
// HTTPProxier.ProxyURL is non-nil.
type ProxyDialer interface {
	DialProxy(ctx context.Context, network, addr string) (net.Conn, error)
}

// Sentinel errors.
var (
	// ErrRotateUnsupported is returned by Rotate on egresses without rotation.
	ErrRotateUnsupported = errors.New("netx: egress does not support rotation")
	// ErrUnhealthy may be returned by DialContext when the egress is down.
	ErrUnhealthy = errors.New("netx: egress unhealthy")
	// ErrNoPublicIP is returned by PublicIP when no echo service answered.
	ErrNoPublicIP = errors.New("netx: could not determine public IP")
)
