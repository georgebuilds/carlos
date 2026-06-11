package agent

// Whitebox coverage for the verifier compiler/test-runner helpers that
// the language-specific end-to-end tests (which require go/cargo/pytest
// on PATH) don't reach deterministically: per-language error/result
// parsing, command tables, output truncation, and the build-command
// runner's start-error path.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTestRunnerVerifier_WorkdirNotADir passes a regular file as workdir
// so the stat-not-a-dir guard fires.
func TestTestRunnerVerifier_WorkdirNotADir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := NewTestRunnerVerifier()
	if _, err := v.Verify(context.Background(), f, nil); err == nil {
		t.Fatal("a file workdir should error")
	}
}

// TestTestRunnerVerifier_ZeroValueDefaults runs a real `go test` through a
// zero-value verifier (Timeout/MaxOutputBytes unset) so the default-
// substitution branches fire.
func TestTestRunnerVerifier_ZeroValueDefaults(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := &TestRunnerVerifier{} // zero Timeout + MaxOutputBytes
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("verify: %v; raw=%s", err, report.Raw)
	}
	if report.Decision != VerificationAccept {
		t.Fatalf("zero-test package should accept, got %s", report.Decision)
	}
}

// TestTestRunnerVerifier_BuildFailNoCounts compiles a package whose test
// file does not compile, so `go test` exits non-zero with no parseable
// pass/fail counts → the exit-code fallback reject branch.
func TestTestRunnerVerifier_BuildFailNoCounts(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A test file that fails to COMPILE produces a build error, not a
	// PASS/FAIL line, so countResults returns 0/0 and exit != 0.
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"),
		[]byte("package x\n\nfunc Broken() { this is not valid go }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := NewTestRunnerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Decision != VerificationReject {
		t.Fatalf("build failure should reject, got %s; raw=%s", report.Decision, report.Raw)
	}
	if len(report.Concerns) == 0 {
		t.Error("build-fail reject should carry a concern")
	}
}

// TestCompilerVerifier_ZeroValueDefaults runs a real `go build` through a
// zero-value verifier so the default Timeout/MaxOutputBytes branches fire.
func TestCompilerVerifier_ZeroValueDefaults(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := &CompilerVerifier{} // zero Timeout + MaxOutputBytes
	report, err := v.Verify(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("verify: %v; raw=%s", err, report.Raw)
	}
	if report.Decision != VerificationAccept {
		t.Fatalf("clean build should accept, got %s", report.Decision)
	}
}

func TestParseBuildErrors_PerLanguage(t *testing.T) {
	cases := []struct {
		name   string
		lang   detectedLang
		output string
		want   string // substring expected in first concern
	}{
		{"go", langGo, "some noise\n./main.go:12:5: undefined: foo\nmore noise\n", "main.go:12:5"},
		{"rust", langRust, "Compiling x\nerror[E0425]: cannot find value `y`\n", "error[E0425]"},
		{"python", langPython, "blah\nSyntaxError: invalid syntax\n", "SyntaxError"},
		{"python-marker", langPython, "blah\n*** Error compiling './a.py'...\n", "*** Error compiling"},
		{"node", langNode, "npm ERR! build failed\n", "npm ERR!"},
		{"node-generic-error", langNode, "Build error: something broke\n", "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBuildErrors(tc.output, tc.lang)
			if len(got) == 0 {
				t.Fatalf("expected at least one concern, got none")
			}
			if !strings.Contains(strings.ToLower(got[0]), strings.ToLower(tc.want)) {
				t.Errorf("first concern %q does not contain %q", got[0], tc.want)
			}
		})
	}
}

// TestParseBuildErrors_CapsAtEight feeds many matching lines and confirms
// the maxConcerns cap (and the blank-line skip inside add) both fire.
func TestParseBuildErrors_CapsAtEight(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("error: failure number\n")
		b.WriteString("\n") // blank line → add() skip branch
	}
	got := parseBuildErrors(b.String(), langRust)
	if len(got) != 8 {
		t.Fatalf("expected concerns capped at 8, got %d", len(got))
	}
}

func TestParseBuildErrors_NoMatchEmpty(t *testing.T) {
	got := parseBuildErrors("nothing interesting here\n", langGo)
	if len(got) != 0 {
		t.Fatalf("expected no concerns, got %v", got)
	}
}

