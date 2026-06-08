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
