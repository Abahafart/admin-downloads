package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/rotation"
	"admindescargas/internal/store"
)

// Deps are the collaborators of the engine. Store, Registry and Config are
// required; the rest is optional.
type Deps struct {
	Store    *store.Store
	Registry *hosters.Registry
	// Rotation hands out egresses (nil = rotation.NewStatic(nil), direct).
	Rotation rotation.Manager
	// Secrets is given to hoster sessions that use an account.
	Secrets accounts.SecretStore
	Config  *config.Config
	Logger  *slog.Logger
	// Accounts picks accounts per hoster (nil = anonymous only).
	Accounts AccountPicker
	// PostProcessor runs after each download (nil = built-in checksum
	// verification only).
	PostProcessor PostProcessor
	// Bus receives events (nil = a new Bus with a 1024-event buffer).
	Bus *Bus
}

// Cancellation causes for jobs. The code that cancels a job is responsible
// for the download's state afterwards.
var (
	errPaused   = errors.New("core: paused")
	errPauseAll = errors.New("core: paused (all)")
	errStopping = errors.New("core: stopping")
	errDeleted  = errors.New("core: deleted")
	errRestart  = errors.New("core: restarting")
)

// Service is the implementation of Engine.
type Service struct {
	st       *store.Store
	reg      *hosters.Registry
	rot      rotation.Manager
	secrets  accounts.SecretStore
	cfg      *config.Config
	log      *slog.Logger
	accts    AccountPicker
	post     PostProcessor
	bus      *Bus
	globalRL *rate.Limiter
	now      func() time.Time

	// progressInterval is the period of progress/stats events.
	progressInterval time.Duration
	// scheduleInterval is the scheduler's polling period (waiting
	// countdowns, deferred downloads).
	scheduleInterval time.Duration

	mu        sync.Mutex
	running   bool
	stopping  bool
	pausedAll bool
	// rotationPaused counts SuspendForRotation calls not yet matched by
	// ResumeFromRotation (a global rotation is changing the machine IP).
	rotationPaused int
	jobs           map[int64]*job
	egressUse      map[string]int // egress id -> running jobs
	egressHost     map[string]int // egress id + "|" + hoster -> running jobs
	sessions       map[string]*hosters.Session
	notBefore      map[int64]time.Time
	dirtyPkgs      map[int64]struct{}
	resolvers      map[int64]context.CancelFunc
	resolveRL      map[string]*rate.Limiter

	bgCtx      context.Context
	bgCancel   context.CancelFunc
	bgWG       sync.WaitGroup
	jobWG      sync.WaitGroup
	kickCh     chan struct{}
	resolveSem chan struct{}
}

var _ Engine = (*Service)(nil)

// New builds the engine. Call Start to load state and begin scheduling.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Registry == nil {
		return nil, fmt.Errorf("%w: store and registry are required", ErrInvalidArgument)
	}
	cfg := d.Config
	if cfg == nil {
		cfg = config.Default()
	}
	rot := d.Rotation
	if rot == nil {
		rot = rotation.NewStatic(nil)
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	bus := d.Bus
	if bus == nil {
		bus = NewBus(1024)
	}
	s := &Service{
		st: d.Store, reg: d.Registry, rot: rot, secrets: d.Secrets, cfg: cfg,
		log: log.With("component", "engine"), accts: d.Accounts, post: d.PostProcessor, bus: bus,
		now:              time.Now,
		progressInterval: 500 * time.Millisecond,
		scheduleInterval: time.Second,
		jobs:             map[int64]*job{},
		egressUse:        map[string]int{},
		egressHost:       map[string]int{},
		sessions:         map[string]*hosters.Session{},
		notBefore:        map[int64]time.Time{},
		dirtyPkgs:        map[int64]struct{}{},
		resolvers:        map[int64]context.CancelFunc{},
		resolveRL:        map[string]*rate.Limiter{},
		kickCh:           make(chan struct{}, 1),
		resolveSem:       make(chan struct{}, 4),
	}
	if l := cfg.Downloads.SpeedLimit; l > 0 {
		s.globalRL = newLimiter(l)
	}
	return s, nil
}

