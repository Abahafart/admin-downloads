package netx

import (
	"context"
	"net"
	"net/netip"
	"time"
)

// DirectID is the ID of the Direct egress.
const DirectID = "direct"

// Direct is the egress that uses the machine's own network connection.
type Direct struct {
	dialer net.Dialer
}

var _ Egress = (*Direct)(nil)

// NewDirect returns the direct egress.
func NewDirect() *Direct {
	return &Direct{dialer: net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}}
}

// ID implements Egress.
func (d *Direct) ID() string { return DirectID }

// Kind implements Egress.
func (d *Direct) Kind() Kind { return KindDirect }

// Scope implements Egress.
func (d *Direct) Scope() Scope { return ScopePerConnection }

// DialContext implements Egress.
func (d *Direct) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialer.DialContext(ctx, network, addr)
}

// Rotate implements Egress; the direct connection cannot rotate by itself
// (a global egress such as the router reconnect does that).
func (d *Direct) Rotate(context.Context) error { return ErrRotateUnsupported }

// PublicIP implements Egress.
func (d *Direct) PublicIP(ctx context.Context) (netip.Addr, error) { return PublicIP(ctx, d) }

// Healthy implements Egress.
func (d *Direct) Healthy() bool { return true }

// Close implements Egress.
func (d *Direct) Close() error { return nil }
