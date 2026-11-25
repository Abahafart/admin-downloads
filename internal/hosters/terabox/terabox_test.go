package terabox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

var fixtures = filepath.Join("..", "..", "..", "testdata", "terabox")

const (
	pageToken  = "D8F1E3A9C27B4D6E8F0A1B2C3D4E5F60718293A4B5C6D7E8F9012345678ABCDE"
	pageLogID  = "48261937502918374650"
	plainToken = "A1B2C3D4E5F6A7B8C9D0E1F2A3B4C5D6E7F8A9B0C1D2E3F4A5B6C7D8E9F0A1B2"
	plainLogID = "11223344556677889900"
	goodNdus   = "testndus"
)

// fakeTeraBox serves the fixtures like the TeraBox web front-end.
type fakeTeraBox struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	pageLoads  map[string]int
	staleOnce  bool
	dlinkNdus  []string // ndus cookie seen on dlink requests
	redirNdus  string
	listParams []url.Values
}

func newFake(t *testing.T) *fakeTeraBox {
	f := &fakeTeraBox{t: t, pageLoads: map[string]int{}, staleOnce: true}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTeraBox) fixture(name string) []byte {
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		f.t.Errorf("fixture %s: %v", name, err)
		return []byte("{}")
	}
	return bytes.ReplaceAll(b, []byte("{{BASE}}"), []byte(f.srv.URL))
}

func (f *fakeTeraBox) json(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(f.fixture(name))
}

func ndus(r *http.Request) string {
	if c, err := r.Cookie("ndus"); err == nil {
		return c.Value
	}
	return ""
}

var pages = map[string]string{"Pla1n": "share_page_plain.html", "Bl0cked": "share_page_blocked.html"}

func tokensFor(surl string) (string, string) {
	if surl == "Pla1n" {
		return plainToken, plainLogID
	}
	return pageToken, pageLogID
}

func (f *fakeTeraBox) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if lang, err := r.Cookie("lang"); err != nil || lang.Value != "en" {
		if !strings.HasPrefix(r.URL.Path, "/cdn/") {
			f.t.Errorf("%s: lang cookie missing", r.URL.Path)
		}
	}
	if r.Header.Get("User-Agent") != UserAgent {
		f.t.Errorf("%s: unexpected User-Agent", r.URL.Path)
	}
	switch {
	case r.URL.Path == pathSharingLink && q.Get("surl") == "Redir3ct":
		http.Redirect(w, r, "/sharing/link?surl=F1leXyZ&from=redirect", http.StatusFound)
	case r.URL.Path == pathSharingLink:
		surl := q.Get("surl")
		if q.Get("from") == "redirect" {
			f.mu.Lock()
			f.redirNdus = ndus(r)
			f.mu.Unlock()
		}
		f.mu.Lock()
		f.pageLoads[surl]++
		f.mu.Unlock()
		page := pages[surl]
		if page == "" {
			page = "share_page.html"
		}
		http.SetCookie(w, &http.Cookie{Name: "csrfToken", Value: "x", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(f.fixture(page))
	case r.URL.Path == pathShortURLInfo || r.URL.Path == pathShareList:
		f.serveAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/file/"):
		name := strings.TrimPrefix(r.URL.Path, "/file/")
		f.mu.Lock()
		f.dlinkNdus = append(f.dlinkNdus, ndus(r))
		f.mu.Unlock()
		if name == "video.mp4" && ndus(r) != goodNdus {
			f.json(w, "errno_dlink_login.json")
			return
		}
		http.Redirect(w, r, f.srv.URL+"/cdn/"+name+"?sign=CDNSIGN", http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/cdn/"):
		name := strings.TrimPrefix(r.URL.Path, "/cdn/")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content(name)))
	case r.URL.Path == pathCheckLogin:
		if ndus(r) != goodNdus {
			f.json(w, "errno_login.json")
			return
		}
		f.json(w, "check_login.json")
	case r.URL.Path == pathMembership:
		f.json(w, "membership.json")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTeraBox) serveAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	short := q.Get("shorturl")
	surl := short
	if r.URL.Path == pathShortURLInfo {
		if !strings.HasPrefix(short, "1") {
			f.t.Errorf("shorturlinfo without leading 1: %q", short)
		}
		surl = strings.TrimPrefix(short, "1")
	}
	tok, logid := tokensFor(surl)
	if q.Get("app_id") != paramAppID || q.Get("channel") != paramChannel || q.Get("web") != "1" || q.Get("clienttype") != "0" {
		f.t.Errorf("%s: fixed params missing: %v", r.URL.Path, q)
	}
	if q.Get("jsToken") != tok || q.Get("dp-logid") != logid {
		f.json(w, "errno_param.json")
		return
	}
	if r.URL.Path == pathShortURLInfo {
		switch surl {
		case "Fold3rAbC":
			f.json(w, "shorturlinfo_folder.json")
		case "Mult1Pack":
			f.json(w, "shorturlinfo_multi.json")
		case "G0ne":
			f.json(w, "errno_notfound.json")
		default:
			f.json(w, "shorturlinfo_file.json")
		}
		return
	}
	f.mu.Lock()
	f.listParams = append(f.listParams, q)
	stale := surl == "St4le" && f.staleOnce
	if stale {
		f.staleOnce = false
	}
	f.mu.Unlock()
	if stale {
		f.json(w, "errno_param.json")
		return
	}
	dir, page := q.Get("dir"), q.Get("page")
	if dir == "" && q.Get("root") != "1" {
		f.t.Errorf("list without dir nor root=1")
	}
	switch {
	case page != "1" && !(surl == "Fold3rAbC" && dir == "/Mi Serie" && page == "2"):
		_, _ = w.Write([]byte(`{"errno":0,"list":[]}`))
	case surl == "Fold3rAbC" && dir == "":
		f.json(w, "list_root_folder.json")
	case surl == "Fold3rAbC" && dir == "/Mi Serie" && page == "1":
		f.json(w, "list_dir_serie_p1.json")
	case surl == "Fold3rAbC" && dir == "/Mi Serie" && page == "2":
		f.json(w, "list_dir_serie_p2.json")
	case surl == "Fold3rAbC" && dir == "/Mi Serie/Temporada 1":
		f.json(w, "list_dir_temporada.json")
	case surl == "Mult1Pack" && dir == "":
		f.json(w, "list_root_multi.json")
	case surl == "Mult1Pack" && dir == "/Extras":
		f.json(w, "list_dir_multi_extras.json")
	case surl == "Pr1vate" && ndus(r) != goodNdus:
		f.json(w, "errno_login.json")
	case surl == "Ver1fy":
		f.json(w, "errno_verify.json")
	case surl == "N0dl":
		f.json(w, "list_root_nodlink.json")
	case dir == "":
		f.json(w, "list_root_file.json")
	default:
		_, _ = w.Write([]byte(`{"errno":0,"list":[]}`))
	}
}