func newLimiter(bytesPerSec int64) *rate.Limiter {
	burst := int64(64 << 10)
	if bytesPerSec < burst {
		burst = max(bytesPerSec, 1024)
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), int(burst))
}

// Bus returns the event bus (for front-ends that want raw access).
func (s *Service) Bus() *Bus { return s.bus }

// Start implements Engine.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("%w: engine already started", ErrInvalidState)
	}
	s.mu.Unlock()

	// Downloads that were being worked on when the process stopped.
	rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{States: []string{
		string(StateResolving), string(StateDownloading), string(StateVerifying), string(StatePostprocessing),
	}})
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		to := StateQueued
		if !s.cfg.Downloads.ResumeOnStart {
			to = StatePaused
		}
		if _, err := s.st.UpdateDownloadsState(ctx, ids, nil, string(to), false); err != nil {
			return err
		}
		s.log.Info("recovered interrupted downloads", "count", len(ids), "state", to)
	}

	s.mu.Lock()
	s.bgCtx, s.bgCancel = context.WithCancel(context.WithoutCancel(ctx))
	s.running, s.stopping = true, false
	s.mu.Unlock()

	// LinkGrabber entries whose listing was interrupted.
	entries, err := s.st.ListGrabberEntries(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.State == string(GrabberPending) || e.State == string(GrabberResolving) {
			s.startResolve(e.ID)
		}
	}

	s.bgWG.Add(2)
	go s.schedulerLoop()
	go s.progressLoop()
	s.kick()
	return nil
}

// Stop implements Engine.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.running || s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	jobs := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	for _, c := range s.resolvers {
		c()
	}
	s.mu.Unlock()

	for _, j := range jobs {
		j.cancel(errStopping)
	}
	err := s.waitJobs(ctx, jobs)
	s.bgCancel()
	done := make(chan struct{})
	go func() { s.bgWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	return err
}

// waitJobs waits until every job has exited (or ctx expires).
func (s *Service) waitJobs(ctx context.Context, jobs []*job) error {
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

func (s *Service) checkRunning() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.stopping {
		return ErrStopped
	}
	return nil
}

// Subscribe implements Engine.
func (s *Service) Subscribe(ctx context.Context) <-chan Event { return s.bus.Subscribe(ctx) }

// Stats implements Engine.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	counts, err := s.st.CountDownloadsByState(ctx)
	if err != nil {
		return Stats{}, err
	}
	return s.statsWithGrabber(ctx, counts)
}

// statsWithGrabber completes statsFrom with the LinkGrabber size.
func (s *Service) statsWithGrabber(ctx context.Context, counts map[string]int) (Stats, error) {
	n, err := s.st.CountGrabberEntries(ctx)
	if err != nil {
		return Stats{}, err
	}
	st := s.statsFrom(counts)
	st.GrabberSize = n
	return st, nil
}

func (s *Service) statsFrom(counts map[string]int) Stats {
	st := Stats{
		Queued:    counts[string(StateQueued)],
		Waiting:   counts[string(StateWaiting)],
		Failed:    counts[string(StateFailed)],
		Completed: counts[string(StateCompleted)],
		Active: counts[string(StateResolving)] + counts[string(StateDownloading)] +
			counts[string(StateVerifying)] + counts[string(StatePostprocessing)],
	}
	s.mu.Lock()
	st.Paused = s.pausedAll
	st.Rotating = s.rotationPaused > 0
	for _, j := range s.jobs {
		st.Speed += j.speedValue()
	}
	s.mu.Unlock()
	return st
}

// --- views ---

