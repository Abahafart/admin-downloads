package generic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"admindescargas/internal/config"
	"admindescargas/internal/hosters"
	"admindescargas/internal/netx"
)

func newPlugin(t *testing.T) *Generic {
	t.Helper()
	h, err := New(config.HosterConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h.(*Generic)
}

func newSession() *hosters.Session {
	return hosters.NewSession(netx.NewDirect(), nil, nil)
}

func resolveOne(t *testing.T, g *Generic, link string) hosters.FileInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := g.Resolve(ctx, newSession(), link)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.IsFolder || len(res.Files) != 1 {
		t.Fatalf("Resolve: want one file, got folder=%v files=%d", res.IsFolder, len(res.Files))
	}
	f := res.Files[0]
	if f.Link != link {
		t.Errorf("Link = %q, want %q", f.Link, link)
	}
	if f.ID != "" {
		t.Errorf("ID = %q, want empty", f.ID)
	}
	return f
}

func TestFilenameFromContentDisposition(t *testing.T) {
	tests := []struct {
		name, header, want string
	}{
		{"quoted", `attachment; filename="report 2024.pdf"`, "report 2024.pdf"},
		{"unquoted", `attachment; filename=archive.zip`, "archive.zip"},
		{"unquoted with spaces", `attachment; filename=my file.zip`, "my file.zip"},
		{"no type", `filename="x.bin"`, "x.bin"},
		{"inline", `inline; filename="a.txt"; size=3`, "a.txt"},
		{"escaped quote", `attachment; filename="say \"hi\".txt"`, `say "hi".txt`},
		{"rfc5987 utf8", `attachment; filename*=UTF-8''Canci%C3%B3n%20de%20a%C3%B1o.mp3`, "Canción de año.mp3"},
		{"rfc5987 lowercase charset and lang", `attachment; filename*=utf-8'es'pel%C3%ADcula.mkv`, "película.mkv"},
		{"rfc5987 latin1", `attachment; filename*=ISO-8859-1''caf%E9.txt`, "café.txt"},
		{"both, star after", `attachment; filename="fallback.zip"; filename*=UTF-8''r%C3%A9al.zip`, "réal.zip"},
		{"both, star first", `attachment; filename*=UTF-8''r%C3%A9al.zip; filename="fallback.zip"`, "réal.zip"},
		{"bad star falls back", `attachment; filename*=KOI8-R''x; filename="ok.zip"`, "ok.zip"},
		{"rfc2047", `attachment; filename="=?UTF-8?B?w7FhbmR1LnR4dA==?="`, "ñandu.txt"},
		{"key case", `attachment; FileName="Up.TXT"`, "Up.TXT"},
		{"none", `attachment`, ""},
		{"empty", ``, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := filenameFromContentDisposition(tc.header); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveContentDisposition(t *testing.T) {
	tests := []struct {
		name, header, want string
	}{
		{"quoted", `attachment; filename="report 2024.pdf"`, "report 2024.pdf"},
		{"unquoted", `attachment; filename=archive.zip`, "archive.zip"},
		{"rfc5987 accents and spaces", `attachment; filename*=UTF-8''Canci%C3%B3n%20de%20a%C3%B1o.mp3`, "Canción de año.mp3"},
		{"filename* wins", `attachment; filename="fallback.zip"; filename*=UTF-8''r%C3%A9al.zip`, "réal.zip"},
		{"traversal", `attachment; filename="../../etc/passwd"`, "passwd"},
		{"windows traversal", `attachment; filename="..\\..\\Windows\\system.ini"`, "system.ini"},
		{"encoded traversal", `attachment; filename*=UTF-8''..%2F..%2Fsecret.txt`, "secret.txt"},
		{"only dots falls back to url", `attachment; filename=".."`, "path-name.bin"},
		{"invalid windows chars", `attachment; filename="a:b*c?.txt"`, "a_b_c_.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Disposition", tc.header)
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Content-Length", "10")
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte("0123456789"))
				}
			}))
			defer srv.Close()
			f := resolveOne(t, newPlugin(t), srv.URL+"/dl/path-name.bin")
			if f.Name != tc.want {
				t.Errorf("Name = %q, want %q", f.Name, tc.want)
			}
			if strings.ContainsAny(f.Name, `/\`) {
				t.Errorf("Name %q contains a separator", f.Name)
			}
			if f.Size != 10 {
				t.Errorf("Size = %d, want 10", f.Size)
			}
			if f.Meta[MetaResumable] != "true" {
				t.Errorf("resumable = %q, want true", f.Meta[MetaResumable])
			}
		})
	}
}

func TestResolveNameFromURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/files/final%20name.iso", http.StatusFound)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", "5")
	}))
	defer srv.Close()
	host, _ := url.Parse(srv.URL)

	tests := []struct {
		name, path, want string
	}{
		{"percent space", "/files/My%20Movie%20(2020).mkv", "My Movie (2020).mkv"},
		{"utf8 path", "/f/%C3%B1and%C3%BA.zip", "ñandú.zip"},
		{"query ignored", "/get/file.tar.gz?token=abc&x=1", "file.tar.gz"},
		{"trailing slash", "/dir/sub/", "sub"},
		{"encoded slash", "/a/b%2Fc.txt", "c.txt"},
		{"redirect target used when original has a name", "/redirect", "redirect"},
		{"root uses host", "/", fsSanitizedHost(host.Host)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := resolveOne(t, newPlugin(t), srv.URL+tc.path)
			if f.Name != tc.want {
				t.Errorf("Name = %q, want %q", f.Name, tc.want)
			}
		})
	}
}

// fsSanitizedHost mirrors what the plugin does with the bare host name of
// an httptest server (127.0.0.1:port -> 127.0.0.1).
func fsSanitizedHost(hostport string) string {
	h, _, _ := strings.Cut(hostport, ":")
	return h
}

func TestResolveNameFromFinalURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/storage/real%20file.7z", http.StatusFound)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", "7")
	}))
	defer srv.Close()
	f := resolveOne(t, newPlugin(t), srv.URL+"/")
	if f.Name != "real file.7z" {
		t.Errorf("Name = %q, want %q", f.Name, "real file.7z")
	}
	if f.Link != srv.URL+"/" {
		t.Errorf("Link must stay the original URL, got %q", f.Link)
	}
}

func TestResolveHeadNotAllowedFallsBackToRangedGET(t *testing.T) {
	for _, code := range []int{http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var gotRange atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(code)
					return
				}
				gotRange.Store(r.Header.Get("Range"))
				w.Header().Set("Content-Range", "bytes 0-0/123456789")
				w.Header().Set("Content-Disposition", `attachment; filename="big.iso"`)
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("x"))
			}))
			defer srv.Close()
			f := resolveOne(t, newPlugin(t), srv.URL+"/x")
			if r, _ := gotRange.Load().(string); r != "bytes=0-0" {
				t.Errorf("Range = %q, want bytes=0-0", r)
			}
			if f.Name != "big.iso" || f.Size != 123456789 || f.Meta[MetaResumable] != "true" {
				t.Errorf("got name=%q size=%d resumable=%q", f.Name, f.Size, f.Meta[MetaResumable])
			}
		})
	}
}

func TestResolveRangeIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Ignores Range and streams the full body.
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write(make([]byte, 100000))
	}))
	defer srv.Close()
	f := resolveOne(t, newPlugin(t), srv.URL+"/stream.bin")
	if f.Size != 100000 {
		t.Errorf("Size = %d, want 100000", f.Size)
	}
	if f.Meta[MetaResumable] != "false" {
		t.Errorf("resumable = %q, want false", f.Meta[MetaResumable])
	}
	spec, err := newPlugin(t).Prepare(context.Background(), newSession(), f)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Resumable {
		t.Error("spec.Resumable = true, want false")
	}
}

func TestResolveHeadIncompleteUsesGET(t *testing.T) {
	// HEAD answers 200 without size or Accept-Ranges; GET fills the gaps.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="from-head.bin"`)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-0/42")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	f := resolveOne(t, newPlugin(t), srv.URL+"/q")
	if f.Name != "from-head.bin" || f.Size != 42 || f.Meta[MetaResumable] != "true" {
		t.Errorf("got name=%q size=%d resumable=%q", f.Name, f.Size, f.Meta[MetaResumable])
	}
}

func TestResolveUnknownSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-0/*")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	f := resolveOne(t, newPlugin(t), srv.URL+"/live.ts")
	if f.Size != -1 {
		t.Errorf("Size = %d, want -1", f.Size)
	}
}

func TestResolveEmptyFile416(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Range", "bytes */0")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer srv.Close()
	f := resolveOne(t, newPlugin(t), srv.URL+"/empty.txt")
	if f.Size != 0 {
		t.Errorf("Size = %d, want 0", f.Size)
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name  string
		code  int
		check func(error) bool
	}{
		{"404", http.StatusNotFound, hosters.IsNotFound},
		{"410", http.StatusGone, hosters.IsExpired},
		{"401", http.StatusUnauthorized, hosters.IsNeedsAuth},
		{"429", http.StatusTooManyRequests, hosters.IsQuota},
		{"503", http.StatusServiceUnavailable, hosters.IsTemporary},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gets atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets.Add(1)
				}
				http.Error(w, "nope", tc.code)
			}))
			defer srv.Close()
			_, err := newPlugin(t).Resolve(context.Background(), newSession(), srv.URL+"/missing.zip")
			if err == nil {
				t.Fatal("want error")
			}
			if _, ok := hosters.AsError(err); !ok || !tc.check(err) {
				t.Errorf("unexpected outcome %v (kind %v)", err, hosters.KindOf(err))
			}
			if gets.Load() != 1 {
				t.Errorf("GET probes = %d, want 1", gets.Load())
			}
		})
	}
}

