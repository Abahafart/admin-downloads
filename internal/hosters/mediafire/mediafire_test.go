package mediafire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

const fixtures = "../../../testdata/mediafire"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestHoster(t *testing.T, srv *httptest.Server) *Hoster {
	t.Helper()
	hh, err := New(config.HosterConfig{RequestsPerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	h := hh.(*Hoster)
	if srv != nil {
		h.webBase = srv.URL
		h.apiBase = srv.URL + "/api/1.5"
	}
	return h
}

func anonSession() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

// fakeMediaFire serves the API from fixtures and file pages from a map.
type fakeMediaFire struct {
	t     *testing.T
	pages map[string]string // quick key -> fixture name (or "redirect:<url>")
	mu    sync.Mutex
	calls []string
}

func (f *fakeMediaFire) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.Path+"?"+r.URL.RawQuery)
	f.mu.Unlock()
	q := r.URL.Query()
	if q.Get("response_format") == "" && strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, "missing response_format", http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/api/1.5/file/get_info.php":
		if q.Get("quick_key") == "abc123def45" {
			w.Write(fixture(f.t, "api_file_info.json"))
		} else {
			w.Write(fixture(f.t, "api_file_info_error.json"))
		}
	case "/api/1.5/folder/get_info.php":
		if q.Get("folder_key") == "rootfolder01" {
			w.Write(fixture(f.t, "api_folder_info.json"))
		} else {
			w.Write([]byte(`{"response":{"message":"Unknown or Invalid FolderKey","error":112,"result":"Error"}}`))
		}
	case "/api/1.5/folder/get_content.php":
		name := fmt.Sprintf("api_folder_%s_%s_%s.json", q.Get("content_type"), q.Get("folder_key"), q.Get("chunk"))
		b, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			b = fixture(f.t, "api_folder_empty.json")
		}
		w.Write(b)
	case "/application/get_session_token.php":
		if c, err := r.Cookie("user"); err == nil && c.Value == "REDACTED" {
			w.Write(fixture(f.t, "api_session_token.json"))
		} else {
			w.Write([]byte(`{"response":{"message":"not logged in","error":105,"result":"Error"}}`))
		}
	case "/api/1.5/user/get_info.php":
		if q.Get("session_token") != "REDACTED_SESSION_TOKEN" {
			w.Write([]byte(`{"response":{"message":"invalid token","error":105,"result":"Error"}}`))
			return
		}
		w.Write(fixture(f.t, "api_user_info.json"))
	case "/error.php":
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>error</html>"))
	default:
		if strings.HasPrefix(r.URL.Path, "/file/") {
			key := strings.Trim(strings.TrimPrefix(r.URL.Path, "/file/"), "/")
			page, ok := f.pages[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if target, ok := strings.CutPrefix(page, "redirect:"); ok {
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
			if page == "429" {
				w.Header().Set("Retry-After", "30")
				http.Error(w, "slow down", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(fixture(f.t, page))
			return
		}
		http.NotFound(w, r)
	}
}

func TestMatch(t *testing.T) {
	h := newTestHoster(t, nil)
	cases := []struct {
		link string
		kind linkKind
		key  string
	}{
		{"https://www.mediafire.com/file/abc123def45/ubuntu.iso/file", kindFile, "abc123def45"},
		{"https://mediafire.com/file/abc123def45", kindFile, "abc123def45"},
		{"http://www.mediafire.com/file_premium/abc123def45/x.zip", kindFile, "abc123def45"},
		{"https://www.mediafire.com/view/abc123def45/pic.jpg", kindFile, "abc123def45"},
		{"https://www.mediafire.com/download/abc123def45", kindFile, "abc123def45"},
		{"https://www.mediafire.com/download.php?abc123def45", kindFile, "abc123def45"},
		{"https://www.mediafire.com/?abc123def45", kindUnknown, "abc123def45"},
		{"https://www.mediafire.com/folder/rootfolder01/Serie", kindFolder, "rootfolder01"},
		{"https://www.mediafire.com/folder/rootfolder01", kindFolder, "rootfolder01"},
		{"https://www.mediafire.com/#rootfolder01", kindFolder, "rootfolder01"},
		{"https://m.mediafire.com/file/abc123def45", kindFile, "abc123def45"},
		{"https://www.mediafire.com/", kindNone, ""},
		{"https://www.mediafire.com/upgrade/", kindNone, ""},
		{"https://www.mediafire.com/?foo=bar", kindNone, ""},
		{"https://download2391.mediafire.com/tok/abc123def45/ubuntu.iso", kindNone, ""},
		{"https://evilmediafire.com/file/abc123def45", kindNone, ""},
		{"https://example.com/file/abc123def45", kindNone, ""},
	}
	for _, c := range cases {
		u, err := url.Parse(c.link)
		if err != nil {
			t.Fatal(err)
		}
		got := h.parse(u)
		if got.kind != c.kind || got.key != c.key {
			t.Errorf("%s: got %+v, want kind=%d key=%q", c.link, got, c.kind, c.key)
		}
		if h.Match(u) != (c.kind != kindNone) {
			t.Errorf("%s: Match mismatch", c.link)
		}
	}
}

func TestMatchExtraDomains(t *testing.T) {
	hh, _ := New(config.HosterConfig{Domains: []string{"MF.example.org"}})
	u, _ := url.Parse("https://mf.example.org/file/abc123def45")
	if !hh.Match(u) {
		t.Fatal("extra domain not matched")
	}
}

func TestParseFilePage(t *testing.T) {
	base, _ := url.Parse("https://www.mediafire.com/file/abc123def45/")
	cases := []struct {
		fixture string
		want    string
		kind    hosters.Kind
		scope   hosters.QuotaScope
	}{
		{"file_page_href.html", "https://download2391.mediafire.com/REDACTEDtoken/abc123def45/ubuntu.iso", hosters.KindOK, 0},
		{"file_page_scrambled.html", "https://download1504.mediafire.com/REDACTEDtoken/abc123def45/game+setup.exe", hosters.KindOK, 0},
		{"file_page_regex.html", "https://download7.mediafire.com/REDACTEDtoken/qk11chars00/notes.pdf", hosters.KindOK, 0},
		{"file_page_dangerous.html", "", hosters.KindFatal, 0},
		{"file_page_removed.html", "", hosters.KindNotFound, 0},
		{"file_page_captcha.html", "", hosters.KindQuota, hosters.ScopeIP},
		{"file_page_password.html", "", hosters.KindFatal, 0},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			got, err := parseFilePage(fixture(t, c.fixture), base)
			if k := hosters.KindOf(err); k != c.kind {
				t.Fatalf("kind = %v (%v), want %v", k, err, c.kind)
			}
			if got != c.want {
				t.Fatalf("link = %q, want %q", got, c.want)
			}
			if c.kind == hosters.KindQuota {
				he, _ := hosters.AsError(err)
				if he.Scope != c.scope || he.RetryAfter <= 0 {
					t.Fatalf("quota = %+v", he)
				}
			}
		})
	}
}

