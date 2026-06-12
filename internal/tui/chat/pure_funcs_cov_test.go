package chat

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
)

// TestSplitWhenPrompt_QuotedAndBareForms covers both parse branches of
// splitWhenPrompt plus the failure cases.
func TestSplitWhenPrompt_QuotedAndBareForms(t *testing.T) {
	// Quoted "when" with a trailing prompt.
	when, prompt, ok := splitWhenPrompt(`"in 5 minutes" deploy the build`)
	if !ok {
		t.Fatal("quoted form should parse")
	}
	if when != "in 5 minutes" {
		t.Errorf("quoted when = %q", when)
	}
	if prompt != "deploy the build" {
		t.Errorf("quoted prompt = %q", prompt)
	}

	// Bare form: first token is the when, the rest is the prompt.
	when, prompt, ok = splitWhenPrompt("tomorrow check the deploy")
	if !ok || when != "tomorrow" || prompt != "check the deploy" {
		t.Errorf("bare form mis-parsed: when=%q prompt=%q ok=%v", when, prompt, ok)
	}

	// Empty input.
	if _, _, ok := splitWhenPrompt("   "); ok {
		t.Error("empty input should not parse")
	}
	// Unterminated quote.
	if _, _, ok := splitWhenPrompt(`"never ends here`); ok {
		t.Error("unterminated quote should not parse")
	}
	// when present but no prompt.
	if _, _, ok := splitWhenPrompt("solo"); ok {
		t.Error("single token (no prompt) should not parse")
	}
	// Quoted when but empty prompt.
	if _, _, ok := splitWhenPrompt(`"5pm"`); ok {
		t.Error("quoted when with no prompt should not parse")
	}
}

// TestAutoSlugName_Sanitizes covers the slug builder including the
// non-alphanumeric -> dash collapsing and the lowercase fold.
func TestAutoSlugName_Sanitizes(t *testing.T) {
	got := autoSlugName("Deploy The Build!! NOW")
	// The prefix before the timestamp suffix should be a kebab slug.
	prefix := got[:strings.LastIndex(got, "-")]
	if strings.Contains(prefix, " ") || strings.Contains(prefix, "!") {
		t.Errorf("slug should drop spaces/punctuation; got %q", got)
	}
	if prefix != strings.ToLower(prefix) {
		t.Errorf("slug should be lowercase; got %q", prefix)
	}
	if !strings.HasPrefix(got, "deploy") {
		t.Errorf("slug should start from the prompt words; got %q", got)
	}
}

// TestAutoSlugName_AllPunctFallsBackToSched covers the empty-slug
// fallback ("sched") when the prompt has no slug-able characters.
func TestAutoSlugName_AllPunctFallsBackToSched(t *testing.T) {
	got := autoSlugName("!!! ??? ...")
	if !strings.HasPrefix(got, "sched-") {
		t.Errorf("all-punct prompt should fall back to 'sched-NNNN'; got %q", got)
	}
}

// TestSelectedArg_BoundsAndState covers the selectedArg guards.
func TestSelectedArg_BoundsAndState(t *testing.T) {
	// Not in arg mode -> no selection.
	s := &slashSuggest{inArgs: false, argMatches: []string{"a"}}
	if _, ok := s.selectedArg(); ok {
		t.Error("not-in-args should report no selection")
	}
	// In arg mode but empty matches.
	s = &slashSuggest{inArgs: true}
	if _, ok := s.selectedArg(); ok {
		t.Error("empty matches should report no selection")
	}
	// In arg mode, cursor out of range.
	s = &slashSuggest{inArgs: true, argMatches: []string{"x", "y"}, argCursor: 9}
	if _, ok := s.selectedArg(); ok {
		t.Error("out-of-range cursor should report no selection")
	}
	// Valid selection.
	s = &slashSuggest{inArgs: true, argMatches: []string{"x", "y"}, argCursor: 1}
	if got, ok := s.selectedArg(); !ok || got != "y" {
		t.Errorf("valid selection = (%q, %v); want (y, true)", got, ok)
	}
}

// TestRenderReportMarkdown_CitationAndVerifierSections covers the
// Citations + Verification branches of RenderReportMarkdown (the
// existing shape test omits them).
func TestRenderReportMarkdown_CitationAndVerifierSections(t *testing.T) {
	r := &research.Report{
		Question:  "Q?",
		Synthesis: "answer",
		Citations: &agent.Audit{ClaimCount: 4, Score: 0.75, Unsupported: []string{"u1", "u2"}},
		Verification: &agent.VerificationReport{
			Decision:   agent.VerificationNeedsRevision,
			Score:      7,
			JudgeModel: "anthropic:claude",
			Concerns:   []string{"thin sourcing", "stale data"},
		},
	}
	out := RenderReportMarkdown(r)
	for _, want := range []string{
		"## Citation audit",
		"claims: 4",
		"coverage score: 0.75",
		"unsupported: 2",
		"## Verifier",
		"decision:",
		"score: 7",
		"judge: anthropic:claude",
		"thin sourcing",
		"stale data",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderReportMarkdown missing %q in:\n%s", want, out)
		}
	}
}

// TestAppendUserMessage_AppendFailureSurfacesErrMsg covers the
// log.Append failure path in appendUserMessage (closed log).
func TestAppendUserMessage_AppendFailureSurfacesErrMsg(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV0000000000000000APP001"
	seedAgent(t, log, agentID, "app", "claude-4.7-sonnet")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	// Close the underlying log so the async append fails.
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	cmd := m.appendUserMessage("after close", nil)
	if cmd == nil {
		t.Fatal("appendUserMessage should return an append command")
	}
	if _, ok := cmd().(errMsg); !ok {
		t.Errorf("append against a closed log should surface an errMsg; got %T", cmd())
	}
}

// TestRenderSwitcherNewFrameTile_FocusedAndUnfocused covers both render
// states of the trailing "+ new frame" tile.
func TestRenderSwitcherNewFrameTile_FocusedAndUnfocused(t *testing.T) {
	focused := renderSwitcherNewFrameTile(true)
	unfocused := renderSwitcherNewFrameTile(false)
	for _, out := range []string{focused, unfocused} {
		if !strings.Contains(out, "new frame") {
			t.Errorf("new-frame tile should label itself; got:\n%s", out)
		}
	}
	// Focused uses a thick border corner; unfocused a rounded one.
	if !strings.Contains(focused, "┏") {
		t.Errorf("focused tile should use a thick border; got:\n%s", focused)
	}
	if !strings.Contains(unfocused, "╭") {
		t.Errorf("unfocused tile should use a rounded border; got:\n%s", unfocused)
	}
}

// TestSwitcherPageCount_DegenerateWidth covers the visible<=0 guard in
// switcherPageCount (returns 1) and the multi-page count.
func TestSwitcherPageCount_DegenerateWidth(t *testing.T) {
	// A normal small width yields 1 page for few frames.
	if got := switcherPageCount(2, 120); got != 1 {
		t.Errorf("2 frames @ 120w should be 1 page; got %d", got)
	}
	// Many frames at a narrow (1-col) width spill onto multiple pages.
	if got := switcherPageCount(20, 40); got < 2 {
		t.Errorf("20 frames @ 40w should need multiple pages; got %d", got)
	}
}
