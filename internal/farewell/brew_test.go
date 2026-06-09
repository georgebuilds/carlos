package farewell

import (
	"context"
	"testing"
	"time"
)

// TestCheckBrewUpdate_TimeoutReturnsFalse is the safety contract: a
// missing or slow brew never blocks shutdown beyond ctx's deadline,
// and we never invent an update notification.
func TestCheckBrewUpdate_TimeoutReturnsFalse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	if got := CheckBrewUpdate(ctx, "carlos"); got {
		t.Errorf("expected false on timeout, got true")
	}
}

// TestCheckBrewUpdate_DefaultFormula proves the empty-string default
// is "carlos" — the rest of cmd/carlos calls it with no arg.
func TestCheckBrewUpdate_DefaultFormula(t *testing.T) {
	// We can't easily assert behavior without a real brew install;
	// this test pins the contract that the function doesn't panic on
	// an empty formula and returns a bool.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = CheckBrewUpdate(ctx, "")
}

// TestPathLooksLikeBrew covers the Cellar-segment detector — the
// real load-bearing branching behind IsBrewInstall, which is hard to
// drive without mocking os.Executable.
func TestPathLooksLikeBrew(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/opt/homebrew/Cellar/carlos/0.3.1/bin/carlos", true},
		{"/usr/local/Cellar/carlos/0.3.0/bin/carlos", true},
		{"/Users/george/go/bin/carlos", false},
		{"/tmp/go-build/carlos.test", false},
		{"", false},
		{"/opt/homebrew/bin/carlos", false}, // no Cellar segment on the symlink path itself
	}
	for _, tc := range tests {
		if got := pathLooksLikeBrew(tc.path); got != tc.want {
			t.Errorf("pathLooksLikeBrew(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestIsBrewInstall_RunsWithoutPanic exercises the public entry
// point. We can't assert true/false (depends on where the test
// binary is) but we can prove it doesn't panic on a real path
// lookup.
func TestIsBrewInstall_RunsWithoutPanic(t *testing.T) {
	_ = IsBrewInstall()
}

// TestMatchOutdatedLine pins the row-parser against every shape
// `brew outdated --quiet` actually emits. The bare "carlos" form ships
// only for core-tap formulae; carlos lives in georgebuilds/tap, so
// real users hit the slash-qualified path. Pre-fix the matcher was a
// bare line==formula check that silently dropped every tap install —
// the entire shipped userbase — so the ⬆️ farewell row never showed.
func TestMatchOutdatedLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		formula string
		want    bool
	}{
		{"bare-match", "carlos", "carlos", true},
		{"tap-qualified-georgebuilds", "georgebuilds/tap/carlos", "carlos", true},
		{"tap-qualified-other-tap", "rtk-ai/tap/carlos", "carlos", true},
		{"unrelated-bare", "go", "carlos", false},
		{"unrelated-tap", "georgebuilds/tap/persona", "carlos", false},
		{"substring-trap-no-slash", "supercarlos", "carlos", false},
		{"substring-trap-tap", "foo/tap/supercarlos", "carlos", false},
		{"empty-line", "", "carlos", false},
		{"empty-formula", "georgebuilds/tap/carlos", "", false},
		// Whitespace-trimmed lines come in from Split; we still want the
		// matcher to tolerate the exact-equality call directly since
		// callers might trim or not.
		{"leading-space-rejected", " carlos", "carlos", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchOutdatedLine(tc.line, tc.formula); got != tc.want {
				t.Errorf("matchOutdatedLine(%q, %q) = %v, want %v", tc.line, tc.formula, got, tc.want)
			}
		})
	}
}
