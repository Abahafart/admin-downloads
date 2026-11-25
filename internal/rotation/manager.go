package rotation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/netip"
	"sort"
	"sync"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/store"
)

// Errors returned by the Rotator.
var (
	ErrClosed        = errors.New("rotation: manager closed")
	ErrUnknownEgress = errors.New("rotation: unknown egress")
	ErrNoEgress      = errors.New("rotation: no egress configured")
	ErrExists        = errors.New("rotation: egress already exists")
	ErrNotImported   = errors.New("rotation: egress is defined in config.toml and cannot be removed from the API")
)

// Hooks are provided by the engine (core.Engine.PauseAll / ResumeAll) and
// run around a global rotation. They can be set after construction with
// SetHooks, since the engine is usually built after the manager.
type Hooks struct {
	PauseAll  func(ctx context.Context) error
	ResumeAll func(ctx context.Context) error
	// Changed, if set, is called (without locks held) when the status of an
	// egress changed (health, quota marks, rotation), e.g. to emit an SSE
	// "egress.updated" event. It must not block.
	Changed func(egressID string)
}

// Settings tunes the Rotator (see SettingsFromConfig).
type Settings struct {
	// Enabled = false pins every download to the direct (or first) egress.
	Enabled  bool
	Strategy string // config.Strategy*
	// MaxPerEgress is the default number of simultaneous downloads per
	// egress (0 = unlimited).
	MaxPerEgress      int
	GlobalRotation    bool
	MinGlobalInterval time.Duration
	VerifyIPChange    bool
	IPServices        []string // empty = netx.DefaultIPEchoServices
	DefaultRetryAfter time.Duration
	HosterRetryAfter  map[string]time.Duration
	// HealthInterval between full health checks (0 = only after failures).
	HealthInterval time.Duration
	DedupeByIP     bool
	// BusyWait is how long Acquire blocks when every usable egress is at
	// its concurrency limit before answering with a short WaitError.
	BusyWait time.Duration
	// GlobalTimeout bounds the wait for the new public IP after a global
	// rotation.
	GlobalTimeout time.Duration
	// MinRotateInterval between two automatic rotations of the same
	// per-connection egress (Tor NEWNYM, sticky session, WireGuard conf).
	MinRotateInterval time.Duration
}

// SettingsFromConfig maps config.toml to Settings.
func SettingsFromConfig(cfg *config.Config) Settings {
	s := Settings{
		Enabled:           cfg.Rotation.Enabled,
		Strategy:          cfg.Rotation.Strategy,
		MaxPerEgress:      cfg.Downloads.MaxPerEgress,
		GlobalRotation:    cfg.Rotation.GlobalRotation,
		MinGlobalInterval: cfg.Rotation.MinGlobalInterval.Duration,
		VerifyIPChange:    cfg.Rotation.VerifyIPChange,
		IPServices:        cfg.Rotation.IPCheckServices,
		DefaultRetryAfter: cfg.Rotation.DefaultRetryAfter.Duration,
		HosterRetryAfter:  map[string]time.Duration{},
		HealthInterval:    cfg.Egress.HealthInterval.Duration,
		DedupeByIP:        cfg.Egress.Proxies.DedupeByIP,
	}
	for name, h := range cfg.Hosters {
		if h.RetryAfter.Duration > 0 {
			s.HosterRetryAfter[name] = h.RetryAfter.Duration
		}
	}
	if cfg.Egress.Router.Enabled && cfg.Egress.Router.Timeout.Duration > 0 {
		s.GlobalTimeout = cfg.Egress.Router.Timeout.Duration
	}
	return s
}

func (s *Settings) fill() {
	if s.Strategy == "" {
		s.Strategy = config.StrategyRoundRobin
	}
	if s.DefaultRetryAfter <= 0 {
		s.DefaultRetryAfter = time.Hour
	}
	if s.BusyWait <= 0 {
		s.BusyWait = 30 * time.Second
	}
	if s.GlobalTimeout <= 0 {
		s.GlobalTimeout = 2 * time.Minute
	}
	if s.MinRotateInterval <= 0 {
		s.MinRotateInterval = 15 * time.Second
	}
	if len(s.IPServices) == 0 {
		s.IPServices = netx.DefaultIPEchoServices
	}
}

