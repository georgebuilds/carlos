package manage

import (
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestMain pins the lipgloss default renderer to truecolor so the
// style assertions (reverse-video escape, color-coded border, etc.)
// work under `go test`, which otherwise sees a non-terminal stdout
// and downgrades to no-color — making the redesigned manage view's
// selection inversion invisible to the tests below. Production
// callers run inside bubbletea where the profile is set from the
// real terminal; this only affects the test harness.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}
