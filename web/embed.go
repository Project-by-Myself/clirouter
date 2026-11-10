// Package web embeds the built dashboard (web/dist) into the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the SPA; unknown paths fall back to index.html.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			// Unknown paths and directories (no listings) fall back to the
			// SPA. fs.Stat rejects "..", so nothing outside dist is reachable.
			if fi, err := fs.Stat(sub, strings.TrimSuffix(p, "/")); err != nil || fi.IsDir() {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
