// Package rotation decides which egress each download uses and reacts to
// quotas (PLAN.md section 6): the QuotaBook marks (hoster, egress) pairs as
// exhausted until a deadline (persisted in quota_marks), the Manager picks
// another healthy per-connection egress with the configured strategy, falls
// back to a global rotation (router / system VPN) and finally answers with a
// WaitError so the download goes to "waiting" with a countdown.
//
// This file holds the contract plus Static, a trivial Manager that always
// returns one egress (netx.Direct), so the engine can be built and tested
// before the real manager exists (rotation agent: manager.go, quotabook.go,
// strategy.go).
package rotation

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

// Request describes what a download needs from the manager.
type Request struct {
	// Hoster is the plugin name; quota marks are per (hoster, egress).
	Hoster string
	// AccountID is "" for anonymous downloads.
	AccountID string
	// AllowRotation is false when an account forbids IP rotation; the
	// manager then keeps PreferEgress (or the first available egress). In
	// that pinned mode, requests with an AccountID ignore the IP quota
	// marks of the egress (account quotas are per account, see Rotator).
	AllowRotation bool
	// PreferEgress is the egress used last time (sticky), "" for none.
	PreferEgress string
	// QuotaScope is the hoster's usual quota scope (Caps.QuotaScope); with
	// ScopeFile/ScopeAccount rotating the IP does not help.
	QuotaScope hosters.QuotaScope
}

// Manager hands out egresses to downloads. Implementations must be safe for
// concurrent use.
type Manager interface {
	// Acquire returns an egress for req and counts it as in use until
	// Release. If every candidate is exhausted for req.Hoster it returns a
	// *WaitError (use AsWait) with the earliest time worth retrying. It may
	// block while a global rotation is in progress.
	Acquire(ctx context.Context, req Request) (netx.Egress, error)
	// Release returns an egress obtained from Acquire.
	Release(hoster, egressID string)
	// ReportQuota records that hoster refused egressID with a quota of the
	// given scope. retryAfter 0 means "use the configured default".
	ReportQuota(ctx context.Context, hoster, egressID string, scope hosters.QuotaScope, retryAfter time.Duration)
	// ReportFailure records a network failure attributable to the egress
	// (connect errors, proxy auth failures) for health tracking.
	ReportFailure(egressID string, err error)
	// Rotate forces a rotation of egressID ("" = global rotation), used by
	// POST /api/v1/rotation/rotate.
	Rotate(ctx context.Context, egressID string) error
	// Egresses returns a snapshot of every egress for the UI / API.
	Egresses() []EgressStatus
	// Get returns the egress with that ID, or nil.
	Get(egressID string) netx.Egress
}

// EgressStatus is the UI/API view of one egress.
type EgressStatus struct {
	ID        string     `json:"id"`
	Kind      netx.Kind  `json:"kind"`
	Scope     netx.Scope `json:"scope"`
	Label     string     `json:"label,omitempty"`
	Healthy   bool       `json:"healthy"`
	PublicIP  string     `json:"public_ip,omitempty"`
	InUse     int        `json:"in_use"`
	LastError string     `json:"last_error,omitempty"`
	CheckedAt time.Time  `json:"checked_at,omitzero"`
	// Exhausted maps hoster name -> time until which the egress is marked
	// as over quota for that hoster.
	Exhausted map[string]time.Time `json:"exhausted,omitempty"`
}

// WaitError means no egress (nor global rotation) can serve the request
// before Until. The engine shows a countdown and retries at Until.
type WaitError struct {
	Until  time.Time
	Reason string
}

// Error implements error.
func (e *WaitError) Error() string {
	return fmt.Sprintf("rotation: no egress available until %s: %s", e.Until.Format(time.RFC3339), e.Reason)
}

// AsWait extracts a *WaitError from err's chain.
func AsWait(err error) (*WaitError, bool) {
	var we *WaitError
	if errors.As(err, &we) {
		return we, true
	}
	return nil, false
}

// Static is a Manager that always returns the same egress and never
// rotates. Quota reports are ignored; the engine's own retry logic handles
// the wait. It is the default until the real manager is wired in.
type Static struct {
	E netx.Egress
}

var _ Manager = (*Static)(nil)

// NewStatic returns a Static manager for e (netx.NewDirect() if nil).
func NewStatic(e netx.Egress) *Static {
	if e == nil {
		e = netx.NewDirect()
	}
	return &Static{E: e}
}

// Acquire implements Manager.
func (s *Static) Acquire(ctx context.Context, _ Request) (netx.Egress, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.E, nil
}

// Release implements Manager.
func (s *Static) Release(string, string) {}

// ReportQuota implements Manager.
func (s *Static) ReportQuota(context.Context, string, string, hosters.QuotaScope, time.Duration) {}

// ReportFailure implements Manager.
func (s *Static) ReportFailure(string, error) {}

// Rotate implements Manager.
func (s *Static) Rotate(ctx context.Context, id string) error {
	if id != "" && id != s.E.ID() {
		return fmt.Errorf("rotation: unknown egress %q", id)
	}
	return s.E.Rotate(ctx)
}

// Egresses implements Manager.
func (s *Static) Egresses() []EgressStatus {
	return []EgressStatus{{ID: s.E.ID(), Kind: s.E.Kind(), Scope: s.E.Scope(), Healthy: s.E.Healthy()}}
}

// Get implements Manager.
func (s *Static) Get(id string) netx.Egress {
	if id == s.E.ID() {
		return s.E
	}
	return nil
}

// FormatIP renders an address for EgressStatus.PublicIP ("" if invalid).
func FormatIP(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}
