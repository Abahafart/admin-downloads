package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"admindescargas/internal/accounts"
	"admindescargas/internal/core"
	"admindescargas/internal/netx"
	"admindescargas/internal/rotation"
)

// Magic ids that make the fake engine fail.
const (
	idNotFound = 404
	idConflict = 409
	idBoom     = 500
)

func idErr(ids ...int64) error {
	for _, id := range ids {
		switch id {
		case idNotFound:
			return fmt.Errorf("download %d: %w", id, core.ErrNotFound)
		case idConflict:
			return fmt.Errorf("download %d: %w", id, core.ErrInvalidState)
		case idBoom:
			return fmt.Errorf("disk on fire (secret=hunter2)")
		}
	}
	return nil
}

type fakeEngine struct {
	bus   *core.Bus
	mu    sync.Mutex
	calls []string
	last  any
}

func newFakeEngine() *fakeEngine { return &fakeEngine{bus: core.NewBus(64)} }

func (f *fakeEngine) rec(name string, arg any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.last = arg
}

func (f *fakeEngine) lastCall() (string, any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return "", nil
	}
	return f.calls[len(f.calls)-1], f.last
}

func (f *fakeEngine) Start(context.Context) error { return nil }
func (f *fakeEngine) Stop(context.Context) error  { return nil }

func (f *fakeEngine) AddLinks(_ context.Context, req core.AddLinksRequest) ([]core.GrabberEntry, error) {
	f.rec("AddLinks", req)
	return []core.GrabberEntry{{ID: 1, URL: "https://example.com/a", State: core.GrabberPending, Source: req.Source}}, nil
}
func (f *fakeEngine) ListLinkGrabber(context.Context) ([]core.GrabberEntry, error) {
	f.rec("ListLinkGrabber", nil)
	return nil, nil
}
func (f *fakeEngine) ConfirmLinkGrabber(_ context.Context, req core.ConfirmRequest) ([]core.Package, error) {
	f.rec("ConfirmLinkGrabber", req)
	return []core.Package{{ID: 7, Name: "pkg"}}, nil
}
func (f *fakeEngine) RemoveLinkGrabber(_ context.Context, ids []int64) error {
	f.rec("RemoveLinkGrabber", ids)
	return idErr(ids...)
}
func (f *fakeEngine) ListDownloads(_ context.Context, flt core.DownloadFilter) ([]core.Download, error) {
	f.rec("ListDownloads", flt)
	return []core.Download{
		{ID: 1, Name: "Serie S01E01.mkv", State: core.StateDownloading},
		{ID: 2, Name: "other.zip", State: core.StateQueued},
	}, nil
}
func (f *fakeEngine) GetDownload(_ context.Context, id int64) (core.Download, error) {
	f.rec("GetDownload", id)
	if err := idErr(id); err != nil {
		return core.Download{}, err
	}
	return core.Download{ID: id, Name: "file.bin"}, nil
}
func (f *fakeEngine) PauseDownloads(_ context.Context, ids []int64) error {
	f.rec("PauseDownloads", ids)
	return idErr(ids...)
}
func (f *fakeEngine) ResumeDownloads(_ context.Context, ids []int64) error {
	f.rec("ResumeDownloads", ids)
	return idErr(ids...)
}
func (f *fakeEngine) RestartDownloads(_ context.Context, ids []int64) error {
	f.rec("RestartDownloads", ids)
	return idErr(ids...)
}
func (f *fakeEngine) RetryDownloads(_ context.Context, ids []int64) error {
	f.rec("RetryDownloads", ids)
	return idErr(ids...)
}
func (f *fakeEngine) DeleteDownloads(_ context.Context, ids []int64, del bool) error {
	f.rec("DeleteDownloads", del)
	return idErr(ids...)
}
func (f *fakeEngine) RetryAllFailed(context.Context) error { f.rec("RetryAllFailed", nil); return nil }
func (f *fakeEngine) ListPackages(context.Context) ([]core.Package, error) {
	f.rec("ListPackages", nil)
	return []core.Package{{ID: 7, Name: "pkg"}}, nil
}
func (f *fakeEngine) GetPackage(_ context.Context, id int64) (core.Package, []core.Download, error) {
	f.rec("GetPackage", id)
	if err := idErr(id); err != nil {
		return core.Package{}, nil, err
	}
	return core.Package{ID: id, Name: "pkg"}, nil, nil
}
func (f *fakeEngine) PausePackages(_ context.Context, ids []int64) error {
	f.rec("PausePackages", ids)
	return idErr(ids...)
}
func (f *fakeEngine) ResumePackages(_ context.Context, ids []int64) error {
	f.rec("ResumePackages", ids)
	return idErr(ids...)
}
func (f *fakeEngine) RestartPackages(_ context.Context, ids []int64) error {
	f.rec("RestartPackages", ids)
	return idErr(ids...)
}
func (f *fakeEngine) RetryPackages(_ context.Context, ids []int64) error {
	f.rec("RetryPackages", ids)
	return idErr(ids...)
}
func (f *fakeEngine) DeletePackages(_ context.Context, ids []int64, del bool) error {
	f.rec("DeletePackages", del)
	return idErr(ids...)
}
func (f *fakeEngine) RescanPackage(_ context.Context, id int64) ([]core.Download, error) {
	f.rec("RescanPackage", id)
	if err := idErr(id); err != nil {
		return nil, err
	}
	return []core.Download{{ID: 9, PackageID: id}}, nil
}
func (f *fakeEngine) PauseAll(context.Context) error  { f.rec("PauseAll", nil); return nil }
func (f *fakeEngine) ResumeAll(context.Context) error { f.rec("ResumeAll", nil); return nil }
func (f *fakeEngine) Stats(context.Context) (core.Stats, error) {
	f.rec("Stats", nil)
	return core.Stats{Speed: 1234, Active: 1}, nil
}
func (f *fakeEngine) Subscribe(ctx context.Context) <-chan core.Event { return f.bus.Subscribe(ctx) }

