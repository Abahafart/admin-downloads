// Package web embeds the built Svelte UI (web/dist) so the daemon and the
// desktop app serve it without external files.
//
// Embed patterns cannot reference parent directories, which is why this package
// lives next to dist. A placeholder dist/index.html is committed so `go
// build` works without Node; `make web` (npm run build) replaces it.
package web

import (
	"bytes"
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the content of web/dist with the "dist/" prefix stripped
// (index.html is at its root).
var Dist fs.FS = mustSub(dist, "dist")

// placeholderMarker identifies the committed placeholder index.html.
var placeholderMarker = []byte("ADESC-PLACEHOLDER")

// IsPlaceholder reports whether the embedded UI is the placeholder page
// (the binary was built without running `make web`).
func IsPlaceholder() bool {
	b, err := fs.ReadFile(Dist, "index.html")
	return err != nil || bytes.Contains(b, placeholderMarker)
}

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err) // dir is a constant that exists at build time
	}
	return sub
}