// Options are the dependencies of a Rotator.
type Options struct {
	Logger *slog.Logger
	Hooks  Hooks
	// Store persists quota marks, imported egresses and health (optional).
	Store *store.Store
	// Secrets keeps proxy passwords and WireGuard keys of imported egresses
	// (an in-memory store is used if nil: imports then do not survive a
	// restart).
	Secrets accounts.SecretStore
	// Now is the clock (time.Now if nil); tests use a fake one.
	Now func() time.Time
	// Rand drives the random strategy (seeded randomly if nil).
	Rand *rand.Rand
	// WireGuard options for imported / configured tunnels.
	WireGuard netx.WireGuardOptions
}

// EgressOptions describe how the manager uses an egress.
type EgressOptions struct {
	Label  string // UI label (Labeler or ID if empty)
	Source string // store.EgressSource*; "" = not persisted
	// Address and Settings are stored in the egresses row (no secrets).
	Address  string
	Settings string
	// MaxConcurrent downloads (0 = Settings.MaxPerEgress).
	MaxConcurrent int
	// LastResort egresses (Tor) are only used when no other per-connection
	// egress is available.
	LastResort bool
}

type entry struct {
	e          netx.Egress
	order      int
	label      string
	source     string
	maxConc    int
	lastResort bool

	inUse      int
	pinned     int // leases of requests that forbid rotation
	lastUsed   time.Time
	healthy    bool
	publicIP   netip.Addr
	lastErr    string
	checkedAt  time.Time
	failures   int
	nextCheck  time.Time
	dupOf      string
	checking   bool
	rotating   bool
	lastRotate time.Time
	noRotate   bool // Rotate returned ErrRotateUnsupported
}

func (en *entry) global() bool { return en.e.Scope() == netx.ScopeGlobal }

func (en *entry) usable() bool { return en.healthy && en.dupOf == "" && !en.rotating }

// Rotator is the real rotation.Manager (the RotationManager of PLAN.md
// section 6). Create it with New (tests) or NewFromConfig (application).
type Rotator struct {
	s       Settings
	log     *slog.Logger
	st      *store.Store
	secrets accounts.SecretStore
	now     func() time.Time
	wgOpts  netx.WireGuardOptions
	book    *QuotaBook

	mu         sync.Mutex
	rnd        *rand.Rand
	hooks      Hooks
	entries    map[string]*entry
	order      []*entry // per-connection first, then global, in Add order
	nextOrder  int
	rr         map[string]int // round-robin cursor (entry order) per hoster
	changed    chan struct{}  // closed and replaced on every state change
	closers    []io.Closer
	global     *globalCall
	lastGlobal time.Time
	closed     bool
	started    bool

	bg     context.Context // detached from callers; canceled by Close
	cancel context.CancelFunc
	stop   chan struct{}
	wg     sync.WaitGroup
}

type globalCall struct {
	done chan struct{}
	err  error
}

var _ Manager = (*Rotator)(nil)

// New returns an empty manager; add egresses with Add, then call Start to
// run the health checker.
func New(ctx context.Context, s Settings, opts Options) (*Rotator, error) {
	s.fill()
	switch s.Strategy {
	case config.StrategyRoundRobin, config.StrategyLeastUsed, config.StrategyRandom, config.StrategyFixed:
	default:
		return nil, fmt.Errorf("rotation: unknown strategy %q", s.Strategy)
	}
	m := &Rotator{
		s:       s,
		log:     opts.Logger,
		st:      opts.Store,
		secrets: opts.Secrets,
		now:     opts.Now,
		wgOpts:  opts.WireGuard,
		rnd:     opts.Rand,
		hooks:   opts.Hooks,
		entries: map[string]*entry{},
		rr:      map[string]int{},
		changed: make(chan struct{}),
		stop:    make(chan struct{}),
	}
	m.bg, m.cancel = context.WithCancel(context.WithoutCancel(ctx))
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.rnd == nil {
		m.rnd = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // G404: egress selection strategy, not security sensitive
	}
	if m.secrets == nil {
		m.secrets = accounts.NewMemoryStore()
	}
	if m.wgOpts.Logger == nil {
		m.wgOpts.Logger = m.log
	}
	var qs QuotaStore
	if m.st != nil {
		qs = m.st
	}
	book, err := NewQuotaBook(ctx, qs, m.now)
	if err != nil {
		return nil, err
	}
	m.book = book
	return m, nil
}

// Book exposes the QuotaBook (account and file marks are recorded there by
// the engine / account manager).
func (m *Rotator) Book() *QuotaBook { return m.book }

// Settings returns the effective settings.
func (m *Rotator) Settings() Settings { return m.s }

