package telegram_test

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/gateway/telegram"
)

func TestEscapeMarkdownV2_AllReservedChars(t *testing.T) {
	// Every reserved char from the Bot API spec; each must end up
	// backslash-prefixed.
	reserved := []rune{
		'_', '*', '[', ']', '(', ')', '~', '`', '>',
		'#', '+', '-', '=', '|', '{', '}', '.', '!', '\\',
	}
	for _, r := range reserved {
		in := string(r)
		got := telegram.EscapeMarkdownV2(in)
		want := "\\" + in
		if got != want {
			t.Errorf("escape %q: want %q got %q", in, want, got)
		}
	}
}

func TestEscapeMarkdownV2_PassesThroughSafeText(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"hello world",
		"こんにちは",
		"emoji 🎉 inside",
	}
	for _, in := range cases {
		got := telegram.EscapeMarkdownV2(in)
		if got != in {
			t.Errorf("safe text %q: want unchanged got %q", in, got)
		}
	}
}

func TestEscapeMarkdownV2_Mixed(t *testing.T) {
	in := "carlos here. tap *Approve* to proceed!"
	got := telegram.EscapeMarkdownV2(in)
	// Expect every . * ! to be escaped; the spaces and letters pass.
	want := "carlos here\\. tap \\*Approve\\* to proceed\\!"
	if got != want {
		t.Errorf("mixed: want %q got %q", want, got)
	}
}

func TestEscapeMarkdownV2_DoesNotDoubleEscape(t *testing.T) {
	// Input already contains a literal backslash; we should escape
	// the backslash (so the API sees \\\\, which renders as a single
	// backslash to the user).
	in := `path\to\file`
	got := telegram.EscapeMarkdownV2(in)
	want := `path\\to\\file`
	if got != want {
		t.Errorf("backslash escape: want %q got %q", want, got)
	}
}

func TestEscapeMarkdownV2_LongString(t *testing.T) {
	// 1000 periods should produce 2000 chars (period + backslash each).
	in := strings.Repeat(".", 1000)
	got := telegram.EscapeMarkdownV2(in)
	if len(got) != 2000 {
		t.Errorf("long escape: want 2000 bytes got %d", len(got))
	}
}
