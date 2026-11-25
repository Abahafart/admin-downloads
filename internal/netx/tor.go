package netx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/textproto"
	"strconv"
	"sync"
	"time"

	"github.com/cretz/bine/control"
	"github.com/cretz/bine/tor"
	"golang.org/x/net/proxy"
)

// TorNote is the UI warning shown next to Tor egresses.
const TorNote = "Tor es lento y algunos hosters (MEGA, Google Drive) bloquean sus nodos de salida; se usa como último recurso."

// Tor defaults.
const (
	DefaultTorSocksAddr   = "127.0.0.1:9050"
	DefaultTorControlAddr = "127.0.0.1:9051"
	// TorNewnymInterval is the minimum time between two NEWNYM signals (tor
	// itself rate-limits them to one every 10 s).
	TorNewnymInterval = 10 * time.Second
)

// TorOptions configures the Tor egress group.
type TorOptions struct {
	// SocksAddr of a running tor (default DefaultTorSocksAddr). With
	// TorPath it is the address the launched tor listens on.
	SocksAddr string
	// ControlAddr of a running tor (default DefaultTorControlAddr); "-"
	// disables the control port (Rotate then only switches SOCKS
	// credentials). Ignored with TorPath (the launched tor's own control
	// connection is used).
	ControlAddr string
	// ControlPassword for HashedControlPassword auth; empty = cookie / none.
	// Never logged.
	ControlPassword string
	// TorPath launches this tor binary (bine) instead of using a running one.
	TorPath string
	// DataDir for the launched tor (keeps the consensus cache between runs).
	DataDir string
	// Circuits is the number of isolated identities exposed as egresses
	// (default 4).
	Circuits int
	// IDPrefix of the circuit egress IDs (default "tor"): "tor:0", "tor:1"...
	IDPrefix string
	Logger   *slog.Logger
}

// Tor manages a tor client (attached or launched) and exposes Circuits:
// egresses that use distinct SOCKS credentials. tor's IsolateSOCKSAuth
// (enabled by default on SocksPort) puts streams with different credentials
// on different circuits, so concurrent downloads leave through different
// exit nodes.
type Tor struct {
	opts     TorOptions
	log      *slog.Logger
	circuits []*TorCircuit

	mu         sync.Mutex
	proc       *tor.Tor // launched tor, nil when attached
	ready      chan struct{}
	startErr   error
	lastNewnym time.Time
	closed     bool
}

// NewTor returns the Tor group. With TorPath the binary is launched in the
// background (circuits report errors until it has bootstrapped); ctx only
// bounds nothing here: the process lives until Close.
func NewTor(ctx context.Context, opts TorOptions) (*Tor, error) {
	if opts.SocksAddr == "" {
		opts.SocksAddr = DefaultTorSocksAddr
	}
	if opts.ControlAddr == "" {
		opts.ControlAddr = DefaultTorControlAddr
	}
	if opts.Circuits <= 0 {
		opts.Circuits = 4
	}
	if opts.IDPrefix == "" {
		opts.IDPrefix = "tor"
	}
	if _, _, err := net.SplitHostPort(opts.SocksAddr); err != nil {
		return nil, fmt.Errorf("netx: tor socks_addr: %w", err)
	}
	t := &Tor{opts: opts, log: opts.Logger, ready: make(chan struct{})}
	if t.log == nil {
		t.log = slog.New(slog.DiscardHandler)
	}
	for i := range opts.Circuits {
		c := &TorCircuit{t: t, id: opts.IDPrefix + ":" + strconv.Itoa(i), idx: i}
		c.newCreds()
		t.circuits = append(t.circuits, c)
	}
	if opts.TorPath == "" {
		close(t.ready)
		return t, nil
	}
	go t.launch(context.WithoutCancel(ctx))
	return t, nil
}

func (t *Tor) launch(ctx context.Context) {
	defer close(t.ready)
	conf := &tor.StartConf{
		ExePath:         t.opts.TorPath,
		DataDir:         t.opts.DataDir,
		NoAutoSocksPort: true,
		EnableNetwork:   true,
		ExtraArgs:       []string{"--SocksPort", t.opts.SocksAddr + " IsolateSOCKSAuth"},
	}
	if conf.DataDir == "" {
		conf.TempDataDirBase = tempDir()
	}
	p, err := tor.Start(ctx, conf)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.startErr = fmt.Errorf("netx: launch tor: %w", err)
		t.log.Warn("tor launch failed", "err", SanitizeError(err))
		return
	}
	if t.closed {
		_ = p.Close()
		return
	}
	t.proc = p
	t.log.Info("tor launched", "socks", t.opts.SocksAddr)
}