var _ core.Engine = (*fakeEngine)(nil)

// fakeAccounts keeps accounts in memory and remembers the last secret it
// received (to check it never comes back).
type fakeAccounts struct {
	mu       sync.Mutex
	list     []accounts.Account
	lastPass string
}

func (f *fakeAccounts) List(context.Context) ([]accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]accounts.Account(nil), f.list...), nil
}
func (f *fakeAccounts) Create(_ context.Context, req AccountCreateRequest) (accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPass = req.Password + req.Cookies + req.APIKey
	a := accounts.Account{ID: fmt.Sprintf("acc-%d", len(f.list)+1), Hoster: req.Hoster, Label: req.Label,
		Kind: accounts.Kind(req.Kind), Username: req.Username, Enabled: *req.Enabled, Status: accounts.StatusUnknown}
	f.list = append(f.list, a)
	return a, nil
}
func (f *fakeAccounts) find(id string) (*accounts.Account, error) {
	for i := range f.list {
		if f.list[i].ID == id {
			return &f.list[i], nil
		}
	}
	return nil, fmt.Errorf("account %q: %w", id, core.ErrNotFound)
}
func (f *fakeAccounts) Update(_ context.Context, id string, p AccountPatch) (accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.find(id)
	if err != nil {
		return accounts.Account{}, err
	}
	if p.Label != nil {
		a.Label = *p.Label
	}
	if p.Enabled != nil {
		a.Enabled = *p.Enabled
	}
	if p.Password != nil {
		f.lastPass = *p.Password
	}
	return *a, nil
}
func (f *fakeAccounts) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.find(id); err != nil {
		return err
	}
	return nil
}
func (f *fakeAccounts) Check(_ context.Context, id string) (accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.find(id)
	if err != nil {
		return accounts.Account{}, err
	}
	a.Status = accounts.StatusOK
	return *a, nil
}
func (f *fakeAccounts) Submit2FA(_ context.Context, id, code string) (accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.find(id)
	if err != nil {
		return accounts.Account{}, err
	}
	if code != "123456" {
		return accounts.Account{}, fmt.Errorf("bad code: %w", core.ErrInvalidArgument)
	}
	a.Status = accounts.StatusOK
	return *a, nil
}
func (f *fakeAccounts) ImportCookies(_ context.Context, hoster, label, cookies string) (accounts.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPass = cookies
	a := accounts.Account{ID: "acc-c", Hoster: hoster, Label: label, Kind: accounts.KindCookie, Enabled: true}
	f.list = append(f.list, a)
	return a, nil
}