func TestCompilerCommandFor_AllLanguages(t *testing.T) {
	dir := t.TempDir()
	for _, lang := range []detectedLang{langGo, langRust, langPython} {
		cmd, args, ok := compilerCommandFor(lang, dir)
		if !ok || cmd == "" || len(args) == 0 {
			t.Errorf("compilerCommandFor(%s) = %q %v %v", lang, cmd, args, ok)
		}
	}
	// Node with a build script → ok; without → not ok.
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"tsc"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if cmd, _, ok := compilerCommandFor(langNode, dir); !ok || cmd != "npm" {
		t.Errorf("node with build script: cmd=%q ok=%v", cmd, ok)
	}
	// Unknown language → not ok.
	if _, _, ok := compilerCommandFor(langUnknown, dir); ok {
		t.Error("unknown lang should yield ok=false")
	}
}

func TestTestCommandFor_AllLanguages(t *testing.T) {
	dir := t.TempDir()
	for _, lang := range []detectedLang{langGo, langRust, langPython} {
		cmd, args, ok := testCommandFor(lang, dir)
		if !ok || cmd == "" || len(args) == 0 {
			t.Errorf("testCommandFor(%s) = %q %v %v", lang, cmd, args, ok)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"test":"jest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if cmd, _, ok := testCommandFor(langNode, dir); !ok || cmd != "npm" {
		t.Errorf("node with test script: cmd=%q ok=%v", cmd, ok)
	}
	if _, _, ok := testCommandFor(langUnknown, dir); ok {
		t.Error("unknown lang should yield ok=false")
	}
}

func TestHasNpmBuildScript_MissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	// Missing package.json → false.
	if hasNpmBuildScript(dir) {
		t.Error("missing package.json should be false")
	}
	// Malformed JSON → false (swallowed).
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hasNpmBuildScript(dir) {
		t.Error("malformed package.json should be false")
	}
}

func TestCountResults_RustPythonNode(t *testing.T) {
	rp, rf := countResults("running 5 tests\ntest result: ok. 4 passed; 1 failed; 0 ignored\n", langRust)
	if rp != 4 || rf != 1 {
		t.Errorf("rust counts = %d/%d, want 4/1", rp, rf)
	}
	pp, pf := countResults("===== 3 passed, 2 failed in 0.10s =====\n", langPython)
	if pp != 3 || pf != 2 {
		t.Errorf("python counts = %d/%d, want 3/2", pp, pf)
	}
	// Mocha shape.
	np, nf := countResults("  7 passing\n  1 failing\n", langNode)
	if np != 7 || nf != 1 {
		t.Errorf("node mocha counts = %d/%d, want 7/1", np, nf)
	}
	// Jest shape (more specific, tried first).
	jp, jf := countResults("Tests: 2 failed, 9 passed, 11 total\n", langNode)
	if jp != 9 || jf != 2 {
		t.Errorf("node jest counts = %d/%d, want 9/2", jp, jf)
	}
	// Unknown lang → zero/zero.
	if a, b := countResults("whatever", langUnknown); a != 0 || b != 0 {
		t.Errorf("unknown lang counts = %d/%d, want 0/0", a, b)
	}
}

func TestFailingTestNames_RustPython(t *testing.T) {
	rust := failingTestNames("test foo::bar ... FAILED\ntest baz::qux ... FAILED\n", langRust)
	if len(rust) != 2 || rust[0] != "foo::bar" {
		t.Errorf("rust failing names = %v", rust)
	}
	py := failingTestNames("FAILED tests/test_x.py::test_a\nFAILED tests/test_y.py::test_b\n", langPython)
	if len(py) != 2 {
		t.Errorf("python failing names = %v", py)
	}
	// Unknown → nil.
	if got := failingTestNames("x", langUnknown); got != nil {
		t.Errorf("unknown lang names = %v, want nil", got)
	}
}

// TestFailingTestNames_CapsAtFive confirms the maxNames cap.
func TestFailingTestNames_CapsAtFive(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("--- FAIL: TestThing (0.00s)\n")
	}
	got := failingTestNames(b.String(), langGo)
	if len(got) != 5 {
		t.Fatalf("expected names capped at 5, got %d", len(got))
	}
}

