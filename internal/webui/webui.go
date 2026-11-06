// Package webui embeds the static browser frontend that visualizes the
// knowledge graph with Cytoscape.js.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var content embed.FS

// Handler serves the embedded frontend. Mount it at "/ui/" (the prefix is
// stripped here). Authentication is applied by the caller.
func Handler() http.Handler {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic(err) // static is embedded at build time
	}
	files := http.StripPrefix("/ui", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/ui/viewer" {
			// Extensionless route for the Markdown viewer page.
			http.ServeFileFS(w, r, sub, "viewer.html")
			return
		}
		files.ServeHTTP(w, r)
	})
}
