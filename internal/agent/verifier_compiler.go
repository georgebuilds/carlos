// Phase 5 slice 5d — Compiler adapter.
//
// Runs the canonical build command for the detected language under
// workdir. Exit code 0 → clean accept (score 10). Non-zero → reject
// (score 1) with the parsed error lines surfaced as concerns. No
// partial-credit middle ground: a project either builds or it doesn't,
// and a "needs revision" verdict from a compiler would be a category
// error.
//
// Detection markers + commands:
//
//	go.mod         → go build ./...
//	Cargo.toml     → cargo build
//	package.json   → npm run build       (skipped if no "build" script)
//	pyproject.toml → python -m compileall .  (best effort — Python doesn't
//	                 have a canonical "build" step; bytecode-compile
//	                 catches at least syntax errors)
//
// Output discipline mirrors internal/tools/bash.go: combined
// stdout+stderr, cap at compilerMaxOutputBytes (64 KiB per slice-5d
// brief), exit code surfaced separately. Timeout default 90 seconds.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CompilerVerifier is the tool-grounded build-pass check.
type CompilerVerifier struct {
	// Timeout caps the build command's wall-clock. Default (zero) =
	// compilerDefaultTimeout. The bash tool's 30s is too tight for a
	// full repo build — 90s is generous without being a denial-of-
	// service risk.
	Timeout time.Duration

	// MaxOutputBytes caps the combined stdout+stderr we keep. Default
	// (zero) = compilerMaxOutputBytes. Errors are usually at the END
	// of the build output, so we keep the TAIL, not the head — see
	// truncateTail.
	MaxOutputBytes int
}

const (
	compilerDefaultTimeout = 90 * time.Second
	compilerMaxOutputBytes = 64 * 1024
)

// NewCompilerVerifier returns a CompilerVerifier with default timeout
// and output cap. Callers that need different limits set the fields
// directly after construction.
func NewCompilerVerifier() *CompilerVerifier {
	return &CompilerVerifier{
		Timeout:        compilerDefaultTimeout,
		MaxOutputBytes: compilerMaxOutputBytes,
	}
}

// Name implements ToolGroundedVerifier. Returns "compiler".
func (*CompilerVerifier) Name() string { return "compiler" }

// ErrCompilerNoToolchain is returned when the detected language's
// build tool is not on PATH. The caller should treat this as "verifier
// could not run" — i.e. fall back to human review.
var ErrCompilerNoToolchain = errors.New("compiler: build toolchain not on PATH")

// ErrCompilerUnknownLanguage is returned when no project marker is
// found under workdir. Same semantics as ErrCompilerNoToolchain — the
// verifier did not run.
var ErrCompilerUnknownLanguage = errors.New("compiler: no recognised project marker under workdir")

// Verify runs the build command for workdir's detected language. The
// content arg is unused — the build is performed against whatever is
// on disk in workdir. The slice-5d design intentionally separates
// artifact text from the workdir snapshot: a diff artifact is APPLIED
// into a Worktree by the caller before Verify is called, and the
// compiler then runs against the on-disk result. Adapters that need to
// pre-process content can wrap CompilerVerifier; the base version is
// content-agnostic.
func (v *CompilerVerifier) Verify(ctx context.Context, workdir string, content []byte) (VerificationReport, error) {
	_ = content // intentional: see method doc

	if workdir == "" {
		return VerificationReport{}, errors.New("compiler: empty workdir")
	}
	if st, err := os.Stat(workdir); err != nil || !st.IsDir() {
		return VerificationReport{}, fmt.Errorf("compiler: workdir %q not a directory", workdir)
	}

	lang := detectLanguage(workdir)
	if lang == langUnknown {
		return VerificationReport{
			Decision:   VerificationReject,
			Score:      1,
			Concerns:   []string{"no recognised project marker in workdir"},
			JudgeModel: "compiler:unknown",
		}, ErrCompilerUnknownLanguage
	}

	cmd, args, ok := compilerCommandFor(lang, workdir)
	if !ok {
		// Language detected but its tool isn't available (e.g.
		// package.json with no "build" script). Treat as
		// "cannot evaluate" — same as missing toolchain.
		return VerificationReport{
			Decision:   VerificationReject,
			Score:      1,
			Concerns:   []string{fmt.Sprintf("no canonical build command available for %s", lang)},
			JudgeModel: "compiler:" + string(lang),
		}, ErrCompilerNoToolchain
	}
	if _, lookErr := exec.LookPath(cmd); lookErr != nil {
		return VerificationReport{
			Decision:   VerificationReject,
			Score:      1,
			Concerns:   []string{fmt.Sprintf("%s not on PATH", cmd)},
			JudgeModel: "compiler:" + string(lang),
		}, fmt.Errorf("%w: %s", ErrCompilerNoToolchain, cmd)
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = compilerDefaultTimeout
	}
	maxLen := v.MaxOutputBytes
	if maxLen <= 0 {
		maxLen = compilerMaxOutputBytes
	}

	out, exit, timedOut := runBuildCommand(ctx, workdir, cmd, args, timeout, maxLen)

	report := VerificationReport{
		JudgeModel: "compiler:" + string(lang),
		Raw:        string(out),
	}
	if timedOut {
		report.Decision = VerificationReject
		report.Score = 1
		report.Concerns = []string{fmt.Sprintf("%s build timed out after %s", lang, timeout)}
		return report, nil
	}
	if exit == 0 {
		report.Decision = VerificationAccept
		report.Score = 10
		return report, nil
	}
	report.Decision = VerificationReject
	report.Score = 1
	report.Concerns = parseBuildErrors(string(out), lang)
	if len(report.Concerns) == 0 {
		// Fall back: surface the last non-empty line so the queue
		// title isn't blank on a no-pattern build failure.
		report.Concerns = []string{fmt.Sprintf("%s build exited %d", lang, exit)}
	}
	return report, nil
}