// SetHooks replaces the hooks (the engine is built after the manager).
func (m *Rotator) SetHooks(h Hooks) {
	m.mu.Lock()
	m.hooks = h
	m.mu.Unlock()
}

// AddCloser registers a resource closed with the manager (Tor group...).
func (m *Rotator) AddCloser(c io.Closer) {
	m.mu.Lock()
	m.closers = append(m.closers, c)
	m.mu.Unlock()
}

// Add registers an egress. IDs must be unique.
func (m *Rotator) Add(ctx context.Context, e netx.Egress, o EgressOptions) error {
	if o.Label == "" {
		if l, ok := e.(netx.Labeler); ok {
			o.Label = l.Label()
		} else {
			o.Label = e.ID()
		}
	}
	maxConc := o.MaxConcurrent
	if maxConc == 0 {
		maxConc = m.s.MaxPerEgress
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if _, dup := m.entries[e.ID()]; dup {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrExists, e.ID())
	}
	en := &entry{e: e, order: m.nextOrder, label: o.Label, source: o.Source, maxConc: maxConc, lastResort: o.LastResort, healthy: e.Healthy()}
	m.nextOrder++
	m.entries[e.ID()] = en
	m.order = append(m.order, en)
	sort.SliceStable(m.order, func(i, j int) bool {
		gi, gj := m.order[i].global(), m.order[j].global()
		if gi != gj {
			return !gi
		}
		return m.order[i].order < m.order[j].order
	})
	m.notifyLocked()
	m.mu.Unlock()
	if m.st != nil && o.Source != "" {
		row := store.EgressRow{
			ID: e.ID(), Kind: string(e.Kind()), Label: o.Label, Source: o.Source,
			Address: o.Address, Settings: o.Settings, Enabled: true, Healthy: true,
		}
		if o.Source == store.EgressSourceImport {
			// Import rows are written by the importer (with secret_ref).
			return nil
		}
		if row.Address == "" {
			row.Address = o.Label
		}
		if err := m.st.UpsertEgress(ctx, row); err != nil {
			m.log.Warn("rotation: persist egress", "egress", e.ID(), "err", err)
		}
	}
	return nil
}

// remove unregisters an egress and closes it.
func (m *Rotator) remove(id string) (*entry, bool) {
	m.mu.Lock()
	en, ok := m.entries[id]
	if ok {
		delete(m.entries, id)
		for i, o := range m.order {
			if o == en {
				m.order = append(m.order[:i:i], m.order[i+1:]...)
				break
			}
		}
		m.notifyLocked()
	}
	m.mu.Unlock()
	if ok {
		_ = en.e.Close()
	}
	return en, ok
}

// notifyLocked wakes Acquire calls waiting for capacity.
func (m *Rotator) notifyLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *Rotator) emit(ids ...string) {
	m.mu.Lock()
	fn := m.hooks.Changed
	m.mu.Unlock()
	if fn == nil {
		return
	}
	for _, id := range ids {
		fn(id)
	}
}

// Close stops the health checker and closes every egress.
func (m *Rotator) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.stop)
	m.cancel()
	entries := append([]*entry(nil), m.order...)
	closers := m.closers
	m.notifyLocked()
	m.mu.Unlock()
	m.wg.Wait()
	var errs []error
	for _, en := range entries {
		errs = append(errs, en.e.Close())
	}
	for _, c := range closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// --- Acquire -----------------------------------------------------------

// selection is the outcome of one selection pass.
type selection struct {
	en       *entry
	busy     bool      // some non-exhausted egress is at its limit
	earliest time.Time // earliest quota deadline among exhausted egresses
	recheck  time.Time // earliest scheduled health recheck
	reason   string
	none     bool // no per-connection egress at all
}

func (m *Rotator) retryAfter(hoster string, d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	if v, ok := m.s.HosterRetryAfter[hoster]; ok && v > 0 {
		return v
	}
	return m.s.DefaultRetryAfter
}

