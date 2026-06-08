package slash

import (
	"errors"
	"strings"
	"testing"
)

func TestParse_NonSlashReturnsErrNotSlash(t *testing.T) {
	cases := []string{"hello", "what's the weather", "", "  ", "no/leading slash"}
	for _, in := range cases {
		_, err := Parse(in)
		if !errors.Is(err, ErrNotSlash) {
			t.Errorf("Parse(%q): want ErrNotSlash, got %v", in, err)
		}
	}
}

func TestParse_VerbOnly(t *testing.T) {
	c, err := Parse("/clear")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Name != "clear" || c.Args != "" {
		t.Errorf("got %+v, want {Name:clear Args:}", c)
	}
}

func TestParse_VerbWithArgs(t *testing.T) {
	c, err := Parse("/memory how does the supervisor work")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Name != "memory" {
		t.Errorf("Name = %q, want memory", c.Name)
	}
	if c.Args != "how does the supervisor work" {
		t.Errorf("Args = %q", c.Args)
	}
}

func TestParse_CaseInsensitive(t *testing.T) {
	c, _ := Parse("/Clear")
	if c.Name != "clear" {
		t.Errorf("got %q, want clear (case-folded)", c.Name)
	}
}

func TestParse_TrimsWhitespace(t *testing.T) {
	c, _ := Parse("   /help   ")
	if c.Name != "help" {
		t.Errorf("got %q, want help", c.Name)
	}
}

func TestFilter_EmptySlashReturnsAllBuiltins(t *testing.T) {
	matches, verb, inArgs := Filter("/")
	if verb != "" || inArgs {
		t.Errorf("verb=%q inArgs=%v, want empty verb and inArgs=false", verb, inArgs)
	}
	if len(matches) != len(Builtins) {
		t.Errorf("got %d matches, want all %d Builtins", len(matches), len(Builtins))
	}
}

func TestFilter_PrefixMatch(t *testing.T) {
	matches, verb, inArgs := Filter("/fr")
	if verb != "fr" || inArgs {
		t.Errorf("verb=%q inArgs=%v, want verb=fr inArgs=false", verb, inArgs)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one /fr* match")
	}
	for _, m := range matches {
		if !strings.HasPrefix(m.Name, "fr") {
			t.Errorf("match %q does not start with 'fr'", m.Name)
		}
	}
}

func TestFilter_NonSlashReturnsEmpty(t *testing.T) {
	matches, verb, inArgs := Filter("hello")
	if len(matches) != 0 || verb != "" || inArgs {
		t.Errorf("got matches=%v verb=%q inArgs=%v, want empty", matches, verb, inArgs)
	}
}

func TestFilter_VerbPlusSpaceLocksToOneSpec(t *testing.T) {
	matches, verb, inArgs := Filter("/frame ")
	if !inArgs {
		t.Error("inArgs should be true after the space")
	}
	if verb != "frame" {
		t.Errorf("verb=%q, want frame", verb)
	}
	if len(matches) != 1 || matches[0].Name != "frame" {
		t.Errorf("got %v, want a single /frame match", matches)
	}
}

func TestFilter_UnknownVerbInArgsModeReturnsNilMatches(t *testing.T) {
	matches, verb, inArgs := Filter("/notreal foo")
	if !inArgs {
		t.Error("inArgs should be true")
	}
	if verb != "notreal" {
		t.Errorf("verb=%q", verb)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches for unknown verb, got %v", matches)
	}
}

func TestGhost_VerbCompletion(t *testing.T) {
	spec, _ := Lookup("frame")
	got := Ghost("/fr", spec)
	if got != "ame" {
		t.Errorf("Ghost(/fr, /frame) = %q, want %q", got, "ame")
	}
}

func TestGhost_VerbExactNoSpaceShowsArgsHint(t *testing.T) {
	spec, _ := Lookup("frame")
	got := Ghost("/frame", spec)
	want := " " + spec.ArgsHint
	if got != want {
		t.Errorf("Ghost(/frame, /frame) = %q, want %q", got, want)
	}
}

func TestGhost_InArgsRendersArgsHint(t *testing.T) {
	spec, _ := Lookup("frame")
	got := Ghost("/frame ", spec)
	if got != spec.ArgsHint {
		t.Errorf("Ghost(/frame ⎵, /frame) = %q, want %q", got, spec.ArgsHint)
	}
}

func TestGhost_InArgsWithUserTextReturnsEmpty(t *testing.T) {
	spec, _ := Lookup("frame")
	got := Ghost("/frame list", spec)
	if got != "" {
		t.Errorf("Ghost should be empty once user has started typing args; got %q", got)
	}
}

func TestGhost_NoArgsHintNoGhost(t *testing.T) {
	spec, _ := Lookup("clear") // /clear has no args
	if got := Ghost("/clear", spec); got != "" {
		t.Errorf("Ghost for an args-less command should be empty; got %q", got)
	}
}

func TestLookup_KnownAndUnknown(t *testing.T) {
	if _, ok := Lookup("clear"); !ok {
		t.Error("/clear should be a built-in")
	}
	if _, ok := Lookup("CLEAR"); !ok {
		t.Error("Lookup should be case-insensitive")
	}
	if _, ok := Lookup("insights"); !ok {
		t.Error("/insights should be a built-in (carlos-specific)")
	}
	if _, ok := Lookup("does-not-exist"); ok {
		t.Error("unknown verb should return false")
	}
}
