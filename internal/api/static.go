package api

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// static serves the embedded web UI with an SPA fallback: unknown paths
// without a file extension get index.html so client-side routes work on
// reload; missing assets are real 404s.
func (s *server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidArgument, "método no permitido")
		return
	}
	if s.d.UI == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if fi, err := fs.Stat(s.d.UI, name); err != nil || fi.IsDir() {
		if path.Ext(name) != "" && name != "index.html" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(name, "assets/") {
		// Vite puts content-hashed files under assets/.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	f, err := s.d.UI.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.ServeFileFS(w, r, s.d.UI, name) //nolint:gosec // G703: name is path.Clean'ed and served from the embedded fs.FS
		return
	}
	http.ServeContent(w, r, name, fi.ModTime(), rs)
}