// Acquire implements rotation.Manager.
func (m *Rotator) Acquire(ctx context.Context, req Request) (netx.Egress, error) {
	var busySince time.Time
	rotateTried := map[string]bool{}
	globalTried := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, ErrClosed
		}
		now := m.now()
		var sel selection
		var err error
		pinnedMode := !req.AllowRotation || !m.s.Enabled
		if pinnedMode {
			sel, err = m.selectPinnedLocked(req, now)
		} else {
			sel = m.selectLocked(req, now)
		}
		if err == nil && sel.none {
			err = ErrNoEgress
		}
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if sel.en != nil {
			m.leaseLocked(sel.en, req, pinnedMode, now)
			e := sel.en.e
			m.mu.Unlock()
			m.emit(e.ID())
			return e, nil
		}
		waitCh := m.changed
		m.mu.Unlock()

		if sel.busy {
			if busySince.IsZero() {
				busySince = time.Now()
			}
			left := m.s.BusyWait - time.Since(busySince)
			if left <= 0 {
				return nil, &WaitError{Until: now.Add(10 * time.Second), Reason: "todas las salidas están ocupadas"}
			}
			t := time.NewTimer(left)
			select {
			case <-waitCh:
			case <-t.C:
			case <-ctx.Done():
			}
			t.Stop()
			continue
		}
		if !pinnedMode && rotationHelps(req.QuotaScope) {
			if m.tryRotateIdle(ctx, req, rotateTried) {
				continue
			}
			if !globalTried {
				globalTried = true
				ok, reason := m.tryGlobal(ctx, req)
				if ok {
					continue
				}
				if reason != "" {
					sel.reason += "; " + reason
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
		}
		return nil, m.waitError(sel, now)
	}
}

func rotationHelps(s hosters.QuotaScope) bool { return s == hosters.ScopeNone || s == hosters.ScopeIP }

func (m *Rotator) waitError(sel selection, now time.Time) *WaitError {
	until := sel.earliest
	if !sel.recheck.IsZero() && (until.IsZero() || sel.recheck.Before(until)) {
		until = sel.recheck
	}
	if until.IsZero() || until.Before(now) {
		until = now.Add(time.Minute)
	}
	reason := sel.reason
	if reason == "" {
		reason = "no hay ninguna salida disponible"
	}
	return &WaitError{Until: until, Reason: reason}
}

func (m *Rotator) leaseLocked(en *entry, req Request, pinned bool, now time.Time) {
	en.inUse++
	if pinned {
		en.pinned++
	}
	en.lastUsed = now
	m.rr[req.Hoster] = en.order
}

// selectPinnedLocked serves requests that must not rotate: PreferEgress if
// it exists, else direct, else the first per-connection egress.
func (m *Rotator) selectPinnedLocked(req Request, now time.Time) (selection, error) {
	var en *entry
	if req.PreferEgress != "" {
		if c, ok := m.entries[req.PreferEgress]; ok && !c.global() {
			en = c
		}
	}
	if en == nil {
		en = m.entries[netx.DirectID]
	}
	if en == nil {
		for _, c := range m.order {
			if !c.global() && !c.lastResort {
				en = c
				break
			}
		}
	}
	if en == nil {
		for _, c := range m.order {
			if !c.global() {
				en = c
				break
			}
		}
	}
	if en == nil {
		return selection{}, ErrNoEgress
	}
	// Downloads with an account (which forbids rotation) are not held by
	// IP marks: those come mostly from anonymous downloads, while account
	// quotas are per account and tracked by the accounts manager. If the
	// hoster does answer with an IP quota, the engine sees it again on the
	// same egress and waits.
	if until := m.book.Until(req.Hoster, hosters.ScopeIP, en.e.ID()); !until.IsZero() && req.AccountID == "" {
		return selection{earliest: until, reason: fmt.Sprintf("cuota de %s agotada en %s (rotación de IP desactivada)", req.Hoster, en.label)}, nil
	}
	if !en.healthy && en.e.ID() != netx.DirectID {
		return selection{recheck: en.nextCheck, reason: fmt.Sprintf("la salida %s no responde (rotación de IP desactivada)", en.label)}, nil
	}
	if en.maxConc > 0 && en.inUse >= en.maxConc {
		return selection{busy: true}, nil
	}
	return selection{en: en}, nil
}

