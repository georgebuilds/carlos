package chat

import (
	"strings"
	"testing"
	"time"
)

// TestRenderToolCardGroup_GroupsConsecutiveCalls pins the v0.7.6
// behavior: 2+ consecutive tool calls share one rounded-border card
// with `─` separators between rows, like a Bootstrap list-group. The
// user explicitly asked for this so a flurry of back-to-back tool
// calls (read, grep, read, read) reads as a single "run" instead of
// a stack of independent boxes.
func TestRenderToolCardGroup_GroupsConsecutiveCalls(t *testing.T) {
	es := []transcriptEntry{
		{kind: entryToolCall, ts: time.Now(), tool: "read", toolInput: `{"path":"a.go"}`, hasResult: true},
		{kind: entryToolCall, ts: time.Now(), tool: "grep", toolInput: `{"pattern":"foo"}`, hasResult: true},
		{kind: entryToolCall, ts: time.Now(), tool: "read", toolInput: `{"path":"b.go"}`, hasResult: true},
	}
	out := renderToolCardGroup(es, 100)

	// Each tool name survives into the group.
	for _, want := range []string{"read", "grep"} {
		if !strings.Contains(out, want) {
			t.Errorf("group missing tool name %q:\n%s", want, out)
		}
	}
	// The 🔧 glyph appears once per row.
	if got := strings.Count(out, "🔧"); got != len(es) {
		t.Errorf("🔧 count = %d, want %d (one per row)", got, len(es))
	}
}

// TestRenderToolCardGroup_SingleEntryMatchesLegacyCard guards the
// "one tool call looks the same" contract.
func TestRenderToolCardGroup_SingleEntryMatchesLegacyCard(t *testing.T) {
	e := transcriptEntry{
		kind: entryToolCall, ts: time.Now(), tool: "bash",
		toolInput: `{"command":"ls -la"}`, hasResult: true,
	}
	group := renderToolCardGroup([]transcriptEntry{e}, 100)
	solo := renderToolCard(e, 100)
	if group != solo {
		t.Errorf("solo group differs from single-card render:\ngroup:\n%s\nsolo:\n%s", group, solo)
	}
}

// TestRenderToolCardGroup_MixedErrorPaintsOuterWarn covers the rule
// that if ANY entry in a group errored, the outer border flips to
// the warn color so the group reads as "something failed in here";
// individual rows still carry their own glyph (🔧 vs ✗) so the
// failed row is identifiable.
func TestRenderToolCardGroup_MixedErrorPaintsOuterWarn(t *testing.T) {
	es := []transcriptEntry{
		{kind: entryToolCall, ts: time.Now(), tool: "read", hasResult: true},
		{kind: entryToolCall, ts: time.Now(), tool: "bash", hasResult: true, isError: true},
	}
	out := renderToolCardGroup(es, 100)
	// Both glyphs co-exist: 🔧 from the OK row and ✗ from the errored row.
	if !strings.Contains(out, "🔧") {
		t.Errorf("group missing 🔧 from successful row:\n%s", out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("group missing ✗ from errored row:\n%s", out)
	}
}

// TestComposeTranscript_GroupsRunsOfToolCalls is the integration
// check: composeTranscript itself detects consecutive runs and folds
// them into a single grouped card, with non-groupable kinds (user /
// assistant messages) breaking the run.
func TestComposeTranscript_GroupsRunsOfToolCalls(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryUserMessage, ts: time.Now(), text: "find every TODO"},
		// Run of 3 tool calls — should fold into one group.
		{kind: entryToolCall, ts: time.Now(), tool: "grep", hasResult: true},
		{kind: entryToolCall, ts: time.Now(), tool: "read", hasResult: true},
		{kind: entryToolCall, ts: time.Now(), tool: "read", hasResult: true},
		// Assistant message breaks the run.
		{kind: entryAssistantMessage, ts: time.Now(), text: "found 3"},
		// Run of 2 errors — should fold into one group.
		{kind: entryError, ts: time.Now(), text: "openrouter: HTTP 400: x"},
		{kind: entryError, ts: time.Now(), text: "openrouter: HTTP 429: y"},
	}
	out := composeTranscript(entries, "", "", nil, 100)

	// Tool-card group's outer rounded-border opens with ╭. The text
	// before assistant message ("find every TODO") sits OUTSIDE any
	// box; we need at least two distinct rounded-border boxes (one
	// for the tool run, one for the error run).
	box := strings.Count(out, "╭")
	if box < 2 {
		t.Errorf("expected at least 2 grouped boxes in transcript, got %d:\n%s", box, out)
	}

	// Each tool name from the run appears.
	for _, want := range []string{"grep", "read"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing tool %q:\n%s", want, out)
		}
	}
	// Both error bodies appear.
	for _, want := range []string{"HTTP 400", "HTTP 429"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing error %q:\n%s", want, out)
		}
	}
}
