package chat

import (
	"strings"
	"testing"
)

// TestRenderEntry_AllKinds drives renderEntry through every transcript
// entry kind so the per-kind branch is exercised and the salient text
// surfaces in the output. The markdown renderer is nil (renderEntry's
// assistant path tolerates nil — it falls back to plain wrapping).
func TestRenderEntry_AllKinds(t *testing.T) {
	cases := []struct {
		name string
		e    transcriptEntry
		want string
	}{
		{"user", transcriptEntry{kind: entryUserMessage, text: "hello boss"}, "hello boss"},
		{"assistant", transcriptEntry{kind: entryAssistantMessage, text: "sure thing"}, "sure thing"},
		{"steering", transcriptEntry{kind: entrySteering, text: "focus on tests"}, "focus on tests"},
		{"stateChange", transcriptEntry{kind: entryStateChange, text: "running"}, "running"},
		{"systemNote", transcriptEntry{kind: entrySystemNote, text: "recovered"}, "recovered"},
		{"slashEcho", transcriptEntry{kind: entrySlashEcho, text: "/help"}, "/help"},
		{"researchProgress", transcriptEntry{kind: entryResearchProgress, text: "synthesizing"}, "synthesizing"},
		{"researchProgressErr", transcriptEntry{kind: entryResearchProgress, text: "failed badly", isError: true}, "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderEntry(c.e, nil, nil, 80)
			if !strings.Contains(out, c.want) {
				t.Errorf("renderEntry(%s) missing %q; got:\n%s", c.name, c.want, out)
			}
		})
	}
}

func TestRenderEntry_LegacyToolResult(t *testing.T) {
	e := transcriptEntry{kind: entryToolResult, text: "line1\nline2"}
	out := renderEntry(e, nil, nil, 80)
	if !strings.Contains(out, "line1") || !strings.Contains(out, "↪") {
		t.Errorf("legacy tool result preview wrong; got:\n%s", out)
	}
}

func TestRenderEntry_LegacyToolResultEmpty(t *testing.T) {
	e := transcriptEntry{kind: entryToolResult, text: ""}
	out := renderEntry(e, nil, nil, 80)
	if !strings.Contains(out, "(empty)") {
		t.Errorf("empty tool result should render (empty); got:\n%s", out)
	}
}

func TestRenderEntry_LegacyToolResultError(t *testing.T) {
	e := transcriptEntry{kind: entryToolResult, text: "boom", isError: true}
	out := renderEntry(e, nil, nil, 80)
	if !strings.Contains(out, "boom") {
		t.Errorf("error tool result should still show body; got:\n%s", out)
	}
}

func TestRenderEntry_ErrorCard(t *testing.T) {
	e := transcriptEntry{kind: entryError, text: "provider exploded"}
	out := renderEntry(e, nil, nil, 80)
	if !strings.Contains(out, "provider exploded") {
		t.Errorf("error card should render the message; got:\n%s", out)
	}
}

func TestRenderAvatarBlock_WrapsAndIndents(t *testing.T) {
	long := strings.Repeat("word ", 40)
	out := renderAvatarBlock("👤", ":", long, colorUser, 40)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("long text should wrap to multiple lines; got %d", len(lines))
	}
	// Continuation lines are indented (start with spaces).
	if !strings.HasPrefix(lines[1], "   ") {
		t.Errorf("continuation line should be indented; got %q", lines[1])
	}
}

func TestRenderAvatarBlock_EmptyBody(t *testing.T) {
	out := renderAvatarBlock("👤", ":", "", colorUser, 40)
	if !strings.Contains(out, "👤") {
		t.Errorf("empty body should still emit the avatar; got %q", out)
	}
}