func TestResolveHead404ThenGETTransportError(t *testing.T) {
	// HEAD says 404; GET dies at the transport level: keep the 404.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		c, _, _ := hj.Hijack()
		_ = c.Close()
	}))
	defer srv.Close()
	_, err := newPlugin(t).Resolve(context.Background(), newSession(), srv.URL+"/gone")
	if !hosters.IsNotFound(err) {
		t.Errorf("want not found, got %v", err)
	}
}

func TestResolveTransportErrorIsTemporaryAndRedacted(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	link := srv.URL + "/file.zip?signature=SECRET"
	srv.Close() // connection refused from now on
	_, err := newPlugin(t).Resolve(context.Background(), newSession(), link)
	if !hosters.IsTemporary(err) {
		t.Fatalf("want temporary, got %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error leaks the URL: %v", err)
	}
}

func TestResolveCanceled(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newPlugin(t).Resolve(ctx, newSession(), srv.URL+"/x")
	if !hosters.IsFatal(err) {
		t.Errorf("want fatal on canceled context, got %v", err)
	}
}

func TestResolveRejectsNonHTTP(t *testing.T) {
	for _, link := range []string{"ftp://example.com/a.zip", "not a url", "/relative/path"} {
		_, err := newPlugin(t).Resolve(context.Background(), newSession(), link)
		if !hosters.IsFatal(err) {
			t.Errorf("%q: want fatal, got %v", link, err)
		}
	}
}

