// Package webui embeds the compiled management UI (Vite + Preact + XP.css).
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the embedded SPA with a fallback to index.html.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.HandlerFunc(placeholder)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return http.HandlerFunc(placeholder)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			fileServer.ServeHTTP(w, clone)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func placeholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>OnebotNoa</title></head>
<body style="font-family:Tahoma,sans-serif;background:#235cdc;color:#fff;padding:40px">
<h2>OnebotNoa</h2>
<p>The management UI has not been built into this binary.</p>
<p>Run <code>scripts/build.ps1 -Web</code> (or <code>npm run build</code> in <code>web/</code>) and rebuild.</p>
<p>API is available under <code>/api/v1/</code>.</p>
</body></html>`))
}
