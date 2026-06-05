package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Slice 5e citations tests. The auditor is a heuristic — these tests
// pin the documented "catches" / "misses" so future tuning is loud.

func TestCitationAuditor_AllCitedScoresOne(t *testing.T) {
	body := []byte("The MAST framework analyses failures [1]. Anthropic published 90.2% gains [1]. Studies show [1] cascading effects.")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount == 0 {
		t.Fatalf("expected claims, got 0")
	}
	if len(a.Unsupported) != 0 {
		t.Errorf("unsupported = %v", a.Unsupported)
	}
	if a.Score != 1.0 {
		t.Errorf("score = %f want 1.0", a.Score)
	}
}

func TestCitationAuditor_NoCitationsScoresZero(t *testing.T) {
	body := []byte("Studies show that 50 million users were affected. Research shows error rates climbed 17.2 times. According to recent reports the issue persists.")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount == 0 {
		t.Fatalf("expected claims")
	}
	if len(a.Unsupported) != a.ClaimCount {
		t.Errorf("expected all unsupported; got %d/%d unsupported", len(a.Unsupported), a.ClaimCount)
	}
	if a.Score != 0.0 {
		t.Errorf("score = %f want 0.0", a.Score)
	}
}

func TestCitationAuditor_MixedScoresBetween(t *testing.T) {
	// First claim is cited [1] in its own sentence. Three padding
	// sentences then push the second claim out of the proximity
	// window (default = 1 sentence). The trailing claim has no
	// citation anywhere within window. Result: partial score.
	body := []byte("Anthropic published 90.2% gains [1]. Padding sentence one. Padding sentence two. Padding sentence three. According to a separate analysis the OpenAI team measured 17.2 times worse error amplification.")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount < 2 {
		t.Fatalf("expected 2+ claims, got %d", a.ClaimCount)
	}
	if a.Score == 0.0 || a.Score == 1.0 {
		t.Errorf("expected partial score, got %f (claims=%d unsupported=%d)", a.Score, a.ClaimCount, len(a.Unsupported))
	}
}

func TestCitationAuditor_ExtractsURLs(t *testing.T) {
	body := []byte("See https://example.com/foo for details. Also visit https://other.org for more.")
	a := (&agent.CitationAuditor{}).Audit(body)
	if len(a.Citations) != 2 {
		t.Errorf("citations = %v", a.Citations)
	}
}

func TestCitationAuditor_ExtractsPaths(t *testing.T) {
	body := []byte("The fix lives in /etc/foo.conf and ./scripts/run.sh. Also see ../README.md.")
	a := (&agent.CitationAuditor{}).Audit(body)
	if len(a.Citations) < 3 {
		t.Errorf("expected 3+ path citations, got %v", a.Citations)
	}
}

func TestCitationAuditor_ExtractsNumericRefs(t *testing.T) {
	body := []byte("Per [1] and [2], this works. See also [42].")
	a := (&agent.CitationAuditor{}).Audit(body)
	want := map[string]bool{"[1]": true, "[2]": true, "[42]": true}
	for _, c := range a.Citations {
		if !want[c] {
			t.Errorf("unexpected citation %q", c)
		}
		delete(want, c)
	}
	if len(want) > 0 {
		t.Errorf("missing citations: %v", want)
	}
}

func TestCitationAuditor_CitationsDeduped(t *testing.T) {
	body := []byte("See [1] for the proof. Also [1] applies. Per [1], confirmed.")
	a := (&agent.CitationAuditor{}).Audit(body)
	count := 0
	for _, c := range a.Citations {
		if c == "[1]" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("[1] appeared %d times in deduped citations", count)
	}
}

func TestCitationAuditor_SuppressesShellCommands(t *testing.T) {
	// "$ ls /tmp" should NOT be classified as a claim. Without
	// suppression, the digit-free line might still skate but a path
	// citation appears, so we use a line that has digits to make sure
	// we're really exercising the shell-prefix suppression.
	body := []byte("$ npm install 4.2.1\n$ ls /tmp\n$ rm -rf node_modules")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount != 0 {
		t.Errorf("shell commands shouldn't classify as claims; got %d claims: %v", a.ClaimCount, a.Unsupported)
	}
}

func TestCitationAuditor_IgnoresCodeFences(t *testing.T) {
	body := []byte("Real prose: the system uses 42 cores.\n\n```\nx = 42  // this line has 42 but is code\nclaim like text here too\n```\n\nMore prose after.")
	a := (&agent.CitationAuditor{}).Audit(body)
	// The "42 cores" claim outside the fence should count. The "42"
	// inside the fence should NOT.
	for _, u := range a.Unsupported {
		if strings.Contains(u, "// this line") {
			t.Errorf("code-fence content leaked into claims: %q", u)
		}
	}
}

func TestCitationAuditor_IgnoresInlineCode(t *testing.T) {
	body := []byte("Run `npm install foo@1.2.3` to set up. The version 1.2.3 supports it.")
	a := (&agent.CitationAuditor{}).Audit(body)
	// The version-1.2.3 line outside backticks should classify; the
	// command inside backticks should NOT (suppressed by inline code
	// stripping).
	if a.ClaimCount == 0 {
		t.Fatalf("expected at least the outside-prose claim")
	}
	for _, u := range a.Unsupported {
		if strings.Contains(u, "npm install") {
			t.Errorf("inline code content leaked into claims: %q", u)
		}
	}
}

