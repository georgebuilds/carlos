package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// urlFixture is a minimal httptest server that maps path → status code,
// with an optional hang handler for timeout tests. Returns a teardown.
type urlFixture struct {
	srv *httptest.Server
}

func newURLFixture(t *testing.T) *urlFixture {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/ok")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/notfound", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/500", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/hang", func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than any reasonable client timeout.
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/head-not-allowed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &urlFixture{srv: srv}
}

func (f *urlFixture) url(path string) string {
	return f.srv.URL + path
}

// makeContent stitches URLs into a body the auditor will extract.
func makeContent(urls ...string) []byte {
	var b strings.Builder
	b.WriteString("Some prose with citations.\n\n")
	for _, u := range urls {
		fmt.Fprintf(&b, "See %s for details.\n", u)
	}
	return []byte(b.String())
}

func TestURLRefetcher_AllHealthy(t *testing.T) {
	f := newURLFixture(t)
	body := makeContent(f.url("/ok"), f.url("/redirect"))

	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept, got %s; concerns=%v", report.Decision, report.Concerns)
	}
	if report.Score != 10 {
		t.Fatalf("expected score 10, got %d", report.Score)
	}
}

func TestURLRefetcher_AllBrokenRejects(t *testing.T) {
	f := newURLFixture(t)
	body := makeContent(f.url("/notfound"), f.url("/500"))

	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject, got %s", report.Decision)
	}
	if report.Score != 1 {
		t.Fatalf("expected score 1, got %d", report.Score)
	}
	if len(report.Concerns) == 0 {
		t.Fatalf("expected concerns")
	}
}

func TestURLRefetcher_MixedNeedsRevision(t *testing.T) {
	f := newURLFixture(t)
	// 2 healthy + 1 broken = 0.667 ratio → needs_revision.
	body := makeContent(f.url("/ok"), f.url("/ok"), f.url("/notfound"))

	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationNeedsRevision {
		t.Fatalf("expected needs_revision, got %s", report.Decision)
	}
	if report.Score < 5 || report.Score > 8 {
		t.Fatalf("expected score in [5,8], got %d", report.Score)
	}
}

func TestURLRefetcher_NoURLsAcceptsTrivially(t *testing.T) {
	body := []byte("Prose with no citations at all.\n")
	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept on no-URL content, got %s", report.Decision)
	}
	if report.Score != 10 {
		t.Fatalf("expected score 10, got %d", report.Score)
	}
	if report.JudgeModel != "urls:none" {
		t.Fatalf("expected JudgeModel=urls:none, got %q", report.JudgeModel)
	}
}

func TestURLRefetcher_HangTimesOut(t *testing.T) {
	f := newURLFixture(t)
	body := makeContent(f.url("/hang"))

	v := agent.NewURLRefetcherVerifier()
	v.PerURLTimeout = 100 * time.Millisecond

	start := time.Now()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dur := time.Since(start); dur > 5*time.Second {
		t.Fatalf("verifier did not time out promptly: %s", dur)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject on timeout, got %s", report.Decision)
	}
	if report.Score != 1 {
		t.Fatalf("expected score 1, got %d", report.Score)
	}
}

func TestURLRefetcher_HeadFallsBackToGet(t *testing.T) {
	f := newURLFixture(t)
	body := makeContent(f.url("/head-not-allowed"))

	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept (GET fallback should succeed), got %s; concerns=%v", report.Decision, report.Concerns)
	}
}

func TestURLRefetcher_NetworkErrorCountsAsBroken(t *testing.T) {
	// Use a port unlikely to have any listener. 127.0.0.1:1 is reserved
	// for tcpmux which is essentially never bound on dev machines.
	body := makeContent("http://127.0.0.1:1/never")

	v := agent.NewURLRefetcherVerifier()
	v.PerURLTimeout = 500 * time.Millisecond
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject on network error, got %s; raw=%v", report.Decision, report.Concerns)
	}
}

func TestURLRefetcher_ConcurrentFetch(t *testing.T) {
	// Validate that the fanout doesn't drop URLs and respects ordering
	// in concerns (sort stability).
	f := newURLFixture(t)
	urls := []string{}
	for i := 0; i < 12; i++ {
		urls = append(urls, f.url("/ok"))
	}
	urls = append(urls, f.url("/notfound"))
	body := makeContent(urls...)

	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// All same-URL /ok deduplicated by auditor to 1 + /notfound = 2.
	// 1 healthy / 2 total = 0.5 ratio → needs_revision (>=0.5 but <0.95).
	if report.Decision != agent.VerificationNeedsRevision {
		t.Fatalf("expected needs_revision for 1/2 healthy, got %s", report.Decision)
	}
}

func TestURLRefetcher_OnlyURLsExtractedNotPaths(t *testing.T) {
	// Body contains a path-style citation (./foo) and a URL. Only the
	// URL should be fetched.
	f := newURLFixture(t)
	body := []byte(fmt.Sprintf("See %s and ./local/path/file.go for details.\n", f.url("/ok")))

	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept (1/1 URLs healthy), got %s; concerns=%v", report.Decision, report.Concerns)
	}
	if !strings.HasPrefix(report.JudgeModel, "urls:1") {
		t.Fatalf("expected JudgeModel=urls:1, got %q", report.JudgeModel)
	}
}
