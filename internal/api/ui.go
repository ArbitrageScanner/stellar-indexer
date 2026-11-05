package api

import (
	_ "embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed openapi.yaml
var openapiSpec []byte

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiSpec)
}

// uiHandler serves the static explorer. index.html is never cached so a new
// binary's UI is picked up on reload; other files are cached briefly.
func uiHandler(ui fs.FS) http.Handler {
	files := http.StripPrefix("/ui/", http.FileServerFS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/ui/")
		if p == "" || p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		files.ServeHTTP(w, r)
	})
}
