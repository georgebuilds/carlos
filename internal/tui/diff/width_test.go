package diff

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestMain pins the lipgloss default renderer to truecolor so the
// style assertions (containsANSI etc.) work under `go test`, which
// otherwise sees a non-terminal stdout and downgrades to no-color.
// Production callers run inside bubbletea where the profile is set
// from the real terminal — this only affects the test harness.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

// widthForTest is a tiny shim around lipgloss.Width so the diff tests
// don't import lipgloss directly. Kept in a _test.go file so the
// production build doesn't gain a redundant export.
func widthForTest(s string) int { return lipgloss.Width(s) }

func TestClipANSI_passthroughWhenFits(t *testing.T) {
	in := "\x1b[31mhello\x1b[0m"
	if got := clipANSI(in, 10); got != in {
		t.Errorf("clipANSI passthrough = %q, want %q", got, in)
	}
}

func TestClipANSI_trims(t *testing.T) {
	in := "hello world"
	got := clipANSI(in, 5)
	if widthForTest(got) != 5 {
		t.Errorf("clipped width = %d, want 5 (got=%q)", widthForTest(got), got)
	}
}

func TestClipANSI_preservesColorAndAppendsReset(t *testing.T) {
	in := "\x1b[31mhello world\x1b[0m"
	got := clipANSI(in, 5)
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("expected color escape preserved, got %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("expected trailing reset, got %q", got)
	}
}

func TestPadOrClip(t *testing.T) {
	if got := padOrClip("abc", 5); widthForTest(got) != 5 {
		t.Errorf("padOrClip short = width %d, want 5", widthForTest(got))
	}
	if got := padOrClip("abcdef", 4); widthForTest(got) != 4 {
		t.Errorf("padOrClip long = width %d, want 4", widthForTest(got))
	}
}
