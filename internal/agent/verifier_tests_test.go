package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// writeGoModuleWithTest scaffolds a tiny Go module containing a test
// file. testBody is appended after the package declaration.
func writeGoModuleWithTest(t *testing.T, testBody string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	src := "package example\n\nimport \"testing\"\n\n" + testBody
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write x_test.go: %v", err)
	}
	// Also write a trivial non-test file so the package isn't empty.
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package example\n"), 0o644); err != nil {
		t.Fatalf("write x.go: %v", err)
	}
	return dir
}

func TestTestRunnerVerifier_GoPasses(t *testing.T) {
	goOrSkip(t)
	dir := writeGoModuleWithTest(t,
		"func TestPasses(t *testing.T) {}\n",
	)
	v := agent.NewTestRunnerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v; raw=%s", err, report.Raw)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept, got %s; raw=%s", report.Decision, report.Raw)
	}
	if report.Score != 10 {
		t.Fatalf("expected score 10, got %d", report.Score)
	}
	if report.JudgeModel != "tests:go" {
		t.Fatalf("expected JudgeModel=tests:go, got %q", report.JudgeModel)
	}
}

func TestTestRunnerVerifier_GoOneFails(t *testing.T) {
	goOrSkip(t)
	dir := writeGoModuleWithTest(t,
		`func TestA(t *testing.T) {}
func TestB(t *testing.T) { t.Fatal("intentional") }
func TestC(t *testing.T) {}
`)
	v := agent.NewTestRunnerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v; raw=%s", err, report.Raw)
	}
	// 2 pass / 1 fail = 0.667 ratio → needs_revision (0.5-0.95 band).
	if report.Decision != agent.VerificationNeedsRevision {
		t.Fatalf("expected needs_revision for 2/3 pass, got %s; raw=%s", report.Decision, report.Raw)
	}
	if report.Score < 5 || report.Score > 8 {
		t.Fatalf("expected score in [5,8] for 0.667 ratio, got %d", report.Score)
	}
	joined := strings.Join(report.Concerns, " ")
	if !strings.Contains(joined, "TestB") {
		t.Fatalf("expected TestB in concerns, got %v", report.Concerns)
	}
}

func TestTestRunnerVerifier_GoMostFail(t *testing.T) {
	goOrSkip(t)
	dir := writeGoModuleWithTest(t,
		`func TestA(t *testing.T) { t.Fatal("a") }
func TestB(t *testing.T) { t.Fatal("b") }
func TestC(t *testing.T) { t.Fatal("c") }
func TestD(t *testing.T) {}
`)
	v := agent.NewTestRunnerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v; raw=%s", err, report.Raw)
	}
	// 1 pass / 3 fail = 0.25 ratio → reject (<0.5).
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject for 1/4 pass, got %s; raw=%s", report.Decision, report.Raw)
	}
}

func TestTestRunnerVerifier_UnknownLanguage(t *testing.T) {
	v := agent.NewTestRunnerVerifier()
	dir := t.TempDir()
	report, err := v.Verify(context.Background(), dir, nil)
	if !errors.Is(err, agent.ErrTestRunnerUnknownLanguage) {
		t.Fatalf("expected ErrTestRunnerUnknownLanguage, got %v", err)
	}
	if report.Decision != agent.VerificationReject {
		t.Fatalf("expected reject, got %s", report.Decision)
	}
}

func TestTestRunnerVerifier_NodeNoTestScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	v := agent.NewTestRunnerVerifier()
	_, err := v.Verify(context.Background(), dir, nil)
	if !errors.Is(err, agent.ErrTestRunnerNoToolchain) {
		t.Fatalf("expected ErrTestRunnerNoToolchain, got %v", err)
	}
}

func TestTestRunnerVerifier_EmptyWorkdirErrors(t *testing.T) {
	v := agent.NewTestRunnerVerifier()
	if _, err := v.Verify(context.Background(), "", nil); err == nil {
		t.Fatal("expected error on empty workdir")
	}
}

func TestTestRunnerVerifier_GoNoTestFilesAcceptClean(t *testing.T) {
	goOrSkip(t)
	// A module with no test files exits 0 and prints "?   pkg" lines,
	// which our parser sees as zero counts → exit 0 fallback → accept.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package example\n"), 0o644); err != nil {
		t.Fatalf("write x.go: %v", err)
	}
	v := agent.NewTestRunnerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v; raw=%s", err, report.Raw)
	}
	if report.Decision != agent.VerificationAccept {
		t.Fatalf("expected accept on zero-test exit 0, got %s; raw=%s", report.Decision, report.Raw)
	}
	if report.Score != 10 {
		t.Fatalf("expected score 10, got %d", report.Score)
	}
}
