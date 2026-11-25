package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/core"
	"admindescargas/internal/rotation"
)

const testToken = "test-token-0123456789"

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	engine *fakeEngine
	accts  *fakeAccounts
	oauth  *fakeOAuth
	rot    *fakeRotation
	clock  *fakeClock
	deps   Deps
}

func newHarness(t *testing.T, mod ...func(*Deps)) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{t: t, engine: newFakeEngine(), accts: &fakeAccounts{}, oauth: &fakeOAuth{},
		rot: &fakeRotation{Static: rotation.NewStatic(nil)}, clock: &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	cfg := config.Default()
	cfg.Server.Token = testToken
	cfg.Server.PasswordHash = cheapHash(t, "correct horse")
	cfg.Server.AllowedOrigins = []string{"chrome-extension://abcdef"}
	cfg.Egress.Tor.ControlPassword = "tor-secret-pw"
	cfg.Desktop.RemoteToken = "remote-secret-token"
	h.deps = Deps{
		Engine:       h.engine,
		Rotation:     h.rot,
		Accounts:     h.accts,
		OAuth:        h.oauth,
		Settings:     NewConfigSettings("", cfg, nil),
		SelfTest:     fakeSelfTest{},
		Egress:       fakeEgress{},
		PublicIP:     fakePublicIP,
		Server:       cfg.Server,
		UI:           fstest.MapFS{"index.html": {Data: []byte("<!doctype html>INDEX")}, "assets/app.js": {Data: []byte("console.log(1)")}},
		ExtraOrigins: []string{"wails://wails.localhost"},
		Context:      ctx,
		SSEHeartbeat: 30 * time.Millisecond,
		Now:          h.clock.Now,
	}
	for _, m := range mod {
		m(&h.deps)
	}
	h.srv = httptest.NewServer(New(h.deps))
	t.Cleanup(h.srv.Close)
	return h
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func noAuth() reqOpt                { return func(r *http.Request) { r.Header.Del("Authorization") } }
func withCookie(c *http.Cookie) reqOpt {
	return func(r *http.Request) { r.Header.Del("Authorization"); r.AddCookie(c) }
}

// do sends a request (with the bearer token unless overridden) and returns
// status and body.
func (h *harness) do(method, path, body string, opts ...reqOpt) (int, string, http.Header) {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func errCode(t *testing.T, body string) string {
	t.Helper()
	var eb ErrorBody
	if err := json.Unmarshal([]byte(body), &eb); err != nil {
		t.Fatalf("error body %q: %v", body, err)
	}
	return eb.Error.Code
}

func TestRoutesHappyPath(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		method, path, body string
		want               int
		call               string // fake engine method expected
		contains           string
	}{
		{"GET", "/api/v1/health", "", 200, "", `"status":"ok"`},
		{"POST", "/api/v1/links", `{"text":"mira https://example.com/a"}`, 200, "AddLinks", `"source":"api"`},
		{"GET", "/api/v1/linkgrabber", "", 200, "ListLinkGrabber", `[]`},
		{"POST", "/api/v1/linkgrabber/confirm", `{"items":[{"entry_id":1}]}`, 200, "ConfirmLinkGrabber", `"name":"pkg"`},
		{"POST", "/api/v1/linkgrabber/remove", `{"ids":[1]}`, 200, "RemoveLinkGrabber", `"ok":true`},
		{"GET", "/api/v1/downloads", "", 200, "ListDownloads", `other.zip`},
		{"GET", "/api/v1/downloads/5", "", 200, "GetDownload", `"id":5`},
		{"POST", "/api/v1/downloads/pause", `{"ids":[1,2]}`, 200, "PauseDownloads", `"ok":true`},
		{"POST", "/api/v1/downloads/resume", `{"ids":[1]}`, 200, "ResumeDownloads", ""},
		{"POST", "/api/v1/downloads/restart", `{"ids":[1]}`, 200, "RestartDownloads", ""},
		{"POST", "/api/v1/downloads/retry", `{"ids":[1]}`, 200, "RetryDownloads", ""},
		{"POST", "/api/v1/downloads/delete", `{"ids":[1],"delete_files":true}`, 200, "DeleteDownloads", ""},
		{"POST", "/api/v1/downloads/retry-failed", "", 200, "RetryAllFailed", ""},
		{"GET", "/api/v1/packages", "", 200, "ListPackages", `"pkg"`},
		{"GET", "/api/v1/packages/7", "", 200, "GetPackage", `"downloads":[]`},
		{"POST", "/api/v1/packages/pause", `{"ids":[7]}`, 200, "PausePackages", ""},
		{"POST", "/api/v1/packages/resume", `{"ids":[7]}`, 200, "ResumePackages", ""},
		{"POST", "/api/v1/packages/restart", `{"ids":[7]}`, 200, "RestartPackages", ""},
		{"POST", "/api/v1/packages/retry", `{"ids":[7]}`, 200, "RetryPackages", ""},
		{"POST", "/api/v1/packages/delete", `{"ids":[7]}`, 200, "DeletePackages", ""},
		{"POST", "/api/v1/packages/7/rescan", "", 200, "RescanPackage", `"package_id":7`},
		{"POST", "/api/v1/pause-all", "", 200, "PauseAll", ""},
		{"POST", "/api/v1/resume-all", "", 200, "ResumeAll", ""},
		{"GET", "/api/v1/stats", "", 200, "Stats", `"speed":1234`},
		{"POST", "/api/v1/accounts", `{"hoster":"mega","kind":"password","username":"u@x","password":"pw-123"}`, 201, "", `"hoster":"mega"`},
		{"GET", "/api/v1/accounts", "", 200, "", `"acc-1"`},
		{"PATCH", "/api/v1/accounts/acc-1", `{"label":"nuevo","password":"pw-456"}`, 200, "", `"label":"nuevo"`},
		{"POST", "/api/v1/accounts/acc-1/check", "", 200, "", `"status":"ok"`},
		{"POST", "/api/v1/accounts/acc-1/2fa", `{"code":"123456"}`, 200, "", `"status":"ok"`},
		{"POST", "/api/v1/accounts/import-cookies", `{"hoster":"terabox","label":"tb","cookies":"ndus=COOKIEVAL"}`, 201, "", `"kind":"cookie"`},
		{"DELETE", "/api/v1/accounts/acc-1", "", 200, "", `"ok":true`},
		{"GET", "/api/v1/oauth/google/start?account_id=acc-1", "", 200, "", `accounts.google.com`},
		{"GET", "/api/v1/egress", "", 200, "", `"id":"direct"`},
		{"POST", "/api/v1/egress/import", `{"text":"socks5://bob:s3cret@1.2.3.4:1080"}`, 200, "", `"added":1`},
		{"POST", "/api/v1/egress/wireguard", `{"name":"mullvad-se","conf":"[Interface]"}`, 200, "", `wg-mullvad-se`},
		{"POST", "/api/v1/egress/direct/test", "", 200, "", `203.0.113.7`},
		{"POST", "/api/v1/rotation/rotate", `{"egress_id":""}`, 200, "", `"ok":true`},
		{"POST", "/api/v1/rotation/rotate", ``, 200, "", `"ok":true`},
		{"GET", "/api/v1/ip", "", 200, "", `198.51.100.1`},
		{"GET", "/api/v1/settings", "", 200, "", `"estilo":"grafito"`},
		{"PUT", "/api/v1/settings", `{"appearance":{"estilo":"terminal","tema":"oscuro","acento":"#f0b45a"}}`, 200, "", `"estilo":"terminal"`},
		{"POST", "/api/v1/selftest/mega", "", 200, "", `"ok":true`},
		{"POST", "/api/v1/selftest/terabox", "", 200, "", `"ok":false`},
		{"GET", "/api/v1/auth/session", "", 200, "", `"method":"token"`},
	}
	for _, c := range cases {
		status, body, _ := h.do(c.method, c.path, c.body)
		if status != c.want {
			t.Errorf("%s %s = %d %s, want %d", c.method, c.path, status, body, c.want)
			continue
		}
		if c.contains != "" && !strings.Contains(body, c.contains) {
			t.Errorf("%s %s body %s lacks %q", c.method, c.path, body, c.contains)
		}
		if c.call != "" {
			if got, _ := h.engine.lastCall(); got != c.call {
				t.Errorf("%s %s called %q, want %q", c.method, c.path, got, c.call)
			}
		}
	}
	if !strings.HasSuffix(h.oauth.gotRedirect, "/api/v1/oauth/google/callback") || !strings.HasPrefix(h.oauth.gotRedirect, "http://127.0.0.1:") {
		t.Errorf("redirect = %q", h.oauth.gotRedirect)
	}
}

func TestDownloadFilters(t *testing.T) {
	h := newHarness(t)
	status, body, _ := h.do("GET", "/api/v1/downloads?state=downloading,queued&package_id=3&q=s01e01", "")
	if status != 200 {
		t.Fatal(status, body)
	}
	_, arg := h.engine.lastCall()
	f := arg.(core.DownloadFilter)
	if f.PackageID != 3 || len(f.States) != 2 || f.States[1] != core.StateQueued {
		t.Fatalf("filter = %+v", f)
	}
	var out []core.Download
	_ = json.Unmarshal([]byte(body), &out)
	if len(out) != 1 || out[0].ID != 1 {
		t.Fatalf("q filter: %s", body)
	}
	if status, body, _ := h.do("GET", "/api/v1/downloads?state=bogus", ""); status != 400 || errCode(t, body) != CodeInvalidArgument {
		t.Fatalf("bad state = %d %s", status, body)
	}
}

func TestErrorMapping(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		method, path, body string
		want               int
		code               string
	}{
		{"GET", "/api/v1/downloads/404", "", 404, CodeNotFound},
		{"GET", "/api/v1/downloads/abc", "", 400, CodeInvalidArgument},
		{"POST", "/api/v1/downloads/pause", `{"ids":[409]}`, 409, CodeInvalidState},
		{"POST", "/api/v1/downloads/pause", `{"ids":[]}`, 400, CodeInvalidArgument},
		{"POST", "/api/v1/downloads/pause", `{not json`, 400, CodeInvalidArgument},
		{"POST", "/api/v1/downloads/explode", `{"ids":[1]}`, 404, CodeNotFound},
		{"POST", "/api/v1/downloads/resume", `{"ids":[500]}`, 500, CodeInternal},
		{"POST", "/api/v1/packages/404/rescan", "", 404, CodeNotFound},
		{"GET", "/api/v1/packages/404", "", 404, CodeNotFound},
		{"POST", "/api/v1/links", `{}`, 400, CodeInvalidArgument},
		{"POST", "/api/v1/linkgrabber/confirm", `{"items":[]}`, 400, CodeInvalidArgument},
		{"POST", "/api/v1/accounts", `{"hoster":"mega","kind":"magic"}`, 400, CodeInvalidArgument},
		{"PATCH", "/api/v1/accounts/nope", `{}`, 404, CodeNotFound},
		{"POST", "/api/v1/accounts/nope/check", "", 404, CodeNotFound},
		{"POST", "/api/v1/accounts/nope/2fa", `{"code":"1"}`, 404, CodeNotFound},
		{"GET", "/api/v1/oauth/google/start", "", 400, CodeInvalidArgument},
		{"GET", "/api/v1/oauth/google/start?account_id=missing", "", 404, CodeNotFound},
		{"POST", "/api/v1/egress/wireguard", `{"name":"x","conf":"bad"}`, 400, CodeInvalidArgument},
		{"POST", "/api/v1/egress/nope/test", "", 404, CodeNotFound},
		{"POST", "/api/v1/rotation/rotate", `{"egress_id":"nope"}`, 404, CodeNotFound},
		{"PUT", "/api/v1/settings", `{"appearance":{"estilo":"rosa","tema":"claro"}}`, 400, CodeInvalidArgument},
		{"PUT", "/api/v1/settings", `{"downloads":{"max_active":0}}`, 400, CodeInvalidArgument},
		{"POST", "/api/v1/selftest/nope", "", 404, CodeNotFound},
		{"GET", "/api/v1/nothing-here", "", 404, CodeNotFound},
		{"POST", "/api/v1/health", "", 404, CodeNotFound},
	}
	for _, c := range cases {
		status, body, hdr := h.do(c.method, c.path, c.body)
		if status != c.want {
			t.Errorf("%s %s = %d %s, want %d", c.method, c.path, status, body, c.want)
			continue
		}
		if !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
			t.Errorf("%s %s content-type %q", c.method, c.path, hdr.Get("Content-Type"))
		}
		if got := errCode(t, body); got != c.code {
			t.Errorf("%s %s code = %q, want %q", c.method, c.path, got, c.code)
		}
		if strings.Contains(body, "hunter2") {
			t.Errorf("%s %s leaks internal error: %s", c.method, c.path, body)
		}
	}
}

