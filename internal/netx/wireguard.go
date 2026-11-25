package netx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// DefaultWGDNS is used inside the tunnel when a .conf has no DNS entry.
var DefaultWGDNS = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")}

// WireGuardOptions configures userspace WireGuard egresses.
type WireGuardOptions struct {
	// Resolver resolves peer endpoint names (net.DefaultResolver if nil).
	// This is the only lookup made outside the tunnel.
	Resolver Resolver
	Logger   *slog.Logger
	// NewBind returns the UDP bind of a device (conn.NewDefaultBind if nil).
	NewBind func() conn.Bind
}

// WireGuardGroup is a set of .conf files of one provider served by at most
// MaxSimultaneous tunnels at a time (provider connection limits). Each
// tunnel is a WireGuard egress; Rotate moves it to a configuration of the
// group that no other tunnel is using.
type WireGuardGroup struct {
	name string
	opts WireGuardOptions
	log  *slog.Logger

	mu       sync.Mutex
	confs    []*WGConfig
	used     []bool
	next     int
	egresses []*WireGuard
}

// NewWireGuardGroup creates min(len(confs), maxSimultaneous) egresses (all
// of them when maxSimultaneous <= 0) with IDs idPrefix+":0", ":1"... Tunnels
// are brought up lazily on first use.
func NewWireGuardGroup(idPrefix, name string, confs []*WGConfig, maxSimultaneous int, opts WireGuardOptions) (*WireGuardGroup, error) {
	if len(confs) == 0 {
		return nil, errors.New("netx: wireguard group without configurations")
	}
	n := len(confs)
	if maxSimultaneous > 0 && maxSimultaneous < n {
		n = maxSimultaneous
	}
	ids := make([]string, n)
	for i := range ids {
		ids[i] = idPrefix + ":" + strconv.Itoa(i)
	}
	return newWireGuardGroup(name, ids, confs, opts)
}

// NewWireGuard returns a single-configuration WireGuard egress with id.
func NewWireGuard(id string, c *WGConfig, opts WireGuardOptions) (*WireGuard, error) {
	g, err := newWireGuardGroup(c.Name, []string{id}, []*WGConfig{c}, opts)
	if err != nil {
		return nil, err
	}
	return g.egresses[0], nil
}

func newWireGuardGroup(name string, ids []string, confs []*WGConfig, opts WireGuardOptions) (*WireGuardGroup, error) {
	for i, c := range confs {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("netx: wireguard %s conf %d: %w", name, i, err)
		}
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.NewBind == nil {
		opts.NewBind = conn.NewDefaultBind
	}
	g := &WireGuardGroup{name: name, opts: opts, log: opts.Logger, confs: confs, used: make([]bool, len(confs))}
	if g.log == nil {
		g.log = slog.New(slog.DiscardHandler)
	}
	for i, id := range ids {
		g.used[i] = true
		g.egresses = append(g.egresses, &WireGuard{g: g, id: id, conf: i})
	}
	g.next = len(ids) % len(confs)
	return g, nil
}

// Name returns the group name.
func (g *WireGuardGroup) Name() string { return g.name }

// Egresses returns the group's tunnels.
func (g *WireGuardGroup) Egresses() []*WireGuard { return g.egresses }

// Close closes every tunnel.
func (g *WireGuardGroup) Close() error {
	var errs []error
	for _, w := range g.egresses {
		errs = append(errs, w.Close())
	}
	return errors.Join(errs...)
}

// swap releases cur and reserves the next free configuration.
func (g *WireGuardGroup) swap(cur int) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for range len(g.confs) {
		i := g.next
		g.next = (g.next + 1) % len(g.confs)
		if !g.used[i] && i != cur {
			g.used[cur] = false
			g.used[i] = true
			return i, nil
		}
	}
	return cur, fmt.Errorf("%w: wireguard group %q has no spare configuration", ErrRotateUnsupported, g.name)
}

// WireGuard is a userspace WireGuard tunnel (wireguard-go + gVisor netstack).
// Only connections dialed through it use the tunnel; the system network is
// not touched and DNS queries go through the tunnel.
type WireGuard struct {
	healthState
	g  *WireGuardGroup
	id string

	mu   sync.Mutex
	conf int
	tun  *wgTunnel
}

var _ Egress = (*WireGuard)(nil)

type wgTunnel struct {
	dev  *device.Device
	tnet *netstack.Net
}

// ID implements Egress.
func (w *WireGuard) ID() string { return w.id }

// Kind implements Egress.
func (w *WireGuard) Kind() Kind { return KindWireGuard }

// Scope implements Egress.
func (w *WireGuard) Scope() Scope { return ScopePerConnection }

// Label implements Labeler.
func (w *WireGuard) Label() string {
	c := w.Config()
	name := c.Name
	if name == "" {
		name = c.Endpoint()
	}
	if w.g.name != "" && w.g.name != name {
		return w.g.name + " · " + name
	}
	return name
}

// Config returns the configuration in use (it holds the private key: never
// log its fields).
func (w *WireGuard) Config() *WGConfig {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.g.confs[w.conf]
}

// Up brings the tunnel up now instead of on first dial.
func (w *WireGuard) Up(ctx context.Context) error {
	_, err := w.tunnel(ctx)
	return err
}

func (w *WireGuard) tunnel(ctx context.Context) (*wgTunnel, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tun != nil {
		return w.tun, nil
	}
	c := w.g.confs[w.conf]
	dns := c.DNS
	if len(dns) == 0 {
		dns = DefaultWGDNS
	}
	tdev, tnet, err := netstack.CreateNetTUN(c.LocalAddrs(), dns, c.EffectiveMTU())
	if err != nil {
		return nil, fmt.Errorf("netx: wireguard %s: netstack: %w", w.id, err)
	}
	log := w.g.log
	logger := &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf: func(format string, args ...any) {
			log.Debug("wireguard: "+fmt.Sprintf(format, args...), "egress", w.id)
		},
	}
	dev := device.NewDevice(tdev, w.g.opts.NewBind(), logger)
	ipc, err := c.IpcConfig(ctx, w.g.opts.Resolver)
	if err == nil {
		err = dev.IpcSet(ipc)
	}
	if err == nil {
		err = dev.Up()
	}
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("netx: wireguard %s: %w", w.id, err)
	}
	w.tun = &wgTunnel{dev: dev, tnet: tnet}
	return w.tun, nil
}

// DialContext implements Egress through the tunnel (names are resolved
// with the tunnel's DNS servers, through the tunnel).
func (w *WireGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	t, err := w.tunnel(ctx)
	if err != nil {
		return nil, err
	}
	c, err := t.tnet.DialContext(ctx, network, addr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("netx: wireguard %s: %w", w.id, err)
	}
	return c, nil
}

// Rotate implements Egress: the tunnel switches to another configuration
// of its group that no other tunnel uses (ErrRotateUnsupported when none
// is free). Connections open on the old tunnel are closed.
func (w *WireGuard) Rotate(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	next, err := w.g.swap(w.conf)
	if err != nil {
		return err
	}
	w.conf = next
	if w.tun != nil {
		w.tun.dev.Close()
		w.tun = nil
	}
	w.resetHealth() // unknown until the next check
	return nil
}

// PublicIP implements Egress.
func (w *WireGuard) PublicIP(ctx context.Context) (netip.Addr, error) { return PublicIP(ctx, w) }

// Close implements Egress: the tunnel is torn down (it comes back up on
// the next dial).
func (w *WireGuard) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tun != nil {
		w.tun.dev.Close()
		w.tun = nil
	}
	return nil
}
