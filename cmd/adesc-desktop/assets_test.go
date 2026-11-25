package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"admindescargas/internal/config"
	"admindescargas/internal/desktop"
)

func TestParseArgs(t *testing.T) {
	a := parseArgs([]string{"--config", "/x/c.toml", "--hidden", "-psn_0_12345", "https://mega.nz/file/a#b", "adesc://add?url=https://mediafire.com/file/x"})
	if a.configPath != "/x/c.toml" || !a.hidden {
		t.Fatalf("flags: %+v", a)
	}
	if !slices.Equal(a.links, []string{"https://mega.nz/file/a#b", "https://mediafire.com/file/x"}) {
		t.Fatalf("links: %q", a.links)
	}
	if a := parseArgs([]string{"--config=/y.toml"}); a.configPath != "/y.toml" {
		t.Fatalf("--config=: %+v", a)
	}
}

func TestInjectScript(t *testing.T) {
	for in, want := range map[string]string{
		`<!doctype html><html><head><meta charset="utf-8"></head><body></body></html>`: `<!doctype html><html><head>` + desktopScriptTag + `<meta charset="utf-8">`,
		`<HTML><HEAD lang="es"><title>x</title>`:                                       `<HEAD lang="es">` + desktopScriptTag + `<title>`,
		`<p>sin head</p>`:                                                              desktopScriptTag + `<p>`,
	} {
		if got := string(injectScript([]byte(in))); !strings.Contains(got, want) {
			t.Errorf("injectScript(%q) = %q, want it to contain %q", in, got, want)
		}
	}
}

func TestUIHandler(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":    {Data: []byte("<html><head></head><body>ui</body></html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := uiHandler(dist)
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	for _, p := range []string{"/", "/index.html", "/descargas", "/ajustes/apariencia"} {
		rec := get(p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), desktopScriptTag) || !strings.Contains(rec.Body.String(), "ui") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content type %q", p, ct)
		}
	}
	if rec := get("/assets/app.js"); rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Errorf("asset: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/assets/missing.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d", rec.Code)
	}
}

func testApp(t *testing.T) *desktopApp {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Server.Token = "tok"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	d := &desktopApp{
		cfgPath: path,
		cfg:     cfg,
		log:     slog.New(slog.DiscardHandler),
		ready:   make(chan struct{}),
	}
	d.tray = &trayUI{d: d}
	d.notify = &notifications{d: d}
	return d
}

func TestConnectionEndpoint(t *testing.T) {
	d := testApp(t)
	d.conn = desktop.Connection{BaseURL: "http://127.0.0.1:7979", Token: "tok", Mode: desktop.ModeIntegrated}
	close(d.ready)
	h := d.assetHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/__adesc/connection", nil)
	req.Header.Set("Origin", desktop.OriginUnix)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["baseURL"] != "http://127.0.0.1:7979" || got["token"] != "tok" || got["mode"] != "integrated" {
		t.Fatalf("body = %v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}

	// A page from another origin inside the webview must not get the token.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/__adesc/connection", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "tok") {
		t.Fatalf("foreign origin: %d %s", rec.Code, rec.Body)
	}

	// Windows origin and requests without Origin are fine.
	for _, origin := range []string{desktop.OriginWindows, ""} {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/__adesc/connection", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("origin %q: %d", origin, rec.Code)
		}
	}
}

func TestConnectionEndpointError(t *testing.T) {
	d := testApp(t)
	d.connErr = io.ErrUnexpectedEOF
	close(d.ready)
	rec := httptest.NewRecorder()
	d.assetHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__adesc/connection", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestClipboardEndpoint(t *testing.T) {
	d := testApp(t)
	h := d.assetHandler()
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/__adesc/clipboard", strings.NewReader(body)))
		return rec
	}
	if rec := put(`{"action":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad action: %d", rec.Code)
	}
	if rec := put(`{"enabled":false,"action":"add","hosters":["mega"]}`); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	saved, err := config.Load(d.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Clipboard.Enabled || saved.Clipboard.Action != "add" || !slices.Equal(saved.Clipboard.Hosters, []string{"mega"}) {
		t.Fatalf("saved clipboard = %+v", saved.Clipboard)
	}
	if !saved.Clipboard.UseWlPaste {
		t.Fatal("fields absent from the body must keep their value")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__adesc/clipboard", nil))
	var st clipboardStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.Available || st.Action != "add" {
		t.Fatalf("status = %+v (%v)", st, err)
	}
}

func TestDesktopSettingsValidation(t *testing.T) {
	d := testApp(t)
	h := d.assetHandler()
	for _, body := range []string{`{"mode":"remote"}`, `{"mode":"bogus"}`, `{"mode":"remote","remote_url":"ftp://x"}`} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/__adesc/desktop", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/__adesc/desktop", strings.NewReader(`{"notifications":false,"close_to_tray":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid settings: %d %s", rec.Code, rec.Body)
	}
	saved, err := config.Load(d.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Desktop.Notifications || saved.Desktop.CloseToTray || saved.Desktop.Mode != "integrated" {
		t.Fatalf("saved desktop = %+v", saved.Desktop)
	}
}

func TestUnknownDesktopEndpoint(t *testing.T) {
	rec := httptest.NewRecorder()
	testApp(t).assetHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__adesc/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}