func TestNilDepsAnswer501(t *testing.T) {
	h := newHarness(t, func(d *Deps) { *d = Deps{Server: d.Server, Now: d.Now} })
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/events", ""},
		{"GET", "/api/v1/downloads", ""},
		{"POST", "/api/v1/links", `{"text":"x"}`},
		{"GET", "/api/v1/accounts", ""},
		{"GET", "/api/v1/oauth/google/start?account_id=a", ""},
		{"GET", "/api/v1/oauth/google/callback?state=x", ""},
		{"GET", "/api/v1/egress", ""},
		{"POST", "/api/v1/egress/import", `{"text":"x"}`},
		{"POST", "/api/v1/egress/x/test", ""},
		{"GET", "/api/v1/ip", ""},
		{"GET", "/api/v1/settings", ""},
		{"POST", "/api/v1/selftest/mega", ""},
		{"GET", "/api/v1/stats", ""},
	} {
		status, body, _ := h.do(c.method, c.path, c.body)
		if status != 501 || errCode(t, body) != CodeNotImplemented {
			t.Errorf("%s %s = %d %s, want 501", c.method, c.path, status, body)
		}
	}
	if status, _, _ := h.do("GET", "/api/v1/health", ""); status != 200 {
		t.Errorf("health = %d", status)
	}
	if status, _, _ := h.do("GET", "/", ""); status != 404 {
		t.Errorf("no UI: / = %d", status)
	}
}