// selectLocked applies PLAN.md section 6 step 3: a healthy, non-exhausted
// per-connection egress with free capacity, chosen by the strategy.
func (m *Rotator) selectLocked(req Request, now time.Time) selection {
	var sel selection
	var normal, last []*entry
	busyNormal := false
	unhealthy := 0
	exhausted := 0
	total := 0
	for _, en := range m.order {
		if en.global() {
			continue
		}
		total++
		if until := m.book.Until(req.Hoster, hosters.ScopeIP, en.e.ID()); !until.IsZero() {
			exhausted++
			if sel.earliest.IsZero() || until.Before(sel.earliest) {
				sel.earliest = until
			}
			continue
		}
		if !en.usable() && en.e.ID() == netx.DirectID && !en.healthy && en.dupOf == "" && !en.rotating &&
			(en.maxConc == 0 || en.inUse < en.maxConc) {
			// A failed health check of the direct connection usually means
			// the IP echo services are unreachable, not that the machine is
			// offline: keep it as a last resort instead of refusing every
			// download (a real outage surfaces as retried network errors).
			last = append(last, en)
			continue
		}
		if !en.usable() {
			unhealthy++
			if !en.nextCheck.IsZero() && (sel.recheck.IsZero() || en.nextCheck.Before(sel.recheck)) {
				sel.recheck = en.nextCheck
			}
			continue
		}
		if en.maxConc > 0 && en.inUse >= en.maxConc {
			if !en.lastResort {
				busyNormal = true
			}
			sel.busy = true
			continue
		}
		if en.lastResort {
			last = append(last, en)
		} else {
			normal = append(normal, en)
		}
	}
	cands := normal
	if len(cands) == 0 && !busyNormal {
		cands = last
	}
	if len(cands) > 0 {
		sel.busy = false
		sel.en = m.pickLocked(req, cands)
		return sel
	}
	if busyNormal {
		sel.busy = true
		return sel
	}
	switch {
	case total == 0:
		sel.none = true
	case exhausted == total:
		sel.reason = fmt.Sprintf("todas las salidas han agotado la cuota de %s", req.Hoster)
	case exhausted > 0:
		sel.reason = fmt.Sprintf("%d salidas con la cuota de %s agotada y %d sin conexión", exhausted, req.Hoster, unhealthy)
	default:
		sel.reason = "ninguna salida responde"
	}
	return sel
}

// pickLocked chooses among candidates (sorted by order) with the strategy;
// PreferEgress wins when it is a candidate (resume on the same IP).
func (m *Rotator) pickLocked(req Request, cands []*entry) *entry {
	if req.PreferEgress != "" {
		for _, c := range cands {
			if c.e.ID() == req.PreferEgress {
				return c
			}
		}
	}
	switch m.s.Strategy {
	case config.StrategyFixed:
		return cands[0]
	case config.StrategyRandom:
		return cands[m.rnd.IntN(len(cands))]
	case config.StrategyLeastUsed:
		best := cands[0]
		for _, c := range cands[1:] {
			if c.inUse < best.inUse || (c.inUse == best.inUse && c.lastUsed.Before(best.lastUsed)) {
				best = c
			}
		}
		return best
	default: // round-robin
		cur, ok := m.rr[req.Hoster]
		if !ok {
			return cands[0]
		}
		for _, c := range cands {
			if c.order > cur {
				return c
			}
		}
		return cands[0]
	}
}

// Release implements rotation.Manager.
func (m *Rotator) Release(hoster, egressID string) {
	m.mu.Lock()
	en, ok := m.entries[egressID]
	if ok && en.inUse > 0 {
		en.inUse--
		if en.pinned > en.inUse {
			en.pinned = en.inUse
		}
		m.notifyLocked()
	}
	m.mu.Unlock()
	if ok {
		m.emit(egressID)
	}
}

// --- Quota & failures ----------------------------------------------------

// ReportQuota implements rotation.Manager. Only IP-scoped quotas (and
// unknown scopes, conservatively) mark the egress; account and file quotas
// are not lifted by another IP and are recorded with ReportSubjectQuota.
func (m *Rotator) ReportQuota(ctx context.Context, hoster, egressID string, scope hosters.QuotaScope, retryAfter time.Duration) {
	if scope != hosters.ScopeIP && scope != hosters.ScopeNone {
		m.log.Debug("rotation: non-IP quota ignored for egress selection", "hoster", hoster, "egress", egressID, "scope", scope.String())
		return
	}
	d := m.retryAfter(hoster, retryAfter)
	until := m.now().Add(d)
	if err := m.book.Mark(ctx, hoster, hosters.ScopeIP, egressID, until, "quota"); err != nil {
		m.log.Warn("rotation: persist quota mark", "hoster", hoster, "egress", egressID, "err", err)
	}
	m.log.Info("rotation: egress over quota", "hoster", hoster, "egress", egressID, "until", until.Format(time.RFC3339))
	m.mu.Lock()
	m.notifyLocked()
	m.mu.Unlock()
	m.emit(egressID)
}

// ReportSubjectQuota records an account (subject = account ID) or file
// (subject = hoster file ID) quota in the QuotaBook.
func (m *Rotator) ReportSubjectQuota(ctx context.Context, hoster string, scope hosters.QuotaScope, subject string, retryAfter time.Duration) error {
	until := m.now().Add(m.retryAfter(hoster, retryAfter))
	return m.book.Mark(ctx, hoster, scope, subject, until, "quota")
}

