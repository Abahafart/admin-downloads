package accounts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeRepo is an in-memory Repo.
type fakeRepo struct {
	mu    sync.Mutex
	rows  map[string]Account
	order []string
	marks map[string]QuotaMark
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rows: map[string]Account{}, marks: map[string]QuotaMark{}}
}

func (r *fakeRepo) InsertAccount(_ context.Context, a *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[a.ID]; ok {
		return errors.New("duplicate")
	}
	r.rows[a.ID] = *a
	r.order = append(r.order, a.ID)
	return nil
}

func (r *fakeRepo) GetAccount(_ context.Context, id string) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.rows[id]
	if !ok {
		return nil, fmt.Errorf("account %q: %w", id, ErrAccountNotFound)
	}
	return &a, nil
}

func (r *fakeRepo) ListAccounts(_ context.Context, hoster string) ([]*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Account
	for _, id := range r.order {
		a, ok := r.rows[id]
		if ok && (hoster == "" || a.Hoster == hoster) {
			out = append(out, &a)
		}
	}
	return out, nil
}

func (r *fakeRepo) UpdateAccount(_ context.Context, a *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[a.ID]; !ok {
		return ErrAccountNotFound
	}
	r.rows[a.ID] = *a
	return nil
}

func (r *fakeRepo) DeleteAccount(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[id]; !ok {
		return ErrAccountNotFound
	}
	delete(r.rows, id)
	r.order = slices.DeleteFunc(r.order, func(s string) bool { return s == id })
	return nil
}

func (r *fakeRepo) MarkAccountQuota(_ context.Context, m QuotaMark) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.marks[m.AccountID] = m
	return nil
}

func (r *fakeRepo) ClearAccountQuota(_ context.Context, _, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.marks, id)
	return nil
}

func (r *fakeRepo) AccountQuotaMarks(_ context.Context, now time.Time) ([]QuotaMark, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []QuotaMark
	for _, m := range r.marks {
		if m.Until.After(now) {
			out = append(out, m)
		}
	}
	return out, nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	m      *Manager
	repo   *fakeRepo
	sec    SecretStore
	clock  *fakeClock
	mu     sync.Mutex
	events []ChangeEvent
	check  func(ctx context.Context, a *Account) (*CheckResult, error)
}

