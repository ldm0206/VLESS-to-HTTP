package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The panel is a hand-written single-page app with no build step, embedded in
// the binary so the container ships one file.
//
//go:embed assets
var assetsFS embed.FS

func (s *Server) staticHandler() (http.Handler, error) {
	root, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		if _, err := fs.Stat(root, path); err != nil {
			// Unknown path: hand the SPA its entry point so client-side routes
			// keep working, but never mask a missing asset.
			if strings.Contains(path, ".") {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, r, root)
			return
		}

		// Every asset revalidates instead of being served blind from cache:
		// the panel and the API must always match, and a stale app.js against
		// a newer backend is far worse than one conditional request.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}), nil
}

func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	raw, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "panel assets missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(raw)
}