// content returns deterministic file bytes.
func content(name string) []byte {
	n := map[string]int{"video.mp4": 262144, "ep01.mkv": 262144, "poster.jpg": 5, "notes.txt": 11}[name]
	if n == 0 {
		n = 3
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + len(name))
	}
	return b
}

func newPlugin(t *testing.T, f *fakeTeraBox, opts map[string]string) *Plugin {
	t.Helper()
	o := map[string]string{OptBaseURL: f.srv.URL, OptAccountURL: f.srv.URL, OptPageSize: "2"}
	for k, v := range opts {
		o[k] = v
	}
	h, err := New(config.HosterConfig{Options: o, RequestsPerSecond: 1000, RetryAfter: config.D(17 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Plugin)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func anon() *hosters.Session {
	s := hosters.NewSession(netx.NewDirect(), nil, nil)
	s.Log = quietLog()
	return s
}

func withCookies(t *testing.T, cookies string) *hosters.Session {
	t.Helper()
	acct := &accounts.Account{ID: "tb1", Hoster: Name, Kind: accounts.KindCookie, Enabled: true}
	s := hosters.NewSession(netx.NewDirect(), acct, nil)
	s.Log = quietLog()
	st := accounts.NewMemoryStore()
	_ = st.Set(context.Background(), acct.ID, accounts.Secret{Cookies: cookies})
	s.Secrets = st
	return s
}

func fetch(t *testing.T, s *hosters.Session, spec *hosters.DownloadSpec, rng string) []byte {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, spec.URL, nil)
	for k, v := range spec.Header {
		req.Header[k] = v
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}

func TestParseShareLinkAndMatch(t *testing.T) {
	h, _ := New(config.HosterConfig{Domains: []string{"newterabox.example"}})
	p := h.(*Plugin)
	cases := []struct {
		in        string
		surl, fid string
		match     bool
	}{
		{"https://www.terabox.com/s/1AbCdEf-gh_I", "AbCdEf-gh_I", "", true},
		{"https://terabox.app/s/1AbCd/", "AbCd", "", true},
		{"https://www.1024terabox.com/sharing/link?surl=AbCd", "AbCd", "", true},
		{"https://www.teraboxapp.com/wap/share/filelist?surl=AbCd&page=1", "AbCd", "", true},
		{"https://www.freeterabox.com/sharing/embed?surl=AbCd", "AbCd", "", true},
		{"https://mirrobox.com/s/1AbCd#fsid=1234", "AbCd", "1234", true},
		{"https://www.4funbox.com/s/1AbCd", "AbCd", "", true},
		{"https://teraboxlink.com/s/1AbCd", "AbCd", "", true},
		{"https://terasharelink.com/s/1AbCd", "AbCd", "", true},
		{"https://share.newterabox.example/s/1AbCd", "AbCd", "", true},
		{"https://www.terabox.com/main?category=all", "", "", false},
		{"https://www.terabox.com/s/1", "", "", false},
		{"https://www.terabox.com/sharing/link?surl=bad%20surl", "", "", false},
		{"https://terabox.com.evil.org/s/1AbCd", "", "", false},
		{"https://example.com/s/1AbCd", "", "", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.in)
		if got := p.Match(u); got != c.match {
			t.Errorf("Match(%s) = %v", c.in, got)
		}
		if !c.match {
			continue
		}
		l, err := parseShareLink(u)
		if err != nil || l.SURL != c.surl || l.FsID != c.fid {
			t.Errorf("%s: %+v %v", c.in, l, err)
		}
	}
	// replace_domains drops the defaults.
	h2, _ := New(config.HosterConfig{Domains: []string{"only.example"}, Options: map[string]string{OptReplaceDomains: "true"}})
	u, _ := url.Parse("https://www.terabox.com/s/1AbCd")
	if h2.Match(u) {
		t.Error("replace_domains kept defaults")
	}
}

func TestExtractTokens(t *testing.T) {
	read := func(n string) []byte { b, _ := os.ReadFile(filepath.Join(fixtures, n)); return b }
	tk := extractTokens(read("share_page.html"))
	if tk.JSToken != pageToken || tk.LogID != pageLogID || tk.BDSToken != "5c1e0a9b7d3f42e6a8b19c0d2e4f6a71" {
		t.Fatalf("share_page: %+v", tk)
	}
	tk = extractTokens(read("share_page_plain.html"))
	if tk.JSToken != plainToken || tk.LogID != plainLogID {
		t.Fatalf("plain: %+v", tk)
	}
	if tk := extractTokens(read("share_page_blocked.html")); tk.JSToken != "" {
		t.Fatalf("blocked: %+v", tk)
	}
	for _, page := range []string{`<script>window.jsToken = "ABC123"</script>`, `{"jsToken":"ABC123"}`, `fn( 'ABC123' )`} {
		if got := extractTokens([]byte(page)).JSToken; got != "ABC123" {
			t.Errorf("%s -> %q", page, got)
		}
	}
}

func TestParseCookies(t *testing.T) {
	doms := DefaultDomains
	netscape := "# Netscape HTTP Cookie File\n" +
		".terabox.com\tTRUE\t/\tTRUE\t1893456000\tndus\tYUKx-abc_123\n" +
		"#HttpOnly_.terabox.com\tTRUE\t/\tTRUE\t0\tcsrfToken\tq1w2e3\n" +
		".google.com\tTRUE\t/\tTRUE\t0\tSID\tleak-me-not\n" +
		"malformed line\n"
	cases := []struct {
		name, in string
		want     map[string]string
	}{
		{"netscape", netscape, map[string]string{"ndus": "YUKx-abc_123", "csrfToken": "q1w2e3"}},
		{"header", "Cookie: ndus=abc; lang=en ;browserid=xyz==", map[string]string{"ndus": "abc", "lang": "en", "browserid": "xyz=="}},
		{"json array", `[{"name":"ndus","value":"abc","domain":".1024terabox.com"},{"name":"x","value":"y","domain":"evil.com"}]`, map[string]string{"ndus": "abc"}},
		{"json object", `{"ndus":"abc"}`, map[string]string{"ndus": "abc"}},
		{"bad values dropped", "ndus=a b; ok=1; =x; ;", map[string]string{"ok": "1"}},
		{"empty", "", map[string]string{}},
	}
	for _, c := range cases {
		cs, err := parseCookies(c.in, doms)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := map[string]string{}
		for _, ck := range cs {
			got[ck.Name] = ck.Value
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s=%q want %q", c.name, k, got[k], v)
			}
		}
	}
	if _, err := parseCookies("[not json", doms); err == nil {
		t.Error("bad JSON accepted")
	}
}

func TestResolveFolderAndPrepare(t *testing.T) {
	f := newFake(t)
	p := newPlugin(t, f, nil)
	s := anon()
	ctx := t.Context()
	var emitted []string
	res, err := p.ResolveStream(ctx, s, "https://www.terabox.com/s/1Fold3rAbC", func(fi hosters.FileInfo) { emitted = append(emitted, fi.RelPath) })
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	byName := map[string]hosters.FileInfo{}
	for _, fi := range res.Files {
		got = append(got, fi.RelPath)
		byName[fi.Name] = fi
	}
	want := []string{"Temporada 1/ep01.mkv", "Temporada 1/.._evil.mkv", "poster.jpg", "notes.txt"}
	if !res.IsFolder || res.Name != "Mi Serie" || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("result %q %v: %q", res.Name, res.IsFolder, got)
	}
	if strings.Join(emitted, "|") != strings.Join(want, "|") {
		t.Fatalf("emitted %q", emitted)
	}
	ep := byName["ep01.mkv"]
	if ep.ID != "702938475610500" || ep.Size != 262144 || ep.Meta[metaDir] != "/Mi Serie/Temporada 1" || ep.Meta[metaSURL] != "Fold3rAbC" ||
		ep.Link != "https://www.terabox.com/s/1Fold3rAbC#fsid=702938475610500" || ep.Checksum != nil || ep.Meta[metaMD5] == "" {
		t.Fatalf("ep01: %+v", ep)
	}
	if notes := byName["notes.txt"]; notes.ID != "702938475610402" || notes.Size != 11 {
		t.Fatalf("numeric fs_id/size: %+v", notes)
	}
	for _, fi := range res.Files {
		for _, v := range fi.Meta {
			if strings.Contains(v, "sign=") {
				t.Fatal("signed dlink persisted in Meta")
			}
		}
	}

	// Prepare from the session cache (no extra listing) and download.
	spec, err := p.Prepare(ctx, s, byName["poster.jpg"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.URL, "/cdn/poster.jpg") || !spec.Resumable || spec.Size != 5 || spec.Expires.IsZero() || spec.Header.Get("User-Agent") != UserAgent {
		t.Fatalf("spec %+v", spec)
	}
	if b := fetch(t, s, spec, ""); !bytes.Equal(b, content("poster.jpg")) {
		t.Fatal("poster content differs")
	}

	// A fresh session (resume after restart) lists the parent dir again.
	s2 := anon()
	f.mu.Lock()
	before := len(f.listParams)
	f.mu.Unlock()
	spec, err = p.Prepare(ctx, s2, ep)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := f.listParams[len(f.listParams)-1]
	after := len(f.listParams)
	f.mu.Unlock()
	if after != before+1 || last.Get("dir") != "/Mi Serie/Temporada 1" {
		t.Fatalf("re-list: %d -> %d, dir=%q", before, after, last.Get("dir"))
	}
	if b := fetch(t, s2, spec, "bytes=1000-1999"); !bytes.Equal(b, content("ep01.mkv")[1000:2000]) {
		t.Fatal("range content differs")
	}

	// The per-file link re-resolves to that file only.
	one, err := p.Resolve(ctx, anon(), ep.Link)
	if err != nil || one.IsFolder || len(one.Files) != 1 || one.Files[0].ID != ep.ID || one.Files[0].RelPath != "" {
		t.Fatalf("fsid link: %+v %v", one, err)
	}
	if _, err := p.Resolve(ctx, anon(), "https://www.terabox.com/s/1Fold3rAbC#fsid=42"); !hosters.IsNotFound(err) {
		t.Fatalf("missing fsid: %v", err)
	}
	// Prepare from the link alone.
	if _, err := p.Prepare(ctx, anon(), hosters.FileInfo{Link: byName["poster.jpg"].Link}); err != nil {
		t.Fatal(err)
	}
}

func TestSingleFileNeedsCookie(t *testing.T) {
	f := newFake(t)
	p := newPlugin(t, f, map[string]string{OptVerifyMD5: "true"})
	ctx := t.Context()
	res, err := p.Resolve(ctx, anon(), "https://www.1024tera.com/sharing/link?surl=F1leXyZ")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 || res.Files[0].Name != "video.mp4" || res.Files[0].Checksum == nil || res.Files[0].Checksum.Algo != hosters.AlgoMD5 {
		t.Fatalf("%+v", res)
	}
	fi := res.Files[0]
	if _, err := p.Prepare(ctx, anon(), fi); !hosters.IsNeedsAuth(err) {
		t.Fatalf("anonymous prepare: %v", err)
	}
	s := withCookies(t, "ndus="+goodNdus+"; other=1")
	spec, err := p.Prepare(ctx, s, fi)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Checksum == nil || spec.Checksum.Value != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("md5 checksum: %+v", spec.Checksum)
	}
	if b := fetch(t, s, spec, ""); !bytes.Equal(b, content("video.mp4")) {
		t.Fatal("content differs")
	}
	f.mu.Lock()
	seen := f.dlinkNdus[len(f.dlinkNdus)-1]
	f.mu.Unlock()
	if seen != goodNdus {
		t.Fatalf("dlink request without ndus cookie: %q", seen)
	}
	// Cookie account without ndus.
	if _, err := p.Resolve(ctx, withCookies(t, "lang=en"), "https://www.terabox.com/s/1F1leXyZ"); !hosters.IsNeedsAuth(err) {
		t.Fatalf("no ndus: %v", err)
	}
	// Redirecting /s/ link (mirror redirect) works too.
	if _, err := p.Resolve(ctx, s, "https://www.terabox.com/s/1Redir3ct"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	rn := f.redirNdus
	f.mu.Unlock()
	if rn != goodNdus {
		t.Fatalf("redirected page request without ndus: %q", rn)
	}
}

func TestMultiRootShare(t *testing.T) {
	f := newFake(t)
	p := newPlugin(t, f, nil)
	res, err := p.Resolve(t.Context(), anon(), "https://www.terabox.com/s/1Mult1Pack")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, fi := range res.Files {
		got = append(got, fi.RelPath)
	}
	if res.Name != "Pack Fotos" || !res.IsFolder || strings.Join(got, "|") != "a.jpg|Extras/b.jpg" {
		t.Fatalf("%q %q", res.Name, got)
	}
}

func TestErrorOutcomes(t *testing.T) {
	f := newFake(t)
	p := newPlugin(t, f, nil)
	ctx := t.Context()
	check := func(link string, s *hosters.Session, want hosters.Kind) *hosters.Error {
		t.Helper()
		_, err := p.Resolve(ctx, s, link)
		he, _ := hosters.AsError(err)
		if hosters.KindOf(err) != want {
			t.Errorf("%s: %v, want %v", link, err, want)
		}
		return he
	}
	check("https://www.terabox.com/s/1Pr1vate", anon(), hosters.KindNeedsAuth)
	check("https://www.terabox.com/s/1Pr1vate", withCookies(t, "ndus="+goodNdus), hosters.KindOK)
	if he := check("https://www.terabox.com/s/1Ver1fy", anon(), hosters.KindQuota); he != nil && (he.Scope != hosters.ScopeIP || he.RetryAfter != 17*time.Minute) {
		t.Errorf("verify quota: %+v", he)
	}
	check("https://www.terabox.com/s/1G0ne", anon(), hosters.KindNotFound)
	he := check("https://www.terabox.com/s/1Bl0cked", anon(), hosters.KindTemporary)
	if he != nil && !errors.Is(he, errNoToken) {
		t.Errorf("blocked page: %v", he)
	}
	check("https://www.terabox.com/s/1Pla1n", anon(), hosters.KindOK)
	check("https://example.com/s/1AbCd", anon(), hosters.KindFatal)

	// Stale jsToken: errno 2 reloads the page once and retries.
	check("https://www.terabox.com/s/1St4le", anon(), hosters.KindOK)
	f.mu.Lock()
	loads := f.pageLoads["St4le"]
	f.mu.Unlock()
	if loads != 2 {
		t.Errorf("stale token: page loaded %d times", loads)
	}

	// Listing without dlink: anonymous Prepare asks for an account.
	res, err := p.Resolve(ctx, anon(), "https://www.terabox.com/s/1N0dl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Prepare(ctx, anon(), res.Files[0]); !hosters.IsNeedsAuth(err) {
		t.Errorf("no dlink: %v", err)
	}
	if _, err := p.Prepare(ctx, anon(), hosters.FileInfo{Link: "https://www.terabox.com/s/1N0dl"}); !hosters.IsFatal(err) {
		t.Errorf("no fsid: %v", err)
	}
}

func TestClassify(t *testing.T) {
	p := &Plugin{retryAfter: time.Hour}
	mk := func(code int, ct string) *http.Response {
		h := http.Header{}
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		return &http.Response{StatusCode: code, Header: h}
	}
	cases := []struct {
		resp *http.Response
		body string
		want hosters.Kind
	}{
		{mk(200, "application/json"), `{"errno":-6}`, hosters.KindNeedsAuth},
		{mk(200, "application/json"), `{"errno":"9019"}`, hosters.KindNeedsAuth},
		{mk(200, "application/json"), `{"errno":400141,"errmsg":"need verify"}`, hosters.KindQuota},
		{mk(200, "application/json"), `{"errno":-62}`, hosters.KindQuota},
		{mk(200, "application/json"), `{"errno":105}`, hosters.KindNotFound},
		{mk(200, "application/json"), `{"errno":123456,"errmsg":"please login first"}`, hosters.KindNeedsAuth},
		{mk(200, "application/json"), `{"errno":123456,"errmsg":"request too frequent"}`, hosters.KindQuota},
		{mk(200, "application/json"), `{"errno":123456}`, hosters.KindFatal},
		{mk(200, "application/json"), `{"errno":0,"list":[]}`, hosters.KindOK},
		{mk(200, "application/octet-stream"), `{"errno":-6}`, hosters.KindOK}, // a JSON file being downloaded
		{mk(403, "text/html"), `<html>`, hosters.KindExpired},
		{mk(404, ""), ``, hosters.KindNotFound},
		{mk(429, ""), ``, hosters.KindQuota},
		{mk(502, ""), ``, hosters.KindTemporary},
	}
	for i, c := range cases {
		if got := hosters.KindOf(p.Classify(c.resp, []byte(c.body), nil)); got != c.want {
			t.Errorf("case %d %s: %v want %v", i, c.body, got, c.want)
		}
	}
	_, err := http.Get("http://127.0.0.1:1/cdn/x?sign=SECRETSIGN")
	if got := p.Classify(nil, nil, err); got == nil || strings.Contains(got.Error(), "SECRETSIGN") {
		t.Fatalf("transport error leaks URL: %v", got)
	}
}

func TestCheckAccount(t *testing.T) {
	f := newFake(t)
	p := newPlugin(t, f, nil)
	ctx := t.Context()
	r, err := p.CheckAccount(ctx, withCookies(t, "ndus="+goodNdus))
	if err != nil || r.Status != accounts.StatusOK || !r.Premium || r.PremiumUntil.Year() != 2030 || r.QuotaTotal != -1 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = p.CheckAccount(ctx, withCookies(t, "ndus=expired"))
	if err != nil || r.Status != accounts.StatusExpired {
		t.Fatalf("expired: %+v %v", r, err)
	}
	r, err = p.CheckAccount(ctx, withCookies(t, "foo=bar"))
	if err != nil || r.Status != accounts.StatusInvalid {
		t.Fatalf("no ndus: %+v %v", r, err)
	}
	if _, err := p.CheckAccount(ctx, anon()); !errors.Is(err, hosters.ErrNoAccount) {
		t.Fatalf("anonymous: %v", err)
	}
}

func TestSelfTestAndCaps(t *testing.T) {
	f := newFake(t)
	p := newPlugin(t, f, map[string]string{OptSelfTestLink: "https://www.terabox.com/s/1Fold3rAbC"})
	if err := p.SelfTest(t.Context(), anon()); err != nil {
		t.Fatal(err)
	}
	if err := newPlugin(t, f, nil).SelfTest(t.Context(), anon()); err == nil {
		t.Fatal("selftest without link must fail")
	}
	c := p.Caps()
	if !c.Folders || !c.Resumable || c.DefaultRetryAfter != 17*time.Minute || len(c.AccountKinds) != 1 || c.AccountKinds[0] != accounts.KindCookie {
		t.Fatalf("caps %+v", c)
	}
	for _, bad := range []map[string]string{{OptBaseURL: "ftp://x"}, {OptPageSize: "0"}, {OptAccountURL: "nope"}} {
		if _, err := New(config.HosterConfig{Options: bad}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	found := false
	for _, n := range hosters.FactoryNames() {
		found = found || n == Name
	}
	if !found {
		t.Fatal("terabox factory not registered")
	}
}
