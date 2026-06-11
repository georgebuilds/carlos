package termscrub

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestScrub(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// SGR mouse report — with and without leading ESC.
		{"sgr with esc", "\x1b[<64;96;7M", ""},
		{"sgr without esc", "[<64;96;7M", ""},
		{"sgr release lowercase", "[<0;1;1m", ""},

		// X11 mouse report (CSI M + 3 printable bytes).
		{"x11 with esc", "\x1b[M+++", ""}, // ESC [ M + 3 printable bytes
		{"x11 without esc", "[M!!!", ""},  // [ M + 3 printable bytes
		{"x11 embedded", "x[M!!!y", "xy"}, // surrounded by real text

		// DSR cursor-position reply.
		{"dsr with esc", "\x1b[12;48R", ""},
		{"dsr without esc", "[12;48R", ""},

		// Device-attributes reply.
		{"da with esc", "\x1b[?1;2c", ""},
		{"da without esc", "[?62;1;6c", ""},

		// OSC 10/11 color reply — BEL- and ST-terminated.
		{"osc11 bel with esc", "\x1b]11;rgb:0000/0000/0000\a", ""},
		{"osc10 bel without esc", "]10;rgb:ffff/ffff/ffff\a", ""},
		{"osc11 st terminated", "\x1b]11;rgb:1111/2222/3333\x1b\\", ""},

		// Bracketed-paste markers.
		{"paste start with esc", "\x1b[200~", ""},
		{"paste end without esc", "[201~", ""},

		// Leak embedded between real text.
		{"embedded sgr", "ab[<1;2;3Mcd", "abcd"},
		{"embedded dsr esc", "ab\x1b[5;6Rcd", "abcd"},

		// Negatives — must be untouched.
		{"hello world brackets", "hello [world]", "hello [world]"},
		{"array index", "array[0]", "array[0]"},
		{"semicolon text", "value;other", "value;other"},
		{"not a real mouse report", "[<not a real mouse report", "[<not a real mouse report"},
		{"rgb stuff", "rgb stuff", "rgb stuff"},
		{"empty", "", ""},
		{"plain text", "plain text 123", "plain text 123"},
		{"lone osc no body", "]11", "]11"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Scrub(tt.in); got != tt.want {
				t.Errorf("Scrub(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFilterTerminalLeaks(t *testing.T) {
	tests := []struct {
		name string
		in   tea.Msg
		want tea.Msg
	}{
		{
			name: "single-rune KeyRunes passes unchanged",
			in:   tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")},
			want: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")},
		},
		{
			name: "paste with bracket passes unchanged",
			in:   tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<1;2;3M paste"), Paste: true},
			want: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<1;2;3M paste"), Paste: true},
		},
		{
			name: "pure leak burst dropped",
			in:   tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<64;96;7M")},
			want: nil,
		},
		{
			name: "mixed text + leak keeps only real text",
			in:   tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab[<1;2;3Mcd")},
			want: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abcd")},
		},
		{
			name: "multi-rune non-leak passes unchanged",
			in:   tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")},
			want: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")},
		},
		{
			name: "non-KeyMsg passes through",
			in:   tea.WindowSizeMsg{Width: 1, Height: 1},
			want: tea.WindowSizeMsg{Width: 1, Height: 1},
		},
		{
			name: "non-KeyRunes key passes unchanged",
			in:   tea.KeyMsg{Type: tea.KeyEnter},
			want: tea.KeyMsg{Type: tea.KeyEnter},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterTerminalLeaks(nil, tt.in)
			if !msgEqual(got, tt.want) {
				t.Errorf("FilterTerminalLeaks(%#v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// msgEqual compares the tea.Msg values our filter can return. KeyMsg holds a
// []rune slice, so it is not directly comparable with ==; compare field-wise.
func msgEqual(a, b tea.Msg) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch av := a.(type) {
	case tea.KeyMsg:
		bv, ok := b.(tea.KeyMsg)
		if !ok || av.Type != bv.Type || av.Paste != bv.Paste || len(av.Runes) != len(bv.Runes) {
			return false
		}
		for i := range av.Runes {
			if av.Runes[i] != bv.Runes[i] {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
