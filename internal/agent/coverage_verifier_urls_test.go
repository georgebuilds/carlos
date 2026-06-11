package agent_test

// Coverage for URLRefetcherVerifier.Verify default-config branches and
// the broken-list cap that NewURLRefetcherVerifier (which pre-fills the
// defaults) bypasses.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestURLRefetcher_ZeroValueUsesDefaults constructs a bare verifier with
// zero PerURLTimeout / MaxParallel and nil Client, so Verify fills the
// defaults itself (the `<= 0` branches). A single healthy URL confirms
// the path runs end-to-end.
func TestURLRefetcher_ZeroValueUsesDefaults(t *testing.T) {
	f := newURLFixture(t)
	body := makeContent(f.url("/ok"))
	v := &agent.URLRefetcherVerifier{} // all zero values
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept for one healthy URL, got %s", report.Decision)
	}
}

// TestURLRefetcher_BrokenListCappedAtEight feeds more than 8 broken URLs
// so the listed-concerns cap fires (the summary line plus 8 entries).
func TestURLRefetcher_BrokenListCappedAtEight(t *testing.T) {
	f := newURLFixture(t)
	var urls []string
	for i := 0; i < 12; i++ {
		// /notfound returns 404 → broken. Add a unique query so each is a
		// distinct extracted URL.
		urls = append(urls, f.url(fmt.Sprintf("/notfound?n=%d", i)))
	}
	body := makeContent(urls...)
	v := agent.NewURLRefetcherVerifier()
	report, err := v.Verify(context.Background(), "", body)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject for all-broken, got %s", report.Decision)
	}
	// concerns = 1 summary line + at most 8 listed URLs.
	if len(report.Concerns) > 9 {
		t.Fatalf("broken concern list not capped: %d entries", len(report.Concerns))
	}
	if !strings.Contains(report.Concerns[0], "URLs broken") {
		t.Errorf("first concern should be the summary, got %q", report.Concerns[0])
	}
}
