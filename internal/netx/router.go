package netx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
)

// Router methods.
const (
	RouterUPnP   = "upnp"
	RouterScript = "script"
)

// RouterID is the ID of the router reconnect egress.
const RouterID = "router"

// IGDConnection is the part of a UPnP IGD WAN connection service used to
// force a reconnect (WANIPConnection1/2, WANPPPConnection1).
type IGDConnection interface {
	ForceTerminationCtx(ctx context.Context) error
	RequestConnectionCtx(ctx context.Context) error
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
}

// RouterOptions configures the router reconnect global egress.
type RouterOptions struct {
	Method string   // RouterUPnP (default) or RouterScript
	Script []string // argv for RouterScript (from config.toml only)
	// WaitAfter is the pause between ForceTermination and RequestConnection
	// (and after the script) before polling for the link (default 10 s).
	WaitAfter time.Duration
	// Timeout bounds the whole reconnect (default 2 min).
	Timeout time.Duration
	// Discover finds the IGD WAN connections (UPnP discovery if nil).
	Discover func(ctx context.Context) ([]IGDConnection, error)
	Logger   *slog.Logger
}

// Router changes the machine's public IP by reconnecting the router's WAN
// link (UPnP IGD) or by running a user script. It is a global egress:
// connections go out directly and Rotate affects every download.
type Router struct {
	healthState
	opts   RouterOptions
	log    *slog.Logger
	dialer net.Dialer
}

var _ Egress = (*Router)(nil)

// NewRouter validates opts and returns the router egress.
func NewRouter(opts RouterOptions) (*Router, error) {
	if opts.Method == "" {
		opts.Method = RouterUPnP
	}
	switch opts.Method {
	case RouterUPnP:
	case RouterScript:
		if len(opts.Script) == 0 || strings.TrimSpace(opts.Script[0]) == "" {
			return nil, errors.New("netx: router: script method without script argv")
		}
	default:
		return nil, fmt.Errorf("netx: router: unknown method %q", opts.Method)
	}
	if opts.WaitAfter <= 0 {
		opts.WaitAfter = 10 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.Discover == nil {
		opts.Discover = DiscoverIGD
	}
	r := &Router{opts: opts, log: opts.Logger, dialer: net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	return r, nil
}

// ID implements Egress.
func (r *Router) ID() string { return RouterID }

// Kind implements Egress.
func (r *Router) Kind() Kind { return KindRouter }

// Scope implements Egress.
func (r *Router) Scope() Scope { return ScopeGlobal }

// Label implements Labeler.
func (r *Router) Label() string {
	if r.opts.Method == RouterScript {
		return "Router (script)"
	}
	return "Router (UPnP)"
}

// Timeout returns the configured reconnect timeout.
func (r *Router) Timeout() time.Duration { return r.opts.Timeout }

// DialContext implements Egress (direct connection).
func (r *Router) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return r.dialer.DialContext(ctx, network, addr)
}

// Rotate reconnects the WAN link. It returns once the router reports an
// external IP again (UPnP) or the script finished and WaitAfter elapsed;
// the caller verifies the public IP changed.
func (r *Router) Rotate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	var err error
	if r.opts.Method == RouterScript {
		err = RunArgv(ctx, r.opts.Script, 0)
		if err == nil {
			err = sleepCtx(ctx, r.opts.WaitAfter)
		}
	} else {
		err = r.reconnectUPnP(ctx)
	}
	if err != nil {
		MarkUnhealthy(r, err)
		return err
	}
	RecordHealth(r, Health{Healthy: true, CheckedAt: time.Now()})
	return nil
}

func (r *Router) reconnectUPnP(ctx context.Context) error {
	conns, err := r.opts.Discover(ctx)
	if err != nil {
		return fmt.Errorf("netx: router: UPnP discovery: %w", err)
	}
	if len(conns) == 0 {
		return errors.New("netx: router: no UPnP IGD WAN connection found (enable UPnP on the router)")
	}
	var errs []error
	for _, c := range conns {
		old, _ := c.GetExternalIPAddressCtx(ctx)
		if err := c.ForceTerminationCtx(ctx); err != nil {
			errs = append(errs, fmt.Errorf("ForceTermination: %w", err))
			continue
		}
		r.log.Info("router: WAN link terminated, reconnecting", "old_ip", old)
		if err := sleepCtx(ctx, r.opts.WaitAfter); err != nil {
			return err
		}
		if err := c.RequestConnectionCtx(ctx); err != nil {
			// Many routers reconnect on their own and reject the call.
			r.log.Debug("router: RequestConnection failed (may reconnect by itself)", "err", SanitizeError(err))
		}
		return r.waitExternalIP(ctx, c)
	}
	return fmt.Errorf("netx: router: %w", errors.Join(errs...))
}

func (r *Router) waitExternalIP(ctx context.Context, c IGDConnection) error {
	poll := min(r.opts.WaitAfter, 3*time.Second)
	for {
		s, err := c.GetExternalIPAddressCtx(ctx)
		if err == nil {
			if a, perr := netip.ParseAddr(strings.TrimSpace(s)); perr == nil && !a.IsUnspecified() {
				r.log.Info("router: WAN link up", "ip", a.String())
				return nil
			}
		}
		if err := sleepCtx(ctx, poll); err != nil {
			return fmt.Errorf("netx: router: link did not come back: %w", err)
		}
	}
}

// PublicIP implements Egress.
func (r *Router) PublicIP(ctx context.Context) (netip.Addr, error) { return PublicIP(ctx, r) }

// Close implements Egress.
func (r *Router) Close() error { return nil }

// DiscoverIGD finds WAN connection services of UPnP Internet Gateway
// Devices on the LAN: IGDv2 WANIPConnection2 first, then WANIPConnection1
// and WANPPPConnection1.
func DiscoverIGD(ctx context.Context) ([]IGDConnection, error) {
	var out []IGDConnection
	var errs []error
	cs2, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx)
	out, errs = appendIGD(out, errs, cs2, err)
	cs1, _, err := internetgateway1.NewWANIPConnection1ClientsCtx(ctx)
	out, errs = appendIGD(out, errs, cs1, err)
	ppp, _, err := internetgateway1.NewWANPPPConnection1ClientsCtx(ctx)
	out, errs = appendIGD(out, errs, ppp, err)
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func appendIGD[T IGDConnection](out []IGDConnection, errs []error, cs []T, err error) ([]IGDConnection, []error) {
	if err != nil {
		return out, append(errs, err)
	}
	for _, c := range cs {
		out = append(out, c)
	}
	return out, errs
}
