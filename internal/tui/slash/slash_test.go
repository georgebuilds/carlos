package slash

import (
	"errors"
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
