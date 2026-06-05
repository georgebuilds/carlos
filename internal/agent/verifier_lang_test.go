package agent

// Internal tests for verifier_adapters helpers + verifier_lang
// detection. Lives in package agent (not agent_test) so it can reach
// the unexported helpers — the rest of the slice-5d test suite is in
// agent_test where it consumes only the public surface.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectLanguage_Go(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLanguage(dir); got != langGo {
		t.Fatalf("got %s, want %s", got, langGo)
	}
}

func TestDetectLanguage_Rust(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLanguage(dir); got != langRust {
		t.Fatalf("got %s, want %s", got, langRust)
	}
}

func TestDetectLanguage_Python(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLanguage(dir); got != langPython {
		t.Fatalf("got %s, want %s", got, langPython)
	}
}

func TestDetectLanguage_Node(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLanguage(dir); got != langNode {
		t.Fatalf("got %s, want %s", got, langNode)
	}
}

func TestDetectLanguage_PriorityGoOverNode(t *testing.T) {
	// Both markers present → Go wins (compiled-language priority).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLanguage(dir); got != langGo {
		t.Fatalf("got %s, want %s (priority)", got, langGo)
	}
}

func TestDetectLanguage_Unknown(t *testing.T) {
	dir := t.TempDir()
	if got := detectLanguage(dir); got != langUnknown {
		t.Fatalf("got %s, want %s", got, langUnknown)
	}
}

func TestScoreFromRatio_Boundaries(t *testing.T) {
	cases := []struct {
		ratio float64
		want  int
	}{
		{0.0, 1},
		{0.01, 1},  // ~rounds to 1.09 → 1
		{0.05, 1},  // ~rounds to 1.45 → 1
		{0.1, 2},   // rounds to 1.9 → 2
		{0.5, 6},   // 1 + 4.5 = 5.5 → 6
		{0.9, 9},   // 1 + 8.1 = 9.1 → 9
		{0.95, 10}, // 1 + 8.55 = 9.55 → 10
		{1.0, 10},
		{1.5, 10},   // clamp high
		{-0.5, 1},   // clamp low
	}
	for _, c := range cases {
		got := scoreFromRatio(c.ratio)
		if got != c.want {
			t.Errorf("scoreFromRatio(%v) = %d, want %d", c.ratio, got, c.want)
		}
	}
}

func TestDecisionFromRatio_Boundaries(t *testing.T) {
	cases := []struct {
		ratio float64
		want  VerificationDecision
	}{
		{0.0, VerificationReject},
		{0.49, VerificationReject},
		{0.5, VerificationNeedsRevision},
		{0.94, VerificationNeedsRevision},
		{0.95, VerificationAccept},
		{1.0, VerificationAccept},
	}
	for _, c := range cases {
		got := decisionFromRatio(c.ratio)
		if got != c.want {
			t.Errorf("decisionFromRatio(%v) = %s, want %s", c.ratio, got, c.want)
		}
	}
}
