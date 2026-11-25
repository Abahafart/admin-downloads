package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// SystemVPNOptions configures a system VPN driven by argv commands. The
// commands come from config.toml only (never from the API) and run without a
// shell.
type SystemVPNOptions struct {
	Name       string
	Connect    []string
	Rotate     []string
	Disconnect []string
	Timeout    time.Duration // per command (default 2 min)
	WaitAfter  time.Duration // settle time after a command (default 5 s)
}

// SystemVPN is a global egress: traffic leaves through the system route
// (whatever the VPN client set up) and Rotate reconnects the VPN.
type SystemVPN struct {
	healthState
	opts   SystemVPNOptions
	dialer net.Dialer
}

var _ Egress = (*SystemVPN)(nil)

// NewSystemVPN validates opts and returns the egress.
func NewSystemVPN(opts SystemVPNOptions) (*SystemVPN, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, errors.New("netx: system vpn: missing name")
	}
	if !validArgv(opts.Rotate) && !validArgv(opts.Connect) {
		return nil, fmt.Errorf("netx: system vpn %s: needs a rotate or connect command", opts.Name)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.WaitAfter <= 0 {
		opts.WaitAfter = 5 * time.Second
	}
	return &SystemVPN{opts: opts, dialer: net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}}, nil
}

func validArgv(a []string) bool { return len(a) > 0 && strings.TrimSpace(a[0]) != "" }

// SystemVPNID returns the egress ID for a VPN name.
func SystemVPNID(name string) string { return "systemvpn:" + name }

// ID implements Egress.
func (v *SystemVPN) ID() string { return SystemVPNID(v.opts.Name) }

// Kind implements Egress.
func (v *SystemVPN) Kind() Kind { return KindSystemVPN }

// Scope implements Egress.
func (v *SystemVPN) Scope() Scope { return ScopeGlobal }

// Label implements Labeler.
func (v *SystemVPN) Label() string { return "VPN " + v.opts.Name }

// Timeout returns the time a full rotation may take.
func (v *SystemVPN) Timeout() time.Duration { return 2*v.opts.Timeout + v.opts.WaitAfter }

// DialContext implements Egress (system route).
func (v *SystemVPN) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return v.dialer.DialContext(ctx, network, addr)
}

func (v *SystemVPN) run(ctx context.Context, argv []string) error {
	if err := RunArgv(ctx, argv, v.opts.Timeout); err != nil {
		return err
	}
	return sleepCtx(ctx, v.opts.WaitAfter)
}

// Connect runs the connect command.
func (v *SystemVPN) Connect(ctx context.Context) error {
	if !validArgv(v.opts.Connect) {
		return fmt.Errorf("netx: system vpn %s: no connect command", v.opts.Name)
	}
	return v.run(ctx, v.opts.Connect)
}

// Disconnect runs the disconnect command (no-op without one).
func (v *SystemVPN) Disconnect(ctx context.Context) error {
	if !validArgv(v.opts.Disconnect) {
		return nil
	}
	return v.run(ctx, v.opts.Disconnect)
}

// Rotate runs the rotate command, or disconnect + connect without one.
func (v *SystemVPN) Rotate(ctx context.Context) error {
	var err error
	if validArgv(v.opts.Rotate) {
		err = v.run(ctx, v.opts.Rotate)
	} else {
		if err = v.Disconnect(ctx); err == nil {
			err = v.Connect(ctx)
		}
	}
	if err != nil {
		MarkUnhealthy(v, err)
		return err
	}
	RecordHealth(v, Health{Healthy: true, CheckedAt: time.Now()})
	return nil
}

// PublicIP implements Egress.
func (v *SystemVPN) PublicIP(ctx context.Context) (netip.Addr, error) { return PublicIP(ctx, v) }

// Close implements Egress. The VPN is left as it is (the user may rely on
// it outside the app).
func (v *SystemVPN) Close() error { return nil }

// ErrIPUnchanged is returned by WaitForIPChange when the public IP stayed
// the same until the deadline.
var ErrIPUnchanged = errors.New("netx: public IP did not change")

// ConfirmedPublicIP asks the echo services in order until two of them agree
// on the IP (or only one answers at all), which filters out stale caches
// and captive portals right after a reconnect.
func ConfirmedPublicIP(ctx context.Context, client *http.Client, services []string) (netip.Addr, error) {
	if len(services) == 0 {
		services = DefaultIPEchoServices
	}
	seen := map[netip.Addr]int{}
	var first netip.Addr
	var errs []error
	for _, svc := range services {
		ip, err := queryEcho(ctx, client, svc)
		if err != nil {
			if ctx.Err() != nil {
				return netip.Addr{}, ctx.Err()
			}
			errs = append(errs, fmt.Errorf("%s: %w", svc, err))
			continue
		}
		if !first.IsValid() {
			first = ip
		}
		seen[ip]++
		if seen[ip] >= 2 {
			return ip, nil
		}
	}
	if len(seen) == 1 {
		return first, nil
	}
	if len(seen) > 1 {
		return netip.Addr{}, fmt.Errorf("%w: echo services disagree", ErrNoPublicIP)
	}
	return netip.Addr{}, fmt.Errorf("%w: %w", ErrNoPublicIP, errors.Join(errs...))
}

// WaitForIPChange polls the public IP through client every poll interval
// until it is valid and different from old (any valid IP if old is
// invalid), or until timeout. It returns the new IP, or an error wrapping
// ErrIPUnchanged / ErrNoPublicIP.
func WaitForIPChange(ctx context.Context, client *http.Client, services []string, old netip.Addr, timeout, poll time.Duration) (netip.Addr, error) {
	if poll <= 0 {
		poll = 3 * time.Second
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		ip, err := ConfirmedPublicIP(ctx, client, services)
		switch {
		case err == nil && (!old.IsValid() || ip != old):
			return ip, nil
		case err == nil:
			lastErr = fmt.Errorf("%w (still %s)", ErrIPUnchanged, ip)
		default:
			lastErr = err
		}
		client.CloseIdleConnections()
		if sleepCtx(ctx, poll) != nil {
			if parent.Err() != nil {
				return netip.Addr{}, parent.Err()
			}
			return netip.Addr{}, lastErr
		}
	}
}