func TestSecretsNeverReturned(t *testing.T) {
	h := newHarness(t)
	_, body, _ := h.do("POST", "/api/v1/accounts", `{"hoster":"mega","kind":"password","password":"TOPSECRETPW"}`)
	_, body2, _ := h.do("POST", "/api/v1/accounts/import-cookies", `{"hoster":"terabox","cookies":"ndus=COOKIESECRET"}`)
	_, body3, _ := h.do("PATCH", "/api/v1/accounts/acc-1", `{"api_key":"APIKEYSECRET","password":"NEWPW"}`)
	_, list, _ := h.do("GET", "/api/v1/accounts", "")
	_, settings, _ := h.do("GET", "/api/v1/settings", "")
	_, egress, _ := h.do("GET", "/api/v1/egress", "")
	_, imp, _ := h.do("POST", "/api/v1/egress/import", `{"text":"x"}`)
	all := strings.Join([]string{body, body2, body3, list, settings, egress, imp}, "\n")
	for _, secret := range []string{"TOPSECRETPW", "COOKIESECRET", "APIKEYSECRET", "NEWPW", testToken,
		h.deps.Server.PasswordHash, "tor-secret-pw", "remote-secret-token", "pa55word", "s3cret", "password_hash"} {
		if strings.Contains(all, secret) {
			t.Errorf("response leaks %q:\n%s", secret, all)
		}
	}
	if h.accts.lastPass == "" {
		t.Error("secret did not reach the service")
	}
	if !strings.Contains(egress, "alice:xxxxx@10.0.0.1") {
		t.Errorf("egress label not redacted as expected: %s", egress)
	}
}