func TestCitationAuditor_ShortFragmentsNotClaims(t *testing.T) {
	body := []byte("Hi. Yes. OK done.")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount != 0 {
		t.Errorf("short fragments shouldn't classify; got %d", a.ClaimCount)
	}
}

func TestCitationAuditor_ProperNounDensityTriggersClaim(t *testing.T) {
	// Two+ capitalised mid-sentence words = proper-noun density.
	body := []byte("The team flew to Berlin and met Anthropic and OpenAI representatives.")
	a := (&agent.CitationAuditor{}).Audit(body)
	if a.ClaimCount == 0 {
		t.Errorf("proper-noun density should trigger claim classification; got 0")
	}
	if len(a.Unsupported) == 0 {
		t.Errorf("expected unsupported (no citation present)")
	}
}

func TestCitationAuditor_LSTmpFalsePositiveMitigated(t *testing.T) {
	// Critical regression: "ls /tmp" as an example command (not inside
	// backticks) should not get flagged when prefixed with a shell
	// marker. Outside shell context it WILL classify (and that's fine
	// — the heuristic is loose by design); we just want to make sure
	// the shell-prefix mitigation works.
	shelled := []byte("$ ls /tmp")
	plain := []byte("Please run ls /tmp to see results.")
	a1 := (&agent.CitationAuditor{}).Audit(shelled)
	a2 := (&agent.CitationAuditor{}).Audit(plain)
	if a1.ClaimCount != 0 {
		t.Errorf("shell-prefixed ls /tmp shouldn't classify; got %d claims", a1.ClaimCount)
	}
	// The plain version has a path citation in the same sentence so
	// even if it classifies it should be SUPPORTED.
	for _, u := range a2.Unsupported {
		if strings.Contains(u, "ls /tmp") {
			t.Errorf("plain ls /tmp got flagged unsupported despite having /tmp citation in same sentence: %q", u)
		}
	}
}

func TestCitationAuditor_AcceptsCustomProximity(t *testing.T) {
	// With window=2, a citation two sentences away should support a
	// claim. With window=0 (which we override to 1), only same-or-
	// adjacent sentences count.
	body := []byte("First sentence has a claim about 42 cores. Padding sentence. Another padding sentence with a citation [1].")
	wide := (&agent.CitationAuditor{ProximityWindow: 3}).Audit(body)
	tight := (&agent.CitationAuditor{ProximityWindow: 1}).Audit(body)
	if wide.Score <= tight.Score {
		t.Errorf("wider proximity should not reduce score: wide=%f tight=%f", wide.Score, tight.Score)
	}
}

// ---- AuditAndQueue ----

func TestAuditAndQueue_AboveThresholdNoQueue(t *testing.T) {
	log := openTestLog(t)
	body := []byte("All claims here have citations [1]. The 90.2% finding is in [1].")
	ref := agent.ArtifactRef{ID: "cit1", AgentID: "child", Kind: agent.ArtifactKindResearch}
	a, err := agent.AuditAndQueue(context.Background(), log, ref, body, 0.5)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if a.Score < 0.5 {
		t.Fatalf("audit precondition: score = %f, expected >=0.5", a.Score)
	}
	pend, _ := agent.ListPendingApprovals(context.Background(), log)
	if len(pend) != 0 {
		t.Errorf("above-threshold should not queue; got %d pending", len(pend))
	}
}

func TestAuditAndQueue_BelowThresholdQueues(t *testing.T) {
	log := openTestLog(t)
	body := []byte("Studies show 50 million users were affected. Research shows the issue persists. According to reports it costs $1B annually.")
	ref := agent.ArtifactRef{ID: "cit2", AgentID: "child", Kind: agent.ArtifactKindResearch}
	a, err := agent.AuditAndQueue(context.Background(), log, ref, body, 0.8)
	if !errors.Is(err, agent.ErrCitationCheckFailed) {
		t.Fatalf("want ErrCitationCheckFailed, got %v", err)
	}
	_ = a
	pend, _ := agent.ListPendingApprovals(context.Background(), log)
	if len(pend) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pend))
	}
	if !strings.Contains(pend[0].Title, "citations missing") {
		t.Errorf("title missing flag: %q", pend[0].Title)
	}
}

func TestAuditAndQueue_NilLogErrors(t *testing.T) {
	_, err := agent.AuditAndQueue(context.Background(), nil, agent.ArtifactRef{}, []byte("foo"), 0.5)
	if err == nil {
		t.Fatalf("expected error on nil log")
	}
}

func TestAuditAndQueue_EmptyBodyAcceptsAtAnyThreshold(t *testing.T) {
	log := openTestLog(t)
	// Empty body has zero claims, so score is 1.0 by convention; any
	// threshold including 1.0 should pass.
	ref := agent.ArtifactRef{ID: "cit3", AgentID: "child", Kind: agent.ArtifactKindResearch}
	_, err := agent.AuditAndQueue(context.Background(), log, ref, []byte(""), 1.0)
	if err != nil {
		t.Errorf("empty body should pass: %v", err)
	}
}
