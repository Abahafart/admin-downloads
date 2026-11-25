package core

import (
	"context"
	"slices"
	"sync"
	"time"

	"admindescargas/internal/downloader"
	"admindescargas/internal/netx"
	"admindescargas/internal/store"
)

// job is one running download (one goroutine).
type job struct {
	id     int64
	pkgID  int64
	hoster string
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu           sync.Mutex
	dl           *downloader.Download
	base         int64 // bytes on disk when no run is active
	size         int64
	state        State
	egressID     string
	publicIP     string
	waitingUntil time.Time
	quotaScope   string
	lastBytes    int64
	lastAt       time.Time
	speed        float64
	reserved     bool // holds an egress reservation
}

func (j *job) set(fn func(j *job)) {
	j.mu.Lock()
	fn(j)
	j.mu.Unlock()
}

func (j *job) written() int64 {
	if j.dl != nil {
		return j.dl.Written()
	}
	return j.base
}

// tick updates the moving-average speed.
func (j *job) tick(now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	w := j.written()
	if j.lastAt.IsZero() || j.dl == nil {
		j.lastAt, j.lastBytes = now, w
		if j.dl == nil {
			j.speed = 0
		}
		return
	}
	dt := now.Sub(j.lastAt).Seconds()
	if dt <= 0 {
		return
	}
	inst := float64(w-j.lastBytes) / dt
	if inst < 0 {
		inst = 0
	}
	j.speed = 0.3*inst + 0.7*j.speed
	j.lastAt, j.lastBytes = now, w
}

func (j *job) speedValue() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return int64(j.speed)
}

func (j *job) progress() Progress {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := Progress{
		ID: j.id, PackageID: j.pkgID, State: j.state, Done: j.written(), Size: j.size,
		Speed: int64(j.speed), ETA: -1, EgressID: j.egressID, PublicIP: j.publicIP, WaitingUntil: j.waitingUntil,
		QuotaScope: j.quotaScope,
	}
	if j.dl != nil {
		p.Connections = j.dl.Conns()
		if sz := j.dl.Size(); sz >= 0 {
			p.Size = sz
		}
	}
	if p.Speed > 0 && p.Size >= 0 {
		p.ETA = max(p.Size-p.Done, 0) / p.Speed
	}
	return p
}

func (s *Service) schedulerLoop() {
	defer s.bgWG.Done()
	t := time.NewTicker(s.scheduleInterval)
	defer t.Stop()
	for {
		select {
		case <-s.bgCtx.Done():
			return
		case <-s.kickCh:
		case <-t.C:
		}
		s.schedule(s.bgCtx)
	}
}

func (s *Service) hosterLimit(hoster string) int {
	if hc := s.cfg.Hoster(hoster); hc.MaxParallel > 0 {
		return hc.MaxParallel
	}
	if n := s.cfg.Downloads.MaxPerHoster; n > 0 {
		return n
	}
	return s.maxActive()
}

func (s *Service) maxActive() int {
	if n := s.cfg.Downloads.MaxActive; n > 0 {
		return n
	}
	return 3
}

// egressCapacity returns how many more jobs the egress pool can take with
// the per-egress limit (-1 = unlimited).
func (s *Service) egressCapacity() int {
	limit := s.cfg.Downloads.MaxPerEgress
	if limit <= 0 {
		return -1
	}
	statuses := s.rot.Egresses()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range statuses {
		// Direct stays usable when its health check fails (the rotation
		// manager keeps it as a last resort), so it always counts.
		if !e.Healthy && e.ID != netx.DirectID {
			continue
		}
		if free := limit - s.egressUse[e.ID]; free > 0 {
			n += free
		}
	}
	return n
}