func newHarness(t *testing.T, sec SecretStore) *harness {
	t.Helper()
	if sec == nil {
		sec = NewMemoryStore()
	}
	h := &harness{repo: newFakeRepo(), sec: sec, clock: &fakeClock{t: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}}
	m, err := NewManager(context.Background(), ManagerOptions{
		Repo:    h.repo,
		Secrets: sec,
		Checker: func(ctx context.Context, a *Account) (*CheckResult, error) {
			if h.check == nil {
				return &CheckResult{Status: StatusOK, QuotaTotal: -1, QuotaUsed: -1}, nil
			}
			return h.check(ctx, a)
		},
		HosterDomains: func(string) []string { return teraboxDomains },
		OnChange: func(e ChangeEvent) {
			h.mu.Lock()
			h.events = append(h.events, e)
			h.mu.Unlock()
		},
		Now: h.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.m = m
	return h
}

func (h *harness) eventTypes() []ChangeType {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []ChangeType
	for _, e := range h.events {
		out = append(out, e.Type)
	}
	return out
}

func (h *harness) create(t *testing.T, hoster string, total, used int64) *Account {
	t.Helper()
	a, err := h.m.Create(context.Background(), NewAccount{Hoster: hoster, Kind: KindAPIKey, Secret: Secret{APIKey: "k-" + hoster}})
	if err != nil {
		t.Fatal(err)
	}
	a.QuotaTotal, a.QuotaUsed, a.Status = total, used, StatusOK
	if err := h.repo.UpdateAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestManagerCRUD(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	m := h.m

	for _, bad := range []NewAccount{
		{Hoster: "", Kind: KindAPIKey, Secret: Secret{APIKey: "x"}},
		{Hoster: "mega", Kind: "bogus"},
		{Hoster: "mega", Kind: KindPassword, Username: "a@b.c"},
		{Hoster: "mega", Kind: KindPassword, Secret: Secret{Password: "p"}},
		{Hoster: "pixeldrain", Kind: KindAPIKey, Secret: Secret{APIKey: "  "}},
		{Hoster: "terabox", Kind: KindCookie, Secret: Secret{Cookies: "garbage\nmore garbage"}},
	} {
		if _, err := m.Create(ctx, bad); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("Create(%+v) err = %v", bad, err)
		}
	}

	a, err := m.Create(ctx, NewAccount{Hoster: "mega", Kind: KindPassword, Username: " me@example.com ", Secret: Secret{Password: "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Enabled || a.AllowRotation || a.Status != StatusUnknown || a.Label != "me@example.com" || a.QuotaTotal != -1 || len(a.ID) != 36 {
		t.Fatalf("defaults: %+v", a)
	}
	if s, _ := h.sec.Get(ctx, a.ID); s.Password != "pw" {
		t.Fatal("secret not stored")
	}

	c, err := m.Create(ctx, NewAccount{Hoster: "terabox", Kind: KindCookie, Secret: Secret{Cookies: "ndus=abc; lang=es"}})
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := h.sec.Get(ctx, c.ID); s.Cookies == "" || s.Cookies[0] != '#' {
		t.Fatalf("cookies not normalized: %q", s.Cookies)
	}

	// Update metadata and credentials.
	s, _ := h.sec.Get(ctx, a.ID)
	s.Session = "cached-sid"
	_ = h.sec.Set(ctx, a.ID, s)
	label, pw, off := "Mi MEGA", "new-pw", false
	a2, err := m.Update(ctx, a.ID, AccountPatch{Label: &label, Password: &pw, Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if a2.Label != "Mi MEGA" || a2.Enabled || a2.Status != StatusDisabled {
		t.Fatalf("update: %+v", a2)
	}
	if s, _ := h.sec.Get(ctx, a.ID); s.Password != "new-pw" || s.Session != "" {
		t.Fatalf("password change must replace password and drop the session: %+v", s.Session)
	}

	list, _ := m.List(ctx, "")
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}
	if err := m.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, a.ID); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if _, err := h.sec.Get(ctx, a.ID); !errors.Is(err, ErrSecretNotFound) {
		t.Fatal("secret not deleted")
	}
	if _, err := m.Update(ctx, "nope", AccountPatch{Label: &label}); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	want := []ChangeType{ChangeCreated, ChangeCreated, ChangeUpdated, ChangeDeleted}
	if got := h.eventTypes(); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}

func TestManagerPickOrdering(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	m := h.m
	small := h.create(t, "mega", 100, 90) // 10 left
	big := h.create(t, "mega", 100, 10)   // 90 left
	unknown := h.create(t, "mega", -1, -1)
	full := h.create(t, "mega", 100, 100) // 0 left: never picked
	_ = h.create(t, "terabox", 1000, 0)   // other hoster
	disabled := h.create(t, "mega", 1000, 0)
	disabled.Enabled = false
	_ = h.repo.UpdateAccount(ctx, disabled)
	invalid := h.create(t, "mega", 1000, 0)
	invalid.Status = StatusInvalid
	_ = h.repo.UpdateAccount(ctx, invalid)

	order := func() []string {
		var ids []string
		var ex []string
		for {
			a, err := m.Pick(ctx, "mega", ex...)
			if err != nil {
				if !errors.Is(err, ErrNoAccount) {
					t.Fatal(err)
				}
				return ids
			}
			ids = append(ids, a.ID)
			ex = append(ex, a.ID)
		}
	}
	got := order()
	want := []string{big.ID, small.ID, unknown.ID}
	if !slices.Equal(got, want) {
		t.Fatalf("pick order %v, want %v (full=%s)", got, want, full.ID)
	}

	// Quota report skips the best account until the block ends.
	if err := m.ReportQuota(ctx, big.ID, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if a, _ := m.Pick(ctx, "mega"); a.ID != small.ID {
		t.Fatalf("after quota got %s", a.ID)
	}
	if a, _ := m.Get(ctx, big.ID); a.Status != StatusQuota || a.StatusMsg == "" {
		t.Fatalf("quota status %+v", a)
	}
	if len(h.repo.marks) != 1 {
		t.Fatal("quota mark not persisted")
	}

	// Every account blocked: NoAccountError with the earliest deadline.
	_ = m.ReportQuota(ctx, small.ID, 10*time.Minute)
	_ = m.ReportQuota(ctx, unknown.ID, 0) // default 1h
	_, err := m.Pick(ctx, "mega")
	var nae *NoAccountError
	if !errors.As(err, &nae) || !errors.Is(err, ErrNoAccount) || !nae.Until.Equal(h.clock.Now().Add(10*time.Minute)) {
		t.Fatalf("err = %v", err)
	}

	// A restarted manager reloads persisted marks.
	m2, err := NewManager(ctx, ManagerOptions{Repo: h.repo, Secrets: h.sec, Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Pick(ctx, "mega"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("reloaded marks not applied: %v", err)
	}

	h.clock.Add(31 * time.Minute)
	if a, _ := m.Pick(ctx, "mega"); a == nil || a.ID != big.ID {
		t.Fatalf("block did not expire: %v", a)
	}
	if err := m.ClearQuota(ctx, unknown.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.QuotaMarks()[unknown.ID]; ok {
		t.Fatal("ClearQuota left the mark")
	}

	if _, err := m.Pick(ctx, "nobody"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("no accounts: %v", err)
	}
}

func TestManagerPickRoundRobin(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	a := h.create(t, "pixeldrain", -1, -1)
	b := h.create(t, "pixeldrain", -1, -1)
	c := h.create(t, "pixeldrain", -1, -1)
	seen := map[string]int{}
	for range 9 {
		acct, err := h.m.Pick(ctx, "pixeldrain")
		if err != nil {
			t.Fatal(err)
		}
		seen[acct.ID]++
	}
	if seen[a.ID] != 3 || seen[b.ID] != 3 || seen[c.ID] != 3 {
		t.Fatalf("round robin uneven: %v", seen)
	}
	// Premium breaks ties.
	b.Premium = true
	_ = h.repo.UpdateAccount(ctx, b)
	for range 3 {
		if acct, _ := h.m.Pick(ctx, "pixeldrain"); acct.ID != b.ID {
			t.Fatal("premium account should win ties")
		}
	}
}

func TestManagerCheck(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	a := h.create(t, "mega", 100, 100)
	_ = h.m.ReportQuota(ctx, a.ID, time.Hour)
	until := h.clock.Now().Add(48 * time.Hour)
	h.check = func(_ context.Context, acct *Account) (*CheckResult, error) {
		if acct.ID != a.ID {
			t.Errorf("checker got %s", acct.ID)
		}
		return &CheckResult{Status: StatusOK, Premium: true, PremiumUntil: until, QuotaTotal: 1000, QuotaUsed: 100, Message: "Pro I"}, nil
	}
	got, err := h.m.Check(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusOK || !got.Premium || got.QuotaLeft() != 900 || got.CheckedAt.IsZero() || got.StatusMsg != "Pro I" {
		t.Fatalf("merged %+v", got)
	}
	if !h.m.QuotaBlockedUntil(a.ID).IsZero() || len(h.repo.marks) != 0 {
		t.Fatal("a successful check with quota left must clear the block")
	}

	h.check = func(context.Context, *Account) (*CheckResult, error) {
		return nil, errors.New("hoster unreachable")
	}
	got, err = h.m.Check(ctx, a.ID)
	if err == nil || got.Status != StatusError || got.StatusMsg != "hoster unreachable" {
		t.Fatalf("error check: %+v %v", got, err)
	}
	evs := h.eventTypes()
	if len(evs) < 4 {
		t.Fatalf("events %v", evs)
	}

	noCheck, _ := NewManager(ctx, ManagerOptions{Repo: h.repo, Secrets: h.sec})
	if _, err := noCheck.Check(ctx, a.ID); !errors.Is(err, ErrNoChecker) {
		t.Fatalf("no checker: %v", err)
	}
}

func TestManagerSubmitMFA(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	a, err := h.m.Create(ctx, NewAccount{Hoster: "mega", Kind: KindPassword, Username: "u@x.y", Secret: Secret{Password: "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	var seenCode string
	h.check = func(ctx context.Context, acct *Account) (*CheckResult, error) {
		s, err := h.sec.Get(ctx, acct.ID)
		if err != nil {
			return nil, err
		}
		seenCode = s.Extra[MFAExtraKey]
		if seenCode == "" {
			return &CheckResult{Status: StatusNeeds2FA, QuotaTotal: -1, QuotaUsed: -1}, nil
		}
		// The plugin stores its new session like Session.SetSecret would.
		s.Session = "sid-123"
		return &CheckResult{Status: StatusOK, QuotaTotal: -1, QuotaUsed: -1}, h.sec.Set(ctx, acct.ID, s)
	}
	if got, _ := h.m.Check(ctx, a.ID); got.Status != StatusNeeds2FA {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := h.m.SubmitMFA(ctx, a.ID, "12a456"); !errors.Is(err, ErrInvalidMFACode) {
		t.Fatalf("bad code: %v", err)
	}
	got, err := h.m.SubmitMFA(ctx, a.ID, " 123 456 ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusOK || seenCode != "123456" {
		t.Fatalf("status %s code %q", got.Status, seenCode)
	}
	s, _ := h.sec.Get(ctx, a.ID)
	if _, ok := s.Extra[MFAExtraKey]; ok || s.Session != "sid-123" || s.Password != "pw" {
		t.Fatalf("after MFA: extra=%v session=%q", s.Extra, s.Session)
	}
}

func TestManagerLockedVault(t *testing.T) {
	ctx := context.Background()
	v, _ := newTestVault(t)
	h := newHarness(t, v)
	m := h.m

	if st := m.StoreStatus(); st.Backend != BackendVault || !st.Locked || st.Initialized {
		t.Fatalf("status %+v", st)
	}
	if _, err := m.Create(ctx, NewAccount{Hoster: "pixeldrain", Kind: KindAPIKey, Secret: Secret{APIKey: "k"}}); !errors.Is(err, ErrLocked) {
		t.Fatalf("create while locked: %v", err)
	}
	if err := m.Unlock(ctx, "wrong but long"); err != nil {
		t.Fatal(err) // first unlock creates the vault
	}
	a, err := m.Create(ctx, NewAccount{Hoster: "pixeldrain", Kind: KindAPIKey, Secret: Secret{APIKey: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Lock(); err != nil {
		t.Fatal(err)
	}
	got, err := m.Check(ctx, a.ID)
	if !errors.Is(err, ErrLocked) || got.Status != StatusError {
		t.Fatalf("check while locked: %+v %v", got, err)
	}
	if err := m.Delete(ctx, a.ID); !errors.Is(err, ErrLocked) {
		t.Fatalf("delete while locked: %v", err)
	}
	if err := m.Unlock(ctx, "nope nope nope"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := m.ChangeMasterPassword(ctx, "wrong but long", "brand new pass"); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	evs := h.eventTypes()
	if !slices.Contains(evs, ChangeLocked) || !slices.Contains(evs, ChangeUnlocked) {
		t.Fatalf("events %v", evs)
	}

	mem := newHarness(t, nil)
	if err := mem.m.Unlock(ctx, "x"); !errors.Is(err, ErrNotLockable) {
		t.Fatalf("memory unlock: %v", err)
	}
}

func TestManagerImportCookiesAndOAuthToken(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	c, err := h.m.Create(ctx, NewAccount{Hoster: "terabox", Kind: KindCookie, Secret: Secret{Cookies: "ndus=old"}})
	if err != nil {
		t.Fatal(err)
	}
	imp, err := h.m.ImportCookies(ctx, c.ID, netscapeSample, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(imp.Cookies) != 4 {
		t.Fatalf("imported %d", len(imp.Cookies))
	}
	s, _ := h.sec.Get(ctx, c.ID)
	if s.Cookies != imp.Normalized {
		t.Fatal("cookies not stored")
	}
	if _, err := h.m.ImportCookies(ctx, c.ID, `[{"name":"a","value":"x","domain":".mega.nz"}]`, nil); !errors.Is(err, ErrNoMatchingCookies) {
		t.Fatalf("foreign cookies: %v", err)
	}

	g, err := h.m.Create(ctx, NewAccount{Hoster: "gdrive", Kind: KindOAuth, Username: "me@gmail.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.m.SaveOAuthToken(ctx, g.ID, &oauth2.Token{AccessToken: "a1", RefreshToken: "r1"}); err != nil {
		t.Fatal(err)
	}
	// Re-consent without refresh token keeps the old one.
	if err := h.m.SaveOAuthToken(ctx, g.ID, &oauth2.Token{AccessToken: "a2"}); err != nil {
		t.Fatal(err)
	}
	s, _ = h.sec.Get(ctx, g.ID)
	if s.OAuthToken.AccessToken != "a2" || s.OAuthToken.RefreshToken != "r1" {
		t.Fatalf("token %+v", s.OAuthToken)
	}
}
