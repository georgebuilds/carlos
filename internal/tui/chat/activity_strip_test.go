package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// makeToolCallEvent builds an agent.Event with EvtToolCall payload so
// the activity-strip tests can drive applyEvent without spinning up a
// SQLite log + bubbletea program. The signature mirrors the seed
// helpers in chat_test.go.
func makeToolCallEvent(t *testing.T, toolName, inputJSON string) agent.Event {
	t.Helper()
	payload, err := json.Marshal(agent.ToolCall{Name: toolName, Input: []byte(inputJSON)})
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	return agent.Event{
		TS:      time.Now().UTC(),
		Type:    agent.EvtToolCall,
		Payload: payload,
	}
}

// newStripTestModel constructs a minimal Model for direct applyEvent
// tests. We can't use the zero-value &Model{} because applyEvent
// dereferences m.proj before the switch on event type. The projection
// itself is left empty so it surfaces a "unknown agent" error which
// applyEvent absorbs into a system_note entry - tests skip past it
// when checking the tool-call payload.
func newStripTestModel() *Model {
	return &Model{proj: agent.NewProjection()}
}

// firstToolEntry returns the first transcriptEntry of kind entryToolCall
// in m.transcript, skipping system notes the projection emits for
// unrecognized agent IDs.
func firstToolEntry(t *testing.T, m *Model) transcriptEntry {
	t.Helper()
	for _, e := range m.transcript {
		if e.kind == entryToolCall {
			return e
		}
	}
	t.Fatalf("no tool-call entry in transcript: %+v", m.transcript)
	return transcriptEntry{}
}

// TestParseSkillName_Happy returns the trimmed "name" field from
// well-formed skill_use input.
func TestParseSkillName_Happy(t *testing.T) {
	got := parseSkillName([]byte(`{"name":"calendar"}`))
	if got != "calendar" {
		t.Errorf("got %q, want %q", got, "calendar")
	}
}

// TestParseSkillName_TrimsWhitespace strips surrounding whitespace so
// the strip's display label doesn't show "  calendar  ".
func TestParseSkillName_TrimsWhitespace(t *testing.T) {
	got := parseSkillName([]byte(`{"name":"  calendar  "}`))
	if got != "calendar" {
		t.Errorf("got %q, want %q", got, "calendar")
	}
}

