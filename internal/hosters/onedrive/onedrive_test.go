package onedrive

import (
	"context"
	"encoding/base64"
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

const (
	folderLink = "https://1drv.ms/f/s!AbCdEfGh123"
	fileLink   = "https://1drv.ms/u/s!SingleFile99"
	goneLink   = "https://1drv.ms/u/s!GoneGone"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "onedrive", name))
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
		body = strings.ReplaceAll(body, "__BASE__", srv.URL)
		body = strings.ReplaceAll(body, "__SHARE__", ShareID(folderLink))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("GET /shares/{share}/root", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("share") {
		case ShareID(folderLink):
			write(w, 200, fixture(t, "root_folder.json"))
		case ShareID(fileLink):
			write(w, 200, fixture(t, "single_file.json"))
		default:
			write(w, 404, fixture(t, "error_not_found.json"))
		}
	})
	mux.HandleFunc("GET /shares/{share}/root/children", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$skiptoken") == "P2" {
			write(w, 200, fixture(t, "root_children_p2.json"))
			return
		}
		write(w, 200, fixture(t, "root_children.json"))
	})
	mux.HandleFunc("GET /shares/{share}/items/{id}/children", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "ROOT!103" {
			write(w, 404, fixture(t, "error_not_found.json"))
			return
		}
		write(w, 200, fixture(t, "assets_children.json"))
	})
	mux.HandleFunc("GET /shares/{share}/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "ROOT!105":
			write(w, 200, fixture(t, "item_file.json"))
		case "ROOT!102":
			write(w, 200, `{"id":"ROOT!102","name":"plan.docx","size":100,"file":{}}`)
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
	h, err := New(config.HosterConfig{Options: map[string]string{"api_base": base}})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*Hoster)
}

func anon() *hosters.Session { return hosters.NewSession(netx.NewDirect(), nil, nil) }

func TestShareID(t *testing.T) {
	// Example from the OneDrive documentation.
	link := "https://onedrive.live.com/redir?resid=1231244193912!12&authKey=1201919!12921!1"
	want := "u!aHR0cHM6Ly9vbmVkcml2ZS5saXZlLmNvbS9yZWRpcj9yZXNpZD0xMjMxMjQ0MTkzOTEyITEyJmF1dGhLZXk9MTIwMTkxOSExMjkyMSEx"
	if got := ShareID(link); got != want {
		t.Errorf("ShareID = %s, want %s", got, want)
	}
	if strings.ContainsAny(ShareID("https://1drv.ms/u/s!Aa?b=c>>"), "+/=") {
		t.Error("share id must be unpadded base64url")
	}
	if _, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ShareID(fileLink), "u!")); err != nil {
		t.Error(err)
	}
}

func TestMatch(t *testing.T) {
	h := newHoster(t, "http://x")
	for link, want := range map[string]bool{
		"https://1drv.ms/u/s!AbCd":                                     true,
		"https://1drv.ms/f/s!AbCd":                                     true,
		"https://1drv.ms/u/c/abcdef1234/EXyz":                          true,
		"https://onedrive.live.com/redir?resid=ABC!12&authkey=!xyz":    true,
		"https://onedrive.live.com/?cid=ABC&id=ABC%21123":              true,
		"https://onedrive.live.com/embed?cid=ABC&resid=ABC!1&authkey=": true,
		"https://onedrive.live.com/":                                   false,
		"https://onedrive.live.com/about/en-us/":                       false,
		"https://1drv.ms/":                                             false,
		"https://contoso-my.sharepoint.com/:u:/g/personal/x":           false,
		"https://1drv.ms.evil.com/u/s!AbCd":                            false,
	} {
		u, _ := url.Parse(link)
		if got := h.Match(u); got != want {
			t.Errorf("Match(%q) = %v, want %v", link, got, want)
		}
	}
}