func (s *Service) downloadView(r store.DownloadRow) Download {
	d := Download{
		ID: r.ID, PackageID: r.PackageID, Hoster: r.Hoster, URL: r.URL, FileID: r.FileID, Name: r.Name,
		RelPath: r.RelPath, DestPath: r.DestPath, State: State(r.State), Size: r.Size, Done: r.DoneBytes,
		ETA: -1, Resumable: r.Resumable, Error: r.Error, ErrorKind: r.ErrorKind, QuotaScope: r.QuotaScope, Attempts: r.Attempts,
		EgressID: r.EgressID, PublicIP: r.PublicIP, AccountID: r.AccountID, WaitingUntil: r.WaitingUntil,
		Priority: r.Priority, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt,
	}
	if r.ChecksumAlgo != "" {
		d.Checksum = &hosters.Checksum{Algo: r.ChecksumAlgo, Value: r.ChecksumValue}
	}
	if d.State != StateWaiting {
		d.WaitingUntil = time.Time{}
		if d.State != StateFailed {
			d.QuotaScope = ""
		}
	}
	if d.State == StateCompleted && d.Size >= 0 {
		d.Done = d.Size
	}
	s.mu.Lock()
	j := s.jobs[r.ID]
	s.mu.Unlock()
	if j != nil {
		p := j.progress()
		if p.Done > d.Done {
			d.Done = p.Done
		}
		if p.Size >= 0 {
			d.Size = p.Size
		}
		d.Speed, d.ETA, d.Connections = p.Speed, p.ETA, p.Connections
	}
	if d.ETA < 0 && d.Speed > 0 && d.Size >= 0 {
		d.ETA = (d.Size - d.Done) / d.Speed
	}
	return d
}

func (s *Service) packageView(p store.PackageRow, dls []Download) Package {
	pk := Package{
		ID: p.ID, Name: p.Name, DestDir: p.DestDir, SourceURL: p.SourceURL, Hoster: p.Hoster,
		IsFolder: p.IsFolder, Priority: p.Priority, ExistsPolicy: p.ExistsPolicy, CreatedAt: p.CreatedAt,
		ETA: -1, FilesTotal: len(dls),
	}
	states := map[State]int{}
	unknownSize := false
	for _, d := range dls {
		states[d.State]++
		if d.Size >= 0 {
			pk.Size += d.Size
		} else {
			unknownSize = true
		}
		pk.Done += d.Done
		pk.Speed += d.Speed
		switch d.State {
		case StateCompleted:
			pk.FilesDone++
		case StateFailed:
			pk.FilesFail++
		}
	}
	pk.State = aggregateState(states, len(dls))
	if pk.Speed > 0 && !unknownSize {
		pk.ETA = max(pk.Size-pk.Done, 0) / pk.Speed
	}
	return pk
}

// aggregateState implements the package state rule: the common state if
// every file agrees; downloading if any file is being worked on; failed if
// the rest is finished; otherwise queued.
func aggregateState(states map[State]int, total int) State {
	if total == 0 {
		return StateCompleted
	}
	for st, n := range states {
		if n == total {
			return st
		}
	}
	for _, st := range []State{StateDownloading, StateResolving, StateVerifying, StatePostprocessing} {
		if states[st] > 0 {
			return StateDownloading
		}
	}
	if states[StateFailed] > 0 && states[StateFailed]+states[StateCompleted] == total {
		return StateFailed
	}
	if states[StateWaiting] > 0 && states[StateQueued] == 0 {
		return StateWaiting
	}
	return StateQueued
}

// publishDownload sends download.updated with the current row.
func (s *Service) publishDownload(ctx context.Context, id int64) {
	r, err := s.st.GetDownload(context.WithoutCancel(ctx), id)
	if err != nil {
		return
	}
	d := s.downloadView(r)
	s.bus.Publish(Event{Type: EventDownloadUpdated, Download: &d})
	s.markPackageDirty(r.PackageID)
}

func (s *Service) markPackageDirty(id int64) {
	s.mu.Lock()
	s.dirtyPkgs[id] = struct{}{}
	s.mu.Unlock()
}

func (s *Service) notice(level NoticeLevel, msg string) {
	s.bus.Publish(Event{Type: EventNotice, Level: level, Message: msg})
}

// downloadNotice is a notice about a download state transition (completed,
// failed, waiting for quota). Event.ID carries the download id so clients
// that already notify on download.updated (the desktop app) can skip it.
func (s *Service) downloadNotice(id int64, level NoticeLevel, msg string) {
	s.bus.Publish(Event{Type: EventNotice, ID: id, Level: level, Message: msg})
}

// wrapStore maps store.ErrNotFound to ErrNotFound.
func wrapStore(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}

func userError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Never leak signed URLs that transport errors may contain.
	if i := strings.Index(msg, "http://"); i >= 0 {
		msg = msg[:i] + "<url>"
	} else if i := strings.Index(msg, "https://"); i >= 0 {
		msg = msg[:i] + "<url>"
	}
	return msg
}
