package research

// Whitebox tests for unexported helpers and unreachable-by-engine
// branches. Targeted at the formatPassageManifest passage-without-URL
// branch and the runSynthesize empty-passages defensive guard.

import (
	"strings"
	"testing"
	"time"
)

func TestFormatPassageManifest_OrphanPassageGetsFallbackLabel(t *testing.T) {
	passages := []Passage{
		{ID: "p1", SourceID: "ghost", Text: "orphaned"},
		{ID: "p2", SourceID: "s1", Text: "linked"},
	}
	sources := []Source{
		{ID: "s1", URL: "https://known.example.com", Title: "Known"},
	}
	got := formatPassageManifest(passages, sources)
	if !strings.Contains(got, "(source URL unavailable)") {
		t.Errorf("expected fallback label for orphaned passage; got %q", got)
	}
	if !strings.Contains(got, "https://known.example.com") {
		t.Errorf("expected linked passage URL; got %q", got)
	}
}

func TestParseReadJSONL_DropsCodeFenceLines(t *testing.T) {
	// Fenced lines themselves get dropped; JSON between them parses.
	body := "```\n{\"text\":\"good\",\"relevance\":8}\n```"
	got := parseReadJSONL(body, "s1")
	if len(got) != 1 {
		t.Fatalf("want 1 passage, got %d: %+v", len(got), got)
	}
	if got[0].Text != "good" {
		t.Errorf("text = %q want good", got[0].Text)
	}
}

func TestParseReadJSONL_ClampsRelevance(t *testing.T) {
	body := `{"text":"low","relevance":-2}
{"text":"high","relevance":99}
{"text":"normal","relevance":5}
`
	got := parseReadJSONL(body, "s1")
	if len(got) != 3 {
		t.Fatalf("want 3 passages, got %d", len(got))
	}
	if got[0].Relevance != 1 {
		t.Errorf("low got %d want 1 (clamp)", got[0].Relevance)
	}
	if got[1].Relevance != 10 {
		t.Errorf("high got %d want 10 (clamp)", got[1].Relevance)
	}
}

func TestParseDecomposeLines_DedupesCaseInsensitive(t *testing.T) {
	body := "Foo\nfoo\nBAR\n"
	got := parseDecomposeLines(body, 5)
	if len(got) != 2 {
		t.Errorf("want 2 deduped, got %v", got)
	}
}

func TestParseDecomposeLines_RespectsMax(t *testing.T) {
	body := "a\nb\nc\nd\ne\n"
	got := parseDecomposeLines(body, 3)
	if len(got) != 3 {
		t.Errorf("want 3 capped, got %v", got)
	}
}

func TestStripBulletPrefix_HandlesAllShapes(t *testing.T) {
	cases := map[string]string{
		"- foo":   "foo",
		"* foo":   "foo",
		"• foo":   "foo",
		"– foo":   "foo",
		"1. foo":  "foo",
		"12. foo": "foo",
		"3) foo":  "foo",
		"foo":     "foo", // already clean
		"":        "",
		"123":     "123", // digits without trailing . or )
	}
	for in, want := range cases {
		if got := stripBulletPrefix(in); got != want {
			t.Errorf("stripBulletPrefix(%q) = %q want %q", in, got, want)
		}
	}
}

func TestTruncateForRead_Short(t *testing.T) {
	in := "short body"
	if got := truncateForRead(in); got != in {
		t.Errorf("short body should pass through, got %q", got)
	}
}

func TestPickTopResults_Short(t *testing.T) {
	// Already covered by engine tests, but the explicit "len <= n"
	// branch here keeps the unit signal clear.
	res := pickTopResults(nil, 5)
	if len(res) != 0 {
		t.Errorf("nil input should yield empty, got %v", res)
	}
}

// runSynthesize directly with empty Passages hits the defensive
// "no passages to synthesize from" guard that Engine.Run won't reach
// because the read phase fails first.
func TestRunSynthesize_DirectEmptyPassages(t *testing.T) {
	e := &Engine{}
	report := &Report{Question: "q?"}
	err := e.runSynthesize(nil, report)
	if err == nil {
		t.Error("expected error on empty passages")
	}
}

// runVerify directly with empty synthesis hits the same defensive
// guard.
func TestRunVerify_DirectEmptySynthesis(t *testing.T) {
	e := &Engine{}
	report := &Report{Question: "q?"}
	err := e.runVerify(nil, report)
	if err == nil {
		t.Error("expected error on empty synthesis")
	}
}

// runRead directly with empty Sources hits its defensive guard.
func TestRunRead_DirectEmptySources(t *testing.T) {
	e := &Engine{}
	report := &Report{Question: "q?"}
	err := e.runRead(nil, report)
	if err == nil {
		t.Error("expected error on empty sources")
	}
}

// runSearch directly with empty Sub hits its defensive guard.
func TestRunSearch_DirectEmptySub(t *testing.T) {
	e := &Engine{}
	report := &Report{Question: "q?"}
	err := e.runSearch(nil, report)
	if err == nil {
		t.Error("expected error on empty sub-queries")
	}
}

// runFetch directly with empty Sources hits its defensive guard.
func TestRunFetch_DirectEmptySources(t *testing.T) {
	e := &Engine{}
	report := &Report{Question: "q?"}
	err := e.runFetch(nil, report)
	if err == nil {
		t.Error("expected error on empty sources")
	}
}

func TestNewResearchAgentID_HandlesSameNanosecond(t *testing.T) {
	// Two calls inside the same nanosecond should still yield distinct
	// IDs because the helper bumps the suffix monotonically.
	t0 := timeNowZero()
	a := newResearchAgentID(t0)
	b := newResearchAgentID(t0)
	if a == b {
		t.Errorf("collision on same nanosecond: %q == %q", a, b)
	}
}

// timeNowZero returns a fixed UTC anchor so the bump branch is
// deterministic regardless of what wall-clock fed earlier tests.
func timeNowZero() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}