func TestResolveFolder(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv.URL)
	var n int
	res, err := h.ResolveStream(context.Background(), anon(), folderLink, func(hosters.FileInfo) { n++ })
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsFolder || res.Name != "Proyecto" {
		t.Fatalf("res = %+v", res)
	}
	want := map[string]hosters.Checksum{
		"plan.docx":       {Algo: hosters.AlgoSHA1, Value: "a94a8fe5ccb19ba61c4c0873d391e987982fbbd3"},
		"budget.xlsx":     {Algo: hosters.AlgoSHA256, Value: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
		"Assets/logo.png": {Algo: hosters.AlgoCRC32, Value: "0d4a1185"},
	}
	if len(res.Files) != len(want) || n != len(want) {
		t.Fatalf("files = %+v (emitted %d)", res.Files, n)
	}
	for _, f := range res.Files {
		w, ok := want[f.RelPath]
		if !ok {
			t.Errorf("unexpected %q", f.RelPath)
			continue
		}
		if f.Checksum == nil || *f.Checksum != w {
			t.Errorf("%s checksum = %+v", f.RelPath, f.Checksum)
		}
		if f.Link != folderLink || f.Meta[metaItem] == "" {
			t.Errorf("%s link/meta = %q %v", f.RelPath, f.Link, f.Meta)
		}
	}

	// Prepare: fresh downloadUrl when present, /content otherwise.
	var logo, plan hosters.FileInfo
	for _, f := range res.Files {
		switch f.Name {
		case "logo.png":
			logo = f
		case "plan.docx":
			plan = f
		}
	}
	spec, err := h.Prepare(context.Background(), anon(), logo)
	if err != nil {
		t.Fatal(err)
	}
	if spec.URL != srv.URL+"/cdn/logo.png?tempauth=REDACTED" || spec.Expires.IsZero() || !spec.Resumable || spec.Size != 150 {
		t.Errorf("logo spec = %+v", spec)
	}
	spec, err = h.Prepare(context.Background(), anon(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(spec.URL, "/items/ROOT%21102/content") || !spec.Expires.IsZero() {
		t.Errorf("plan spec = %+v", spec)
	}
}

func TestResolveFile(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv.URL)
	res, err := h.Resolve(context.Background(), anon(), fileLink)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsFolder || len(res.Files) != 1 || res.Files[0].Name != "video.mp4" || res.Files[0].RelPath != "" {
		t.Fatalf("res = %+v", res)
	}
	spec, err := h.Prepare(context.Background(), anon(), res.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(spec.URL, "/shares/"+ShareID(fileLink)+"/root/content") {
		t.Errorf("spec = %+v", spec)
	}
}

func TestNotFound(t *testing.T) {
	srv := newServer(t)
	h := newHoster(t, srv.URL)
	if _, err := h.Resolve(context.Background(), anon(), goneLink); !hosters.IsNotFound(err) {
		t.Errorf("err = %v", err)
	}
}

func TestNextLinkHostChecked(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shares/{share}/root", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"R","name":"F","folder":{"childCount":1}}`)
	})
	mux.HandleFunc("GET /shares/{share}/root/children", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"value":[],"@odata.nextLink":"https://evil.example/steal"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := newHoster(t, srv.URL)
	if _, err := h.Resolve(context.Background(), anon(), folderLink); !hosters.IsFatal(err) {
		t.Errorf("err = %v, want fatal", err)
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
		{302, "", hosters.KindOK},
		{404, `{"error":{"code":"itemNotFound"}}`, hosters.KindNotFound},
		{403, `{"error":{"code":"accessDenied"}}`, hosters.KindNeedsAuth},
		{401, `{"error":{"code":"unauthenticated"}}`, hosters.KindNeedsAuth},
		{429, `{"error":{"code":"activityLimitReached"}}`, hosters.KindQuota},
		{503, `{"error":{"code":"serviceNotAvailable"}}`, hosters.KindTemporary},
		{403, ``, hosters.KindExpired},
	} {
		err := h.Classify(&http.Response{StatusCode: tt.code, Header: http.Header{}}, []byte(tt.body), nil)
		if got := hosters.KindOf(err); got != tt.kind {
			t.Errorf("%d %s: %v (%v), want %v", tt.code, tt.body, got, err, tt.kind)
		}
	}
}
