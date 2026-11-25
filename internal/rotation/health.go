package rotation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"admindescargas/internal/netx"
)

// healthTick is how often the checker looks for due rechecks.
const healthTick = 5 * time.Second

// maxParallelChecks bounds concurrent health checks.
const maxParallelChecks = 8

// Start runs the background health checker until Close: a first check of
// every per-connection egress, then one every Settings.HealthInterval, plus
// the rechecks scheduled by ReportFailure. It also prunes expired quota
// marks.
func (m *Rotator) Start(context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.started {
		return
	}
	m.started = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		cctx := m.bg
		m.checkDue(cctx, true)
		t := time.NewTicker(healthTick)
		defer t.Stop()
		lastFull := time.Now()
		lastPrune := time.Now()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
			}
			full := m.s.HealthInterval > 0 && time.Since(lastFull) >= m.s.HealthInterval
			if full {
				lastFull = time.Now()
			}
			m.checkDue(cctx, full)
			if time.Since(lastPrune) >= 10*time.Minute {
				lastPrune = time.Now()
				if err := m.book.Prune(cctx); err != nil {
					m.log.Warn("rotation: prune quota marks", "err", err)
				}
			}
		}
	}()
}

// CheckNow health-checks every per-connection egress and waits for the
// results.
func (m *Rotator) CheckNow(ctx context.Context) { m.checkDue(ctx, true) }

// checkDue checks the egresses whose recheck is due (all when full).
func (m *Rotator) checkDue(ctx context.Context, full bool) {
	m.mu.Lock()
	now := m.now()
	var due []*entry
	for _, en := range m.order {
		if en.global() || en.checking || en.rotating {
			continue
		}
		if full || (!en.nextCheck.IsZero() && !now.Before(en.nextCheck)) {
			en.checking = true
			due = append(due, en)
		}
	}
	m.mu.Unlock()
	sem := make(chan struct{}, maxParallelChecks)
	var wg sync.WaitGroup
	for _, en := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m.checkEntry(ctx, en)
		}()
	}
	wg.Wait()
}

// checkEntry runs one health check (en.checking must be set).
func (m *Rotator) checkEntry(ctx context.Context, en *entry) netx.Health {
	h := netx.CheckHealth(ctx, en.e, m.s.IPServices)
	m.mu.Lock()
	en.checking = false
	if ctx.Err() != nil {
		m.notifyLocked()
		m.mu.Unlock()
		return h
	}
	m.applyHealthLocked(en, h)
	m.notifyLocked()
	m.mu.Unlock()
	m.persistHealth(en)
	m.emit(en.e.ID())
	return h
}

// applyHealthLocked stores a check result and recomputes the duplicate
// state (PLAN.md: proxies leaving through the same IP are deduplicated).
func (m *Rotator) applyHealthLocked(en *entry, h netx.Health) {
	en.checkedAt = h.CheckedAt
	if h.Healthy {
		en.healthy = true
		en.failures = 0
		en.lastErr = ""
		en.nextCheck = time.Time{}
		en.publicIP = h.PublicIP
	} else {
		en.healthy = false
		en.failures++
		en.lastErr = h.LastError
		en.nextCheck = m.now().Add(failureBackoff(en.failures))
	}
	en.dupOf = ""
	if !m.s.DedupeByIP || !h.Healthy || !h.PublicIP.IsValid() || en.global() || en.e.Kind() == netx.KindTor {
		return
	}
	for _, o := range m.order {
		if o == en || o.global() || o.e.Kind() == netx.KindTor || o.dupOf != "" || !o.healthy {
			continue
		}
		if o.order < en.order && o.publicIP == h.PublicIP {
			en.dupOf = o.e.ID()
			return
		}
	}
	// en is the first with this IP: later egresses checked earlier with the
	// same exit become its duplicates (checks run concurrently).
	for _, o := range m.order {
		if o != en && !o.global() && o.e.Kind() != netx.KindTor && o.dupOf == "" && o.healthy &&
			o.order > en.order && o.publicIP == h.PublicIP {
			o.dupOf = en.e.ID()
		}
	}
}

func (m *Rotator) persistHealth(en *entry) {
	if m.st == nil {
		return
	}
	m.mu.Lock()
	id, healthy, ip, lastErr, at := en.e.ID(), en.healthy && en.dupOf == "", FormatIP(en.publicIP), en.lastErr, en.checkedAt
	if en.dupOf != "" {
		lastErr = "duplicate exit IP of " + en.dupOf
	}
	m.mu.Unlock()
	// Background bookkeeping: never let a canceled caller lose it.
	// Still record the last state while shutting down.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(m.bg), 10*time.Second)
	defer cancel()
	if err := m.st.UpdateEgressHealth(ctx, id, healthy, ip, lastErr, at); err != nil {
		m.log.Warn("rotation: persist egress health", "egress", id, "err", err)
	}
}

// TestEgress health-checks one egress now and returns its status (global
// egresses are not rotated: only their direct connectivity is checked).
func (m *Rotator) TestEgress(ctx context.Context, id string) (EgressStatus, error) {
	for {
		m.mu.Lock()
		en, ok := m.entries[id]
		if !ok {
			m.mu.Unlock()
			return EgressStatus{}, fmt.Errorf("%w: %s", ErrUnknownEgress, id)
		}
		if en.checking {
			// A background check is running: wait for its result.
			ch := m.changed
			m.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return EgressStatus{}, ctx.Err()
			}
		}
		en.checking = true
		m.mu.Unlock()
		m.checkEntry(ctx, en)
		break
	}
	if err := ctx.Err(); err != nil {
		return EgressStatus{}, err
	}
	return m.Status(id)
}
