package todo

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTaskLine_Plain(t *testing.T) {
	got, ok := parseTaskLine("- [ ] buy milk")
	if !ok {
		t.Fatal("expected a task line")
	}
	if got.Done {
		t.Error("should not be done")
	}
	if got.Text != "buy milk" {
		t.Errorf("text = %q", got.Text)
	}
	if got.Marker != "-" {
		t.Errorf("marker = %q", got.Marker)
	}
}

func TestParseTaskLine_Done(t *testing.T) {
	for _, in := range []string{"- [x] done", "- [X] done"} {
		got, ok := parseTaskLine(in)
		if !ok || !got.Done {
			t.Errorf("%q: want done task, got ok=%v done=%v", in, ok, got.Done)
		}
	}
}

func TestParseTaskLine_AllMarkers(t *testing.T) {
	for _, marker := range []string{"-", "*", "+"} {
		got, ok := parseTaskLine(marker + " [ ] x")
		if !ok {
			t.Fatalf("marker %q not recognised", marker)
		}
		if got.Marker != marker {
			t.Errorf("marker = %q want %q", got.Marker, marker)
		}
	}
}

func TestParseTaskLine_Indent(t *testing.T) {
	got, ok := parseTaskLine("    - [ ] nested")
	if !ok {
		t.Fatal("expected task")
	}
	if got.Indent != "    " {
		t.Errorf("indent = %q", got.Indent)
	}
}

func TestParseTaskLine_DueTagsAndID(t *testing.T) {
	got, ok := parseTaskLine("- [ ] ship release 📅 2026-06-25 #work #urgent ^todo-ab12cd")
	if !ok {
		t.Fatal("expected task")
	}
	if got.Text != "ship release" {
		t.Errorf("text = %q (due/tags/id should be stripped)", got.Text)
	}
	if got.Due != "2026-06-25" {
		t.Errorf("due = %q", got.Due)
	}
	if !reflect.DeepEqual(got.Tags, []string{"work", "urgent"}) {
		t.Errorf("tags = %v", got.Tags)
	}
	if got.ID != "todo-ab12cd" {
		t.Errorf("id = %q", got.ID)
	}
}

// Regression: a mid-word caret in prose must NOT be mistaken for a block ref
// and stripped. Before the boundary fix, "use a^b" lost "^b" on round trip.
func TestParseTaskLine_EmbeddedCaretNotABlockRef(t *testing.T) {
	got, ok := parseTaskLine("- [ ] handle a^b in the parser ^todo-1")
	if !ok {
		t.Fatal("expected task")
	}
	if got.ID != "todo-1" {
		t.Errorf("real block ref should still parse: id=%q", got.ID)
	}
	if got.Text != "handle a^b in the parser" {
		t.Errorf("embedded caret was mangled: %q", got.Text)
	}
	// And with no real block ref, the embedded caret survives a round trip.
	got2, _ := parseTaskLine("- [ ] React^18 upgrade ^todo-2")
	if got2.Text != "React^18 upgrade" {
		t.Errorf("embedded caret stripped: %q", got2.Text)
	}
	out := got2.render()
	if !strings.Contains(out, "React^18 upgrade") {
		t.Errorf("round trip lost embedded caret: %q", out)
	}
}

func TestParseTaskLine_NotATask(t *testing.T) {
	for _, in := range []string{
		"",
		"plain prose",
		"# heading",
		"- bullet without checkbox",
		"-[ ] missing space",
		"  not a task either",
	} {
		if _, ok := parseTaskLine(in); ok {
			t.Errorf("%q should not parse as a task", in)
		}
	}
}

func TestParseTaskLine_CRLF(t *testing.T) {
	got, ok := parseTaskLine("- [ ] windows line\r")
	if !ok {
		t.Fatal("expected task")
	}
	if got.Text != "windows line" {
		t.Errorf("text = %q (CR should be trimmed)", got.Text)
	}
}

func TestRenderRoundTrip(t *testing.T) {
	in := "- [x] ship release 📅 2026-06-25 #work #urgent ^todo-ab12cd"
	parsed, ok := parseTaskLine(in)
	if !ok {
		t.Fatal("parse failed")
	}
	out := parsed.render()
	if out != in {
		t.Errorf("round trip changed line:\n in  = %q\n out = %q", in, out)
	}
}

func TestRender_NoExtras(t *testing.T) {
	tl := taskLine{Marker: "-", Text: "simple"}
	if got := tl.render(); got != "- [ ] simple" {
		t.Errorf("render = %q", got)
	}
}

func TestNewTaskLine(t *testing.T) {
	tl := newTaskLine(Draft{Text: "  do thing  ", Due: "2026-07-01", Tags: []string{"#home", " ", "errand"}}, "todo-xyz")
	if tl.Text != "do thing" {
		t.Errorf("text = %q", tl.Text)
	}
	if tl.Done {
		t.Error("new task should be open")
	}
	if !reflect.DeepEqual(tl.Tags, []string{"home", "errand"}) {
		t.Errorf("tags = %v (should strip # and blanks)", tl.Tags)
	}
	rendered := tl.render()
	if !strings.Contains(rendered, "^todo-xyz") {
		t.Errorf("rendered line missing id: %q", rendered)
	}
}

func TestApplyPatch_Partial(t *testing.T) {
	tl, _ := parseTaskLine("- [ ] original 📅 2026-01-01 #a ^todo-1")
	done := true
	tl.applyPatch(Patch{Done: &done})
	if !tl.Done {
		t.Error("done not applied")
	}
	if tl.Text != "original" || tl.Due != "2026-01-01" || !reflect.DeepEqual(tl.Tags, []string{"a"}) {
		t.Errorf("untouched fields changed: %+v", tl)
	}

	newText := "rewritten"
	newTags := []string{"b", "c"}
	tl.applyPatch(Patch{Text: &newText, Tags: &newTags})
	if tl.Text != "rewritten" {
		t.Errorf("text = %q", tl.Text)
	}
	if !reflect.DeepEqual(tl.Tags, []string{"b", "c"}) {
		t.Errorf("tags = %v", tl.Tags)
	}
	if tl.ID != "todo-1" {
		t.Errorf("id should survive patch: %q", tl.ID)
	}
}

func TestNewID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if !strings.HasPrefix(id, "todo-") {
			t.Fatalf("id missing prefix: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id minted: %q", id)
		}
		seen[id] = true
	}
}

func TestParseDue(t *testing.T) {
	if _, ok := ParseDue(""); ok {
		t.Error("empty string should not parse")
	}
	if _, ok := ParseDue("not-a-date"); ok {
		t.Error("garbage should not parse")
	}
	d, ok := ParseDue("2026-06-25")
	if !ok {
		t.Fatal("valid date should parse")
	}
	if d.Year() != 2026 || d.Month() != 6 || d.Day() != 25 {
		t.Errorf("parsed wrong: %v", d)
	}
}

func TestFilterMatch(t *testing.T) {
	cases := []struct {
		f    Filter
		done bool
		want bool
	}{
		{FilterOpen, false, true},
		{FilterOpen, true, false},
		{FilterDone, true, true},
		{FilterDone, false, false},
		{FilterAll, true, true},
		{FilterAll, false, true},
		{Filter("garbage"), false, true}, // unknown == open
	}
	for _, c := range cases {
		if got := c.f.match(c.done); got != c.want {
			t.Errorf("%s.match(%v) = %v want %v", c.f, c.done, got, c.want)
		}
	}
}