// compilerCommandFor returns the (cmd, args) tuple for the language's
// canonical build, plus a bool indicating whether a usable command was
// determined. For Node we additionally peek at package.json to confirm
// a "build" script exists — projects that don't define one shouldn't be
// blamed for "failing to build".
func compilerCommandFor(lang detectedLang, workdir string) (string, []string, bool) {
	switch lang {
	case langGo:
		return "go", []string{"build", "./..."}, true
	case langRust:
		return "cargo", []string{"build"}, true
	case langPython:
		// compileall is the cheapest syntax-checker that ships with
		// Python; it walks .py files and bytecode-compiles them.
		// Errors out on syntax errors, succeeds otherwise.
		return "python", []string{"-m", "compileall", "-q", "."}, true
	case langNode:
		if !hasNpmBuildScript(workdir) {
			return "", nil, false
		}
		return "npm", []string{"run", "build", "--silent"}, true
	default:
		return "", nil, false
	}
}

// hasNpmBuildScript peeks at package.json and reports whether a
// "build" script is defined. We swallow JSON parse errors and missing
// files (return false) — the worst case is we report "no build script"
// for a malformed package.json, which is correct behavior.
func hasNpmBuildScript(workdir string) bool {
	raw, err := os.ReadFile(filepath.Join(workdir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return false
	}
	_, ok := pkg.Scripts["build"]
	return ok
}

// runBuildCommand executes cmd+args in workdir with the given timeout,
// captures combined stdout+stderr (tail-truncated to maxLen), and
// returns (output, exit code, timed-out). A non-zero exit from the
// child is NOT signaled as a Go error here — it's part of the verdict.
//
// Mirrors internal/tools/bash.go's process-group handling so a context
// cancel kills grandchildren too (Go's build pulls in compile+link
// subprocesses; cargo and npm spawn additional workers).
func runBuildCommand(ctx context.Context, workdir, cmd string, args []string, timeout time.Duration, maxLen int) ([]byte, int, bool) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(execCtx, cmd, args...)
	c.Dir = workdir
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf

	err := c.Start()
	if err != nil {
		fmt.Fprintf(&buf, "\n[start error: %v]\n", err)
		return truncateTail(buf.Bytes(), maxLen), -1, false
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-execCtx.Done():
			if c.Process != nil {
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()
	waitErr := c.Wait()
	close(done)

	timedOut := errors.Is(execCtx.Err(), context.DeadlineExceeded)

	exit := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exit = ee.ExitCode()
		} else {
			fmt.Fprintf(&buf, "\n[wait error: %v]\n", waitErr)
			exit = -1
		}
	}
	return truncateTail(buf.Bytes(), maxLen), exit, timedOut
}

// truncateTail returns the LAST maxLen bytes of buf, prefixed with a
// "[truncated, N earlier bytes]" marker when truncation happens.
// Compiler errors usually live at the end of the output; tail-truncation
// surfaces them instead of the early "Compiling foo v0.1.0" noise that
// dominates the head of a cargo / npm build.
func truncateTail(buf []byte, maxLen int) []byte {
	if len(buf) <= maxLen {
		return buf
	}
	dropped := len(buf) - maxLen
	tail := buf[dropped:]
	out := make([]byte, 0, maxLen+64)
	out = append(out, []byte(fmt.Sprintf("[truncated, %d earlier bytes]\n", dropped))...)
	out = append(out, tail...)
	return out
}

// parseBuildErrors pulls the language-specific error lines out of a
// build's stderr/stdout. Per-language heuristics; document each:
//
//	go: lines starting with "./" or absolute path containing ":" and
//	    a line number, e.g. "./main.go:12:5: undefined: foo".
//	rust: lines starting with "error" or "error[" (cargo color is
//	      stripped because we don't allocate a tty).
//	python: lines containing "SyntaxError" or "*** " (compileall's
//	        error marker).
//	node/npm: lines containing "error" (lowercased) or starting with
//	          "npm ERR!".
//
// Returns up to 8 concern lines; the queue UI shows only the first
// anyway, but the report's full Raw field carries the rest for
// debugging.
func parseBuildErrors(output string, lang detectedLang) []string {
	const maxConcerns = 8
	out := []string{}
	add := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		if len(out) >= maxConcerns {
			return
		}
		out = append(out, line)
	}
	for _, line := range strings.Split(output, "\n") {
		if len(out) >= maxConcerns {
			break
		}
		switch lang {
		case langGo:
			// "./main.go:12:5: error msg" or "main.go:12:5: error msg"
			if strings.Contains(line, ".go:") && strings.Count(line, ":") >= 2 {
				add(line)
			}
		case langRust:
			low := strings.ToLower(strings.TrimSpace(line))
			if strings.HasPrefix(low, "error") {
				add(line)
			}
		case langPython:
			if strings.Contains(line, "SyntaxError") || strings.HasPrefix(strings.TrimSpace(line), "*** ") {
				add(line)
			}
		case langNode:
			low := strings.ToLower(line)
			if strings.HasPrefix(strings.TrimSpace(line), "npm ERR!") || strings.Contains(low, "error") {
				add(line)
			}
		}
	}
	return out
}

// Compile-time check: CompilerVerifier implements ToolGroundedVerifier.
var _ ToolGroundedVerifier = (*CompilerVerifier)(nil)