// ClearQuota removes the IP quota mark of (hoster, egressID); hoster ""
// clears every hoster.
func (m *Rotator) ClearQuota(ctx context.Context, hoster, egressID string) error {
	var err error
	if hoster == "" {
		err = m.book.ClearSubject(ctx, hosters.ScopeIP, egressID)
	} else {
		err = m.book.Clear(ctx, hoster, hosters.ScopeIP, egressID)
	}
	m.mu.Lock()
	m.notifyLocked()
	m.mu.Unlock()
	m.emit(egressID)
	return err
}

func failureBackoff(n int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < n && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}

// ReportFailure implements rotation.Manager. Non-direct egresses are marked
// unhealthy at the first failure, direct after three in a row; a recheck is
// scheduled with exponential backoff.
func (m *Rotator) ReportFailure(egressID string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	m.mu.Lock()
	en, ok := m.entries[egressID]
	if !ok {
		m.mu.Unlock()
		return
	}
	en.failures++
	threshold := 1
	if egressID == netx.DirectID {
		threshold = 3
	}
	msg := netx.SanitizeError(err)
	en.lastErr = msg
	marked := false
	if en.failures >= threshold && en.healthy {
		en.healthy = false
		marked = true
	}
	if !en.healthy {
		en.nextCheck = m.now().Add(failureBackoff(en.failures - threshold + 1))
	}
	m.notifyLocked()
	m.mu.Unlock()
	if marked {
		netx.MarkUnhealthy(en.e, err)
		m.log.Warn("rotation: egress marked unhealthy", "egress", egressID, "err", msg)
		m.persistHealth(en)
	}
	m.emit(egressID)
}

// --- Per-connection rotation (step 3b) -----------------------------------

// tryRotateIdle rotates one idle, exhausted, rotatable per-connection egress
// (Tor circuit, sticky session, WireGuard group with a spare conf) and
// reports whether it now has a new exit IP.
func (m *Rotator) tryRotateIdle(ctx context.Context, req Request, tried map[string]bool) bool {
	for range 3 {
		m.mu.Lock()
		now := m.now()
		var pick *entry
		for _, en := range m.order {
			if en.global() || en.noRotate || en.rotating || en.inUse > 0 || !en.healthy || en.dupOf != "" || tried[en.e.ID()] {
				continue
			}
			if now.Sub(en.lastRotate) < m.s.MinRotateInterval {
				continue
			}
			if !m.book.Exhausted(req.Hoster, hosters.ScopeIP, en.e.ID()) {
				continue
			}
			if pick == nil || (pick.lastResort && !en.lastResort) {
				pick = en
			}
		}
		if pick == nil {
			m.mu.Unlock()
			return false
		}
		tried[pick.e.ID()] = true
		m.mu.Unlock()
		if m.rotateOne(ctx, pick, false) == nil {
			return true
		}
	}
	return false
}

// rotateOne rotates a per-connection egress, verifies the exit IP changed
// (when VerifyIPChange) and clears its IP quota marks.
func (m *Rotator) rotateOne(ctx context.Context, en *entry, force bool) error {
	m.mu.Lock()
	if en.rotating {
		m.mu.Unlock()
		return errors.New("rotation: rotation already in progress")
	}
	en.rotating = true
	old := en.publicIP
	m.mu.Unlock()

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	id := en.e.ID()
	err := en.e.Rotate(rctx)
	var h netx.Health
	if err == nil && m.s.VerifyIPChange {
		h = netx.CheckHealth(rctx, en.e, m.s.IPServices)
		switch {
		case !h.Healthy:
			err = fmt.Errorf("rotation: %s unreachable after rotation: %s", id, h.LastError)
		case old.IsValid() && h.PublicIP == old && !force:
			err = fmt.Errorf("%w (%s)", netx.ErrIPUnchanged, id)
		}
	}

	m.mu.Lock()
	en.rotating = false
	en.lastRotate = m.now()
	if errors.Is(err, netx.ErrRotateUnsupported) {
		en.noRotate = true
	}
	if !h.CheckedAt.IsZero() {
		m.applyHealthLocked(en, h)
	}
	m.notifyLocked()
	m.mu.Unlock()

	if err != nil {
		if !errors.Is(err, netx.ErrRotateUnsupported) {
			m.log.Info("rotation: egress rotation did not help", "egress", id, "err", netx.SanitizeError(err))
		}
		m.emit(id)
		return err
	}
	if cerr := m.book.ClearSubject(rctx, hosters.ScopeIP, id); cerr != nil {
		m.log.Warn("rotation: clear quota marks", "egress", id, "err", cerr)
	}
	m.log.Info("rotation: egress rotated", "egress", id, "ip", FormatIP(h.PublicIP))
	m.persistHealth(en)
	m.emit(id)
	return nil
}

