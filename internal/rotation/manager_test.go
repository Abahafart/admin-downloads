package rotation

import (
	"context"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
	"admindescargas/internal/store"
)

type testEnv struct {
	m     *Rotator
	ids   *identities
	clock *fakeClock
	echo  string
}

func newEnv(t *testing.T, s Settings, st *store.Store, egresses ...string) *testEnv {
	t.Helper()
	ids := newIdentities()
	echo := ids.echo(t)
	clock := newClock()
	if s.Strategy == "" {
		s.Strategy = config.StrategyRoundRobin
	}
	s.Enabled = true
	s.IPServices = []string{echo.URL}
	m, err := New(context.Background(), s, Options{Store: st, Now: clock.Now, Rand: rand.New(rand.NewPCG(1, 2))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	env := &testEnv{m: m, ids: ids, clock: clock, echo: echo.URL}
	for i, id := range egresses {
		env.add(t, id, EgressOptions{})
		ids.setIP(id, "198.51.100."+string(rune('1'+i)))
	}
	return env
}

func (env *testEnv) add(t *testing.T, id string, o EgressOptions) *fakeEgress {
	t.Helper()
	f := newFake(env.ids, id)
	if err := env.m.Add(context.Background(), f, o); err != nil {
		t.Fatal(err)
	}
	return f
}

func (env *testEnv) acquire(t *testing.T, req Request) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e, err := env.m.Acquire(ctx, req)
	if err != nil {
		t.Fatalf("Acquire(%+v): %v", req, err)
	}
	return e.ID()
}

func anon(hoster string) Request {
	return Request{Hoster: hoster, AllowRotation: true, QuotaScope: hosters.ScopeIP}
}

func TestStrategies(t *testing.T) {
	ids3 := []string{"a", "b", "c"}
	t.Run("round-robin", func(t *testing.T) {
		env := newEnv(t, Settings{Strategy: config.StrategyRoundRobin}, nil, ids3...)
		var got []string
		for range 6 {
			id := env.acquire(t, anon("mega"))
			got = append(got, id)
			env.m.Release("mega", id)
		}
		if strings.Join(got, "") != "abcabc" {
			t.Fatalf("got %v", got)
		}
		// Independent cursor per hoster.
		if id := env.acquire(t, anon("gofile")); id != "a" {
			t.Fatalf("gofile got %s", id)
		}
	})
	t.Run("least-used", func(t *testing.T) {
		env := newEnv(t, Settings{Strategy: config.StrategyLeastUsed}, nil, ids3...)
		var got []string
		for range 3 {
			got = append(got, env.acquire(t, anon("mega"))) // never released
		}
		slices.Sort(got)
		if strings.Join(got, "") != "abc" {
			t.Fatalf("got %v", got)
		}
		env.m.Release("mega", "b")
		if id := env.acquire(t, anon("mega")); id != "b" {
			t.Fatalf("least used = %s, want b", id)
		}
	})
	t.Run("fixed", func(t *testing.T) {
		env := newEnv(t, Settings{Strategy: config.StrategyFixed}, nil, ids3...)
		for range 3 {
			if id := env.acquire(t, anon("mega")); id != "a" {
				t.Fatalf("fixed got %s", id)
			}
		}
		env.m.ReportQuota(context.Background(), "mega", "a", hosters.ScopeIP, time.Hour)
		if id := env.acquire(t, anon("mega")); id != "b" {
			t.Fatalf("fixed after quota got %s", id)
		}
	})
	t.Run("random", func(t *testing.T) {
		env := newEnv(t, Settings{Strategy: config.StrategyRandom}, nil, ids3...)
		seen := map[string]int{}
		for range 60 {
			id := env.acquire(t, anon("mega"))
			seen[id]++
			env.m.Release("mega", id)
		}
		if len(seen) != 3 {
			t.Fatalf("random used %v", seen)
		}
	})
	t.Run("prefer", func(t *testing.T) {
		env := newEnv(t, Settings{}, nil, ids3...)
		req := anon("mega")
		req.PreferEgress = "c"
		if id := env.acquire(t, req); id != "c" {
			t.Fatalf("prefer got %s", id)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		if _, err := New(context.Background(), Settings{Strategy: "nope"}, Options{}); err == nil {
			t.Fatal("unknown strategy accepted")
		}
	})
}

func TestQuotaSwitchAndWaitError(t *testing.T) {
	env := newEnv(t, Settings{}, nil, "a", "b")
	ctx := context.Background()
	id := env.acquire(t, anon("mega"))
	env.m.ReportQuota(ctx, "mega", id, hosters.ScopeIP, 10*time.Minute)
	env.m.Release("mega", id)
	next := env.acquire(t, anon("mega"))
	if next == id {
		t.Fatalf("still got %s after quota", id)
	}
	// Other hosters are not affected.
	req := anon("gofile")
	req.PreferEgress = id
	if got := env.acquire(t, req); got != id {
		t.Fatalf("gofile got %s", got)
	}
	env.clock.Add(time.Minute)
	env.m.ReportQuota(ctx, "mega", next, hosters.ScopeIP, 0) // default retry-after
	_, err := env.m.Acquire(ctx, anon("mega"))
	we, ok := AsWait(err)
	if !ok {
		t.Fatalf("err = %v, want WaitError", err)
	}
	// Earliest deadline: first mark (t0+10m), not the default 1h.
	if want := newClock().Now().Add(10 * time.Minute); !we.Until.Equal(want) {
		t.Fatalf("Until = %s, want %s (%s)", we.Until, want, we.Reason)
	}
	st := statusOf(t, env.m, id)
	if st.Exhausted["mega"].IsZero() || len(st.Exhausted) != 1 {
		t.Fatalf("Exhausted = %v", st.Exhausted)
	}
	// After the deadline the egress is usable again.
	env.clock.Add(10 * time.Minute)
	if got := env.acquire(t, anon("mega")); got != id {
		t.Fatalf("after expiry got %s", got)
	}
	// Account/file quotas do not mark the egress.
	env.m.ReportQuota(ctx, "gdrive", "a", hosters.ScopeFile, time.Hour)
	if env.m.Book().Exhausted("gdrive", hosters.ScopeIP, "a") {
		t.Fatal("file quota marked the egress")
	}
}

func statusOf(t *testing.T, m *Rotator, id string) EgressStatus {
	t.Helper()
	for _, s := range m.Egresses() {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no status for %s", id)
	return EgressStatus{}
}

func TestPinnedRequests(t *testing.T) {
	env := newEnv(t, Settings{}, nil)
	env.add(t, netx.DirectID, EgressOptions{})
	env.add(t, "p1", EgressOptions{})
	ctx := context.Background()
	acc := Request{Hoster: "mega", AccountID: "acc1", AllowRotation: false, QuotaScope: hosters.ScopeAccount}
	if id := env.acquire(t, acc); id != netx.DirectID {
		t.Fatalf("pinned got %s", id)
	}
	acc.PreferEgress = "p1"
	if id := env.acquire(t, acc); id != "p1" {
		t.Fatalf("pinned prefer got %s", id)
	}
	// IP marks do not hold account downloads (their quota is per account).
	env.m.ReportQuota(ctx, "mega", "p1", hosters.ScopeIP, time.Hour)
	if id := env.acquire(t, acc); id != "p1" {
		t.Fatalf("pinned account download held by an IP mark: got %s", id)
	}
	// An anonymous pinned request on the same egress waits.
	anonPinned := Request{Hoster: "mega", AllowRotation: false, PreferEgress: "p1", QuotaScope: hosters.ScopeIP}
	if _, err := env.m.Acquire(ctx, anonPinned); err == nil {
		t.Fatal("pinned anonymous request ignored the IP mark")
	} else if _, ok := AsWait(err); !ok {
		t.Fatalf("pinned exhausted: err = %v", err)
	}
	// Rotation disabled globally pins anonymous requests too.
	env.m.s.Enabled = false
	env.m.ReportQuota(ctx, "mega", netx.DirectID, hosters.ScopeIP, time.Hour)
	if _, err := env.m.Acquire(ctx, anon("mega")); err == nil {
		t.Fatal("rotation disabled but Acquire switched egress")
	}
}

func TestConcurrencyLimitAndLastResort(t *testing.T) {
	env := newEnv(t, Settings{MaxPerEgress: 1, BusyWait: 300 * time.Millisecond}, nil, "a")
	env.add(t, "tor:0", EgressOptions{LastResort: true})
	ctx := context.Background()
	if id := env.acquire(t, anon("mega")); id != "a" {
		t.Fatalf("got %s", id)
	}
	// "a" is busy: wait for it rather than falling to Tor.
	done := make(chan string)
	go func() {
		e, err := env.m.Acquire(ctx, anon("mega"))
		if err != nil {
			done <- "err:" + err.Error()
			return
		}
		done <- e.ID()
	}()
	time.Sleep(50 * time.Millisecond)
	env.m.Release("mega", "a")
	if got := <-done; got != "a" {
		t.Fatalf("waiter got %s", got)
	}
	// Busy past BusyWait: short WaitError.
	_, err := env.m.Acquire(ctx, anon("mega"))
	if we, ok := AsWait(err); !ok || !strings.Contains(we.Reason, "ocupadas") {
		t.Fatalf("err = %v", err)
	}
	env.m.Release("mega", "a")
	// Exhausted normal egress: Tor is used as last resort.
	env.m.ReportQuota(ctx, "mega", "a", hosters.ScopeIP, time.Hour)
	if id := env.acquire(t, anon("mega")); id != "tor:0" {
		t.Fatalf("last resort got %s", id)
	}
	if st := statusOf(t, env.m, "tor:0"); st.InUse != 1 {
		t.Fatalf("InUse = %d", st.InUse)
	}
}

func TestReportFailureAndRecheck(t *testing.T) {
	env := newEnv(t, Settings{}, nil, "a", "b")
	env.m.ReportFailure("a", errors.New("proxyconnect tcp: socks5://u:pw@h:1 connection refused"))
	st := statusOf(t, env.m, "a")
	if st.Healthy || strings.Contains(st.LastError, "pw") {
		t.Fatalf("status = %+v", st)
	}
	for range 3 {
		if id := env.acquire(t, anon("mega")); id != "b" {
			t.Fatalf("unhealthy egress handed out: %s", id)
		}
		env.m.Release("mega", "b")
	}
	// Not due yet.
	env.m.checkDue(context.Background(), false)
	if statusOf(t, env.m, "a").Healthy {
		t.Fatal("rechecked too early")
	}
	env.clock.Add(31 * time.Second)
	env.m.checkDue(context.Background(), false)
	st = statusOf(t, env.m, "a")
	if !st.Healthy || st.PublicIP != "198.51.100.1" {
		t.Fatalf("after recheck = %+v", st)
	}
	// Direct needs three failures in a row.
	env.add(t, netx.DirectID, EgressOptions{})
	env.m.ReportFailure(netx.DirectID, errors.New("x"))
	env.m.ReportFailure(netx.DirectID, errors.New("x"))
	if !statusOf(t, env.m, netx.DirectID).Healthy {
		t.Fatal("direct marked unhealthy after 2 failures")
	}
	env.m.ReportFailure(netx.DirectID, errors.New("x"))
	if statusOf(t, env.m, netx.DirectID).Healthy {
		t.Fatal("direct still healthy after 3 failures")
	}
	env.m.ReportFailure("b", context.Canceled)
	if !statusOf(t, env.m, "b").Healthy {
		t.Fatal("cancellation counted as failure")
	}
}

func TestDedupeByIP(t *testing.T) {
	env := newEnv(t, Settings{DedupeByIP: true}, nil, "a", "b", "c")
	env.ids.setIP("c", "198.51.100.1") // same exit as "a"
	env.m.CheckNow(context.Background())
	st := statusOf(t, env.m, "c")
	if st.Healthy || !strings.Contains(st.LastError, "a") {
		t.Fatalf("c = %+v", st)
	}
	for range 4 {
		id := env.acquire(t, anon("mega"))
		if id == "c" {
			t.Fatal("duplicate handed out")
		}
		env.m.Release("mega", id)
	}
}

func TestRotateIdleExhaustedEgress(t *testing.T) {
	env := newEnv(t, Settings{VerifyIPChange: true}, nil)
	f := env.add(t, "sess", EgressOptions{})
	env.ids.setIP("sess", "203.0.113.1")
	n := 1
	f.rotate = func(context.Context) error {
		n++
		env.ids.setIP("sess", "203.0.113."+string(rune('0'+n)))
		return nil
	}
	env.m.CheckNow(context.Background())
	env.m.ReportQuota(context.Background(), "mega", "sess", hosters.ScopeIP, time.Hour)
	if id := env.acquire(t, anon("mega")); id != "sess" {
		t.Fatalf("got %s", id)
	}
	if f.rotated.Load() != 1 || env.m.Book().Exhausted("mega", hosters.ScopeIP, "sess") {
		t.Fatal("egress not rotated / mark not cleared")
	}
	if st := statusOf(t, env.m, "sess"); st.PublicIP != "203.0.113.2" {
		t.Fatalf("ip = %s", st.PublicIP)
	}
	// In use: never rotated under a running download.
	env.m.ReportQuota(context.Background(), "mega", "sess", hosters.ScopeIP, time.Hour)
	env.clock.Add(time.Minute)
	if _, err := env.m.Acquire(context.Background(), anon("mega")); err == nil {
		t.Fatal("expected WaitError")
	}
	if f.rotated.Load() != 1 {
		t.Fatal("rotated an egress in use")
	}
	// Rotation that keeps the IP does not clear the mark.
	env.m.Release("mega", "sess")
	f.rotate = func(context.Context) error { return nil }
	if _, err := env.m.Acquire(context.Background(), anon("mega")); err == nil {
		t.Fatal("same IP after rotation should still wait")
	}
}

// TestGlobalRotation checks PLAN.md section 6 step 4: pause, rotate,
// verify the IP changed, resume, then hand out the direct egress.
func TestGlobalRotation(t *testing.T) {
	env := newEnv(t, Settings{GlobalRotation: true, VerifyIPChange: true, MinGlobalInterval: 10 * time.Minute, GlobalTimeout: 5 * time.Second}, nil)
	env.add(t, netx.DirectID, EgressOptions{})
	env.ids.setIP("direct", "192.0.2.1")
	var mu sync.Mutex
	var events []string
	record := func(s string) {
		mu.Lock()
		events = append(events, s)
		mu.Unlock()
	}
	router := newFake(env.ids, "router")
	router.kind, router.scope = netx.KindRouter, netx.ScopeGlobal
	router.rotate = func(context.Context) error {
		record("rotate")
		env.ids.setIP("direct", "192.0.2.2")
		return nil
	}
	if err := env.m.Add(context.Background(), router, EgressOptions{}); err != nil {
		t.Fatal(err)
	}
	env.m.SetHooks(Hooks{
		PauseAll:  func(context.Context) error { record("pause"); return nil },
		ResumeAll: func(context.Context) error { record("resume"); return nil },
	})
	env.m.CheckNow(context.Background())
	ctx := context.Background()
	env.m.ReportQuota(ctx, "mega", netx.DirectID, hosters.ScopeIP, time.Hour)

	if id := env.acquire(t, anon("mega")); id != netx.DirectID {
		t.Fatalf("got %s", id)
	}
	mu.Lock()
	if strings.Join(events, ",") != "pause,rotate,resume" {
		t.Fatalf("events = %v", events)
	}
	mu.Unlock()
	if env.m.Book().Exhausted("mega", hosters.ScopeIP, netx.DirectID) {
		t.Fatal("direct still marked")
	}
	if st := statusOf(t, env.m, netx.DirectID); st.PublicIP != "192.0.2.2" {
		t.Fatalf("direct ip = %s", st.PublicIP)
	}
	env.m.Release("mega", netx.DirectID)

	// Minimum interval between global rotations.
	env.m.ReportQuota(ctx, "mega", netx.DirectID, hosters.ScopeIP, time.Hour)
	_, err := env.m.Acquire(ctx, anon("mega"))
	if we, ok := AsWait(err); !ok || !strings.Contains(we.Reason, "próxima reconexión") {
		t.Fatalf("err = %v", err)
	}
	if router.rotated.Load() != 1 {
		t.Fatal("rotated twice within the minimum interval")
	}

	// Account downloads that forbid rotation postpone it.
	env.clock.Add(11 * time.Minute)
	env.acquire(t, Request{Hoster: "other", AccountID: "x", AllowRotation: false})
	_, err = env.m.Acquire(ctx, anon("mega"))
	if we, ok := AsWait(err); !ok || !strings.Contains(we.Reason, "aplazada") {
		t.Fatalf("err = %v", err)
	}
	env.m.Release("other", netx.DirectID)

	// A rotation that does not change the IP fails and resumes anyway.
	router.rotate = func(context.Context) error { record("rotate"); return nil }
	env.m.s.GlobalTimeout = 300 * time.Millisecond
	mu.Lock()
	events = nil
	mu.Unlock()
	_, err = env.m.Acquire(ctx, anon("mega"))
	if we, ok := AsWait(err); !ok || !strings.Contains(we.Reason, "falló") {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	if strings.Join(events, ",") != "pause,rotate,resume" {
		t.Fatalf("events = %v", events)
	}
	mu.Unlock()

	// Manual global rotation through the API ignores the interval.
	router.rotate = func(context.Context) error { env.ids.setIP("direct", "192.0.2.3"); return nil }
	if err := env.m.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := env.m.Rotate(ctx, "nope"); !errors.Is(err, ErrUnknownEgress) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuotaMarksPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adesc.db")
	ctx := context.Background()
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, Settings{}, st, "a", "b")
	env.m.ReportQuota(ctx, "mega", "a", hosters.ScopeIP, 2*time.Hour)
	if err := env.m.ReportSubjectQuota(ctx, "mega", hosters.ScopeAccount, "acc1", time.Hour); err != nil {
		t.Fatal(err)
	}
	env.m.Close()
	st.Close()

	st2, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	env2 := newEnv(t, Settings{}, st2, "a", "b")
	if !env2.m.Book().Exhausted("mega", hosters.ScopeIP, "a") || !env2.m.Book().Exhausted("mega", hosters.ScopeAccount, "acc1") {
		t.Fatal("marks lost across restart")
	}
	for range 3 {
		if id := env2.acquire(t, anon("mega")); id != "b" {
			t.Fatalf("got %s", id)
		}
		env2.m.Release("mega", "b")
	}
	if st := statusOf(t, env2.m, "a"); st.Exhausted["mega"].IsZero() {
		t.Fatal("Exhausted not reported")
	}
	if err := env2.m.ClearQuota(ctx, "mega", "a"); err != nil {
		t.Fatal(err)
	}
	marks, _ := st2.ListQuotaMarks(ctx, time.Time{})
	if len(marks) != 1 || marks[0].Scope != "account" {
		t.Fatalf("marks = %+v", marks)
	}
}

// TestUnhealthyDirectIsLastResort: a failed health check of direct (often
// just unreachable IP echo services) must not refuse every download.
func TestUnhealthyDirectIsLastResort(t *testing.T) {
	env := newEnv(t, Settings{}, nil)
	env.add(t, netx.DirectID, EgressOptions{})
	for range 3 {
		env.m.ReportFailure(netx.DirectID, errors.New("ip echo unreachable"))
	}
	if statusOf(t, env.m, netx.DirectID).Healthy {
		t.Fatal("direct still healthy")
	}
	if id := env.acquire(t, anon("mega")); id != netx.DirectID {
		t.Fatalf("got %s", id)
	}
	env.m.Release("mega", netx.DirectID)
	// A healthy proxy is preferred over the unhealthy direct.
	env.add(t, "p1", EgressOptions{})
	for range 3 {
		if id := env.acquire(t, anon("mega")); id != "p1" {
			t.Fatalf("unhealthy direct preferred over a healthy proxy: %s", id)
		}
		env.m.Release("mega", "p1")
	}
}
