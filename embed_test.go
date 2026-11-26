package pingdrop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewSPAHandlerEmbeddedBaseline(t *testing.T) {
	handler := NewSPAHandler()

	recAsset := httptest.NewRecorder()
	reqAsset := httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil)
	handler.ServeHTTP(recAsset, reqAsset)

	if recAsset.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("expected Referrer-Policy=no-referrer, got %q", recAsset.Header().Get("Referrer-Policy"))
	}
	if recAsset.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing .js asset, got %d", recAsset.Code)
	}

	recRoute := httptest.NewRecorder()
	reqRoute := httptest.NewRequest(http.MethodGet, "/room/123456", nil)
	handler.ServeHTTP(recRoute, reqRoute)

	if recRoute.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("expected Referrer-Policy=no-referrer, got %q", recRoute.Header().Get("Referrer-Policy"))
	}
	if recRoute.Code != http.StatusServiceUnavailable && recRoute.Code != http.StatusOK {
		t.Fatalf("expected 503 (before web build) or 200 (after web build), got %d", recRoute.Code)
	}

	recDotfile := httptest.NewRecorder()
	reqDotfile := httptest.NewRequest(http.MethodGet, "/.gitkeep", nil)
	handler.ServeHTTP(recDotfile, reqDotfile)

	if recDotfile.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("expected Referrer-Policy=no-referrer, got %q", recDotfile.Header().Get("Referrer-Policy"))
	}
	if recDotfile.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for dotfile /.gitkeep, got %d", recDotfile.Code)
	}
}

func TestSPAHandlerFallbackAndAssets(t *testing.T) {
	distFS := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><html><body>pingdrop</body></html>")},
		"assets/app.js": {Data: []byte("console.log('pingdrop');")},
	}
	handler := newSPAHandler(distFS)

	t.Run("root serves index with no-cache and referrer policy", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("expected Referrer-Policy=no-referrer, got %q", rec.Header().Get("Referrer-Policy"))
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("expected Cache-Control=no-cache, got %q", rec.Header().Get("Cache-Control"))
		}
		if !strings.Contains(rec.Body.String(), "pingdrop") {
			t.Fatalf("expected index.html body, got %q", rec.Body.String())
		}
	})

	t.Run("unknown route without extension falls back to index.html", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/room/654321", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("expected Referrer-Policy=no-referrer, got %q", rec.Header().Get("Referrer-Policy"))
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("expected Cache-Control=no-cache, got %q", rec.Header().Get("Cache-Control"))
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("expected text/html content-type, got %q", rec.Header().Get("Content-Type"))
		}
		if !strings.Contains(rec.Body.String(), "pingdrop") {
			t.Fatalf("expected fallback index.html body, got %q", rec.Body.String())
		}
	})

	t.Run("existing asset serves with immutable cache header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("expected Referrer-Policy=no-referrer, got %q", rec.Header().Get("Referrer-Policy"))
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("expected immutable Cache-Control, got %q", rec.Header().Get("Cache-Control"))
		}
	})

	t.Run("missing asset with extension returns 404", func(t *testing.T) {
		for _, path := range []string{"/assets/missing.js", "/favicon.ico"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			handler.ServeHTTP(rec, req)

			if rec.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("%s: expected Referrer-Policy=no-referrer, got %q", path, rec.Header().Get("Referrer-Policy"))
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: expected 404, got %d", path, rec.Code)
			}
		}
	})
}
