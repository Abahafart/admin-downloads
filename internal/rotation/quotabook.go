package rotation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"admindescargas/internal/hosters"
	"admindescargas/internal/store"
)

// QuotaStore persists quota marks (*store.Store implements it).
type QuotaStore interface {
	ListQuotaMarks(ctx context.Context, after time.Time) ([]store.QuotaMark, error)
	UpsertQuotaMark(ctx context.Context, m store.QuotaMark) error
	DeleteQuotaMark(ctx context.Context, hoster, scope, subject string) error
	DeleteQuotaMarksBySubject(ctx context.Context, scope, subject string) (int64, error)
	PurgeExpiredQuotaMarks(ctx context.Context, before time.Time) (int64, error)
}

type markKey struct{ hoster, scope, subject string }

// QuotaBook records which (hoster, scope, subject) triples are over quota
// until when: subject is an egress ID for hosters.ScopeIP, an account ID
// for ScopeAccount and a hoster file ID for ScopeFile. Marks are kept in
// memory and written through to the QuotaStore (table quota_marks), so they
// survive restarts. Safe for concurrent use.
type QuotaBook struct {
	st  QuotaStore // may be nil (memory only)
	now func() time.Time

	mu    sync.RWMutex
	marks map[markKey]store.QuotaMark
}

// NewQuotaBook loads the active marks from st (which may be nil).
func NewQuotaBook(ctx context.Context, st QuotaStore, now func() time.Time) (*QuotaBook, error) {
	if now == nil {
		now = time.Now
	}
	b := &QuotaBook{st: st, now: now, marks: map[markKey]store.QuotaMark{}}
	if st == nil {
		return b, nil
	}
	ms, err := st.ListQuotaMarks(ctx, now())
	if err != nil {
		return nil, fmt.Errorf("rotation: load quota marks: %w", err)
	}
	for _, m := range ms {
		b.marks[markKey{m.Hoster, m.Scope, m.Subject}] = m
	}
	return b, nil
}

// Mark records that subject is exhausted for hoster until until. An existing
// later deadline is kept. The in-memory mark is set even if persisting fails
// (the error is returned for logging).
func (b *QuotaBook) Mark(ctx context.Context, hoster string, scope hosters.QuotaScope, subject string, until time.Time, reason string) error {
	k := markKey{hoster, scope.String(), subject}
	b.mu.Lock()
	if old, ok := b.marks[k]; ok && old.Until.After(until) {
		b.mu.Unlock()
		return nil
	}
	m := store.QuotaMark{Hoster: hoster, Scope: k.scope, Subject: subject, Until: until.UTC(), Reason: reason, CreatedAt: b.now().UTC()}
	b.marks[k] = m
	b.mu.Unlock()
	if b.st != nil {
		return b.st.UpsertQuotaMark(ctx, m)
	}
	return nil
}

// Until returns the deadline of an active mark, or the zero time.
func (b *QuotaBook) Until(hoster string, scope hosters.QuotaScope, subject string) time.Time {
	b.mu.RLock()
	m, ok := b.marks[markKey{hoster, scope.String(), subject}]
	b.mu.RUnlock()
	if !ok || !m.Until.After(b.now()) {
		return time.Time{}
	}
	return m.Until
}

// Exhausted reports whether an active mark exists.
func (b *QuotaBook) Exhausted(hoster string, scope hosters.QuotaScope, subject string) bool {
	return !b.Until(hoster, scope, subject).IsZero()
}

// Subject returns hoster -> deadline of the active marks of subject.
func (b *QuotaBook) Subject(scope hosters.QuotaScope, subject string) map[string]time.Time {
	now := b.now()
	sc := scope.String()
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out map[string]time.Time
	for k, m := range b.marks {
		if k.scope == sc && k.subject == subject && m.Until.After(now) {
			if out == nil {
				out = map[string]time.Time{}
			}
			out[k.hoster] = m.Until
		}
	}
	return out
}

// Clear removes one mark.
func (b *QuotaBook) Clear(ctx context.Context, hoster string, scope hosters.QuotaScope, subject string) error {
	b.mu.Lock()
	delete(b.marks, markKey{hoster, scope.String(), subject})
	b.mu.Unlock()
	if b.st != nil {
		return b.st.DeleteQuotaMark(ctx, hoster, scope.String(), subject)
	}
	return nil
}

// ClearSubject removes the marks of subject for every hoster (after an
// egress got a new exit IP).
func (b *QuotaBook) ClearSubject(ctx context.Context, scope hosters.QuotaScope, subject string) error {
	sc := scope.String()
	b.mu.Lock()
	for k := range b.marks {
		if k.scope == sc && k.subject == subject {
			delete(b.marks, k)
		}
	}
	b.mu.Unlock()
	if b.st != nil {
		_, err := b.st.DeleteQuotaMarksBySubject(ctx, sc, subject)
		return err
	}
	return nil
}

// Prune forgets expired marks (memory and store).
func (b *QuotaBook) Prune(ctx context.Context) error {
	now := b.now()
	b.mu.Lock()
	for k, m := range b.marks {
		if !m.Until.After(now) {
			delete(b.marks, k)
		}
	}
	b.mu.Unlock()
	if b.st != nil {
		_, err := b.st.PurgeExpiredQuotaMarks(ctx, now)
		return err
	}
	return nil
}
