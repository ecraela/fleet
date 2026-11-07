// Package webui embeds the standalone console in the manager executable.
package webui

import (
	"embed"
	"github.com/ecraela/fleet/internal/buildinfo"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var bundle embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(bundle, "dist")
	return handler(root)
}

func handler(root fs.FS) http.Handler {
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(name, "legal/") || name == "legal" {
			if name == "legal" && !strings.HasSuffix(r.URL.Path, "/") {
				http.Redirect(w, r, "/legal/", http.StatusMovedPermanently)
				return
			}
			file := strings.TrimPrefix(name, "legal/")
			if name == "legal" || file == "" {
				file = "index.html"
			}
			switch file {
			case "index.html", "LICENSE", "THIRD_PARTY_NOTICES.txt", "dependencies.json", "source.tar.gz", "source.json":
			default:
				http.NotFound(w, r)
				return
			}
			if _, err := fs.Stat(root, "legal/"+file); err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			if file == "LICENSE" || file == "THIRD_PARTY_NOTICES.txt" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			}
			r.URL.Path = "/legal/" + file
			if file == "index.html" {
				r.URL.Path = "/legal/"
			}
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "flags/4x3/") {
			if _, err := fs.Stat(root, name); err != nil {
				http.NotFound(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=86400")
			}
			files.ServeHTTP(w, r)
			return
		}
		if !consolePath(name) {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "Console bundle unavailable. Run make web-build before building the manager.", 503)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != "HEAD" {
			_, _ = w.Write([]byte(strings.ReplaceAll(string(index), "__FLEET_RELEASE_VERSION__", html.EscapeString(buildinfo.Version))))
		}
	})
}

func consolePath(name string) bool {
	if name == "" || name == "login" {
		return true
	}
	for _, section := range []string{"overview", "applications", "infrastructure", "activity", "settings"} {
		if name == section || strings.HasPrefix(name, section+"/") {
			return true
		}
	}
	return false
}
