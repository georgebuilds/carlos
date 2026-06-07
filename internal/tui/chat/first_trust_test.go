package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/workspace"
)

func newTrustModelInDir(t *testing.T, dir string) *Model {
	t.Helper()
	pol := workspace.NewPolicy(
		workspace.NewStore(filepath.Join(t.TempDir(), "trusted.json")),
		dir,
	)
	m := newFramedModel(t, FrameUI{})
	WithWorkspacePolicy(pol)(m)
	return m
}

func TestShouldOfferFirstTrustPrompt_GitDirTriggers(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTrustModelInDir(t, dir)
	if !m.shouldOfferFirstTrustPrompt() {
		t.Error("git dir should trigger the prompt")
	}
}

func TestShouldOfferFirstTrustPrompt_GoModTriggers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTrustModelInDir(t, dir)
	if !m.shouldOfferFirstTrustPrompt() {
		t.Error("go.mod should trigger the prompt")
	}
}

func TestShouldOfferFirstTrustPrompt_PlainDirSilent(t *testing.T) {
	m := newTrustModelInDir(t, t.TempDir())
	if m.shouldOfferFirstTrustPrompt() {
		t.Error("plain dir should not trigger the prompt")
	}
}

func TestShouldOfferFirstTrustPrompt_TrustedDirSilent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTrustModelInDir(t, dir)
	m.workspace.SetTrusted(true)
	if m.shouldOfferFirstTrustPrompt() {
		t.Error("trusted dir should not re-trigger the prompt")
	}
}

func TestShouldOfferFirstTrustPrompt_NilWorkspaceSilent(t *testing.T) {
	m := newFramedModel(t, FrameUI{})
	if m.shouldOfferFirstTrustPrompt() {
		t.Error("nil workspace should not trigger the prompt")
	}
}

func TestHandleFirstTrustKey_YTrustsAndDismisses(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTrustModelInDir(t, dir)
	m.initFirstTrustPrompt()
	if !m.showFirstTrust {
		t.Fatal("prompt should be open before key")
	}
	if !m.handleFirstTrustKey("y") {
		t.Error("y should be consumed")
	}
	if m.showFirstTrust {
		t.Error("y should close the prompt")
	}
	if !m.firstTrustDismissed {
		t.Error("y should mark dismissed for the session")
	}
	if len(m.queuedCmds) == 0 {
		t.Error("y should queue the trustSlashEnable command")
	}
}

func TestHandleFirstTrustKey_NDismissesWithoutTrusting(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTrustModelInDir(t, dir)
	m.initFirstTrustPrompt()
	if !m.handleFirstTrustKey("n") {
		t.Error("n should be consumed")
	}
	if m.workspace.IsTrusted() {
		t.Error("n should not flip the trust flag")
	}
	if !m.firstTrustDismissed {
		t.Error("n should mark dismissed")
	}
}

func TestHandleFirstTrustKey_EscDismisses(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTrustModelInDir(t, dir)
	m.initFirstTrustPrompt()
	if !m.handleFirstTrustKey("esc") {
		t.Error("esc should be consumed")
	}
	if !m.firstTrustDismissed {
		t.Error("esc should mark dismissed")
	}
}

func TestHandleFirstTrustKey_UnrelatedKeyPassesThrough(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newTrustModelInDir(t, dir)
	m.initFirstTrustPrompt()
	if m.handleFirstTrustKey("a") {
		t.Error("unrelated key should not be consumed")
	}
	if !m.showFirstTrust {
		t.Error("unrelated key should leave the prompt open")
	}
}

func TestRenderFirstTrustPrompt_ContainsExpectedCopy(t *testing.T) {
	out := renderFirstTrustPrompt("/Users/george/Code/carlos", 80)
	for _, want := range []string{"trust this workspace", "/Users/george/Code/carlos", "y", "n", "esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
