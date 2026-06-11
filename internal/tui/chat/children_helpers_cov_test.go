package chat

import (
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func TestChildrenViewFunc_Snapshot(t *testing.T) {
	want := []ChildSnapshot{{AgentID: "01ABC"}}
	var f ChildrenView = ChildrenViewFunc(func() []ChildSnapshot { return want })
	got := f.Snapshot()
	if len(got) != 1 || got[0].AgentID != "01ABC" {
		t.Errorf("ChildrenViewFunc.Snapshot passthrough failed: %+v", got)
	}
}

func TestFormatElapsed_AllBuckets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, "0s"},
		{0, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{59 * time.Minute, "59m"},
		{2 * time.Hour, "2h"},
		{25 * time.Hour, "25h"},
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Errorf("formatElapsed(%v) = %q want %q", c.d, got, c.want)
		}
	}
}

func TestFormatCents_Buckets(t *testing.T) {
	cases := []struct {
		cents int
		want  string
	}{
		{-10, "$0.000"},
		{0, "$0.000"},
		{1, "$0.010"},
		{14, "$0.140"},
		{125, "$1.250"},
		{1000, "$10.000"},
	}
	for _, c := range cases {
		if got := formatCents(c.cents); got != c.want {
			t.Errorf("formatCents(%d) = %q want %q", c.cents, got, c.want)
		}
	}
}

func TestShortChildID_ShortAndLong(t *testing.T) {
	if got := shortChildID("ab"); got != "ab" {
		t.Errorf("short id passthrough: %q", got)
	}
	if got := shortChildID("01J0AAAAAAAAAAAAAAAA1234"); got != "1234" {
		t.Errorf("long id tail: %q", got)
	}
	if got := shortChildID("ABCD"); got != "ABCD" {
		t.Errorf("len==4 passthrough should be verbatim: %q", got)
	}
	if got := shortChildID("ABCDE"); got != "bcde" {
		t.Errorf("len>4 should lowercase the tail: %q", got)
	}
}

func TestTruncateCells_Edges(t *testing.T) {
	if got := truncateCells("hello", 0); got != "" {
		t.Errorf("maxW<=0 should be empty: %q", got)
	}
	if got := truncateCells("hello", -3); got != "" {
		t.Errorf("negative maxW should be empty: %q", got)
	}
	if got := truncateCells("hello", 1); got != "…" {
		t.Errorf("maxW==1 should be ellipsis only: %q", got)
	}
	if got := truncateCells("hi", 5); got != "hi" {
		t.Errorf("under-cap passthrough: %q", got)
	}
	if got := truncateCells("hello world", 5); got != "hell…" {
		t.Errorf("truncation: %q", got)
	}
}

func TestIsAgentTypeWord(t *testing.T) {
	cases := map[string]bool{
		"research":         true,
		"verify":           true,
		"a-b_c":            true,
		"agent42":          true,
		"":                 false,
		"UPPER":            false,
		"has space":        false,
		"json{":            false,
		"waytoolongword16": false, // 16 chars
	}
	for in, want := range cases {
		if got := isAgentTypeWord(in); got != want {
			t.Errorf("isAgentTypeWord(%q) = %v want %v", in, got, want)
		}
	}
}

func TestShortAgentType(t *testing.T) {
	if got := shortAgentType("research phase: synth", "id"); got != "research" {
		t.Errorf("agent-type head: %q", got)
	}
	// First token not a type word → fallback "agent".
	if got := shortAgentType("JSON{x} blah", "id"); got != "agent" {
		t.Errorf("non-type head fallback: %q", got)
	}
	if got := shortAgentType("", "id"); got != "agent" {
		t.Errorf("empty fallback: %q", got)
	}
}

func TestChildStateGlyph_AllStates(t *testing.T) {
	cases := map[agent.State]string{
		agent.StateRunning:       "◆",
		agent.StateCompacting:    "◆",
		agent.StateAwaitingInput: "◇",
		agent.StateBlocked:       "◇",
		agent.StatePausedByUser:  "◇",
		agent.StateDone:          "✓",
		agent.StateFailed:        "✗",
		agent.StateOrphaned:      "✗",
		agent.StateSpawning:      "·",
	}
	for st, want := range cases {
		if got := childStateGlyph(st); got != want {
			t.Errorf("childStateGlyph(%v) = %q want %q", st, got, want)
		}
	}
}

func TestChildStateColor_AllBranches(t *testing.T) {
	cases := map[agent.State]string{
		agent.StateAwaitingInput: string(colorWarn),
		agent.StateBlocked:       string(colorWarn),
		agent.StateOrphaned:      string(colorWarn),
		agent.StateFailed:        string(colorWarn),
		agent.StateRunning:       string(colorAgent),
		agent.StateCompacting:    string(colorAgent),
		agent.StateDone:          string(colorOK),
		agent.StateSpawning:      string(colorMuted),
	}
	for st, want := range cases {
		if got := string(childStateColor(st)); got != want {
			t.Errorf("childStateColor(%v) = %q want %q", st, got, want)
		}
	}
}
