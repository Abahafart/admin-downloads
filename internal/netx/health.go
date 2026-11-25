package netx

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"sync"
	"time"
)

// Health is the result of the last health check of an egress.
type Health struct {
	Healthy   bool
	PublicIP  netip.Addr
	LastError string
	CheckedAt time.Time
}

// HealthReporter is implemented by egresses that keep their own Health
// (every implementation of this package except Direct).
type HealthReporter interface {
	Health() Health
}

// healthState is embedded by egress implementations. The zero value is
// "healthy, never checked" so a fresh egress is tried before its first check.
type healthState struct {
	mu sync.Mutex
	h  Health
	ok bool // set once a check (or failure) has been recorded
}

// Healthy implements Egress.
func (s *healthState) Healthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.ok || s.h.Healthy
}

// Health implements HealthReporter.
func (s *healthState) Health() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.h
	if !s.ok {
		h.Healthy = true
	}
	return h
}

func (s *healthState) setHealth(h Health) {
	s.mu.Lock()
	s.h, s.ok = h, true
	s.mu.Unlock()
}

// resetHealth forgets the last check (after a rotation).
func (s *healthState) resetHealth() {
	s.mu.Lock()
	s.h, s.ok = Health{}, false
	s.mu.Unlock()
}

// healthSetter is implemented by egresses embedding healthState.
type healthSetter interface{ setHealth(Health) }

// DefaultHealthTimeout bounds a CheckHealth call.
const DefaultHealthTimeout = 30 * time.Second

// CheckHealth probes e by asking the IP echo services (DefaultIPEchoServices
// if services is empty) for the public IP seen through it. This proves both
// that a connection through e works and what its exit IP is. The result is
// recorded in e when it is one of this package's implementations.
func CheckHealth(ctx context.Context, e Egress, services []string) Health {
	if len(services) == 0 {
		services = DefaultIPEchoServices
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultHealthTimeout)
	defer cancel()
	c := NewHTTPClient(e, nil)
	defer c.CloseIdleConnections()
	ip, err := PublicIPWith(ctx, c, services)
	h := Health{Healthy: err == nil, PublicIP: ip, CheckedAt: time.Now()}
	if err != nil {
		h.LastError = SanitizeError(err)
	}
	RecordHealth(e, h)
	return h
}

// RecordHealth stores h in e if e keeps its own health (no-op otherwise).
func RecordHealth(e Egress, h Health) {
	if hs, ok := e.(healthSetter); ok {
		hs.setHealth(h)
	}
}

// MarkUnhealthy records a failure on e (keeping its last known public IP).
func MarkUnhealthy(e Egress, err error) {
	h := Health{CheckedAt: time.Now(), LastError: SanitizeError(err)}
	if hr, ok := e.(HealthReporter); ok {
		h.PublicIP = hr.Health().PublicIP
	}
	RecordHealth(e, h)
}

var userinfoRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// SanitizeError renders err for logs and the UI: *url.Error wrappers are
// dropped (their URL may be a signed download link), credentials embedded in
// URLs are removed and the text is capped at 300 bytes.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	for errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err // drop the URL, keep the cause
	}
	s := userinfoRE.ReplaceAllString(err.Error(), "$1")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
