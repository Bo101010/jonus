// Package webui embeds the built Svelte frontend and serves it as a
// single-page application.
//
// The embed directive can only reach files inside this package's directory, so
// the Vite build is configured to emit into internal/webui/dist rather than
// the conventional web/dist.
package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// distFS holds the Vite build output. The all: prefix includes files whose
// names begin with "_" or ".", which Vite emits for some assets.
//
//go:embed all:dist
var distFS embed.FS

// NewHandler returns an http.Handler serving the embedded frontend.
//
// Any request that does not match a real file is served index.html so that
// client-side routes survive a page reload. The returned error can only occur
// if the binary was built without a frontend, which the build does not allow.
func NewHandler() (http.Handler, error) {
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded frontend: %w", err)
	}
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(dist, name); err != nil {
			// Not a real asset: treat it as a client-side route and hand the
			// request to index.html.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	}), nil
}