// --- Global rotation (step 4) --------------------------------------------

func (m *Rotator) globals() []*entry {
	var out []*entry
	for _, en := range m.order {
		if en.global() {
			out = append(out, en)
		}
	}
	return out
}

// tryGlobal runs (or joins) a global rotation when it may lift the quota of
// req.Hoster on the direct egress. It returns true on success; otherwise a
// reason for the WaitError ("" when global rotation does not apply).
func (m *Rotator) tryGlobal(ctx context.Context, req Request) (bool, string) {
	m.mu.Lock()
	gl := m.globals()
	direct := m.entries[netx.DirectID]
	if !m.s.GlobalRotation || len(gl) == 0 || direct == nil {
		m.mu.Unlock()
		return false, ""
	}
	if !m.book.Exhausted(req.Hoster, hosters.ScopeIP, netx.DirectID) {
		m.mu.Unlock()
		return false, ""
	}
	if direct.pinned > 0 {
		m.mu.Unlock()
		return false, "reconexión global aplazada: hay descargas con cuenta que no permiten cambiar la IP"
	}
	now := m.now()
	call := m.global
	if call == nil {
		if next := m.lastGlobal.Add(m.s.MinGlobalInterval); !m.lastGlobal.IsZero() && now.Before(next) {
			m.mu.Unlock()
			return false, "próxima reconexión global a las " + next.Local().Format("15:04:05")
		}
		call = m.startGlobalLocked(ctx, gl)
	}
	m.mu.Unlock()
	select {
	case <-call.done:
	case <-ctx.Done():
		return false, ""
	}
	if call.err != nil {
		return false, "la reconexión global falló: " + netx.SanitizeError(call.err)
	}
	return true, ""
}

func (m *Rotator) startGlobalLocked(ctx context.Context, gl []*entry) *globalCall {
	call := &globalCall{done: make(chan struct{})}
	m.global = call
	hooks := m.hooks
	// Detached: the caller's download may be paused (its context canceled)
	// by PauseAll, but a router must never be left disconnected half-way.
	gctx := context.WithoutCancel(ctx)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		call.err = m.runGlobal(gctx, gl, hooks)
		m.mu.Lock()
		m.global = nil
		m.lastGlobal = m.now()
		m.notifyLocked()
		m.mu.Unlock()
		close(call.done)
	}()
	return call
}

// runGlobal: pause every download, rotate the first global egress that
// works, verify the public IP changed, resume (PLAN.md section 6 step 4).
func (m *Rotator) runGlobal(ctx context.Context, gl []*entry, hooks Hooks) error {
	m.mu.Lock()
	direct := m.entries[netx.DirectID]
	var old netip.Addr
	if direct != nil {
		old = direct.publicIP
	}
	m.mu.Unlock()
	client := netx.NewHTTPClient(netx.NewDirect(), nil)
	defer client.CloseIdleConnections()
	if !old.IsValid() && m.s.VerifyIPChange {
		qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		old, _ = netx.ConfirmedPublicIP(qctx, client, m.s.IPServices)
		cancel()
	}

	if hooks.PauseAll != nil {
		if err := hooks.PauseAll(ctx); err != nil {
			return fmt.Errorf("rotation: pause downloads: %w", err)
		}
	}
	defer func() {
		if hooks.ResumeAll != nil {
			rctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if err := hooks.ResumeAll(rctx); err != nil {
				m.log.Error("rotation: resume downloads after global rotation", "err", err)
			}
		}
	}()

	var errs []error
	for _, g := range gl {
		id := g.e.ID()
		timeout := m.s.GlobalTimeout
		if t, ok := g.e.(interface{ Timeout() time.Duration }); ok && t.Timeout() > 0 {
			timeout = t.Timeout()
		}
		m.log.Info("rotation: global rotation", "egress", id, "old_ip", FormatIP(old))
		rctx, cancel := context.WithTimeout(ctx, timeout)
		err := g.e.Rotate(rctx)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			m.setGlobalHealth(g, err)
			continue
		}
		var ip netip.Addr
		if m.s.VerifyIPChange {
			ip, err = netx.WaitForIPChange(ctx, client, m.s.IPServices, old, m.s.GlobalTimeout, 3*time.Second)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
				m.setGlobalHealth(g, err)
				continue
			}
		}
		m.setGlobalHealth(g, nil)
		if err := m.book.ClearSubject(ctx, hosters.ScopeIP, netx.DirectID); err != nil {
			m.log.Warn("rotation: clear quota marks", "egress", netx.DirectID, "err", err)
		}
		if direct != nil {
			m.mu.Lock()
			if ip.IsValid() {
				direct.publicIP = ip
			}
			direct.healthy, direct.failures, direct.lastErr = true, 0, ""
			m.mu.Unlock()
		}
		m.log.Info("rotation: global rotation done", "egress", id, "new_ip", FormatIP(ip))
		m.emit(id, netx.DirectID)
		return nil
	}
	return errors.Join(errs...)
}

