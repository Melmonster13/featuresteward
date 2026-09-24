package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHandler(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>FeatureSteward</title>")},
		"assets/app-1a.js": {Data: []byte("console.log(1)")},
		".keep":            {},
	})

	rec := serve(h, "GET", "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "FeatureSteward") {
		t.Fatalf("GET / = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self'") ||
		strings.Contains(got, "unsafe-inline") || !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", got)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("index headers = %v", rec.Header())
	}

	rec = serve(h, "GET", "/assets/app-1a.js")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset = %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	for _, path := range []string{"/assets/", "/.keep", "/nope.js"} {
		if rec := serve(h, "GET", path); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	if rec := serve(h, "POST", "/"); rec.Code != 405 {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

func TestHandlerWithoutBuild(t *testing.T) {
	rec := serve(handler(fstest.MapFS{".keep": {}}), "GET", "/")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("unbuilt = %d %q", rec.Code, rec.Body)
	}
}

// The embedded files must at least contain the placeholder that keeps
// go:embed happy in a clean checkout.
func TestEmbeddedFilesLoad(t *testing.T) {
	if rec := serve(Handler(), "GET", "/.keep"); rec.Code != 404 {
		t.Errorf("dotfile served: %d", rec.Code)
	}
}
