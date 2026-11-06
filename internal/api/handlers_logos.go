package api

import (
	"net/http"
	"strconv"
)

// logos lists the assets with an issuer-published image (see internal/logos).
func (s *Server) logos(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.LogoIndex(r.Context())
	s.reply(w, r, map[string]any{"logos": rows}, err)
}

// assetLogo serves one stored image. Only raster types sniffed at fetch time
// are ever stored, so the body is safe to serve from the explorer's origin;
// a ?v= matching the content version makes the response immutable.
func (s *Server) assetLogo(w http.ResponseWriter, r *http.Request) {
	l, err := s.repo.Logo(r.Context(), assetParam(r.PathValue("key")))
	if err == nil && l == nil {
		err = errNotFound
	}
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	etag := `"` + l.SHA256 + `"`
	h := w.Header()
	h.Set("Content-Type", l.ContentType)
	h.Set("ETag", etag)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	if v := r.URL.Query().Get("v"); v != "" && len(l.SHA256) >= len(v) && l.SHA256[:len(v)] == v {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=3600")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(l.Data)))
	// The body is a PNG the resolver re-encoded from pixels only, served with
	// nosniff and a sandbox CSP, so it cannot run as a document.
	_, _ = w.Write(l.Data) //nolint:gosec // see above
}