func (m *Rotator) setGlobalHealth(g *entry, err error) {
	m.mu.Lock()
	g.checkedAt = m.now()
	if err != nil {
		g.lastErr = netx.SanitizeError(err)
		g.healthy = false
	} else {
		g.lastErr = ""
		g.healthy = true
	}
	m.mu.Unlock()
	m.emit(g.e.ID())
}

// Rotate implements rotation.Manager: "" runs the global rotation now
// (ignoring the minimum interval), a global egress ID rotates through that
// egress only, any other ID rotates that per-connection egress.
func (m *Rotator) Rotate(ctx context.Context, egressID string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	var gl []*entry
	if egressID == "" {
		gl = m.globals()
		if len(gl) == 0 {
			m.mu.Unlock()
			return fmt.Errorf("%w: no global egress (router / system VPN) configured", netx.ErrRotateUnsupported)
		}
	} else {
		en, ok := m.entries[egressID]
		if !ok {
			m.mu.Unlock()
			return fmt.Errorf("%w: %s", ErrUnknownEgress, egressID)
		}
		if !en.global() {
			m.mu.Unlock()
			return m.rotateOne(ctx, en, true)
		}
		gl = []*entry{en}
	}
	call := m.global
	if call == nil {
		call = m.startGlobalLocked(ctx, gl)
	}
	m.mu.Unlock()
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- Status -------------------------------------------------------------

// Get implements rotation.Manager.
func (m *Rotator) Get(egressID string) netx.Egress {
	m.mu.Lock()
	defer m.mu.Unlock()
	if en, ok := m.entries[egressID]; ok {
		return en.e
	}
	return nil
}

// Egresses implements rotation.Manager.
func (m *Rotator) Egresses() []EgressStatus {
	m.mu.Lock()
	entries := append([]*entry(nil), m.order...)
	out := make([]EgressStatus, 0, len(entries))
	for _, en := range entries {
		out = append(out, m.statusLocked(en))
	}
	m.mu.Unlock()
	for i := range out {
		out[i].Exhausted = m.book.Subject(hosters.ScopeIP, out[i].ID)
	}
	return out
}

func (m *Rotator) statusLocked(en *entry) EgressStatus {
	st := EgressStatus{
		ID:        en.e.ID(),
		Kind:      en.e.Kind(),
		Scope:     en.e.Scope(),
		Label:     en.label,
		Healthy:   en.healthy && en.dupOf == "",
		PublicIP:  FormatIP(en.publicIP),
		InUse:     en.inUse,
		LastError: en.lastErr,
		CheckedAt: en.checkedAt,
	}
	if en.dupOf != "" {
		st.LastError = "misma IP de salida que " + en.dupOf
	}
	return st
}

// Status returns the status of one egress.
func (m *Rotator) Status(egressID string) (EgressStatus, error) {
	m.mu.Lock()
	en, ok := m.entries[egressID]
	if !ok {
		m.mu.Unlock()
		return EgressStatus{}, fmt.Errorf("%w: %s", ErrUnknownEgress, egressID)
	}
	st := m.statusLocked(en)
	m.mu.Unlock()
	st.Exhausted = m.book.Subject(hosters.ScopeIP, egressID)
	return st, nil
}

// EgressNote returns a UI note for an egress kind (Spanish), or "".
func EgressNote(k netx.Kind) string {
	switch k {
	case netx.KindTor:
		return netx.TorNote
	case netx.KindRouter, netx.KindSystemVPN:
		return "Cambia la IP de todo el equipo: se pausan todas las descargas mientras reconecta."
	}
	return ""
}