func TestMatch(t *testing.T) {
	g := newPlugin(t)
	tests := []struct {
		raw  string
		want bool
	}{
		{"http://example.com/file.zip", true},
		{"https://example.com/", true},
		{"HTTPS://EXAMPLE.COM/A", true},
		{"https://127.0.0.1:8080/x", true},
		{"https://[::1]/x", true},
		{"ftp://example.com/file.zip", false},
		{"ftps://example.com/file.zip", false},
		{"mailto:user@example.com", false},
		{"magnet:?xt=urn:btih:abc", false},
		{"file:///etc/passwd", false},
		{"/relative/path", false},
		{"example.com/file.zip", false},
		{"http://", false},
		{"https:///path", false},
	}
	for _, tc := range tests {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := g.Match(u); got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if g.Match(nil) {
		t.Error("Match(nil) = true")
	}
}

func TestCapsAndConfig(t *testing.T) {
	g := newPlugin(t)
	c := g.Caps()
	if !c.Fallback || !c.Resumable || c.QuotaScope != hosters.ScopeIP || c.DefaultRetryAfter != DefaultRetryAfter {
		t.Errorf("unexpected default caps %+v", c)
	}
	if g.Name() != "generic" {
		t.Errorf("Name = %q", g.Name())
	}

	h, err := New(config.HosterConfig{MaxConnsPerFile: 4, MaxParallel: 2, RetryAfter: config.D(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	c = h.Caps()
	if c.MaxConnsPerFile != 4 || c.MaxParallel != 2 || c.DefaultRetryAfter != time.Hour {
		t.Errorf("config not applied: %+v", c)
	}
	if _, err := New(config.HosterConfig{MaxConnsPerFile: -1}); err == nil {
		t.Error("negative max_conns_per_file accepted")
	}
	if !slices.Contains(hosters.FactoryNames(), "generic") {
		t.Error("factory not registered")
	}
}

func TestPrepare(t *testing.T) {
	g := newPlugin(t)
	f := hosters.FileInfo{Name: "a.zip", Size: 99, Link: "https://example.com/a.zip"}
	spec, err := g.Prepare(context.Background(), newSession(), f)
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL != f.Link || spec.Size != 99 || !spec.Resumable {
		t.Errorf("unexpected spec %+v", spec)
	}
	f.Meta = map[string]string{MetaResumable: "false"}
	if spec, _ = g.Prepare(context.Background(), newSession(), f); spec.Resumable {
		t.Error("resumable=false not honored")
	}
	if _, err := g.Prepare(context.Background(), newSession(), hosters.FileInfo{}); !hosters.IsFatal(err) {
		t.Errorf("empty link: want fatal, got %v", err)
	}
}

func TestClassify(t *testing.T) {
	g := newPlugin(t)
	if err := g.Classify(&http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}}, nil, nil); !hosters.IsNotFound(err) {
		t.Errorf("404: got %v", err)
	}
	if err := g.Classify(&http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, nil, nil); err != nil {
		t.Errorf("200: got %v", err)
	}
}

// fakeHoster is a specific (non-fallback) plugin used to check ordering.
type fakeHoster struct{ host string }

func (f fakeHoster) Name() string          { return "fake" }
func (f fakeHoster) Match(u *url.URL) bool { return u.Hostname() == f.host }
func (f fakeHoster) Resolve(context.Context, *hosters.Session, string) (*hosters.ResolveResult, error) {
	return nil, nil
}
func (f fakeHoster) Prepare(context.Context, *hosters.Session, hosters.FileInfo) (*hosters.DownloadSpec, error) {
	return nil, nil
}
func (f fakeHoster) Classify(*http.Response, []byte, error) error { return nil }
func (f fakeHoster) Caps() hosters.Caps                           { return hosters.Caps{} }

func TestRegistryPrefersSpecificHoster(t *testing.T) {
	r := hosters.NewRegistry()
	// Register the fallback first: ordering must not depend on insertion.
	if err := r.Register(newPlugin(t)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeHoster{host: "special.example"}); err != nil {
		t.Fatal(err)
	}
	if h := r.MatchString("https://special.example/file/abc"); h == nil || h.Name() != "fake" {
		t.Errorf("specific link matched %v, want fake", h)
	}
	if h := r.MatchString("https://other.example/file.zip"); h == nil || h.Name() != "generic" {
		t.Errorf("other link matched %v, want generic", h)
	}
	if h := r.MatchString("ftp://other.example/file.zip"); h != nil {
		t.Errorf("ftp link matched %q, want none", h.Name())
	}
	all := r.All()
	if len(all) != 2 || all[len(all)-1].Name() != "generic" {
		t.Errorf("All() should list the fallback last")
	}
}
