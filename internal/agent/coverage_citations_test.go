package agent_test

// Coverage for splitSentences' end-of-paragraph boundary branches,
// driven through the public CitationAuditor.Audit:
//
//   - a sentence ending in trailing punctuation that runs to EOF
//     (e.g. "...gains.)") so the punctuation-extension hits end>=len(p).
//   - a sentence whose terminator is followed only by trailing
//     whitespace to EOF, so the next-non-space scan hits j>=len(p).

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

func TestCitationAuditor_TrailingPunctuationToEOF(t *testing.T) {
	// The claim sentence ends with ".)" at the very end of the body so
	// splitSentences extends past the close-paren and finds EOF.
	body := []byte("Research shows 42 percent of runs improved (a measured gain.)")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount == 0 {
		t.Fatalf("expected at least one claim, got 0")
	}
}

func TestCitationAuditor_TrailingWhitespaceToEOF(t *testing.T) {
	// The final sentence's period is followed only by trailing spaces,
	// so the next-non-space lookahead reaches EOF.
	body := []byte("The system measured 17 failures across 3 runs.   ")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount == 0 {
		t.Fatalf("expected at least one claim, got 0")
	}
}

// TestCitationAuditor_EmptyBodyNoClaims drives Audit with a body that
// produces no sentences (whitespace only), exercising the empty-paragraph
// skip and the zero-claim early return.
func TestCitationAuditor_EmptyBodyNoClaims(t *testing.T) {
	a := (&agent.CitationAuditor{}).Audit([]byte("   \n\n   \n\n"))
	if a.ClaimCount != 0 {
		t.Fatalf("whitespace body should yield 0 claims, got %d", a.ClaimCount)
	}
}