// schedule starts queued (and due waiting) downloads within the limits.
func (s *Service) schedule(ctx context.Context) {
	s.mu.Lock()
	if s.pausedAll || s.rotationPaused > 0 || s.stopping || !s.running {
		s.mu.Unlock()
		return
	}
	free := s.maxActive() - len(s.jobs)
	now := s.now()
	for id, t := range s.notBefore {
		if !t.After(now) {
			delete(s.notBefore, id)
		}
	}
	s.mu.Unlock()
	if free <= 0 {
		return
	}
	rows, err := s.st.ListDownloads(ctx, store.DownloadQuery{States: []string{string(StateQueued), string(StateWaiting)}})
	if err != nil || len(rows) == 0 {
		return
	}
	pkgs, err := s.st.ListPackages(ctx)
	if err != nil {
		return
	}
	pprio := map[int64]int{}
	for _, p := range pkgs {
		pprio[p.ID] = p.Priority
	}
	slices.SortStableFunc(rows, func(a, b store.DownloadRow) int {
		if a.Priority != b.Priority {
			return b.Priority - a.Priority
		}
		if pa, pb := pprio[a.PackageID], pprio[b.PackageID]; pa != pb {
			return pb - pa
		}
		if a.PackageID != b.PackageID {
			return int(a.PackageID - b.PackageID)
		}
		return a.Position - b.Position
	})
	capacity := s.egressCapacity()
	for _, r := range rows {
		if free <= 0 || capacity == 0 {
			return
		}
		if r.State == string(StateWaiting) && r.WaitingUntil.After(now) {
			continue
		}
		s.mu.Lock()
		if s.pausedAll || s.rotationPaused > 0 || s.stopping {
			s.mu.Unlock()
			return
		}
		if _, busy := s.jobs[r.ID]; busy || s.notBefore[r.ID].After(now) {
			s.mu.Unlock()
			continue
		}
		n := 0
		for _, j := range s.jobs {
			if j.hoster == r.Hoster {
				n++
			}
		}
		if n >= s.hosterLimit(r.Hoster) {
			s.mu.Unlock()
			continue
		}
		jctx, cancel := context.WithCancelCause(s.bgCtx)
		j := &job{id: r.ID, pkgID: r.PackageID, hoster: r.Hoster, ctx: jctx, cancel: cancel,
			done: make(chan struct{}), base: r.DoneBytes, size: r.Size, state: State(r.State)}
		s.jobs[r.ID] = j
		s.jobWG.Add(1)
		s.mu.Unlock()
		go s.runJob(j)
		free--
		if capacity > 0 {
			capacity--
		}
	}
}

func (s *Service) runJob(j *job) {
	defer s.jobWG.Done()
	defer func() {
		s.mu.Lock()
		delete(s.jobs, j.id)
		s.mu.Unlock()
		j.cancel(nil)
		close(j.done)
		s.markPackageDirty(j.pkgID)
		s.kick()
	}()
	s.execute(j)
}

// deferJob makes the scheduler skip the download for a moment (egress
// limits reached).
func (s *Service) deferJob(id int64) {
	s.mu.Lock()
	s.notBefore[id] = s.now().Add(2 * time.Second)
	s.mu.Unlock()
}

func (s *Service) progressLoop() {
	defer s.bgWG.Done()
	t := time.NewTicker(s.progressInterval)
	defer t.Stop()
	for {
		select {
		case <-s.bgCtx.Done():
			return
		case <-t.C:
		}
		s.publishProgress(s.bgCtx)
	}
}

// publishProgress emits one progress batch, the stats and updates of the
// packages touched since the last tick.
func (s *Service) publishProgress(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	jobs := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
		s.dirtyPkgs[j.pkgID] = struct{}{}
	}
	dirty := make([]int64, 0, len(s.dirtyPkgs))
	for id := range s.dirtyPkgs {
		dirty = append(dirty, id)
	}
	clear(s.dirtyPkgs)
	s.mu.Unlock()

	if len(jobs) > 0 {
		batch := make([]Progress, 0, len(jobs))
		for _, j := range jobs {
			j.tick(now)
			batch = append(batch, j.progress())
		}
		slices.SortFunc(batch, func(a, b Progress) int { return int(a.ID - b.ID) })
		s.bus.Publish(Event{Type: EventProgress, Progress: batch})
	}
	for _, id := range dirty {
		p, dls, err := s.GetPackage(ctx, id)
		if err != nil {
			continue
		}
		_ = dls
		s.bus.Publish(Event{Type: EventPackageUpdated, Package: &p})
	}
	if len(jobs) > 0 || len(dirty) > 0 {
		if counts, err := s.st.CountDownloadsByState(ctx); err == nil {
			if st, err := s.statsWithGrabber(ctx, counts); err == nil {
				s.bus.Publish(Event{Type: EventStats, Stats: &st})
			}
		}
	}
}
