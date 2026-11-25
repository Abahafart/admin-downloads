package pixeldrain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "pixeldrain", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestHoster(t *testing.T, base string) *Hoster {
	t.Helper()
	h, err := New(config.HosterConfig{Options: map[string]string{"api_base": base}})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Hoster)
}

func anon() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

func withKey(t *testing.T, key string) *hosters.Session {
	t.Helper()
	s := anon()
	s.Account = &accounts.Account{ID: "acc1", Hoster: Name, Kind: accounts.KindAPIKey}
	st := accounts.NewMemoryStore()
	if err := st.Set(context.Background(), "acc1", accounts.Secret{APIKey: key}); err != nil {
		t.Fatal(err)
	}
	s.Secrets = st
	return s
}

func server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, b []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(b)
	}
	mux.HandleFunc("/api/file/abc123/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, fixture(t, "file_info.json"))
	})
	mux.HandleFunc("/api/file/hot999/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, fixture(t, "file_info_ratelimited.json"))
	})
	mux.HandleFunc("/api/file/gone00/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, fixture(t, "error_not_found.json"))
	})
	mux.HandleFunc("/api/list/L1st01", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, fixture(t, "list.json"))
	})
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "" || pass != "goodkey" {
			writeJSON(w, 401, []byte(`{"success":false,"value":"unauthorized","message":"no"}`))
			return
		}
		writeJSON(w, 200, fixture(t, "user.json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestMatch(t *testing.T) {
	h := newTestHoster(t, "http://x")
	tests := []struct {
		link string
		want bool
	}{
		{"https://pixeldrain.com/u/abc123", true},
		{"https://www.pixeldrain.com/u/abc123?embed", true},
		{"https://pixeldrain.com/u/abc123,def456", true},
		{"https://pixeldrain.com/l/L1st01#item=2", true},
		{"https://pixeldrain.net/u/abc123", true},
		{"https://pixeldra.in/u/abc123", true},
		{"https://pixeldrain.com/api/file/abc123?download", true},
		{"https://pixeldrain.com/api/list/L1st01", true},
		{"https://pixeldrain.com/", false},
		{"https://pixeldrain.com/u/", false},
		{"https://pixeldrain.com/user/files", false},
		{"https://evilpixeldrain.com/u/abc123", false},
		{"https://example.com/u/abc123", false},
	}
	for _, tt := range tests {
		u, _ := url.Parse(tt.link)
		if got := h.Match(u); got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.link, got, tt.want)
		}
	}
}

func TestResolveFile(t *testing.T) {
	srv := server(t)
	h := newTestHoster(t, srv.URL+"/api")
	res, err := h.Resolve(context.Background(), anon(), "https://pixeldrain.com/u/abc123")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 {
		t.Fatalf("got %+v", res)
	}
	f := res.Files[0]
	if f.ID != "abc123" || f.Name != "video.mkv" || f.Size != 1048576 || f.RelPath != "" {
		t.Errorf("file = %+v", f)
	}
	if f.Checksum == nil || f.Checksum.Algo != hosters.AlgoSHA256 || f.Checksum.Value != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Errorf("checksum = %+v", f.Checksum)
	}
}

