package research_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestWebFetchAdapter_RoundTrip exercises the adapter against an
// httptest server, asserting the JSON envelope produced by
// WebFetchTool.Execute maps cleanly onto a research.Source.
func TestWebFetchAdapter_RoundTrip(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Adapter Test</title></head><body><p>Hello from adapter.</p></body></html>`))
	}))
	defer ts.Close()

	tool := &tools.WebFetchTool{AllowPrivate: true}
	adapter := &research.WebFetchAdapter{Tool: tool}
	src, err := adapter.Fetch(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if src.Title != "Adapter Test" {
		t.Errorf("Title = %q want Adapter Test", src.Title)
	}
	if !strings.Contains(src.Content, "Hello from adapter") {
		t.Errorf("Content missing expected text: %q", src.Content)
	}
	if src.URL == "" {
		t.Errorf("URL empty")
	}
	if src.FetchedAt.IsZero() {
		t.Errorf("FetchedAt zero")
	}
}

func TestWebFetchAdapter_NilToolErrors(t *testing.T) {
	adapter := &research.WebFetchAdapter{Tool: nil}
	_, err := adapter.Fetch(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected error for nil Tool")
	}
}

// TestWebFetchAdapter_RespectRobotsFalseBypassesRobots verifies the
// adapter's RespectRobots=false option threads through to the tool.
// Without it, a Disallow: / robots.txt blocks the fetch before any
// HTTP request fires — which is exactly the carlos-research-on-Yelp
// failure mode we shipped the option to address.
func TestWebFetchAdapter_RespectRobotsFalseBypassesRobots(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>past robots</title></head><body><p>made it.</p></body></html>`))
	}))
	defer ts.Close()

	tool := &tools.WebFetchTool{AllowPrivate: true}

	// Default behavior: blocked by robots.txt.
	defaultAdapter := &research.WebFetchAdapter{Tool: tool}
	if _, err := defaultAdapter.Fetch(context.Background(), ts.URL); err == nil {
		t.Fatal("default adapter should respect robots.txt and error")
	}

	// With override: fetch succeeds.
	override := false
	bypassAdapter := &research.WebFetchAdapter{Tool: tool, RespectRobots: &override}
	src, err := bypassAdapter.Fetch(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("RespectRobots=false should bypass: %v", err)
	}
	if !strings.Contains(src.Content, "made it") {
		t.Errorf("Content missing expected text: %q", src.Content)
	}
}

// TestWebFetchTool_UserAgentOverride verifies the tool honors a
// custom UA. Listing sites 403 the polite-bot UA; the research
// command sets a realistic Chrome string to actually get content.
func TestWebFetchTool_UserAgentOverride(t *testing.T) {
	var capturedUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		capturedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>x</title></head><body><p>ok</p></body></html>`))
	}))
	defer ts.Close()

	tool := &tools.WebFetchTool{
		AllowPrivate: true,
		UserAgent:    "Mozilla/5.0 (test) custom-ua",
	}
	adapter := &research.WebFetchAdapter{Tool: tool}
	if _, err := adapter.Fetch(context.Background(), ts.URL); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(capturedUA, "test") {
		t.Errorf("UA override not honored; got %q", capturedUA)
	}
}
