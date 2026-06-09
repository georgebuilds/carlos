package chat

import (
	"reflect"
	"testing"
)

func TestExtractArgFragment(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"hello", ""},
		{"/model", ""},
		{"/model ", ""},
		{"/model openrouter:", "openrouter:"},
		{"/model openrouter:gpt-5", "openrouter:gpt-5"},
		{"  /model   typed-thing", "  typed-thing"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := extractArgFragment(tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestSlashSuggestRefresh_ArgsMode_PopulatesArgMatches verifies the
// /model autocomplete flow end-to-end: typing past the verb triggers
// the argFn, results land in argMatches, the cursor starts at 0.
func TestSlashSuggestRefresh_ArgsMode_PopulatesArgMatches(t *testing.T) {
	argFn := func(verb, partial string) []string {
		if verb == "model" {
			return []string{"anthropic:", "openai:", "openrouter:"}
		}
		return nil
	}
	var s slashSuggest
	s.refresh("/model ", argFn)
	if !s.inArgs {
		t.Error("expected inArgs=true after verb+space")
	}
	if len(s.argMatches) != 3 {
		t.Errorf("expected 3 arg matches; got %d", len(s.argMatches))
	}
	if s.argCursor != 0 {
		t.Errorf("expected argCursor=0; got %d", s.argCursor)
	}
}

// TestSlashSuggestRefresh_ArgsMode_NilFnLeavesArgMatchesEmpty exercises
// the fallback: argFn=nil disables arg completion entirely.
func TestSlashSuggestRefresh_ArgsMode_NilFnLeavesArgMatchesEmpty(t *testing.T) {
	var s slashSuggest
	s.refresh("/model openrouter:", nil)
	if !s.inArgs {
		t.Error("expected inArgs=true")
	}
	if len(s.argMatches) != 0 {
		t.Errorf("nil argFn should leave argMatches empty; got %v", s.argMatches)
	}
}

// TestSlashSuggestArgCursorNav walks up/down past the bounds to
// confirm wrap-around.
func TestSlashSuggestArgCursorNav(t *testing.T) {
	s := slashSuggest{argMatches: []string{"a", "b", "c"}, inArgs: true}
	s.argCursorDown() // 0→1
	s.argCursorDown() // 1→2
	s.argCursorDown() // wraps to 0
	if s.argCursor != 0 {
		t.Errorf("expected wrap to 0; got %d", s.argCursor)
	}
	s.argCursorUp() // wraps to 2
	if s.argCursor != 2 {
		t.Errorf("expected wrap to 2; got %d", s.argCursor)
	}
}

func TestSlashSuggestArgCursorNav_EmptyIsNoOp(t *testing.T) {
	s := slashSuggest{}
	s.argCursorUp()
	s.argCursorDown()
	if s.argCursor != 0 {
		t.Errorf("empty argMatches should leave cursor=0; got %d", s.argCursor)
	}
}

// TestSlashSuggestArgCompletion produces the textarea replacement
// string: "/<verb> <arg>".
func TestSlashSuggestArgCompletion(t *testing.T) {
	s := slashSuggest{
		open:       true,
		inArgs:     true,
		verb:       "model",
		argMatches: []string{"openrouter:google/gemini-3.5-flash", "anthropic:claude-opus-4-7"},
		argCursor:  1,
	}
	got := s.argCompletion()
	want := "/model anthropic:claude-opus-4-7"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSlashSuggestArgCompletion_NoSelection(t *testing.T) {
	s := slashSuggest{open: true, inArgs: true, verb: "model"}
	if got := s.argCompletion(); got != "" {
		t.Errorf("no arg matches should yield empty; got %q", got)
	}
}

// TestSlashSuggestRefresh_PreservesArgCursorOnRefresh — when the
// user types more and the list still contains the previously-
// focused entry, the cursor should land on it again.
func TestSlashSuggestRefresh_PreservesArgCursorOnRefresh(t *testing.T) {
	argFn := func(verb, partial string) []string {
		// Return the same two entries regardless of typed fragment —
		// what we're testing is "preserve cursor across refreshes",
		// not the filter logic.
		return []string{"openrouter:gpt-5", "openrouter:claude-opus-4-7"}
	}
	var s slashSuggest
	s.refresh("/model openrouter:", argFn)
	// Move cursor to the second entry
	s.argCursorDown()
	if s.argCursor != 1 {
		t.Fatalf("setup expected cursor=1; got %d", s.argCursor)
	}
	// Type another character
	s.refresh("/model openrouter:c", argFn)
	if !reflect.DeepEqual(s.argMatches, []string{"openrouter:gpt-5", "openrouter:claude-opus-4-7"}) {
		t.Fatalf("unexpected matches after refresh: %v", s.argMatches)
	}
	if s.argCursor != 1 {
		t.Errorf("cursor should stick to claude-opus-4-7; got %d", s.argCursor)
	}
}

// TestSelectedArg covers both branches: hit and miss.
func TestSelectedArg(t *testing.T) {
	s := slashSuggest{inArgs: true, argMatches: []string{"a", "b"}, argCursor: 1}
	got, ok := s.selectedArg()
	if !ok || got != "b" {
		t.Errorf("got (%q, %v); want (b, true)", got, ok)
	}
	empty := slashSuggest{inArgs: true}
	if _, ok := empty.selectedArg(); ok {
		t.Error("empty matches should report not-ok")
	}
}

// TestArgCompleterFn_Dispatch pins the per-verb dispatcher: a nil
// FrameUI.ModelCompletions disables the helper entirely; otherwise
// "model" routes to the configured completer, and unknown verbs
// return nil so the suggest band falls back to the static hint.
func TestArgCompleterFn_Dispatch(t *testing.T) {
	// Nil wiring → nil function.
	bareModel := &Model{}
	if fn := bareModel.argCompleterFn(); fn != nil {
		t.Error("nil ModelCompletions should yield nil fn")
	}
	// Wired but only "model" routes.
	m := &Model{
		frame: FrameUI{
			ModelCompletions: func(partial string) []string {
				return []string{"openrouter:" + partial}
			},
		},
	}
	fn := m.argCompleterFn()
	if fn == nil {
		t.Fatal("wired completer should yield non-nil fn")
	}
	if got := fn("model", "gpt-5"); len(got) != 1 || got[0] != "openrouter:gpt-5" {
		t.Errorf("model dispatch: got %v", got)
	}
	if got := fn("frame", "anything"); got != nil {
		t.Errorf("non-model verb should return nil; got %v", got)
	}
}
