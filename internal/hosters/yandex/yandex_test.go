package yandex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "yandex", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, strings.ReplaceAll(body, "__BASE__", srv.URL))
	}
	mux.HandleFunc("GET /v1/disk/public/resources", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		key, p, off := q.Get("public_key"), q.Get("path"), q.Get("offset")
		switch {
		case key == "https://disk.yandex.ru/d/AbCd123" && p == "" && off == "":
			write(w, 200, fixture(t, "root_p1.json"))
		case key == "https://disk.yandex.ru/d/AbCd123" && p == "" && off == "2":
			write(w, 200, fixture(t, "root_p2.json"))
		case key == "https://disk.yandex.ru/d/AbCd123" && p == "/Album 1":
			write(w, 200, fixture(t, "album1.json"))
		case key == "https://disk.yandex.ru/i/FiLe999":
			write(w, 200, fixture(t, "file.json"))
		default:
			write(w, 404, fixture(t, "error_not_found.json"))
		}
	})
	mux.HandleFunc("GET /v1/disk/public/resources/download", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("public_key") == "https://disk.yandex.ru/i/FiLe999":
			write(w, 200, fixture(t, "download.json"))
		case q.Get("path") == "/cover.jpg":
			write(w, 403, fixture(t, "error_limit.json"))
		case q.Get("path") == "/Album 1/01 - Intro.flac":
			write(w, 200, fixture(t, "download.json"))
		default:
			write(w, 404, fixture(t, "error_not_found.json"))
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newHoster(t *testing.T, base string) *Hoster {
	t.Helper()
	h, err := New(config.HosterConfig{Options: map[string]string{"api_base": base, "page_size": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Hoster)
}

func anon() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

func TestMatch(t *testing.T) {
	h := newHoster(t, "http://x")
	for link, want := range map[string]bool{
		"https://disk.yandex.ru/d/AbCd123":             true,
		"https://disk.yandex.com/d/AbCd123/Sub/Folder": true,
		"https://disk.yandex.ru/i/FiLe999":             true,
		"https://yadi.sk/d/AbCd123":                    true,
		"https://yadi.sk/i/FiLe999":                    true,
		"https://disk.360.yandex.ru/d/AbCd123":         true,
		"https://disk.yandex.ru/public?hash=abc%3D%3D": true,
		"https://disk.yandex.ru/client/disk":           false,
		"https://disk.yandex.ru/d/":                    false,
		"https://yandex.ru/d/AbCd123":                  false,
		"https://disk.yandex.ru.evil.com/d/AbCd123":    false,
		"https://disk.yandex.ru/public?nohash=1":       false,
	} {
		u, _ := url.Parse(link)
		if got := h.Match(u); got != want {
			t.Errorf("Match(%q) = %v, want %v", link, got, want)
		}
	}
	u, _ := url.Parse("https://disk.yandex.ru/d/AbCd123/Sub/../Folder")
	if p, _ := h.parse(u); p.publicKey != "https://disk.yandex.ru/d/AbCd123" || p.subPath != "/Folder" {
		t.Errorf("parse = %+v", p)
	}
}

func TestResolveFolder(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv.URL)
	var emitted int
	res, err := h.ResolveStream(context.Background(), anon(), "https://disk.yandex.ru/d/AbCd123", func(hosters.FileInfo) { emitted++ })
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "Música" {
		t.Fatalf("res = %+v", res)
	}
	want := map[string]*hosters.Checksum{
		"cover.jpg":               {Algo: hosters.AlgoMD5, Value: "5d41402abc4b2a76b9719d911017c592"},
		"notes.txt":               {Algo: hosters.AlgoSHA256, Value: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
		"Album 1/01 - Intro.flac": {Algo: hosters.AlgoSHA256, Value: "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"},
	}
	if len(res.Files) != len(want) || emitted != len(want) {
		t.Fatalf("files = %+v", res.Files)
	}
	for _, f := range res.Files {
		c, ok := want[f.RelPath]
		if !ok {
			t.Errorf("unexpected %q", f.RelPath)
			continue
		}
		if f.Checksum == nil || *f.Checksum != *c {
			t.Errorf("%s checksum %+v", f.RelPath, f.Checksum)
		}
		if f.Meta[metaKey] != "https://disk.yandex.ru/d/AbCd123" {
			t.Errorf("%s meta %+v", f.RelPath, f.Meta)
		}
	}
	for _, f := range res.Files {
		switch f.RelPath {
		case "Album 1/01 - Intro.flac":
			spec, err := h.Prepare(context.Background(), anon(), f)
			if err != nil {
				t.Fatal(err)
			}
			if spec.URL != srv.URL+"/dl/video.mp4?sign=REDACTED" || !spec.Resumable || spec.Size != 3000000 {
				t.Errorf("spec = %+v", spec)
			}
		case "cover.jpg":
			_, err := h.Prepare(context.Background(), anon(), f)
			if he, ok := hosters.AsError(err); !ok || he.Kind != hosters.KindQuota || he.Scope != hosters.ScopeFile {
				t.Errorf("limit: %v", err)
			}
		}
	}
}

func TestResolveFile(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv.URL)
	res, err := h.Resolve(context.Background(), anon(), "https://disk.yandex.ru/i/FiLe999")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 || res.Files[0].Name != "video.mp4" || res.Files[0].RelPath != "" || res.Files[0].Size != 123456 {
		t.Fatalf("res = %+v", res)
	}
	spec, err := h.Prepare(context.Background(), anon(), res.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(spec.URL, srv.URL+"/dl/") {
		t.Errorf("spec = %+v", spec)
	}
}

func TestNotFound(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv.URL)
	if _, err := h.Resolve(context.Background(), anon(), "https://disk.yandex.ru/d/Missing"); !hosters.IsNotFound(err) {
		t.Errorf("err = %v", err)
	}
}

func TestClassify(t *testing.T) {
	h := newHoster(t, "http://x")
	for _, tt := range []struct {
		code int
		body string
		kind hosters.Kind
	}{
		{200, "", hosters.KindOK},
		{404, `{"error":"DiskNotFoundError"}`, hosters.KindNotFound},
		{403, `{"error":"DiskResourceDownloadLimitExceededError"}`, hosters.KindQuota},
		{429, `{"error":"TooManyRequestsError"}`, hosters.KindQuota},
		{401, `{"error":"UnauthorizedError"}`, hosters.KindNeedsAuth},
		{400, `{"error":"FieldValidationError"}`, hosters.KindFatal},
		{503, `{"error":"DiskServiceUnavailableError"}`, hosters.KindTemporary},
		{410, ``, hosters.KindExpired},
	} {
		err := h.Classify(&http.Response{StatusCode: tt.code, Header: http.Header{}}, []byte(tt.body), nil)
		if got := hosters.KindOf(err); got != tt.kind {
			t.Errorf("%d %s: %v (%v), want %v", tt.code, tt.body, got, err, tt.kind)
		}
	}
}
