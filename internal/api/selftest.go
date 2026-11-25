package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"admindescargas/internal/core"
	"admindescargas/internal/hosters"
	"admindescargas/internal/rotation"
)

// RegistrySelfTester is the standard SelfTestRunner: it runs the plugin's
// hosters.SelfTester with an anonymous session on an egress acquired from
// the rotation manager.
type RegistrySelfTester struct {
	Registry *hosters.Registry
	Rotation rotation.Manager
	Log      *slog.Logger
	// Timeout bounds one test (default 2 minutes).
	Timeout time.Duration
}

var _ SelfTestRunner = (*RegistrySelfTester)(nil)

// SelfTest implements SelfTestRunner.
func (t *RegistrySelfTester) SelfTest(ctx context.Context, name string) (SelfTestResult, error) {
	h := t.Registry.Get(name)
	if h == nil {
		return SelfTestResult{}, fmt.Errorf("hoster %q: %w", name, core.ErrNotFound)
	}
	st, ok := h.(hosters.SelfTester)
	if !ok {
		return SelfTestResult{}, badRequest("el plugin %q no tiene selftest", name)
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	e, err := t.Rotation.Acquire(ctx, rotation.Request{Hoster: name, AllowRotation: true, QuotaScope: h.Caps().QuotaScope})
	if err != nil {
		return SelfTestResult{Hoster: name, Error: "sin salida de red: " + err.Error()}, nil
	}
	defer t.Rotation.Release(name, e.ID())
	s := hosters.NewSession(e, nil, nil)
	s.Hoster = name
	if t.Log != nil {
		s.Log = t.Log.With("hoster", name, "egress", e.ID(), "selftest", true)
	}
	start := time.Now()
	err = st.SelfTest(ctx, s)
	res := SelfTestResult{Hoster: name, OK: err == nil, DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}