func TestOAuthCallbackIsPublic(t *testing.T) {
	h := newHarness(t)
	status, body, hdr := h.do("GET", "/api/v1/oauth/google/callback?state=good&code=c", "", noAuth())
	if status != 200 || !strings.Contains(body, "conectada") || !strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
		t.Fatalf("callback = %d %s", status, body)
	}
	status, body, _ = h.do("GET", "/api/v1/oauth/google/callback?state=<script>alert(1)</script>", "", noAuth())
	if status != 400 || strings.Contains(body, "<script>alert") {
		t.Fatalf("bad state = %d %s", status, body)
	}
}

func TestStaticSPA(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		path     string
		want     int
		contains string
	}{
		{"/", 200, "INDEX"},
		{"/descargas", 200, "INDEX"},
		{"/ajustes/apariencia", 200, "INDEX"},
		{"/assets/app.js", 200, "console.log"},
		{"/assets/missing.js", 404, ""},
		{"/__adesc/connection", 404, ""},
		{"/../../etc/passwd", 200, "INDEX"},
	} {
		status, body, hdr := h.do("GET", c.path, "", noAuth())
		if status != c.want || !strings.Contains(body, c.contains) {
			t.Errorf("GET %s = %d %q", c.path, status, body)
		}
		if hdr.Get("X-Frame-Options") != "DENY" || hdr.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s missing security headers", c.path)
		}
	}
	if status, _, _ := h.do("POST", "/", "", noAuth()); status != 405 {
		t.Errorf("POST / = %d", status)
	}
}