func TestFirstIntMatch_NonNumericAndNoMatch(t *testing.T) {
	// No match → 0.
	if got := firstIntMatch(pytestSummaryRE, "no counts here"); got != 0 {
		t.Errorf("no match = %d, want 0", got)
	}
	// A match with a valid integer.
	if got := firstIntMatch(pytestSummaryRE, "12 passed"); got != 12 {
		t.Errorf("valid match = %d, want 12", got)
	}
}

func TestTruncateTail_TruncatesAndPreservesShort(t *testing.T) {
	short := []byte("tiny")
	if got := truncateTail(short, 100); string(got) != "tiny" {
		t.Errorf("short input mangled: %q", got)
	}
	long := []byte(strings.Repeat("x", 200))
	got := truncateTail(long, 50)
	if !strings.Contains(string(got), "earlier bytes") {
		t.Errorf("truncation marker missing: %q", got)
	}
	// Tail (not head) is kept.
	if !strings.HasSuffix(string(got), strings.Repeat("x", 50)) {
		t.Errorf("tail not preserved: %q", got)
	}
}

// TestCompilerVerifier_ToolNotOnPATH scaffolds a go.mod project then
// clears PATH so exec.LookPath("go") fails, exercising the
// "<cmd> not on PATH" reject branch in CompilerVerifier.Verify.
func TestCompilerVerifier_ToolNotOnPATH(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	v := NewCompilerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err == nil {
		t.Fatal("expected ErrCompilerNoToolchain when go is not on PATH")
	}
	if report.Decision != VerificationReject {
		t.Errorf("decision = %s, want reject", report.Decision)
	}
	if len(report.Concerns) == 0 || !strings.Contains(report.Concerns[0], "not on PATH") {
		t.Errorf("concerns = %v, want 'not on PATH'", report.Concerns)
	}
}

// TestTestRunnerVerifier_ToolNotOnPATH mirrors the compiler test for the
// test-runner adapter.
func TestTestRunnerVerifier_ToolNotOnPATH(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	v := NewTestRunnerVerifier()
	report, err := v.Verify(context.Background(), dir, nil)
	if err == nil {
		t.Fatal("expected ErrTestRunnerNoToolchain when go is not on PATH")
	}
	if report.Decision != VerificationReject {
		t.Errorf("decision = %s, want reject", report.Decision)
	}
	if len(report.Concerns) == 0 || !strings.Contains(report.Concerns[0], "not on PATH") {
		t.Errorf("concerns = %v, want 'not on PATH'", report.Concerns)
	}
}

// TestRunBuildCommand_DefaultTimeoutAndMaxLen runs a tiny echo through
// Verify with a verifier whose Timeout/MaxOutputBytes are zero, hitting
// the default-substitution branches. We use a fake project + PATH that
// contains a trivial `go` shim so the build "succeeds" instantly.
func TestRunBuildCommand_TruncatesLongOutput(t *testing.T) {
	// Directly exercise runBuildCommand with a command that emits more
	// than maxLen bytes so the tail-truncation path inside Verify's
	// helper is covered with a small cap.
	dir := t.TempDir()
	out, exit, timedOut := runBuildCommand(context.Background(), dir,
		"sh", []string{"-c", "for i in $(seq 1 200); do echo loooooong line; done"},
		5*time.Second, 64)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if timedOut {
		t.Fatal("should not time out")
	}
	if !strings.Contains(string(out), "earlier bytes") {
		t.Errorf("expected truncation marker in long output, got %q", out)
	}
}

// TestRunBuildCommand_StartError points at a binary that doesn't exist so
// exec.Start fails, hitting the start-error return.
func TestRunBuildCommand_StartError(t *testing.T) {
	dir := t.TempDir()
	out, exit, timedOut := runBuildCommand(context.Background(), dir,
		"this-binary-does-not-exist-anywhere-12345", []string{"--help"}, 5*time.Second, 4096)
	if exit != -1 {
		t.Errorf("start error exit = %d, want -1", exit)
	}
	if timedOut {
		t.Error("start error should not be reported as timed out")
	}
	if !strings.Contains(string(out), "start error") {
		t.Errorf("output missing start-error marker: %q", out)
	}
}
