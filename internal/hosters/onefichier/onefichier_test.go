package onefichier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "onefichier", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fake struct {
	srv   *httptest.Server
	dl    string // download base on a different host name
	posts atomic.Int32
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{}
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, name string) {
		b := strings.ReplaceAll(string(fixture(t, name)), "__DL__", f.dl)
		b = strings.ReplaceAll(b, "__WEB__", f.srv.URL)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, b)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.SplitN(r.URL.RawQuery, "&", 2)[0]
		switch {
		case r.URL.Path != "/":
			http.NotFound(w, r)
		case id == "abcde12345fghij67890" && r.Method == http.MethodGet:
			page(w, "page_form.html")
		case id == "abcde12345fghij67890" && r.Method == http.MethodPost:
			_ = r.ParseForm()
			if r.PostForm.Get("adz") != "12.5482" || r.PostForm.Get("dl_no_ssl") != "on" || r.PostForm.Has("dlinline") {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			f.posts.Add(1)
			page(w, "page_link.html")
		case id == "cntdwn99999" && r.Method == http.MethodGet:
			page(w, "page_countdown.html")
		case id == "cntdwn99999" && r.Method == http.MethodPost:
			f.posts.Add(1)
			page(w, "page_link.html")
		case id == "waitwait12345":
			page(w, "page_wait.html")
		case id == "frwait12345":
			page(w, "page_fr_wait.html")
		case id == "pwprot12345":
			page(w, "page_password.html")
		default:
			page(w, "page_not_found.html")
		}
	})
	mux.HandleFunc("POST /check_links.pl", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		l := r.PostForm.Get("links[]")
		if strings.Contains(l, "abcde12345fghij67890") {
			_, _ = io.WriteString(w, l+";holidays;2025.rar;1256277934\n")
			return
		}
		_, _ = io.WriteString(w, l+";;NOT FOUND\n")
	})
	mux.HandleFunc("GET /dir/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "DiR0123456" || r.URL.Query().Get("json") != "1" {
			page(w, "page_not_found.html")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "dir.json"))
	})
	api := func(path string, fn func(w http.ResponseWriter, in map[string]any)) {
		mux.HandleFunc("POST /v1"+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("Authorization") != "Bearer GOODKEY" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"status":"KO","message":"Not authenticated #247"}`)
				return
			}
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			fn(w, in)
		})
	}
	api(apiFileInfo, func(w http.ResponseWriter, in map[string]any) {
		if in["url"] != "https://1fichier.com/?abcde12345fghij67890" {
			_, _ = io.WriteString(w, `{"status":"KO","message":"Resource not found #469"}`)
			return
		}
		_, _ = io.WriteString(w, `{"url":"https://1fichier.com/?abcde12345fghij67890","filename":"holidays.rar","size":1256277934,"pass":0}`)
	})
	api(apiGetToken, func(w http.ResponseWriter, in map[string]any) {
		_, _ = io.WriteString(w, `{"url":"https://a-7.1fichier.com/p123456789","status":"OK"}`)
	})
	api(apiUserInfo, func(w http.ResponseWriter, in map[string]any) {
		_, _ = io.WriteString(w, `{"email":"x@example.com","offer":"Premium","premium_until":"2027-01-01 00:00:00"}`)
	})
	f.srv = httptest.NewServer(mux)
	f.dl = strings.Replace(f.srv.URL, "127.0.0.1", "localhost", 1)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) hoster(t *testing.T) *Hoster {
	t.Helper()
	h, err := New(config.HosterConfig{Options: map[string]string{
		"web_base": f.srv.URL, "api_base": f.srv.URL + "/v1", "api_interval": "0s", "max_inline_wait": "5s",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Hoster)
}

func anon() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

func withKey(key string) *hosters.Session {
	s := anon()
	s.Account = &accounts.Account{ID: "k1", Hoster: Name, Kind: accounts.KindAPIKey}
	st := accounts.NewMemoryStore()
	_ = st.Set(context.Background(), "k1", accounts.Secret{APIKey: key})
	s.Secrets = st
	return s
}

func TestMatch(t *testing.T) {
	h, _ := New(config.HosterConfig{})
	for link, want := range map[string]bool{
		"https://1fichier.com/?abcde12345fghij67890":       true,
		"https://1fichier.com/?abcde12345fghij67890&af=12": true,
		"https://www.1fichier.com/?abcde12345":             true,
		"https://abcde12345.1fichier.com/":                 true,
		"https://1fichier.com/dir/DiR0123456":              true,
		"https://desfichiers.com/?abcde12345fghij67890":    true,
		"https://megadl.fr/?abcde12345":                    true,
		"https://1fichier.com/":                            false,
		"https://1fichier.com/register.pl":                 false,
		"https://a-7.1fichier.com/p123456789":              false,
		"https://api.1fichier.com/v1/file/info.cgi":        false,
		"https://1fichier.com.evil.org/?abcde12345":        false,
	} {
		u, _ := url.Parse(link)
		if got := h.Match(u); got != want {
			t.Errorf("Match(%q) = %v, want %v", link, got, want)
		}
	}
}

func TestResolveFree(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t)
	res, err := h.Resolve(context.Background(), anon(), "https://1fichier.com/?ABCDE12345fghij67890&af=1")
	if err != nil {
		t.Fatal(err)
	}
	fi := res.Files[0]
	if fi.Name != "holidays;2025.rar" || fi.Size != 1256277934 || fi.Link != "https://1fichier.com/?abcde12345fghij67890" {
		t.Errorf("file = %+v", fi)
	}
	if _, err := h.Resolve(context.Background(), anon(), "https://1fichier.com/?zzzzz99999"); !hosters.IsNotFound(err) {
		t.Errorf("missing: %v", err)
	}
}

func TestResolveDir(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t)
	res, err := h.Resolve(context.Background(), anon(), "https://1fichier.com/dir/DiR0123456")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || len(res.Files) != 3 {
		t.Fatalf("res = %+v", res)
	}
	want := []string{"part1.rar", "part1 (1).rar", "readme.nfo"}
	for i, fi := range res.Files {
		if fi.RelPath != want[i] {
			t.Errorf("%d: %q, want %q", i, fi.RelPath, want[i])
		}
	}
	if res.Files[2].Size != -1 {
		t.Errorf("size = %d", res.Files[2].Size)
	}
	if _, err := h.Resolve(context.Background(), anon(), "https://1fichier.com/dir/NoSuchDir1"); !hosters.IsNotFound(err) {
		t.Errorf("missing dir: %v", err)
	}
}

func TestPrepareFree(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t)
	spec, err := h.Prepare(context.Background(), anon(), hosters.FileInfo{ID: "abcde12345fghij67890", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL != f.dl+"/c123456789abcdef" || spec.Resumable || spec.MaxConns != 1 || f.posts.Load() != 1 {
		t.Errorf("spec = %+v posts=%d", spec, f.posts.Load())
	}
}

func TestPrepareFreeCountdown(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t)
	start := time.Now()
	spec, err := h.Prepare(context.Background(), anon(), hosters.FileInfo{ID: "cntdwn99999"})
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 2*time.Second {
		t.Errorf("countdown not honored (%s)", el)
	}
	if !strings.HasSuffix(spec.URL, "/c123456789abcdef") {
		t.Errorf("spec = %+v", spec)
	}
	// Cancellation interrupts the countdown.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := h.Prepare(ctx, anon(), hosters.FileInfo{ID: "cntdwn99999"}); err == nil {
		t.Error("expected cancellation error")
	}
}

func TestPrepareFreeErrors(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t)
	ctx := context.Background()
	_, err := h.Prepare(ctx, anon(), hosters.FileInfo{ID: "waitwait12345"})
	he, ok := hosters.AsError(err)
	if !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeIP || he.RetryAfter != 12*time.Minute {
		t.Errorf("wait: %v", err)
	}
	_, err = h.Prepare(ctx, anon(), hosters.FileInfo{ID: "frwait12345"})
	if he, ok := hosters.AsError(err); !ok || he.Kind != hosters.KindQuota || he.RetryAfter != 3*time.Minute {
		t.Errorf("fr wait: %v", err)
	}
	if _, err := h.Prepare(ctx, anon(), hosters.FileInfo{ID: "pwprot12345"}); !hosters.IsNeedsAuth(err) {
		t.Errorf("password: %v", err)
	}
	if _, err := h.Prepare(ctx, anon(), hosters.FileInfo{ID: "gone0000000"}); !hosters.IsNotFound(err) {
		t.Errorf("gone: %v", err)
	}
}

func TestPremium(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t)
	ctx := context.Background()
	s := withKey("GOODKEY")
	res, err := h.Resolve(ctx, s, "https://1fichier.com/?abcde12345fghij67890")
	if err != nil {
		t.Fatal(err)
	}
	if res.Files[0].Name != "holidays.rar" || res.Files[0].Size != 1256277934 {
		t.Errorf("file = %+v", res.Files[0])
	}
	spec, err := h.Prepare(ctx, s, res.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL != "https://a-7.1fichier.com/p123456789" || !spec.Resumable {
		t.Errorf("spec = %+v", spec)
	}
	if _, err := h.Resolve(ctx, s, "https://1fichier.com/?zzzzz99999"); !hosters.IsNotFound(err) {
		t.Errorf("api not found: %v", err)
	}
	if _, err := h.Resolve(ctx, withKey("BAD"), "https://1fichier.com/?abcde12345fghij67890"); !hosters.IsNeedsAuth(err) {
		t.Errorf("bad key: %v", err)
	}
	chk, err := h.CheckAccount(ctx, s)
	if err != nil || chk.Status != accounts.StatusOK || !chk.Premium || chk.PremiumUntil.Year() != 2027 {
		t.Errorf("check = %+v err %v", chk, err)
	}
	chk, err = h.CheckAccount(ctx, withKey("BAD"))
	if err != nil || chk.Status != accounts.StatusInvalid {
		t.Errorf("bad check = %+v err %v", chk, err)
	}
}

func TestClassify(t *testing.T) {
	h, _ := New(config.HosterConfig{})
	html := http.Header{"Content-Type": {"text/html"}}
	for _, tt := range []struct {
		code int
		hdr  http.Header
		body string
		kind hosters.Kind
	}{
		{200, http.Header{"Content-Type": {"application/octet-stream"}}, "RAR!", hosters.KindOK},
		{200, html, string(fixture(t, "page_wait.html")), hosters.KindQuota},
		{200, html, string(fixture(t, "page_not_found.html")), hosters.KindNotFound},
		{200, html, "<html><body>hello</body></html>", hosters.KindExpired},
		{403, http.Header{}, `{"status":"KO","message":"Flood detected: IP Locked #38"}`, hosters.KindTemporary},
		{200, http.Header{}, `{"status":"KO","message":"Must be a customer (Premium, Access) #236"}`, hosters.KindNeedsAuth},
		{503, http.Header{}, "", hosters.KindTemporary},
	} {
		err := h.Classify(&http.Response{StatusCode: tt.code, Header: tt.hdr}, []byte(tt.body), nil)
		if got := hosters.KindOf(err); got != tt.kind {
			t.Errorf("%d %.40q: %v (%v), want %v", tt.code, tt.body, got, err, tt.kind)
		}
	}
}

func TestParseCheckLinks(t *testing.T) {
	got := parseCheckLinks([]byte("https://1fichier.com/?a;x;y.zip;123\nhttps://1fichier.com/?b;;NOT FOUND\n\n"))
	if len(got) != 2 || got[0].name != "x;y.zip" || got[0].size != 123 || !got[1].notFound {
		t.Errorf("got %+v", got)
	}
}