type fakeOAuth struct {
	gotRedirect string
}

func (f *fakeOAuth) Start(_ context.Context, id, redirect string) (string, error) {
	if id == "missing" {
		return "", fmt.Errorf("account: %w", core.ErrNotFound)
	}
	f.gotRedirect = redirect
	return "https://accounts.google.com/o/oauth2/auth?state=xyz", nil
}
func (f *fakeOAuth) CompleteURL(ctx context.Context, pasted string) error {
	u, err := url.Parse(pasted)
	if err != nil {
		return fmt.Errorf("bad url: %w", core.ErrInvalidArgument)
	}
	return f.Callback(ctx, u.Query())
}
func (f *fakeOAuth) Callback(_ context.Context, q url.Values) error {
	if q.Get("state") != "good" {
		return fmt.Errorf("unknown state: %w", core.ErrInvalidArgument)
	}
	return nil
}

type fakeEgress struct{}

func (fakeEgress) ImportProxies(_ context.Context, text string) (ImportResult, error) {
	return ImportResult{Added: 1, Errors: []ImportLineError{{Line: 2, Message: "bad line socks5://bob:s3cret@1.2.3.4:1080"}}}, nil
}
func (fakeEgress) ImportWireGuard(_ context.Context, name, conf string) (ImportResult, error) {
	if conf == "bad" {
		return ImportResult{}, fmt.Errorf("parse conf: %w", core.ErrInvalidArgument)
	}
	return ImportResult{Added: 1, IDs: []string{"wg-" + name}}, nil
}
func (fakeEgress) Test(_ context.Context, id string) (rotation.EgressStatus, error) {
	if id != "direct" {
		return rotation.EgressStatus{}, fmt.Errorf("egress %q: %w", id, core.ErrNotFound)
	}
	return rotation.EgressStatus{ID: id, Healthy: true, PublicIP: "203.0.113.7"}, nil
}

// fakeRotation wraps Static and adds a proxy egress with credentials in its label.
type fakeRotation struct {
	*rotation.Static
	rotated []string
	mu      sync.Mutex
}

func (f *fakeRotation) Egresses() []rotation.EgressStatus {
	out := f.Static.Egresses()
	return append(out, rotation.EgressStatus{ID: "p1", Kind: netx.KindSOCKS5, Label: "socks5://alice:pa55word@10.0.0.1:1080",
		LastError: "dial socks5://alice:pa55word@10.0.0.1:1080: refused"})
}

func (f *fakeRotation) Rotate(ctx context.Context, id string) error {
	f.mu.Lock()
	f.rotated = append(f.rotated, id)
	f.mu.Unlock()
	return nil
}

type fakeSelfTest struct{}

func (fakeSelfTest) SelfTest(_ context.Context, h string) (SelfTestResult, error) {
	switch h {
	case "mega":
		return SelfTestResult{Hoster: h, OK: true, DurationMS: 5}, nil
	case "terabox":
		return SelfTestResult{Hoster: h, OK: false, Error: "layout changed"}, nil
	}
	return SelfTestResult{}, fmt.Errorf("hoster %q: %w", h, core.ErrNotFound)
}

func fakePublicIP(_ context.Context, e netx.Egress) (netip.Addr, error) {
	return netip.MustParseAddr("198.51.100.1"), nil
}

// cheapHash builds a fast argon2id PHC string for tests.
func cheapHash(t testing.TB, pw string) string {
	t.Helper()
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(pw), salt, 1, 1024, 1, 32)
	b := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=1024,t=1,p=1$%s$%s", b.EncodeToString(salt), b.EncodeToString(key))
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