func TestResolveFile(t *testing.T) {
	fake := &fakeMediaFire{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := newTestHoster(t, srv)

	res, err := h.Resolve(context.Background(), anonSession(), "https://www.mediafire.com/file/abc123def45/ubuntu.iso/file")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 {
		t.Fatalf("res = %+v", res)
	}
	f := res.Files[0]
	if f.ID != "abc123def45" || f.Name != "ubuntu.iso" || f.Size != 1288490188 || f.RelPath != "" {
		t.Fatalf("file = %+v", f)
	}
	if f.Checksum == nil || f.Checksum.Algo != hosters.AlgoSHA256 || f.Checksum.Value != strings.ToLower("3B4C8F1E2D9A0B7C6E5F4A3B2C1D0E9F8A7B6C5D4E3F2A1B0C9D8E7F6A5B4C3D") {
		t.Fatalf("checksum = %+v", f.Checksum)
	}
	if f.Link != "https://www.mediafire.com/file/abc123def45/ubuntu.iso/file" {
		t.Fatalf("link = %q", f.Link)
	}

	_, err = h.Resolve(context.Background(), anonSession(), "https://www.mediafire.com/file/nonexist0001")
	if !hosters.IsNotFound(err) {
		t.Fatalf("removed file: %v", err)
	}
}

func TestResolveFolderRecursivePaginatedStreaming(t *testing.T) {
	fake := &fakeMediaFire{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := newTestHoster(t, srv)

	var emitted []string
	res, err := h.ResolveStream(context.Background(), anonSession(), "https://www.mediafire.com/folder/rootfolder01/Serie", func(f hosters.FileInfo) {
		emitted = append(emitted, f.RelPath)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "Serie Completa" {
		t.Fatalf("res = %+v", res)
	}
	want := []string{"readme.txt", "cover.jpg", "extra.nfo", "Temporada 1/ep01.mkv", "Temporada 1/ep02.mkv", ".._.._etc/passwd"}
	var got []string
	for _, f := range res.Files {
		got = append(got, f.RelPath)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if strings.Join(emitted, "|") != strings.Join(want, "|") {
		t.Fatalf("emitted = %v", emitted)
	}
	if res.Files[3].Size != 734003200 || res.Files[3].Checksum == nil || res.Files[2].Size != 7 {
		t.Fatalf("file meta = %+v / %+v", res.Files[3], res.Files[2])
	}
	if res.Files[1].Checksum != nil {
		t.Fatal("empty hash must not produce a checksum")
	}
	// /?<key> falls back to a folder when the file lookup fails.
	res2, err := h.Resolve(context.Background(), anonSession(), "https://www.mediafire.com/?rootfolder01")
	if err != nil || !res2.IsFolder || len(res2.Files) != 6 {
		t.Fatalf("ambiguous key: %v %+v", err, res2)
	}
	if _, err := h.Resolve(context.Background(), anonSession(), "https://www.mediafire.com/folder/nofolder0001"); !hosters.IsNotFound(err) {
		t.Fatalf("missing folder: %v", err)
	}
}

func TestPrepare(t *testing.T) {
	fake := &fakeMediaFire{t: t, pages: map[string]string{
		"abc123def45":  "file_page_href.html",
		"scrambled001": "file_page_scrambled.html",
		"removed00001": "file_page_removed.html",
		"captcha00001": "file_page_captcha.html",
		"ratelimit001": "429",
		"premium00001": "redirect:https://download99.mediafire.com/tok/premium00001/a.bin",
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := newTestHoster(t, srv)
	ctx := context.Background()
	sum := &hosters.Checksum{Algo: hosters.AlgoSHA256, Value: "ab"}

	spec, err := h.Prepare(ctx, anonSession(), hosters.FileInfo{ID: "abc123def45", Size: 42, Checksum: sum})
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL != "https://download2391.mediafire.com/REDACTEDtoken/abc123def45/ubuntu.iso" || !spec.Resumable || spec.Size != 42 || spec.Checksum != sum {
		t.Fatalf("spec = %+v", spec)
	}
	spec, err = h.Prepare(ctx, anonSession(), hosters.FileInfo{Link: "https://www.mediafire.com/file/scrambled001/x/file"})
	if err != nil || !strings.HasPrefix(spec.URL, "https://download1504.mediafire.com/") {
		t.Fatalf("scrambled: %v %+v", err, spec)
	}
	spec, err = h.Prepare(ctx, anonSession(), hosters.FileInfo{ID: "premium00001"})
	if err != nil || spec.URL != "https://download99.mediafire.com/tok/premium00001/a.bin" {
		t.Fatalf("premium redirect: %v %+v", err, spec)
	}
	if _, err := h.Prepare(ctx, anonSession(), hosters.FileInfo{ID: "removed00001"}); !hosters.IsNotFound(err) {
		t.Fatalf("removed: %v", err)
	}
	if _, err := h.Prepare(ctx, anonSession(), hosters.FileInfo{ID: "captcha00001"}); !hosters.IsQuota(err) {
		t.Fatalf("captcha: %v", err)
	}
	_, err = h.Prepare(ctx, anonSession(), hosters.FileInfo{ID: "ratelimit001"})
	he, ok := hosters.AsError(err)
	if !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeIP || he.RetryAfter.Seconds() != 30 {
		t.Fatalf("429: %v", err)
	}
	if _, err := h.Prepare(ctx, anonSession(), hosters.FileInfo{ID: "missing00001"}); !hosters.IsNotFound(err) {
		t.Fatalf("404: %v", err)
	}
}

func TestPrepareErrnoRedirect(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/file/"):
			http.Redirect(w, r, srvURL+"/error.php?errno=378&origin=download", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body>Something went wrong</body></html>"))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	h := newTestHoster(t, srv)
	_, err := h.Prepare(context.Background(), anonSession(), hosters.FileInfo{ID: "abc123def45"})
	if !hosters.IsNotFound(err) || !strings.Contains(err.Error(), "378") {
		t.Fatalf("errno: %v", err)
	}
}

func TestClassify(t *testing.T) {
	h := newTestHoster(t, nil)
	mk := func(code int, rawURL, ct string) *http.Response {
		u, _ := url.Parse(rawURL)
		r := &http.Response{StatusCode: code, Header: http.Header{}, Request: &http.Request{URL: u}}
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return r
	}
	dl := "https://download2391.mediafire.com/tok/abc/x.bin"
	cases := []struct {
		name string
		resp *http.Response
		body []byte
		err  error
		kind hosters.Kind
	}{
		{"ok", mk(206, dl, "application/octet-stream"), nil, nil, hosters.KindOK},
		{"429", mk(429, dl, ""), nil, nil, hosters.KindQuota},
		{"captcha", mk(200, "https://www.mediafire.com/file/x", "text/html"), fixture(t, "file_page_captcha.html"), nil, hosters.KindQuota},
		{"html instead of file", mk(200, dl, "text/html"), []byte("<html>hi</html>"), nil, hosters.KindExpired},
		{"dl 404", mk(404, dl, ""), nil, nil, hosters.KindExpired},
		{"dl 403", mk(403, dl, ""), nil, nil, hosters.KindExpired},
		{"removed", mk(200, "https://www.mediafire.com/file/x", "text/html"), fixture(t, "file_page_removed.html"), nil, hosters.KindNotFound},
		{"errno", mk(200, "https://www.mediafire.com/error.php?errno=320", "text/html"), nil, nil, hosters.KindNotFound},
		{"api error", mk(200, "https://www.mediafire.com/api/1.5/file/get_info.php", "application/json"), fixture(t, "api_file_info_error.json"), nil, hosters.KindNotFound},
		{"page 404", mk(404, "https://www.mediafire.com/file/x", "text/html"), nil, nil, hosters.KindNotFound},
		{"5xx", mk(503, dl, ""), nil, nil, hosters.KindTemporary},
		{"transport", nil, nil, errors.New("reset"), hosters.KindTemporary},
		{"canceled", nil, nil, context.Canceled, hosters.KindFatal},
	}
	for _, c := range cases {
		got := hosters.KindOf(h.Classify(c.resp, c.body, c.err))
		if got != c.kind {
			t.Errorf("%s: kind %v, want %v", c.name, got, c.kind)
		}
	}
	he, _ := hosters.AsError(h.Classify(mk(429, dl, ""), nil, nil))
	if he.Scope != hosters.ScopeIP || he.RetryAfter <= 0 {
		t.Fatalf("429 quota = %+v", he)
	}
}

func TestParseCookies(t *testing.T) {
	netscape := "# Netscape HTTP Cookie File\n" +
		".mediafire.com\tTRUE\t/\tTRUE\t1999999999\tuser\tREDACTED\n" +
		"#HttpOnly_.mediafire.com\tTRUE\t/\tTRUE\t0\tukey\tREDACTED2\n" +
		"broken line\n"
	cs, err := parseCookies(netscape, ".mediafire.com")
	if err != nil || len(cs) != 2 || cs[0].Name != "user" || !cs[1].HttpOnly || cs[0].Expires.IsZero() {
		t.Fatalf("netscape: %v %+v", err, cs)
	}
	js := `[{"name":"user","value":"REDACTED","domain":".mediafire.com","path":"/","secure":true,"expirationDate":1999999999.5}]`
	cs, err = parseCookies(js, ".mediafire.com")
	if err != nil || len(cs) != 1 || cs[0].Value != "REDACTED" || cs[0].Expires.IsZero() {
		t.Fatalf("json: %v %+v", err, cs)
	}
	cs, err = parseCookies(`{"cookies":[{"name":"a","value":"1"}]}`, ".mediafire.com")
	if err != nil || len(cs) != 1 || cs[0].Domain != ".mediafire.com" || cs[0].Path != "/" {
		t.Fatalf("wrapped json: %v %+v", err, cs)
	}
	cs, err = parseCookies("Cookie: user=REDACTED; ukey=x", ".mediafire.com")
	if err != nil || len(cs) != 2 || cs[1].Name != "ukey" {
		t.Fatalf("header: %v %+v", err, cs)
	}
	if _, err := parseCookies("   ", "x"); err == nil {
		t.Fatal("empty must fail")
	}
	if _, err := parseCookies("[not json", "x"); err == nil {
		t.Fatal("bad json must fail")
	}
}

func accountSession(t *testing.T, cookies string) *hosters.Session {
	t.Helper()
	acct := &accounts.Account{ID: "acc1", Hoster: Name, Kind: accounts.KindCookie, Enabled: true}
	s := hosters.NewSession(netx.NewDirect(), acct, nil)
	store := accounts.NewMemoryStore()
	if err := store.Set(context.Background(), acct.ID, accounts.Secret{Cookies: cookies}); err != nil {
		t.Fatal(err)
	}
	s.Secrets = store
	return s
}

func TestCookiesLoadedIntoJar(t *testing.T) {
	h := newTestHoster(t, nil)
	s := accountSession(t, ".mediafire.com\tTRUE\t/\tTRUE\t1999999999\tuser\tREDACTED\n")
	if err := h.ensureLogin(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://www.mediafire.com/file/x", "https://download12.mediafire.com/a/b"} {
		u, _ := url.Parse(raw)
		cs := s.Jar.Cookies(u)
		if len(cs) != 1 || cs[0].Name != "user" {
			t.Fatalf("%s: cookies %v", raw, cs)
		}
	}
}

func TestCheckAccount(t *testing.T) {
	fake := &fakeMediaFire{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := newTestHoster(t, srv)
	ctx := context.Background()

	res, err := h.CheckAccount(ctx, accountSession(t, "user=REDACTED"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != accounts.StatusOK || !res.Premium || res.QuotaTotal != 10737418240 || res.QuotaUsed != 1048576 {
		t.Fatalf("check = %+v", res)
	}
	res, err = h.CheckAccount(ctx, accountSession(t, "user=wrong"))
	if err != nil || res.Status != accounts.StatusExpired {
		t.Fatalf("bad cookies: %v %+v", err, res)
	}
	res, _ = h.CheckAccount(ctx, accountSession(t, ""))
	if res.Status != accounts.StatusInvalid {
		t.Fatalf("empty cookies: %+v", res)
	}
}

func TestSelfTestAPIShape(t *testing.T) {
	fake := &fakeMediaFire{t: t}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := newTestHoster(t, srv)
	if err := h.SelfTest(context.Background(), anonSession()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistered(t *testing.T) {
	found := false
	for _, n := range hosters.FactoryNames() {
		if n == Name {
			found = true
		}
	}
	if !found {
		t.Fatal("factory not registered")
	}
}

func TestClassifyRedactsSignedURLs(t *testing.T) {
	h := newTestHoster(t, nil)
	err := h.Classify(nil, nil, &url.Error{Op: "Get", URL: "https://download1.mediafire.com/SIGNED/k/f.zip?session_token=SECRET", Err: errors.New("reset")})
	if strings.Contains(err.Error(), "SECRET") || !hosters.IsTemporary(err) {
		t.Fatalf("err = %v", err)
	}
}
