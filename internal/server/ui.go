package server

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/RoyalNoob/quest-map/web"
)

// uiAssets are the files of the web UI, served without a token (the page then gets the session cookie).
var uiAssets = map[string]string{"/": "index.html", "/app.js": "app.js", "/app.css": "app.css", "/icon.svg": "icon.svg"}

func isUIAsset(r *http.Request) bool {
	_, ok := uiAssets[r.URL.Path]
	ok = ok || r.URL.Path == "/custom.css" || strings.HasPrefix(r.URL.Path, "/plugins/") // sandboxed, §8.5
	return ok && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

// maxCustomCSS caps the custom.css that is served (a stylesheet larger than this is a mistake).
const maxCustomCSS = 1 << 20

func (s *Server) uiRoutes(mux *http.ServeMux) {
	// The person's own stylesheet for this machine; empty when there is none, so the page never sees a 404.
	mux.HandleFunc("GET /custom.css", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache")
		// Served without the token like the page itself, but only to the page: a cross-site page linking it
		// gets nothing (browsers send Sec-Fetch-Site).
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			return
		}
		if s.css == "" {
			return
		}
		f, err := os.Open(s.css)
		if err != nil {
			return
		}
		defer f.Close()
		io.Copy(w, io.LimitReader(f, maxCustomCSS))
	})
	types := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8",
		"app.css": "text/css; charset=utf-8", "icon.svg": "image/svg+xml"}
	for path, file := range uiAssets {
		pattern := "GET " + path
		if path == "/" {
			pattern = "GET /{$}"
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			data, err := fs.ReadFile(web.Files, file)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			h := w.Header()
			h.Set("Content-Type", types[file])
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Cache-Control", "no-cache")
			if file == "index.html" {
				h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
				h.Set("X-Frame-Options", "DENY")
				http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.uiSecret, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
			w.Write(data)
		})
	}
}
