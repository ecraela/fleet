package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestLegalDownloadsArePublicExactAndDoNotFallBackToTheSPA(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("console")}}
	for _, name := range []string{"index.html", "LICENSE", "THIRD_PARTY_NOTICES.txt", "dependencies.json", "source.tar.gz", "source.json"} {
		files["legal/"+name] = &fstest.MapFile{Data: []byte("legal fixture " + name)}
	}
	h := handler(files)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/legal", nil))
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/legal/" {
		t.Fatal("legal page must keep relative source links under /legal/")
	}
	for _, name := range []string{"", "LICENSE", "THIRD_PARTY_NOTICES.txt", "dependencies.json", "source.tar.gz", "source.json"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, "/legal/"+name, nil))
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
				t.Fatalf("%s %s: %d %v", method, name, w.Code, w.Header())
			}
			if method == http.MethodGet && w.Body.String() != "legal fixture "+mapLegalName(name) {
				t.Fatalf("wrong legal download: %q", w.Body.String())
			}
			if method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
		}
	}
	for _, route := range []string{"/legal/unknown", "/legal/private.key", "/legal/source-files.json", "/legal/notices/hidden.txt"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route, nil))
		if w.Code != 404 {
			t.Fatalf("unexpected legal route: %s: %d", route, w.Code)
		}
	}
}

func mapLegalName(name string) string {
	if name == "" {
		return "index.html"
	}
	return name
}

func TestConsoleDeepLinksAndRefresh(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("<main>console</main>")}, "assets/app.js": {Data: []byte("export {}")}}
	h := handler(files)
	for _, route := range []string{"/", "/login", "/overview", "/applications", "/applications/cluster/app/traffic", "/infrastructure/nodes/node/configuration", "/infrastructure/clusters/cluster/tls", "/activity?cluster=example", "/settings"} {
		for _, method := range []string{"GET", "HEAD"} {
			t.Run(method+route, func(t *testing.T) {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(method, route, nil))
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" {
					t.Fatalf("route response: %d %v", w.Code, w.Header())
				}
				if method == "GET" && w.Body.String() != "<main>console</main>" || method == "HEAD" && w.Body.Len() != 0 {
					t.Fatalf("unexpected route body: %q", w.Body.String())
				}
			})
		}
	}
}

func TestConsoleDoesNotHideMissingAssetsOrAPIPaths(t *testing.T) {
	h := handler(fstest.MapFS{"index.html": {Data: []byte("console")}})
	for _, route := range []string{"/assets/missing.js", "/flags/4x3/missing.svg", "/api/haproxy/v1/nodes", "/v1/nodes", "/converge.haproxy.control.v1.Control/Connect", "/etc/passwd", "/unexpected"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != 404 {
			t.Fatalf("%s returned %d", route, w.Code)
		}
	}
}

func TestCountryFlagAssetsRemainLocalAndSeparateFromConsoleRoutes(t *testing.T) {
	h := handler(fstest.MapFS{"index.html": {Data: []byte("console")}, "flags/4x3/gb.svg": {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/flags/4x3/gb.svg", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || w.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatal(w.Code, w.Header())
	}
}
