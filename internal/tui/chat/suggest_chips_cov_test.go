package chat

import (
	"strings"
	"testing"
)

func TestRenderSlashArgChips_EmptyReturnsNothing(t *testing.T) {
	s := slashSuggest{}
	if got := renderSlashArgChips(s, 80); got != "" {
		t.Errorf("empty argMatches should render nothing; got %q", got)
	}
}

func TestRenderSlashArgChips_RendersAllWhenFits(t *testing.T) {
	s := slashSuggest{
		inArgs:     true,
		argMatches: []string{"alpha", "beta", "gamma"},
		argCursor:  1,
	}
	out := renderSlashArgChips(s, 120)
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(out, want) {
			t.Errorf("chip %q missing from wide render: %q", want, out)
		}
	}
}

func TestRenderSlashArgChips_OverflowTail(t *testing.T) {
	// Many long chips in a narrow box must emit a "+N" overflow marker.
	matches := []string{
		"openrouter:google/gemini-3.5-flash",
		"anthropic:claude-opus-4-7",
		"openai:gpt-5-mini",
		"openrouter:meta/llama-4-70b",
	}
	s := slashSuggest{inArgs: true, argMatches: matches, argCursor: 0}
	out := renderSlashArgChips(s, 30)
	if !strings.Contains(out, "+") {
		t.Errorf("narrow render should show overflow marker; got %q", out)
	}
}

func TestRenderSlashArgChips_LeftSlideWindow(t *testing.T) {
	// Cursor deep in the list slides the window so a "+N · " left
	// marker prefixes the visible chips.
	matches := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	s := slashSuggest{inArgs: true, argMatches: matches, argCursor: 6}
	out := renderSlashArgChips(s, 60)
	if !strings.Contains(out, "+") {
		t.Errorf("deep cursor should produce a left slide marker; got %q", out)
	}
}

// suggestModel builds a model with the slash suggest band already open
// so handleSlashSuggestKey navigates real matches.
func suggestModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	m.ta.SetValue("/")
	m.slashSuggest.refresh("/", nil)
	m.slashSuggest.open = true
	return m
}

func TestHandleSlashSuggestKey_NotOpenIgnored(t *testing.T) {
	m := newTestModel(t)
	if _, handled := m.handleSlashSuggestKey("tab"); handled {
		t.Error("closed suggest band should not handle keys")
	}
}

func TestHandleSlashSuggestKey_ReadOnlyIgnored(t *testing.T) {
	m := suggestModel(t)
	m.readOnly = true
	if _, handled := m.handleSlashSuggestKey("tab"); handled {
		t.Error("read-only mode should not handle suggest keys")
	}
}

func TestHandleSlashSuggestKey_EscDismisses(t *testing.T) {
	m := suggestModel(t)
	_, handled := m.handleSlashSuggestKey("esc")
	if !handled {
		t.Fatal("esc should be handled")
	}
	if m.slashSuggest.open {
		t.Error("esc should dismiss the suggest band")
	}
}

func TestHandleSlashSuggestKey_DownUpNavigate(t *testing.T) {
	m := suggestModel(t)
	if len(m.slashSuggest.matches) < 2 {
		t.Skip("need at least two builtins to navigate")
	}
	before := m.slashSuggest.cursor
	_, handled := m.handleSlashSuggestKey("down")
	if !handled {
		t.Fatal("down should navigate when >1 match")
	}
	if m.slashSuggest.cursor == before {
		t.Error("down should move the cursor")
	}
	_, handled = m.handleSlashSuggestKey("up")
	if !handled {
		t.Error("up should navigate when >1 match")
	}
}

func TestHandleSlashSuggestKey_TabCompletesVerb(t *testing.T) {
	m := newTestModel(t)
	m.ta.SetValue("/per") // prefix of /permissions
	m.slashSuggest.refresh("/per", nil)
	m.slashSuggest.open = true
	_, handled := m.handleSlashSuggestKey("tab")
	if !handled {
		t.Fatal("tab should be handled")
	}
	if !strings.HasPrefix(m.ta.Value(), "/permissions") {
		t.Errorf("tab should complete to the verb; got %q", m.ta.Value())
	}
}

func TestHandleSlashSuggestKey_ArgTabCompletes(t *testing.T) {
	m := newTestModel(t)
	m.frame.ModelCompletions = func(partial string) []string {
		return []string{"openrouter:google/gemini-3.5-flash"}
	}
	m.ta.SetValue("/model open")
	m.slashSuggest.refresh("/model open", m.argCompleterFn())
	m.slashSuggest.open = true
	if len(m.slashSuggest.argMatches) == 0 {
		t.Fatal("precondition: arg matches should be populated")
	}
	_, handled := m.handleSlashSuggestKey("tab")
	if !handled {
		t.Fatal("tab in args mode should be handled")
	}
	if !strings.Contains(m.ta.Value(), "gemini") {
		t.Errorf("arg tab should complete to the focused suggestion; got %q", m.ta.Value())
	}
}

func TestHandleSlashSuggestKey_ArgUpDownNavigate(t *testing.T) {
	m := newTestModel(t)
	m.frame.ModelCompletions = func(partial string) []string {
		return []string{"openrouter:a", "openrouter:b", "openrouter:c"}
	}
	m.ta.SetValue("/model open")
	m.slashSuggest.refresh("/model open", m.argCompleterFn())
	m.slashSuggest.open = true
	before := m.slashSuggest.argCursor
	_, handled := m.handleSlashSuggestKey("down")
	if !handled {
		t.Fatal("down should navigate arg matches")
	}
	if m.slashSuggest.argCursor == before {
		t.Error("down should move the arg cursor")
	}
	_, handled = m.handleSlashSuggestKey("up")
	if !handled {
		t.Error("up should navigate arg matches")
	}
}
