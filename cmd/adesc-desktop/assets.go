package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"admindescargas/internal/apiclient"
	"admindescargas/internal/config"
	"admindescargas/internal/desktop"
	"admindescargas/web"
)

// desktopJS is injected into index.html: drag & drop of links, text and
// small files anywhere in the window (a UI component that handles a drop
// itself calls preventDefault and this script leaves it alone).
//
//go:embed desktop.js
var desktopJS []byte

// assetHandler serves the embedded web UI (web.Dist) and the desktop
// endpoints under /__adesc/ to the webview. Only the webview reaches this
// handler (it is not a network server); requests carrying a foreign Origin
// are refused anyway, so a page navigated inside the webview cannot read
// the token.
//
// Endpoints (JSON):
//
//	GET  /__adesc/connection            {"baseURL","token","mode"} (waits for the core; 503 + {"error"} on failure)
//	GET  /__adesc/desktop               desktopInfo
//	PUT  /__adesc/desktop               desktopSettings -> desktopInfo
//	GET  /__adesc/clipboard             clipboardStatus
//	PUT  /__adesc/clipboard             config.ClipboardConfig -> clipboardStatus
//	POST /__adesc/appearance            {"estilo","tema","esquema"} (window background)
//	POST /__adesc/dialog/download-folder -> {"path"}  (204 if canceled)
//	POST /__adesc/import/proxies        -> API answer (204 if canceled)
//	POST /__adesc/import/wireguard      -> API answer
//	POST /__adesc/import/cookies?hoster=<name>&label=<label> -> API answer
//	POST /__adesc/drop                  {"text","files":[{"name","text"}]}
//	POST /__adesc/window/hide | /__adesc/window/quit
func (d *desktopApp) assetHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__adesc/connection", d.handleConnection)
	mux.HandleFunc("GET /__adesc/desktop", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.desktopInfo())
	})
	mux.HandleFunc("PUT /__adesc/desktop", func(w http.ResponseWriter, r *http.Request) {
		var s desktopSettings
		if !decodeJSON(w, r, &s) {
			return
		}
		if err := d.applyDesktopSettings(r.Context(), s); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, d.desktopInfo())
	})
	mux.HandleFunc("GET /__adesc/clipboard", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.clipboardStatus())
	})
	mux.HandleFunc("PUT /__adesc/clipboard", func(w http.ResponseWriter, r *http.Request) {
		c := d.config().Clipboard
		if !decodeJSON(w, r, &c) {
			return
		}
		switch c.Action {
		case config.ClipboardAsk, config.ClipboardAdd, config.ClipboardLinkGrabber:
		default:
			writeErr(w, http.StatusBadRequest, errors.New("action debe ser ask, add o linkgrabber"))
			return
		}
		if err := d.setClipboardConfig(c); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, d.clipboardStatus())
	})
	mux.HandleFunc("POST /__adesc/appearance", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			desktop.Appearance
			Esquema string `json:"esquema"`
		}
		if !decodeJSON(w, r, &a) {
			return
		}
		d.applyAppearance(a.Appearance, a.Esquema)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /__adesc/dialog/download-folder", func(w http.ResponseWriter, r *http.Request) {
		dir, err := d.pickDownloadFolder()
		if errors.Is(err, errCancelled) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": dir})
	})
	mux.HandleFunc("POST /__adesc/import/{kind}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res, err := d.importFile(r.Context(), importKind(r.PathValue("kind")), q.Get("hoster"), q.Get("label"))
		if errors.Is(err, errCancelled) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			writeAPIErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(res) //nolint:gosec // G705: JSON from the daemon API, served as application/json
	})
	mux.HandleFunc("POST /__adesc/drop", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text  string        `json:"text"`
			Files []droppedFile `json:"files"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if err := d.handleDrop(r.Context(), body.Text, body.Files); err != nil {
			writeAPIErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /__adesc/window/{action}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "hide":
			d.hideWindow()
		case "quit":
			go d.quit()
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /__adesc/desktop.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(desktopJS)
	})
	mux.Handle("/__adesc/", http.NotFoundHandler())
	mux.Handle("/", uiHandler(web.Dist))
	return d.logRequests(originGuard(mux))
}

// logRequests logs asset requests at debug level (method and path only:
// queries may carry a cookie hoster/label, bodies may carry files).
func (d *desktopApp) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.log.Debug("asset request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// originGuard refuses /__adesc/ requests whose Origin is not the webview
// (a page navigated inside the window must not read the token).
func originGuard(next http.Handler) http.Handler {
	allowed := map[string]bool{desktop.OriginDev: true}
	for _, o := range desktop.WebviewOrigins() {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__adesc/") {
			if o := r.Header.Get("Origin"); o != "" && o != "null" && !allowed[strings.TrimSuffix(strings.ToLower(o), "/")] {
				writeErr(w, http.StatusForbidden, errors.New("origen no permitido"))
				return
			}
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (d *desktopApp) handleConnection(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	conn, err := d.waitConnection(ctx)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, conn)
}

// uiHandler serves the SPA: existing files as is, any other path without
// extension gets index.html; index.html gets the desktop script.
func uiHandler(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" || p == "index.html" {
			serveIndex(w, dist)
			return
		}
		if _, err := fs.Stat(dist, p); err != nil {
			if path.Ext(p) == "" {
				serveIndex(w, dist) // client-side route
				return
			}
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

const desktopScriptTag = `<script src="/__adesc/desktop.js"></script>`

func serveIndex(w http.ResponseWriter, dist fs.FS) {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(injectScript(b))
}

// injectScript inserts the desktop script right after <head> (or at the
// start when there is no head).
func injectScript(html []byte) []byte {
	s := string(html)
	lower := strings.ToLower(s)
	if i := strings.Index(lower, "<head"); i >= 0 {
		if j := strings.Index(lower[i:], ">"); j >= 0 {
			k := i + j + 1
			return []byte(s[:k] + desktopScriptTag + s[k:])
		}
	}
	return []byte(desktopScriptTag + s)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// writeAPIErr forwards the status and message of an API error.
func writeAPIErr(w http.ResponseWriter, err error) {
	var ae *apiclient.Error
	if errors.As(err, &ae) {
		writeErr(w, ae.Status, errors.New(ae.Message))
		return
	}
	writeErr(w, http.StatusBadGateway, err)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2*maxImportFile))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}