func TestResolveNotFound(t *testing.T) {
	srv := server(t)
	h := newTestHoster(t, srv.URL+"/api")
	_, err := h.Resolve(context.Background(), anon(), "https://pixeldrain.com/u/gone00")
	if !hosters.IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestResolveList(t *testing.T) {
	srv := server(t)
	h := newTestHoster(t, srv.URL+"/api")
	res, err := h.Resolve(context.Background(), anon(), "https://pixeldrain.com/l/L1st01")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "My Album" || len(res.Files) != 3 {
		t.Fatalf("got %+v", res)
	}
	want := []string{"part.rar", "part (1).rar", "readme.txt"}
	for i, f := range res.Files {
		if f.RelPath != want[i] {
			t.Errorf("file %d RelPath = %q, want %q", i, f.RelPath, want[i])
		}
	}
	if res.Files[2].Size != -1 {
		t.Errorf("unknown size = %d, want -1", res.Files[2].Size)
	}

	one, err := h.Resolve(context.Background(), anon(), "https://pixeldrain.com/l/L1st01#item=1")
	if err != nil {
		t.Fatal(err)
	}
	if one.IsFolder || len(one.Files) != 1 || one.Files[0].ID != "f2bbbb" {
		t.Fatalf("item=1 got %+v", one)
	}
}

func TestPrepare(t *testing.T) {
	srv := server(t)
	h := newTestHoster(t, srv.URL+"/api")
	f := hosters.FileInfo{ID: "abc123", Name: "video.mkv", Link: "https://pixeldrain.com/u/abc123"}

	spec, err := h.Prepare(context.Background(), anon(), f)
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL != srv.URL+"/api/file/abc123?download" || !spec.Resumable || spec.Size != 1048576 {
		t.Errorf("spec = %+v", spec)
	}
	if spec.Header.Get("Authorization") != "" {
		t.Error("anonymous spec must not carry auth")
	}

	spec, err = h.Prepare(context.Background(), withKey(t, "goodkey"), f)
	if err != nil {
		t.Fatal(err)
	}
	r := &http.Request{Header: spec.Header}
	if u, p, ok := r.BasicAuth(); !ok || u != "" || p != "goodkey" {
		t.Errorf("basic auth = %q %q %v", u, p, ok)
	}
}

func TestPrepareRateLimited(t *testing.T) {
	srv := server(t)
	h := newTestHoster(t, srv.URL+"/api")
	f := hosters.FileInfo{ID: "hot999", Link: "https://pixeldrain.com/u/hot999"}
	if _, err := h.Prepare(context.Background(), anon(), f); !hosters.IsNeedsAuth(err) {
		t.Fatalf("anonymous err = %v, want needs auth", err)
	}
	if _, err := h.Prepare(context.Background(), withKey(t, "goodkey"), f); err != nil {
		t.Fatalf("with key err = %v", err)
	}
}

func TestClassify(t *testing.T) {
	h := newTestHoster(t, "http://x")
	tests := []struct {
		code int
		body string
		kind hosters.Kind
		sc   hosters.QuotaScope
	}{
		{200, "binarydata", hosters.KindOK, 0},
		{206, "", hosters.KindOK, 0},
		{404, `{"success":false,"value":"not_found"}`, hosters.KindNotFound, 0},
		{403, `{"success":false,"value":"file_rate_limited_captcha_required"}`, hosters.KindNeedsAuth, 0},
		{403, `{"success":false,"value":"virus_detected_captcha_required"}`, hosters.KindNeedsAuth, 0},
		{403, `{"success":false,"value":"ip_download_limited_captcha_required"}`, hosters.KindQuota, hosters.ScopeIP},
		{429, `{"success":false,"value":"rate_limited"}`, hosters.KindQuota, hosters.ScopeIP},
		{429, ``, hosters.KindQuota, hosters.ScopeIP},
		{401, `{"success":false,"value":"unauthorized"}`, hosters.KindNeedsAuth, 0},
		{503, `<html>`, hosters.KindTemporary, 0},
	}
	for _, tt := range tests {
		resp := &http.Response{StatusCode: tt.code, Header: http.Header{}}
		err := h.Classify(resp, []byte(tt.body), nil)
		if got := hosters.KindOf(err); got != tt.kind {
			t.Errorf("%d %s: kind = %v (%v), want %v", tt.code, tt.body, got, err, tt.kind)
			continue
		}
		if tt.kind == hosters.KindQuota {
			he, _ := hosters.AsError(err)
			if he.Scope != tt.sc {
				t.Errorf("%d %s: scope = %v", tt.code, tt.body, he.Scope)
			}
		}
	}
}

func TestCheckAccount(t *testing.T) {
	srv := server(t)
	h := newTestHoster(t, srv.URL+"/api")
	res, err := h.CheckAccount(context.Background(), withKey(t, "goodkey"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != accounts.StatusOK || !res.Premium || res.QuotaTotal != 1000000 || res.QuotaUsed != 250000 {
		t.Errorf("res = %+v", res)
	}
	res, err = h.CheckAccount(context.Background(), withKey(t, "badkey"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != accounts.StatusInvalid {
		t.Errorf("bad key status = %v", res.Status)
	}
}

func TestRegistered(t *testing.T) {
	found := false
	for _, n := range hosters.FactoryNames() {
		found = found || n == Name
	}
	if !found {
		t.Fatal("factory not registered")
	}
	h, _ := New(config.HosterConfig{})
	if c := h.Caps(); !c.Folders || !c.Resumable || c.QuotaScope != hosters.ScopeIP {
		t.Errorf("caps = %+v", c)
	}
}