// Circuits returns the isolated identities (stable order and IDs).
func (t *Tor) Circuits() []*TorCircuit { return t.circuits }

// NewIdentity sends SIGNAL NEWNYM: tor stops using its current circuits for
// new streams. Calls closer than TorNewnymInterval are coalesced.
func (t *Tor) NewIdentity(ctx context.Context) error {
	select {
	case <-t.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.startErr != nil {
		return t.startErr
	}
	if time.Since(t.lastNewnym) < TorNewnymInterval {
		return nil
	}
	var err error
	if t.proc != nil {
		err = t.proc.Control.Signal("NEWNYM")
	} else {
		err = t.signalAttached(ctx, "NEWNYM")
	}
	if err != nil {
		return fmt.Errorf("netx: tor NEWNYM: %w", err)
	}
	t.lastNewnym = time.Now()
	return nil
}

// hasControl reports whether NEWNYM can be sent.
func (t *Tor) hasControl() bool { return t.opts.TorPath != "" || t.opts.ControlAddr != "-" }

func (t *Tor) signalAttached(ctx context.Context, sig string) error {
	if t.opts.ControlAddr == "-" {
		return errors.New("control port disabled")
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", t.opts.ControlAddr)
	if err != nil {
		return err
	}
	defer nc.Close()
	deadline := time.Now().Add(15 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = nc.SetDeadline(deadline)
	c := control.NewConn(textproto.NewConn(nc))
	defer c.Close()
	if err := c.Authenticate(t.opts.ControlPassword); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	if err := c.Signal(sig); err != nil {
		return err
	}
	_ = c.Quit()
	return nil
}

func (t *Tor) socksReady(ctx context.Context) error {
	select {
	case <-t.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startErr
}

// Close stops a launched tor.
func (t *Tor) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.proc != nil {
		err := t.proc.Close()
		t.proc = nil
		return err
	}
	return nil
}

// TorCircuit is one isolated Tor identity (a per-connection egress).
type TorCircuit struct {
	healthState
	t       *Tor
	id      string
	idx     int
	forward net.Dialer

	mu         sync.Mutex
	user, pass string
}

var _ Egress = (*TorCircuit)(nil)

func (c *TorCircuit) newCreds() {
	var b [16]byte
	_, _ = rand.Read(b[:])
	c.mu.Lock()
	c.user = "adesc-" + strconv.Itoa(c.idx) + "-" + hex.EncodeToString(b[:8])
	c.pass = hex.EncodeToString(b[8:])
	c.mu.Unlock()
}

// SOCKSUser returns the current isolation username (not a secret; tests and
// diagnostics use it).
func (c *TorCircuit) SOCKSUser() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.user
}

// ID implements Egress.
func (c *TorCircuit) ID() string { return c.id }

// Kind implements Egress.
func (c *TorCircuit) Kind() Kind { return KindTor }

// Scope implements Egress.
func (c *TorCircuit) Scope() Scope { return ScopePerConnection }

// Label implements Labeler.
func (c *TorCircuit) Label() string { return "Tor #" + strconv.Itoa(c.idx+1) }

// DialContext implements Egress through tor's SOCKS port with this
// circuit's isolation credentials (remote DNS).
func (c *TorCircuit) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := c.t.socksReady(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	auth := &proxy.Auth{User: c.user, Password: c.pass}
	c.mu.Unlock()
	return dialSOCKS5(ctx, &c.forward, c.t.opts.SocksAddr, auth, network, addr)
}

// Rotate implements Egress: the circuit gets new isolation credentials (so
// new streams build a fresh circuit, without touching other circuits'
// streams) and tor is sent NEWNYM when a control port is available.
// NEWNYM failures are logged, not returned: the credential switch already
// gives a new circuit.
func (c *TorCircuit) Rotate(ctx context.Context) error {
	c.newCreds()
	if c.t.hasControl() {
		if err := c.t.NewIdentity(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.t.log.Warn("tor NEWNYM failed; using new circuit credentials only", "egress", c.id, "err", SanitizeError(err))
		}
	}
	return nil
}

// PublicIP implements Egress.
func (c *TorCircuit) PublicIP(ctx context.Context) (netip.Addr, error) { return PublicIP(ctx, c) }

// Close implements Egress (the group is closed with Tor.Close).
func (c *TorCircuit) Close() error { return nil }
