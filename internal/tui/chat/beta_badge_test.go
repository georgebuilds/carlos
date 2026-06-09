package chat

import (
	"strings"
	"testing"
)

// TestRenderBetaBadge_ContainsLabelAndURL pins the load-bearing
// substrings: "BETA" label + the issues URL + the friendly nudge.
// We don't snapshot the exact rendered string since lipgloss colour
// ANSI sequences differ between TERMs; only check content.
func TestRenderBetaBadge_ContainsLabelAndURL(t *testing.T) {
	got := renderBetaBadge()
	for _, want := range []string{
		"BETA",
		"github.com/georgebuilds/carlos/issues",
		"bug",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in rendered badge; got:\n%s", want, got)
		}
	}
}

// TestRenderEmptyState_IncludesBetaBadge confirms the new-chat
// greeting picks up the badge alongside the example prompts.
func TestRenderEmptyState_IncludesBetaBadge(t *testing.T) {
	got := renderEmptyState("Tester", 80, 30, false)
	if !strings.Contains(got, "BETA") {
		t.Errorf("empty-state should include the BETA badge; got:\n%s", got)
	}
	if !strings.Contains(got, "/help for slash commands") {
		t.Errorf("empty-state should still surface the /help hint; got:\n%s", got)
	}
}
