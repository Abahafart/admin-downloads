package dropbox

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
	"testing"

	"admindescargas/internal/accounts"
	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "dropbox", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const folderLink = "https://www.dropbox.com/scl/fo/abc123/keykey?rlkey=RL1&dl=0"

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/scl/fi/file01/report.pdf", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dl") != "1" || r.URL.Query().Get("rlkey") != "RK" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="report final.pdf"`)
		w.Header().Set("Content-Length", "4096")
		w.Header().Set("Content-Type", "application/pdf")
	})
	mux.HandleFunc("/s/gone01/x.bin", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/s/busy01/x.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, fixture(t, "traffic_limit.html"))
	})
	mux.HandleFunc("/scl/fo/abc123/keykey", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="Vacaciones.zip"`)
		w.Header().Set("Content-Type", "application/zip")
	})
	mux.HandleFunc("/sHoRt", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.dropbox.com/s/short01/movie.mkv?dl=0", http.StatusFound)
	})
	mux.HandleFunc("/s/short01/movie.mkv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "77")
	})
	mux.HandleFunc("POST /2/files/list_folder", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TOKEN" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error_summary":"invalid_access_token/..."}`)
			return
		}
		var arg listFolderArg
		_ = json.NewDecoder(r.Body).Decode(&arg)
		if arg.SharedLink.URL != folderLink {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, fixture(t, "error_not_found.json"))
			return
		}
		switch arg.Path {
		case "":
			_, _ = io.WriteString(w, fixture(t, "list_folder_root.json"))
		case "/Fotos":
			_, _ = io.WriteString(w, fixture(t, "list_folder_sub.json"))
		default:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error_summary":"path/not_found/.."}`)
		}
	})
	mux.HandleFunc("POST /2/files/list_folder/continue", func(w http.ResponseWriter, r *http.Request) {
		var arg map[string]string
		_ = json.NewDecoder(r.Body).Decode(&arg)
		if arg["cursor"] != "CURSOR1" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error_summary":"reset/.."}`)
			return
		}
		_, _ = io.WriteString(w, fixture(t, "list_folder_continue.json"))
	})
	mux.HandleFunc("POST /2/sharing/get_shared_link_metadata", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{".tag":"folder","name":"Vacaciones","id":"id:root"}`)
	})
	mux.HandleFunc("POST /2/users/get_current_account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TOKEN" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error_summary":"invalid_access_token/"}`)
			return
		}
		_, _ = io.WriteString(w, `{"email":"x@example.com","account_type":{".tag":"pro"}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newHoster(t *testing.T, srv *httptest.Server, mode string) *Hoster {
	t.Helper()
	h, err := New(config.HosterConfig{Options: map[string]string{
		"web_base": srv.URL, "api_base": srv.URL, "content_base": srv.URL, "folder_mode": mode,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Hoster)
}

func anon() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

func withToken(t *testing.T, tok string) *hosters.Session {
	s := anon()
	s.Account = &accounts.Account{ID: "d1", Hoster: Name, Kind: accounts.KindAPIKey}
	st := accounts.NewMemoryStore()
	_ = st.Set(context.Background(), "d1", accounts.Secret{APIKey: tok})
	s.Secrets = st
	return s
}

func TestMatch(t *testing.T) {
	h, _ := New(config.HosterConfig{})
	for link, want := range map[string]bool{
		"https://www.dropbox.com/s/abc/file.zip?dl=0":         true,
		"https://www.dropbox.com/scl/fi/abc/file.zip?rlkey=x": true,
		"https://www.dropbox.com/scl/fo/abc/key?rlkey=x":      true,
		"https://www.dropbox.com/sh/abc/key?dl=0":             true,
		"https://dl.dropboxusercontent.com/s/abc/file.zip":    true,
		"https://dl.dropbox.com/s/abc/file.zip":               true,
		"https://db.tt/AbCd":                                  true,
		"https://www.dropbox.com/home":                        false,
		"https://www.dropbox.com/scl/fi/":                     false,
		"https://dropbox.com.evil.net/s/abc/file.zip":         false,
		"https://www.dropbox.com/request/abc":                 false,
	} {
		u, _ := url.Parse(link)
		if got := h.Match(u); got != want {
			t.Errorf("Match(%q) = %v, want %v", link, got, want)
		}
	}
}

func TestResolveFile(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv, "")
	res, err := h.Resolve(context.Background(), anon(), "https://www.dropbox.com/scl/fi/file01/report.pdf?rlkey=RK&dl=0")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 {
		t.Fatalf("res = %+v", res)
	}
	f := res.Files[0]
	if f.Name != "report final.pdf" || f.Size != 4096 || f.ID != "file01" {
		t.Errorf("file = %+v", f)
	}
	spec, err := h.Prepare(context.Background(), anon(), f)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(spec.URL)
	if u.Query().Get("dl") != "1" || u.Query().Get("rlkey") != "RK" || !spec.Resumable || spec.Method != "" {
		t.Errorf("spec = %+v", spec)
	}
}

func TestResolveErrors(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv, "")
	if _, err := h.Resolve(context.Background(), anon(), "https://www.dropbox.com/s/gone01/x.bin"); !hosters.IsNotFound(err) {
		t.Errorf("gone: %v", err)
	}
	_, err := h.Resolve(context.Background(), anon(), "https://www.dropbox.com/s/busy01/x.bin")
	if he, ok := hosters.AsError(err); !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeFile {
		t.Errorf("busy: %v", err)
	}
}

func TestResolveShortLink(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv, "")
	res, err := h.Resolve(context.Background(), anon(), "https://db.tt/sHoRt")
	if err != nil {
		t.Fatal(err)
	}
	f := res.Files[0]
	if f.Name != "movie.mkv" || f.Size != 77 || !strings.Contains(f.Link, "/s/short01/movie.mkv") {
		t.Errorf("file = %+v", f)
	}
}

func TestFolderZip(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv, "")
	res, err := h.Resolve(context.Background(), anon(), folderLink)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 || res.Files[0].Name != "Vacaciones.zip" {
		t.Fatalf("res = %+v", res)
	}
	spec, err := h.Prepare(context.Background(), anon(), res.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if spec.Resumable || spec.MaxConns != 1 || !strings.Contains(spec.URL, "dl=1") {
		t.Errorf("spec = %+v", spec)
	}
}

func TestFolderAPI(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv, "")
	s := withToken(t, "TOKEN")
	res, err := h.Resolve(context.Background(), s, folderLink)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "Vacaciones" {
		t.Fatalf("res = %+v", res)
	}
	want := []string{"notes.txt", "año.pdf", "Fotos/img1.jpg"}
	if len(res.Files) != len(want) {
		t.Fatalf("files = %+v", res.Files)
	}
	for i, f := range res.Files {
		if f.RelPath != want[i] {
			t.Errorf("file %d = %q, want %q", i, f.RelPath, want[i])
		}
	}
	spec, err := h.Prepare(context.Background(), s, res.Files[1])
	if err != nil {
		t.Fatal(err)
	}
	if spec.Method != http.MethodPost || spec.URL != srv.URL+pathSharedLinkFile || spec.Header.Get("Authorization") != "Bearer TOKEN" {
		t.Errorf("spec = %+v", spec)
	}
	arg := spec.Header.Get("Dropbox-API-Arg")
	for _, r := range arg {
		if r > 0x7f {
			t.Fatalf("Dropbox-API-Arg has non-ASCII: %q", arg)
		}
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(arg), &decoded); err != nil || decoded["path"] != "/año.pdf" || decoded["url"] != folderLink {
		t.Errorf("arg = %q (%v)", arg, err)
	}

	// Forced zip mode ignores the account.
	hz := newHoster(t, srv, "zip")
	res, err = hz.Resolve(context.Background(), s, folderLink)
	if err != nil || res.IsFolder || len(res.Files) != 1 {
		t.Errorf("zip mode res = %+v err %v", res, err)
	}
	// Forced api mode without account needs auth.
	ha := newHoster(t, srv, "api")
	if _, err := ha.Resolve(context.Background(), anon(), folderLink); !hosters.IsNeedsAuth(err) {
		t.Errorf("api mode anonymous: %v", err)
	}
	// Bad token.
	if _, err := h.Resolve(context.Background(), withToken(t, "BAD"), folderLink); !hosters.IsNeedsAuth(err) {
		t.Errorf("bad token: %v", err)
	}
}

func TestCheckAccount(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv, "")
	res, err := h.CheckAccount(context.Background(), withToken(t, "TOKEN"))
	if err != nil || res.Status != accounts.StatusOK || !res.Premium {
		t.Errorf("res = %+v err %v", res, err)
	}
	res, err = h.CheckAccount(context.Background(), withToken(t, "BAD"))
	if err != nil || res.Status != accounts.StatusInvalid {
		t.Errorf("bad res = %+v err %v", res, err)
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
		{200, http.Header{"Content-Disposition": {"attachment"}}, "data", hosters.KindOK},
		{200, html, "<p>This link is password protected</p>", hosters.KindNeedsAuth},
		{429, html, fixture(t, "traffic_limit.html"), hosters.KindQuota},
		{409, nil, `{"error_summary":"path/not_found/.."}`, hosters.KindNotFound},
		{429, nil, `{"error_summary":"too_many_requests/.."}`, hosters.KindTemporary},
		{401, nil, `{"error_summary":"expired_access_token/"}`, hosters.KindNeedsAuth},
		{400, html, "zip_too_large", hosters.KindFatal},
		{404, html, "", hosters.KindNotFound},
		{503, nil, "", hosters.KindTemporary},
	} {
		hdr := tt.hdr
		if hdr == nil {
			hdr = http.Header{}
		}
		err := h.Classify(&http.Response{StatusCode: tt.code, Header: hdr}, []byte(tt.body), nil)
		if got := hosters.KindOf(err); got != tt.kind {
			t.Errorf("%d %.30q: %v (%v), want %v", tt.code, tt.body, got, err, tt.kind)
		}
	}
}

func TestHeaderJSON(t *testing.T) {
	got, err := headerJSON(map[string]string{"path": "/é😀"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"path":"/\u00e9\ud83d\ude00"}`; got != want {
		t.Errorf("got %s", got)
	}
}
