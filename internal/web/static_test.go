package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPA_ServesIndex(t *testing.T) {
	h := SPA()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "carlos web") {
		t.Errorf("index.html should mention carlos web, got: %s", body[:min(120, len(body))])
	}
}

func TestSPA_DeepLinkFallsBackToIndex(t *testing.T) {
	h := SPA()
	rec := httptest.NewRecorder()
	// A client-side route with no matching asset must serve the app shell
	// (index.html), not 404, so deep links load the SPA.
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/thread/01JABC", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("deep link = %d, want 200 (index fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<div id=\"app\">") &&
		!strings.Contains(rec.Body.String(), "carlos web") {
		t.Error("deep link should serve the app shell")
	}
}

func TestSPA_SecurityHeaders(t *testing.T) {
	h := SPA()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
}

func TestNewToken_UniqueAnd64Hex(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b {
		t.Error("tokens must be unique per call")
	}
	if len(a) != 64 {
		t.Errorf("token len = %d, want 64 hex chars (256-bit)", len(a))
	}
}
