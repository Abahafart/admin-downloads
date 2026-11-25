package gofile

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "gofile", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type fake struct {
	srv          *httptest.Server
	guestCalls   atomic.Int32
	jsCalls      atomic.Int32
	rejectTokens atomic.Int32 // reject the first n content calls with error-auth
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{}
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.ReplaceAll(body, "__BASE__", f.srv.URL))
	}
	mux.HandleFunc("POST /accounts", func(w http.ResponseWriter, r *http.Request) {
		f.guestCalls.Add(1)
		j(w, fixture(t, "accounts.json"))
	})
	mux.HandleFunc("GET /dist/js/global.js", func(w http.ResponseWriter, r *http.Request) {
		f.jsCalls.Add(1)
		_, _ = io.WriteString(w, fixture(t, "global.js"))
	})
	mux.HandleFunc("GET /contents/{id}", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer GUESTTOKEN123" && auth != "Bearer PREMIUMTOKEN" {
			j(w, `{"status":"error-auth","data":{}}`)
			return
		}
		if f.rejectTokens.Load() > 0 {
			f.rejectTokens.Add(-1)
			j(w, `{"status":"error-auth","data":{}}`)
			return
		}
		if r.URL.Query().Get("wt") != "4fd6sg89d7s6" {
			j(w, `{"status":"error-notPremium","data":{}}`)
			return
		}
		switch r.PathValue("id") {
		case "AbCdEf", "f0lder01":
			j(w, fixture(t, "contents_root.json"))
		case "sub00001":
			j(w, fixture(t, "contents_sub.json"))
		case "locked01":
			j(w, fixture(t, "contents_password.json"))
		case "limited":
			w.WriteHeader(http.StatusTooManyRequests)
			j(w, `{"status":"error-rateLimit","data":{}}`)
		default:
			j(w, `{"status":"error-notFound","data":{}}`)
		}
	})
	mux.HandleFunc("GET /accounts/getid", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer PREMIUMTOKEN" {
			j(w, `{"status":"error-auth","data":{}}`)
			return
		}
		j(w, `{"status":"ok","data":{"id":"acc-premium"}}`)
	})
	mux.HandleFunc("GET /accounts/acc-premium", func(w http.ResponseWriter, r *http.Request) {
		j(w, `{"status":"ok","data":{"id":"acc-premium","tier":"premium","email":"x@example.com"}}`)
	})
	mux.HandleFunc("GET /download/web/{id}/{name}", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("accountToken")
		if err != nil || c.Value != "GUESTTOKEN123" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "payload")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) hoster(t *testing.T, opts map[string]string) *Hoster {
	t.Helper()
	o := map[string]string{"api_base": f.srv.URL, "site_base": f.srv.URL}
	for k, v := range opts {
		o[k] = v
	}
	h, err := New(config.HosterConfig{Options: o, RequestsPerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Hoster)
}

func anon() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

func TestMatch(t *testing.T) {
	h, _ := New(config.HosterConfig{})
	for link, want := range map[string]bool{
		"https://gofile.io/d/AbCdEf":       true,
		"https://www.gofile.io/d/AbCdEf/":  true,
		"https://gofile.io/d/":             false,
		"https://gofile.io/welcome":        false,
		"https://store1.gofile.io/d/AbCd":  false,
		"https://notgofile.io/d/AbCdEf":    false,
		"https://gofile.io/d/Ab%2F..%2Fxx": false,
	} {
		u, _ := url.Parse(link)
		if got := h.Match(u); got != want {
			t.Errorf("Match(%q) = %v, want %v", link, got, want)
		}
	}
}

func TestExtractWebsiteToken(t *testing.T) {
	for js, want := range map[string]string{
		`appdata.wt = "4fd6sg89d7s6";`:                       "4fd6sg89d7s6",
		`fetch(u,{headers:{"X-Website-Token":"abc123XYZ"}})`: "abc123XYZ",
		`const o={wt:'zzzzzz99'}`:                            "zzzzzz99",
		`nothing here`:                                       "",
	} {
		if got := ExtractWebsiteToken([]byte(js)); got != want {
			t.Errorf("ExtractWebsiteToken(%q) = %q, want %q", js, got, want)
		}
	}
}

func TestResolveFolderAndPrepare(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	s := anon()
	var streamed []string
	res, err := h.ResolveStream(context.Background(), s, "https://gofile.io/d/AbCdEf", func(fi hosters.FileInfo) {
		streamed = append(streamed, fi.RelPath)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "Season 1" {
		t.Fatalf("res = %+v", res)
	}
	want := []string{"cover.jpg", "ep01.mkv", "Extras/making-of.mp4"}
	if len(res.Files) != len(want) {
		t.Fatalf("files = %+v", res.Files)
	}
	for i, fi := range res.Files {
		if fi.RelPath != want[i] || streamed[i] != want[i] {
			t.Errorf("file %d = %q (streamed %q), want %q", i, fi.RelPath, streamed[i], want[i])
		}
	}
	ep := res.Files[1]
	if ep.Checksum == nil || ep.Checksum.Value != "5d41402abc4b2a76b9719d911017c592" || ep.Size != 1234 {
		t.Errorf("ep01 = %+v", ep)
	}
	if f.guestCalls.Load() != 1 || f.jsCalls.Load() != 1 {
		t.Errorf("guest calls %d js calls %d, want 1/1 (cached)", f.guestCalls.Load(), f.jsCalls.Load())
	}

	spec, err := h.Prepare(context.Background(), s, res.Files[2])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(spec.URL, "/download/web/file0003/making-of.mp4") || spec.Size != 99 {
		t.Errorf("spec = %+v", spec)
	}
	// The download must succeed with the session client thanks to the
	// accountToken cookie.
	req, _ := http.NewRequest(http.MethodGet, spec.URL, nil)
	resp, err := s.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(b) != "payload" {
		t.Errorf("download = %d %q", resp.StatusCode, b)
	}
}

func TestGuestTokenRefresh(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	f.rejectTokens.Store(1)
	if _, err := h.Resolve(context.Background(), anon(), "https://gofile.io/d/AbCdEf"); err != nil {
		t.Fatal(err)
	}
	if f.guestCalls.Load() != 2 {
		t.Errorf("guest calls = %d, want 2 (token refreshed)", f.guestCalls.Load())
	}
}

func TestErrors(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	ctx := context.Background()
	if _, err := h.Resolve(ctx, anon(), "https://gofile.io/d/missing"); !hosters.IsNotFound(err) {
		t.Errorf("missing: %v", err)
	}
	if _, err := h.Resolve(ctx, anon(), "https://gofile.io/d/locked01"); !hosters.IsNeedsAuth(err) {
		t.Errorf("locked: %v", err)
	}
	_, err := h.Resolve(ctx, anon(), "https://gofile.io/d/limited")
	if he, ok := hosters.AsError(err); !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeIP {
		t.Errorf("limited: %v", err)
	}
	// A wrong fixed wt is reported (the fake answers error-notPremium).
	before := f.jsCalls.Load()
	bad := f.hoster(t, map[string]string{"website_token": "wrongwt"})
	if _, err := bad.Resolve(ctx, anon(), "https://gofile.io/d/AbCdEf"); !hosters.IsNeedsAuth(err) {
		t.Errorf("bad wt: %v", err)
	}
	if f.jsCalls.Load() != before {
		t.Errorf("fixed website_token must skip scraping")
	}
}

func premium(t *testing.T) *hosters.Session {
	s := anon()
	s.Account = &accounts.Account{ID: "p1", Hoster: Name, Kind: accounts.KindAPIKey}
	st := accounts.NewMemoryStore()
	_ = st.Set(context.Background(), "p1", accounts.Secret{APIKey: "PREMIUMTOKEN"})
	s.Secrets = st
	return s
}

func TestPremiumAccount(t *testing.T) {
	f := newFake(t)
	h := f.hoster(t, nil)
	s := premium(t)
	if _, err := h.Resolve(context.Background(), s, "https://gofile.io/d/AbCdEf"); err != nil {
		t.Fatal(err)
	}
	if f.guestCalls.Load() != 0 {
		t.Error("premium session must not create guest accounts")
	}
	res, err := h.CheckAccount(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != accounts.StatusOK || !res.Premium {
		t.Errorf("check = %+v", res)
	}
}

func TestClassify(t *testing.T) {
	h, _ := New(config.HosterConfig{})
	for _, tt := range []struct {
		code int
		body string
		kind hosters.Kind
	}{
		{200, "data", hosters.KindOK},
		{200, `{"status":"error-notFound"}`, hosters.KindNotFound},
		{429, `{"status":"error-rateLimit"}`, hosters.KindQuota},
		{403, `forbidden`, hosters.KindExpired},
		{401, ``, hosters.KindNeedsAuth},
		{500, ``, hosters.KindTemporary},
	} {
		err := h.Classify(&http.Response{StatusCode: tt.code, Header: http.Header{}}, []byte(tt.body), nil)
		if got := hosters.KindOf(err); got != tt.kind {
			t.Errorf("%d %q: %v, want %v", tt.code, tt.body, got, tt.kind)
		}
	}
}
