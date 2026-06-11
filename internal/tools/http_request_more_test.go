package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHTTPRequestClient_InjectedClientWins — when t.Client is set, client()
// returns it verbatim (the early-return branch).
func TestHTTPRequestClient_InjectedClientWins(t *testing.T) {
	injected := &http.Client{}
	tool := &HTTPRequestTool{Client: injected}
	if got := tool.client(time.Second, false); got != injected {
		t.Error("client() should return the injected client unchanged")
	}
}

// TestHTTPRequestClient_DialControlRejectsPrivate — the built client's
// Dialer.Control closes the DNS-rebinding TOCTOU by rejecting a dial to a
// private IP at connect time when allowPrivate is false.
func TestHTTPRequestClient_DialControlRejectsPrivate(t *testing.T) {
	tool := &HTTPRequestTool{}
	cli := tool.client(2*time.Second, false)
	tr, ok := cli.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", cli.Transport)
	}
	// Dialing a loopback address must be refused by Control before connect.
	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:9")
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Errorf("Control should reject a private dial; got %v", err)
	}
}

// TestHTTPRequestClient_DialControlAllowsPrivateWhenOptedIn — with
// allowPrivate=true the Control hook is a no-op (the dial proceeds and
// fails only because nothing is listening, not because Control blocked it).
func TestHTTPRequestClient_DialControlAllowsPrivateWhenOptedIn(t *testing.T) {
	tool := &HTTPRequestTool{}
	cli := tool.client(500*time.Millisecond, true)
	tr := cli.Transport.(*http.Transport)
	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:9")
	if err != nil && strings.Contains(err.Error(), "private address") {
		t.Errorf("allowPrivate=true should not trip the Control guard; got %v", err)
	}
}

// TestHTTPRequestClient_RedirectToPrivateBlocked — the CheckRedirect hook
// refuses a 30x bounce to a private host when allowPrivate is false.
func TestHTTPRequestClient_RedirectToPrivateBlocked(t *testing.T) {
	// Drive CheckRedirect directly: with allowPrivate=false a 30x bounce to
	// a private host (here the cloud metadata link-local address) is refused.
	tool := &HTTPRequestTool{}
	cli := tool.client(time.Second, false)
	req, _ := http.NewRequest("GET", "http://169.254.169.254/x", nil)
	origin, _ := http.NewRequest("GET", "http://public.example/start", nil)
	via := []*http.Request{origin}
	if err := cli.CheckRedirect(req, via); err == nil ||
		!strings.Contains(err.Error(), "private host") {
		t.Errorf("CheckRedirect should block a private redirect; got %v", err)
	}
}

// TestHTTPRequest_NoHost — a URL with a scheme but no host is rejected.
func TestHTTPRequest_NoHost(t *testing.T) {
	tool := &HTTPRequestTool{}
	err := httpReqExecErr(t, tool, map[string]any{"url": "http:///just/a/path"})
	if err == nil || !strings.Contains(err.Error(), "no host") {
		t.Errorf("want no-host error, got %v", err)
	}
}

// TestHTTPRequest_TimeoutCapped — a per-call timeout above the 300s ceiling
// is clamped; the request still completes against a fast local server.
func TestHTTPRequest_TimeoutCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	res := httpReqExec(t, tool, map[string]any{
		"url":             srv.URL,
		"timeout_seconds": 100000, // far above the 300s cap
	})
	if res.Status != 200 {
		t.Errorf("status = %d, want 200 (timeout should be clamped, not rejected)", res.Status)
	}
}

// TestHTTPRequest_TransportErrorWraps — a dial failure is wrapped with the
// method + URL for the model.
func TestHTTPRequest_TransportErrorWraps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true, Timeout: time.Second}
	err := httpReqExecErr(t, tool, map[string]any{"url": dead, "method": "GET"})
	if err == nil || !strings.Contains(err.Error(), "GET") {
		t.Errorf("want wrapped transport error naming the method, got %v", err)
	}
}

// TestHTTPRequest_MaxRedirectsExceeded drives the real client() builder
// (no injected t.Client) so the CheckRedirect closure is exercised: a
// server that always 302s to itself trips the "stopped after N redirects"
// guard once the cap is hit.
func TestHTTPRequest_MaxRedirectsExceeded(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always bounce to ourselves; same host so headers survive but the
		// redirect counter climbs until CheckRedirect refuses.
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	// AllowPrivate=true so the loopback dial is permitted; MaxRedirects=2
	// so the loop terminates quickly via the cap rather than running away.
	tool := &HTTPRequestTool{AllowPrivate: true, MaxRedirects: 2}
	err := httpReqExecErr(t, tool, map[string]any{"url": srv.URL})
	if err == nil {
		t.Fatal("expected a redirect-cap error")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error should mention redirects; got %v", err)
	}
}

// TestHTTPRequest_DefaultMaxRedirects exercises the maxRedir<=0 default
// branch of client() by leaving MaxRedirects unset and following a single
// same-host redirect to a 200.
func TestHTTPRequest_DefaultMaxRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, srv.URL+"/end", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("arrived"))
	}))
	defer srv.Close()

	tool := &HTTPRequestTool{AllowPrivate: true} // MaxRedirects defaulted
	res := httpReqExec(t, tool, map[string]any{"url": srv.URL + "/start"})
	if res.Status != 200 {
		t.Errorf("status = %d, want 200 after following redirect", res.Status)
	}
	if !strings.Contains(res.Body, "arrived") {
		t.Errorf("body = %q, want 'arrived'", res.Body)
	}
}