// TestParseSkillName_Empty defends the nil / empty / malformed / missing
// inputs. Each must return "" so the caller falls back to the bare
// tool name (parseSkillName never propagates a parse error - the
// strip always renders).
func TestParseSkillName_Empty(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"nil", nil},
		{"empty bytes", []byte{}},
		{"malformed json", []byte(`{"name":`)},
		{"missing field", []byte(`{"other":"x"}`)},
		{"name is empty", []byte(`{"name":""}`)},
		{"name is whitespace", []byte(`{"name":"   "}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseSkillName(c.in); got != "" {
				t.Errorf("got %q, want empty", got)
			}
		})
	}
}

// TestSegmentLabel_Skill wraps the skill name with the 📚 glyph so the
// strip surfaces the skill, not the generic "skill_use" tool name.
func TestSegmentLabel_Skill(t *testing.T) {
	e := transcriptEntry{tool: "skill_use", isSkill: true, skillName: "calendar"}
	got := segmentLabel(e)
	want := stripGlyphSkill + " calendar"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSegmentLabel_SkillFallback_NameMissing falls back to the bare
// tool name when the JSON input didn't yield a skill name.
func TestSegmentLabel_SkillFallback_NameMissing(t *testing.T) {
	e := transcriptEntry{tool: "skill_use", isSkill: true}
	got := segmentLabel(e)
	want := stripGlyphSkill + " skill_use"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSegmentLabel_SkillFallback_AllMissing handles the (unlikely but
// possible) replay state where both skillName and tool are blank.
func TestSegmentLabel_SkillFallback_AllMissing(t *testing.T) {
	e := transcriptEntry{isSkill: true}
	got := segmentLabel(e)
	want := stripGlyphSkill + " " + skillUseToolName
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSegmentLabel_RegularTool returns the bare tool name.
func TestSegmentLabel_RegularTool(t *testing.T) {
	e := transcriptEntry{tool: "bash"}
	if got := segmentLabel(e); got != "bash" {
		t.Errorf("got %q, want %q", got, "bash")
	}
}

// TestSegmentLabel_RegularTool_EmptyName returns "?" so a malformed
// transcript entry still produces a renderable strip.
func TestSegmentLabel_RegularTool_EmptyName(t *testing.T) {
	if got := segmentLabel(transcriptEntry{}); got != "?" {
		t.Errorf("got %q, want %q", got, "?")
	}
}

// TestStripRollup_Empty returns nil so callers can range safely.
func TestStripRollup_Empty(t *testing.T) {
	if got := stripRollup(nil); got != nil {
		t.Errorf("nil entries: got %v, want nil", got)
	}
}

// TestStripRollup_FoldsSameNameSameState folds adjacent same-name
// same-error entries into one segment with count > 1.
func TestStripRollup_FoldsSameNameSameState(t *testing.T) {
	es := []transcriptEntry{
		{tool: "read", hasResult: true},
		{tool: "read", hasResult: true},
	}
	out := stripRollup(es)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(out), out)
	}
	if out[0].label != "read" || out[0].count != 2 {
		t.Errorf("got %+v, want {label:read count:2}", out[0])
	}
}

// TestStripRollup_PreservesDistinctOrder keeps distinct tool names as
// separate segments in their original transcript order.
func TestStripRollup_PreservesDistinctOrder(t *testing.T) {
	es := []transcriptEntry{
		{tool: "read", hasResult: true},
		{tool: "grep", hasResult: true},
		{tool: "bash", hasResult: true},
	}
	out := stripRollup(es)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	for i, want := range []string{"read", "grep", "bash"} {
		if out[i].label != want {
			t.Errorf("seg[%d].label = %q, want %q", i, out[i].label, want)
		}
		if out[i].count != 1 {
			t.Errorf("seg[%d].count = %d, want 1", i, out[i].count)
		}
	}
}

// TestStripRollup_SplitsOnErrorState does NOT fold a successful read
// with an errored read: they need different visual treatment (warn
// color, dotted underline) and folding would hide the failure.
func TestStripRollup_SplitsOnErrorState(t *testing.T) {
	es := []transcriptEntry{
		{tool: "read", hasResult: true},
		{tool: "read", hasResult: true, isError: true},
		{tool: "read", hasResult: true},
	}
	out := stripRollup(es)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3 (split on error boundary)", len(out))
	}
	if out[1].isError != true {
		t.Errorf("middle segment isError = %v, want true", out[1].isError)
	}
}

// TestStripRollup_SplitsOnSkillName does NOT fold two skill_use calls
// loading different skills into one segment - they're conceptually
// distinct loads and read better listed separately.
func TestStripRollup_SplitsOnSkillName(t *testing.T) {
	es := []transcriptEntry{
		{tool: "skill_use", isSkill: true, skillName: "calendar", hasResult: true},
		{tool: "skill_use", isSkill: true, skillName: "onboarding", hasResult: true},
	}
	out := stripRollup(es)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one per skill)", len(out))
	}
}

// TestStripRollup_FoldsSameSkillTwice does fold two skill_use calls
// for the SAME skill into "📚 calendar ×2" - same label, same state.
func TestStripRollup_FoldsSameSkillTwice(t *testing.T) {
	es := []transcriptEntry{
		{tool: "skill_use", isSkill: true, skillName: "calendar", hasResult: true},
		{tool: "skill_use", isSkill: true, skillName: "calendar", hasResult: true},
	}
	out := stripRollup(es)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (same skill folds)", len(out))
	}
	if out[0].count != 2 {
		t.Errorf("count = %d, want 2", out[0].count)
	}
}

// TestStripGlyph_Empty returns the neutral tool chevron for a defensive
// empty group.
func TestStripGlyph_Empty(t *testing.T) {
	glyph, c := stripGlyph(nil)
	if glyph != stripGlyphTool || c != colorTool {
		t.Errorf("empty: got (%q, %v), want (%q, %v)", glyph, c, stripGlyphTool, colorTool)
	}
}

// TestStripGlyph_AllError flips both glyph and color when every entry
// failed - the most aggressive visual signal in the strip system.
func TestStripGlyph_AllError(t *testing.T) {
	es := []transcriptEntry{
		{hasResult: true, isError: true},
		{hasResult: true, isError: true},
	}
	glyph, c := stripGlyph(es)
	if glyph != stripGlyphError || c != colorWarn {
		t.Errorf("got (%q, %v), want (%q, %v)", glyph, c, stripGlyphError, colorWarn)
	}
}

// TestStripGlyph_AllSkill surfaces 📚 in the accent color when the
// whole run is skill loads.
func TestStripGlyph_AllSkill(t *testing.T) {
	es := []transcriptEntry{
		{isSkill: true, hasResult: true},
		{isSkill: true, hasResult: true},
	}
	glyph, c := stripGlyph(es)
	if glyph != stripGlyphSkill || c != colorAccent {
		t.Errorf("got (%q, %v), want (%q, %v)", glyph, c, stripGlyphSkill, colorAccent)
	}
}

// TestStripGlyph_MixedToolError keeps the neutral chevron when any
// entry succeeded - per the all-or-nothing rule the per-segment color
// handles the per-entry kind.
func TestStripGlyph_MixedToolError(t *testing.T) {
	es := []transcriptEntry{
		{hasResult: true},
		{hasResult: true, isError: true},
	}
	glyph, c := stripGlyph(es)
	if glyph != stripGlyphTool || c != colorTool {
		t.Errorf("got (%q, %v), want (%q, %v)", glyph, c, stripGlyphTool, colorTool)
	}
}

// TestStripGlyph_MixedSkillTool also keeps the neutral chevron - one
// non-skill defeats the all-skill condition.
func TestStripGlyph_MixedSkillTool(t *testing.T) {
	es := []transcriptEntry{
		{isSkill: true, hasResult: true},
		{tool: "bash", hasResult: true},
	}
	glyph, c := stripGlyph(es)
	if glyph != stripGlyphTool || c != colorTool {
		t.Errorf("got (%q, %v), want (%q, %v)", glyph, c, stripGlyphTool, colorTool)
	}
}

// TestStripMetadata_Empty returns "" so the layout code can skip the
// meta section entirely.
func TestStripMetadata_Empty(t *testing.T) {
	if got := stripMetadata(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestStripMetadata_AllRunning short-circuits to the spinner-equivalent
// "running…" when nothing has returned yet (mid-replay).
func TestStripMetadata_AllRunning(t *testing.T) {
	es := []transcriptEntry{{tool: "bash"}, {tool: "read"}}
	got := stripMetadata(es)
	if !strings.Contains(got, "running…") {
		t.Errorf("got %q, want substring \"running…\"", got)
	}
}

// TestStripMetadata_SingleLine surfaces the singular "1 line" so the
// strip reads naturally for tools whose output is just a status row.
func TestStripMetadata_SingleLine(t *testing.T) {
	es := []transcriptEntry{{tool: "echo", hasResult: true, toolResult: "ok"}}
	got := stripMetadata(es)
	if !strings.Contains(got, "1 line") {
		t.Errorf("got %q, want substring \"1 line\"", got)
	}
}

// TestStripMetadata_MultiLineSum sums line counts across the group so
// "read ×2" with a 10-line and 12-line result reads as "22 lines".
func TestStripMetadata_MultiLineSum(t *testing.T) {
	es := []transcriptEntry{
		{tool: "read", hasResult: true, toolResult: strings.Repeat("a\n", 10)},
		{tool: "read", hasResult: true, toolResult: strings.Repeat("b\n", 12)},
	}
	got := stripMetadata(es)
	if !strings.Contains(got, "22 lines") {
		t.Errorf("got %q, want substring \"22 lines\"", got)
	}
}

// TestStripMetadata_NoOutput surfaces "no output" for tools that
// returned empty (notes_recent on a fresh vault, e.g.).
func TestStripMetadata_NoOutput(t *testing.T) {
	es := []transcriptEntry{{tool: "notes", hasResult: true}}
	got := stripMetadata(es)
	if !strings.Contains(got, "no output") {
		t.Errorf("got %q, want substring \"no output\"", got)
	}
}

// TestStripMetadata_ErrorChipSingular renders "1 error" when exactly
// one entry failed inside a mixed group.
func TestStripMetadata_ErrorChipSingular(t *testing.T) {
	es := []transcriptEntry{
		{tool: "bash", hasResult: true, toolResult: "ok"},
		{tool: "bash", hasResult: true, isError: true, toolResult: "no such file"},
	}
	got := stripMetadata(es)
	if !strings.Contains(got, "1 error") {
		t.Errorf("got %q, want substring \"1 error\"", got)
	}
	if !strings.Contains(got, "1 line") {
		t.Errorf("got %q, want substring \"1 line\" (the successful row)", got)
	}
}

// TestStripMetadata_ErrorChipPlural renders "3 errors" for the
// all-failed run.
func TestStripMetadata_ErrorChipPlural(t *testing.T) {
	es := []transcriptEntry{
		{tool: "bash", hasResult: true, isError: true},
		{tool: "bash", hasResult: true, isError: true},
		{tool: "bash", hasResult: true, isError: true},
	}
	got := stripMetadata(es)
	if !strings.Contains(got, "3 errors") {
		t.Errorf("got %q, want substring \"3 errors\"", got)
	}
}

// TestStripHint is a smoke test that the affordance contains both the
// keybind ("e") and the action label ("expand") so users learn the
// gesture without diving into /help.
func TestStripHint(t *testing.T) {
	got := stripHint()
	if !strings.Contains(got, "e") {
		t.Errorf("hint missing key glyph: %q", got)
	}
	if !strings.Contains(got, "expand") {
		t.Errorf("hint missing label: %q", got)
	}
}

// TestComposeStripLine_FullLayout renders all three sections when
// there's enough width.
func TestComposeStripLine_FullLayout(t *testing.T) {
	out := composeStripLine("LEFT", "META", "HINT", 60)
	if !strings.Contains(out, "LEFT") || !strings.Contains(out, "META") || !strings.Contains(out, "HINT") {
		t.Errorf("expected all three sections in: %q", out)
	}
}

// TestComposeStripLine_DropsHintFirst surrenders the expand hint
// before sacrificing the metadata.
func TestComposeStripLine_DropsHintFirst(t *testing.T) {
	// Width tight enough to fit LEFT + META + 1 gap but not the HINT.
	out := composeStripLine("LEFT", "META", "HINT", 13)
	if strings.Contains(out, "HINT") {
		t.Errorf("hint should have been dropped at narrow width: %q", out)
	}
	if !strings.Contains(out, "META") {
		t.Errorf("meta should survive: %q", out)
	}
}

// TestComposeStripLine_DropsMetaWhenTighter falls all the way back to
// the left section when neither hint nor meta fit.
func TestComposeStripLine_DropsMetaWhenTighter(t *testing.T) {
	out := composeStripLine("LEFT", "META", "HINT", 4)
	if strings.Contains(out, "META") || strings.Contains(out, "HINT") {
		t.Errorf("only LEFT should remain at width 4: %q", out)
	}
	if !strings.Contains(out, "LEFT") {
		t.Errorf("LEFT should always render: %q", out)
	}
}

// TestComposeStripLine_NoMeta skips the meta-gap logic when the meta
// section is empty (defensive empty-group case).
func TestComposeStripLine_NoMeta(t *testing.T) {
	out := composeStripLine("LEFT", "", "", 60)
	if out != "LEFT" {
		t.Errorf("got %q, want %q", out, "LEFT")
	}
}

// TestRenderToolStrip_Empty returns "" for an empty group so the
// caller can append unconditionally.
func TestRenderToolStrip_Empty(t *testing.T) {
	if got := renderToolStrip(nil, 100); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestStripSegmentsList_Empty exercises the defensive guard inside
// stripSegmentsList. renderToolStrip's own len(es)==0 short-circuit
// means stripSegmentsList is never called with empty input in
// production, but the guard documents that future refactors of the
// caller must keep that invariant. Pin it explicitly.
func TestStripSegmentsList_Empty(t *testing.T) {
	if got := stripSegmentsList(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestRenderToolStrip_SingleToolHasArgsPreview surfaces the input
// preview after the tool name on solo calls - the old single-card
// behavior the user already relies on for "what was the call?".
func TestRenderToolStrip_SingleToolHasArgsPreview(t *testing.T) {
	e := transcriptEntry{
		tool: "bash", toolInput: `{"cmd":"ls -la ~/Desktop"}`,
		hasResult: true, toolResult: "a\nb\nc",
	}
	got := renderToolStrip([]transcriptEntry{e}, 120)
	if !strings.Contains(got, "bash") {
		t.Errorf("missing tool name: %q", got)
	}
	if !strings.Contains(got, "ls -la") {
		t.Errorf("missing args preview: %q", got)
	}
	if !strings.Contains(got, "3 lines") {
		t.Errorf("missing line count: %q", got)
	}
	if !strings.Contains(got, stripGlyphTool) {
		t.Errorf("missing tool glyph: %q", got)
	}
}

// TestRenderToolStrip_SingleSkillUsesBooksGlyph surfaces the skill's
// own name (not "skill_use") and leads with 📚 instead of ▸.
func TestRenderToolStrip_SingleSkillUsesBooksGlyph(t *testing.T) {
	e := transcriptEntry{
		tool: "skill_use", isSkill: true, skillName: "calendar",
		toolInput: `{"name":"calendar"}`, hasResult: true,
		toolResult: "skill body...",
	}
	got := renderToolStrip([]transcriptEntry{e}, 100)
	if !strings.Contains(got, stripGlyphSkill) {
		t.Errorf("missing 📚 glyph: %q", got)
	}
	if !strings.Contains(got, "calendar") {
		t.Errorf("missing skill name: %q", got)
	}
	if strings.Contains(got, "skill_use") {
		t.Errorf("strip should hide skill_use tool name when skillName present: %q", got)
	}
}

// TestRenderToolStrip_MultiSameNameRollsUp folds three reads into one
// "read ×3" segment - the load-bearing win of Concept A.
func TestRenderToolStrip_MultiSameNameRollsUp(t *testing.T) {
	es := []transcriptEntry{
		{tool: "read", hasResult: true, toolResult: "a"},
		{tool: "read", hasResult: true, toolResult: "b"},
		{tool: "read", hasResult: true, toolResult: "c"},
	}
	got := renderToolStrip(es, 100)
	if !strings.Contains(got, "read") {
		t.Errorf("missing tool name: %q", got)
	}
	if !strings.Contains(got, "×3") {
		t.Errorf("missing rollup multiplier ×3: %q", got)
	}
	if strings.Count(got, "read") != 1 {
		t.Errorf("read should appear once (rolled), got %d times: %q", strings.Count(got, "read"), got)
	}
}

// TestRenderToolStrip_MixedNamesListInline keeps distinct names side
// by side, separated by middots, with same-name folding still active.
func TestRenderToolStrip_MixedNamesListInline(t *testing.T) {
	es := []transcriptEntry{
		{tool: "read", hasResult: true, toolResult: "a"},
		{tool: "read", hasResult: true, toolResult: "b"},
		{tool: "git_status", hasResult: true, toolResult: "ok"},
		{tool: "git_log", hasResult: true, toolResult: "x"},
	}
	got := renderToolStrip(es, 120)
	for _, want := range []string{"read", "×2", "git_status", "git_log", "·"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
}

// TestRenderToolStrip_AllErroredFlipsGlyph swaps the chevron for a
// cross on an all-failed run, signaling the failure at a glance.
func TestRenderToolStrip_AllErroredFlipsGlyph(t *testing.T) {
	es := []transcriptEntry{
		{tool: "bash", hasResult: true, isError: true},
		{tool: "bash", hasResult: true, isError: true},
	}
	got := renderToolStrip(es, 100)
	if !strings.Contains(got, stripGlyphError) {
		t.Errorf("missing ✗ glyph: %q", got)
	}
	if !strings.Contains(got, "2 errors") {
		t.Errorf("missing error chip: %q", got)
	}
	if strings.Contains(got, stripGlyphTool) {
		t.Errorf("should not contain ▸ when all errored: %q", got)
	}
}

// TestRenderToolStrip_MixedErrorsKeepNeutralGlyph leaves the chevron
// neutral when at least one entry succeeded - the per-segment color
// already paints the failed name in warn.
func TestRenderToolStrip_MixedErrorsKeepNeutralGlyph(t *testing.T) {
	es := []transcriptEntry{
		{tool: "bash", hasResult: true, toolResult: "ok"},
		{tool: "bash", hasResult: true, isError: true},
	}
	got := renderToolStrip(es, 100)
	if !strings.Contains(got, stripGlyphTool) {
		t.Errorf("missing neutral ▸ glyph in mixed group: %q", got)
	}
	if !strings.Contains(got, "1 error") {
		t.Errorf("missing error chip: %q", got)
	}
}

// TestRenderToolStrip_NarrowWidthDropsHint exercises the
// composeStripLine fallback: a strip whose left section already eats
// most of contentW must drop the trailing "e expand" hint before it
// sacrifices the metadata. We force the budget squeeze by listing
// multiple distinct tool names AND requesting a tight viewport.
func TestRenderToolStrip_NarrowWidthDropsHint(t *testing.T) {
	es := []transcriptEntry{
		{tool: "very_long_tool_name_a", hasResult: true, toolResult: "ok"},
		{tool: "very_long_tool_name_b", hasResult: true, toolResult: "ok"},
	}
	got := renderToolStrip(es, 38)
	if !strings.Contains(got, "very_long_tool_name_a") {
		t.Errorf("missing first tool name: %q", got)
	}
	if strings.Contains(got, "expand") {
		t.Errorf("hint should have dropped at the squeezed width: %q", got)
	}
}

// TestRenderToolStrip_HasLeftIndent renders a 4-space leading indent
// so the strip aligns with the legacy tool card's left edge.
func TestRenderToolStrip_HasLeftIndent(t *testing.T) {
	es := []transcriptEntry{{tool: "bash", hasResult: true, toolResult: "ok"}}
	got := renderToolStrip(es, 100)
	if !strings.HasPrefix(got, strings.Repeat(" ", stripIndent)) {
		t.Errorf("expected %d-space indent, got: %q", stripIndent, got[:stripIndent+1])
	}
}

// TestRenderToolStrip_SingleLine guards the load-bearing property of
// Concept A: a 6-call group always renders as ONE line, never more.
func TestRenderToolStrip_SingleLine(t *testing.T) {
	es := []transcriptEntry{
		{tool: "read", hasResult: true, toolResult: "a"},
		{tool: "git_status", hasResult: true, toolResult: "b"},
		{tool: "git_log", hasResult: true, toolResult: "c"},
		{tool: "git_show", hasResult: true, toolResult: "d"},
		{tool: "read", hasResult: true, toolResult: "e"},
		{tool: "notes_recent", hasResult: true, toolResult: "f"},
	}
	got := renderToolStrip(es, 120)
	if strings.Contains(got, "\n") {
		t.Errorf("strip must be one line, got newline-bearing output:\n%s", got)
	}
}

// TestRenderToolStrip_SkillNameOverridesPreview is the regression
// check for "the args preview leaks the {\"name\":\"...\"} JSON of a
// skill load". Skills suppress the preview path because the skill
// name IS the meaningful label.
func TestRenderToolStrip_SkillNameOverridesPreview(t *testing.T) {
	e := transcriptEntry{
		tool: "skill_use", isSkill: true, skillName: "calendar",
		toolInput: `{"name":"calendar"}`,
		hasResult: true, toolResult: "skill body",
	}
	got := renderToolStrip([]transcriptEntry{e}, 120)
	if strings.Contains(got, `"name":"calendar"`) {
		t.Errorf("skill strip leaked input JSON preview: %q", got)
	}
}

// TestApplyEvent_TagsSkillUse pins the ingest path: a tool_call with
// name "skill_use" creates a transcriptEntry with isSkill=true and a
// parsed skillName. This is the seam every later renderer relies on.
func TestApplyEvent_TagsSkillUse(t *testing.T) {
	m := newStripTestModel()
	m.applyEvent(makeToolCallEvent(t, "skill_use", `{"name":"calendar"}`))

	e := firstToolEntry(t, m)
	if !e.isSkill {
		t.Errorf("isSkill = false, want true")
	}
	if e.skillName != "calendar" {
		t.Errorf("skillName = %q, want %q", e.skillName, "calendar")
	}
}

// TestApplyEvent_LeavesRegularToolUntagged confirms the inverse: a
// non-skill tool call must NOT carry isSkill.
func TestApplyEvent_LeavesRegularToolUntagged(t *testing.T) {
	m := newStripTestModel()
	m.applyEvent(makeToolCallEvent(t, "bash", `{"cmd":"ls"}`))

	e := firstToolEntry(t, m)
	if e.isSkill {
		t.Errorf("bash tool call wrongly tagged as skill")
	}
}

// TestApplyEvent_TagsSkillUseWithMalformedInputStillFlagged is the
// edge case where the skill_use input JSON failed to parse - we still
// want isSkill=true so the strip renders with 📚 (just with the bare
// tool name fallback).
func TestApplyEvent_TagsSkillUseWithMalformedInputStillFlagged(t *testing.T) {
	m := newStripTestModel()
	m.applyEvent(makeToolCallEvent(t, "skill_use", `not json`))

	e := firstToolEntry(t, m)
	if !e.isSkill {
		t.Errorf("malformed-input skill_use should still flag isSkill")
	}
	if e.skillName != "" {
		t.Errorf("skillName should be empty on parse failure, got %q", e.skillName)
	}
}

// TestTranscriptSeparator_Matrix pins all four cells of the spacing
// matrix in one place so a future refactor of the conversational-
// turn breathing rule has a single failure surface.
func TestTranscriptSeparator_Matrix(t *testing.T) {
	cases := []struct {
		name         string
		priorContent bool
		wantsBlank   bool
		want         string
	}{
		{"first non-turn entry: empty", false, false, ""},
		{"first turn entry: open with blank line", false, true, "\n"},
		{"non-turn after content: single newline", true, false, "\n"},
		{"turn after content: blank-line separator", true, true, "\n\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := transcriptSeparator(c.priorContent, c.wantsBlank)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestWantsLeadingBlankLine pins the categorisation of which entry
// kinds get the extra breathing room. Today: user and assistant
// turns only; everything else chains tightly.
func TestWantsLeadingBlankLine(t *testing.T) {
	yes := []entryKind{entryUserMessage, entryAssistantMessage}
	no := []entryKind{
		entryToolCall, entryToolResult, entrySteering, entryStateChange,
		entrySystemNote, entrySlashEcho, entryResearchProgress,
		entryUserShell, entryError,
	}
	for _, k := range yes {
		if !wantsLeadingBlankLine(k) {
			t.Errorf("kind %v: want true", k)
		}
	}
	for _, k := range no {
		if wantsLeadingBlankLine(k) {
			t.Errorf("kind %v: want false", k)
		}
	}
}

// TestComposeTranscript_FirstUserGetsLeadingBlankLine pins the
// "including the first message of a conversation" rule: a transcript
// that opens with a user turn starts with a single newline so the
// 👤 avatar sits one row down from whatever chrome sits above the
// viewport.
func TestComposeTranscript_FirstUserGetsLeadingBlankLine(t *testing.T) {
	entries := []transcriptEntry{{kind: entryUserMessage, text: "hello"}}
	out := composeTranscript(entries, "", "", nil, nil, 80)
	if !strings.HasPrefix(out, "\n") {
		t.Errorf("expected leading blank line; got %q", out)
	}
}

// TestComposeTranscript_FirstNonTurnSkipsBlankLine guards the
// inverse: a transcript that opens with a tool strip or error card
// (no avatar) does NOT pay the breathing-room cost. Tool work as the
// first thing in a transcript is rare but legal (replay of a
// truncated log) and shouldn't lose the top row to whitespace.
func TestComposeTranscript_FirstNonTurnSkipsBlankLine(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryToolCall, tool: "bash", hasResult: true, toolResult: "ok"},
	}
	out := composeTranscript(entries, "", "", nil, nil, 80)
	if strings.HasPrefix(out, "\n") {
		t.Errorf("non-turn opening shouldn't lead with a blank line; got %q", out[:8])
	}
}

// TestComposeTranscript_TurnAfterToolGetsBlankLine pins the most
// common case: tool strip then assistant reply must have a blank line
// between, so the conversational rhythm reads as exchange rather than
// "the agent kept typing".
func TestComposeTranscript_TurnAfterToolGetsBlankLine(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryToolCall, tool: "bash", hasResult: true, toolResult: "ok"},
		{kind: entryAssistantMessage, text: "done"},
	}
	out := composeTranscript(entries, "", "", nil, nil, 80)
	if !strings.Contains(out, "\n\n") {
		t.Errorf("expected blank line between tool and assistant: %q", out)
	}
}

// TestComposeTranscript_AlternatingTurnsCountGaps pins the spacing
// invariant: each conversational turn gets exactly one blank-line
// gap before it (or a leading blank line if it's the first thing),
// and tool runs between turns chain tightly with single-newline
// separators. A four-entry transcript (user, assistant, tool, tool)
// should therefore expose exactly ONE "\n\n" gap (the one before the
// assistant) plus a leading "\n" for the opening user message.
func TestComposeTranscript_AlternatingTurnsCountGaps(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryUserMessage, text: "hey"},
		{kind: entryAssistantMessage, text: "ok"},
		{kind: entryToolCall, tool: "read", hasResult: true, toolResult: "a"},
		{kind: entryToolCall, tool: "grep", hasResult: true, toolResult: "b"},
	}
	out := composeTranscript(entries, "", "", nil, nil, 80)
	if got := strings.Count(out, "\n\n"); got != 1 {
		t.Errorf("want exactly 1 blank-line gap; got %d in:\n%s", got, out)
	}
	if !strings.HasPrefix(out, "\n") {
		t.Errorf("expected leading blank line: %q", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("triple-newline (double blank line) sneaked in: %q", out)
	}
}

// TestComposeTranscript_ThinkingRowGetsBlankLine pins the thinking-
// pulse case: when the agent is mid-call but hasn't streamed any text
// yet, the thinking row stands in for the assistant turn and gets the
// same blank-line breathing room so the transcript reads as
// "user said X, agent is thinking" rather than "user said X / agent
// is thinking" jammed together.
func TestComposeTranscript_ThinkingRowGetsBlankLine(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryUserMessage, text: "hey"},
	}
	out := composeTranscript(entries, "", "🧢: thinking…", nil, nil, 80)
	if strings.Count(out, "\n\n") < 1 {
		t.Errorf("expected blank line before thinking row: %q", out)
	}
	if !strings.Contains(out, "thinking") {
		t.Errorf("missing thinking row body: %q", out)
	}
}

// TestComposeTranscript_LiveTextGetsBlankLine pins the streaming
// assistant case: live text surfaces a 🧢 avatar so it gets the same
// breathing room as a committed assistant message.
func TestComposeTranscript_LiveTextGetsBlankLine(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryUserMessage, text: "hey"},
	}
	out := composeTranscript(entries, "streaming reply...", "", nil, nil, 80)
	// Two blank-line gaps: one before the user (leading), one between
	// user and live text.
	if strings.Count(out, "\n\n") < 1 {
		t.Errorf("expected at least one blank line before live assistant text: %q", out)
	}
	if !strings.Contains(out, "streaming reply") {
		t.Errorf("missing live text body: %q", out)
	}
}

// TestComposeTranscript_RunOfToolsFoldsToStrip is the integration
// check that composeTranscript still routes consecutive tool calls
// through the strip renderer post-swap.
func TestComposeTranscript_RunOfToolsFoldsToStrip(t *testing.T) {
	entries := []transcriptEntry{
		{kind: entryUserMessage, text: "find every TODO"},
		{kind: entryToolCall, tool: "grep", hasResult: true, toolResult: "x"},
		{kind: entryToolCall, tool: "read", hasResult: true, toolResult: "y"},
		{kind: entryToolCall, tool: "read", hasResult: true, toolResult: "z"},
		{kind: entryAssistantMessage, text: "found 3"},
	}
	out := composeTranscript(entries, "", "", nil, nil, 120)

	if !strings.Contains(out, stripGlyphTool) {
		t.Errorf("missing ▸ leading glyph: %q", out)
	}
	if !strings.Contains(out, "grep") || !strings.Contains(out, "read") {
		t.Errorf("missing tool names: %q", out)
	}
	if !strings.Contains(out, "×2") {
		t.Errorf("missing read ×2 rollup: %q", out)
	}
	if strings.Contains(out, "╭") {
		t.Errorf("transcript should not contain bordered card character: %q", out)
	}
}
