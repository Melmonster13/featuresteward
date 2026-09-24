// Package web serves the dashboard's built files (npm run build in web/).
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var files embed.FS

// csp lets pages load only the dashboard's own scripts and styles and
// call only this server. Inline scripts and styles are refused.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Handler serves the dashboard. Without a build it explains how to make one.
func Handler() http.Handler {
	dist, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return handler(dist)
}

func handler(dist fs.FS) http.Handler {
	_, err := fs.Stat(dist, "index.html")
	built := err == nil
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			http.Error(w, "The dashboard isn't built. Run: make web", http.StatusNotFound)
			return
		}
		// No directory listings or dotfiles.
		if (r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/")) || strings.Contains(r.URL.Path, "/.") {
			http.NotFound(w, r)
			return
		}
		// Built assets have content hashes in their names, so they never change.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
