package agent_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// goOrSkip skips when the Go toolchain isn't on PATH. Mirrors the
// gitOrSkip pattern in internal/sandbox/worktree_test.go.
func goOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
}

// writeGoModule scaffolds a minimal Go module in dir with the given
// main.go body. Returns the module dir.
func writeGoModule(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return dir
}

func TestCompilerVerifier_GoBuildsClean(t *testing.T) {
	goOrSkip(t)
	dir := writeGoModule(t, "package main\n\nfunc main() {}\n")

	v := agent.NewCompilerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept, got %s; raw=%s", report.Decision, report.Raw)
	}
	if report.Score != 10 {
		t.Fatalf("expected score 10, got %d", report.Score)
	}
	if report.JudgeModel != "compiler:go" {
		t.Fatalf("expected JudgeModel=compiler:go, got %q", report.JudgeModel)
	}
}

func TestCompilerVerifier_GoSyntaxErrorRejects(t *testing.T) {
	goOrSkip(t)
	dir := writeGoModule(t, "package main\n\nfunc main() { this is not go code\n")

	v := agent.NewCompilerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject, got %s; raw=%s", report.Decision, report.Raw)
	}
	if report.Score != 1 {
		t.Fatalf("expected score 1, got %d", report.Score)
	}
	if len(report.Concerns) == 0 {
		t.Fatalf("expected at least one concern; raw=%s", report.Raw)
	}
}

func TestCompilerVerifier_UnknownLanguage(t *testing.T) {
	dir := t.TempDir() // no markers
	v := agent.NewCompilerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if !errors.Is(err, agent.ErrCompilerUnknownLanguage) {
		t.Fatalf("expected ErrCompilerUnknownLanguage, got %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject decision on unknown lang, got %s", report.Decision)
	}
	if report.JudgeModel != "compiler:unknown" {
		t.Fatalf("expected compiler:unknown, got %q", report.JudgeModel)
	}
}

func TestCompilerVerifier_EmptyWorkdir(t *testing.T) {
	v := agent.NewCompilerVerifier()
	if _, err := v.Verify(context.Background(), "", nil); err == nil {
		t.Fatal("expected error on empty workdir")
	}
}

func TestCompilerVerifier_MissingWorkdir(t *testing.T) {
	v := agent.NewCompilerVerifier()
	if _, err := v.Verify(context.Background(), "/no/such/path/exists/here/__x__", nil); err == nil {
		t.Fatal("expected error on missing workdir")
	}
}

func TestCompilerVerifier_NodeNoBuildScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	v := agent.NewCompilerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if !errors.Is(err, agent.ErrCompilerNoToolchain) {
		t.Fatalf("expected ErrCompilerNoToolchain, got %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject decision, got %s", report.Decision)
	}
	if !strings.Contains(report.Concerns[0], "no canonical build command") {
		t.Fatalf("expected 'no canonical build command' concern, got %v", report.Concerns)
	}
}

func TestCompilerVerifier_Timeout(t *testing.T) {
	goOrSkip(t)
	// A go build that pulls in nothing should complete in well under
	// a microsecond timeout — except for process spawn, which is what
	// we exploit here. A 1ns timeout is effectively "kill immediately".
	dir := writeGoModule(t, "package main\n\nfunc main() {}\n")
	v := agent.NewCompilerVerifier()
	v.Timeout = 1 * time.Nanosecond

	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject on timeout, got %s", report.Decision)
	}
	// Either we parsed a "timed out" concern (preferred) or the
	// child died before producing output and we fell back to a
	// generic exit concern. Both are acceptable evidence that
	// timeout handling fired.
	if len(report.Concerns) == 0 {
		t.Fatalf("expected at least one concern on timeout, got none")
	}
}
