// Phase 5 slice 5d — TestRunner adapter.
//
// Runs the canonical test command for the detected language under
// workdir, parses pass/fail counts from the output, and scores:
//
//	all pass    → score 10, accept
//	any failure → score scales by failure ratio, decision derived via
//	              decisionFromRatio (>=0.95 accept; <0.5 reject; else
//	              needs_revision)
//
// File name is verifier_tests.go (plural) NOT verifier_test.go — files
// ending in _test.go are interpreted as test files by `go test`, which
// would mean ToolGroundedVerifier doesn't exist in production binaries.
//
// Detection markers + commands:
//
//	go.mod         → go test ./...
//	Cargo.toml     → cargo test
//	package.json   → npm test            (skipped if no "test" script)
//	pyproject.toml → pytest -q           (also tried for pytest.ini, setup.py)
//
// Output parsing is best-effort per language; document each pattern
// in countResults. Timeout default 5 minutes — test suites are
// inherently slower than builds.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TestRunnerVerifier is the tool-grounded test-pass check.
type TestRunnerVerifier struct {
	// Timeout caps the test command's wall-clock. Default (zero) =
	// testRunnerDefaultTimeout.
	Timeout time.Duration

	// MaxOutputBytes caps the combined stdout+stderr we keep. Default
	// (zero) = testRunnerMaxOutputBytes. We tail-truncate (same as
	// CompilerVerifier) because the summary line at the end is the
	// most useful signal for the parser.
	MaxOutputBytes int
}

const (
	testRunnerDefaultTimeout   = 5 * time.Minute
	testRunnerMaxOutputBytes   = 64 * 1024
)

// NewTestRunnerVerifier returns a TestRunnerVerifier with default
// timeout and output cap.
func NewTestRunnerVerifier() *TestRunnerVerifier {
	return &TestRunnerVerifier{
		Timeout:        testRunnerDefaultTimeout,
		MaxOutputBytes: testRunnerMaxOutputBytes,
	}
}

// Name implements ToolGroundedVerifier. Returns "tests".
func (*TestRunnerVerifier) Name() string { return "tests" }

// ErrTestRunnerNoToolchain is returned when the language's test tool
// is not on PATH (or no "test" script for Node).
var ErrTestRunnerNoToolchain = errors.New("tests: test toolchain not on PATH")

// ErrTestRunnerUnknownLanguage is returned when no project marker is
// found under workdir.
var ErrTestRunnerUnknownLanguage = errors.New("tests: no recognised project marker under workdir")

// Verify runs the test command for workdir's detected language and
// scores the outcome. content is unused — see CompilerVerifier.Verify
// for the same content-is-on-disk discipline.
func (v *TestRunnerVerifier) Verify(ctx context.Context, workdir string, content []byte) (VerificationReport, error) {
	_ = content

	if workdir == "" {
		return VerificationReport{}, errors.New("tests: empty workdir")
	}
	if st, err := os.Stat(workdir); err != nil || !st.IsDir() {
		return VerificationReport{}, fmt.Errorf("tests: workdir %q not a directory", workdir)
	}

	lang := detectLanguage(workdir)
	if lang == langUnknown {
		return VerificationReport{
			Decision:   VerificationReject,
			Score:      1,
			Concerns:   []string{"no recognised project marker in workdir"},
			JudgeModel: "tests:unknown",
		}, ErrTestRunnerUnknownLanguage
	}

	cmd, args, ok := testCommandFor(lang, workdir)
	if !ok {
		return VerificationReport{
			Decision:   VerificationReject,
			Score:      1,
			Concerns:   []string{fmt.Sprintf("no canonical test command available for %s", lang)},
			JudgeModel: "tests:" + string(lang),
		}, ErrTestRunnerNoToolchain
	}
	if _, lookErr := exec.LookPath(cmd); lookErr != nil {
		return VerificationReport{
			Decision:   VerificationReject,
			Score:      1,
			Concerns:   []string{fmt.Sprintf("%s not on PATH", cmd)},
			JudgeModel: "tests:" + string(lang),
		}, fmt.Errorf("%w: %s", ErrTestRunnerNoToolchain, cmd)
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = testRunnerDefaultTimeout
	}
	maxLen := v.MaxOutputBytes
	if maxLen <= 0 {
		maxLen = testRunnerMaxOutputBytes
	}

	out, exit, timedOut := runBuildCommand(ctx, workdir, cmd, args, timeout, maxLen)

	report := VerificationReport{
		JudgeModel: "tests:" + string(lang),
		Raw:        string(out),
	}
	if timedOut {
		report.Decision = VerificationReject
		report.Score = 1
		report.Concerns = []string{fmt.Sprintf("%s tests timed out after %s", lang, timeout)}
		return report, nil
	}

	pass, fail := countResults(string(out), lang)

	// Fallback when we can't extract counts: rely on the exit code.
	// A zero exit + zero counts is a green pass with no parseable
	// summary (e.g. `go test ./...` in a package with no tests just
	// prints "no test files"); we treat that as a clean accept since
	// the toolchain itself said success.
	if pass == 0 && fail == 0 {
		if exit == 0 {
			report.Decision = VerificationAccept
			report.Score = 10
			return report, nil
		}
		report.Decision = VerificationReject
		report.Score = 1
		report.Concerns = []string{fmt.Sprintf("%s tests exited %d (no test counts parsed)", lang, exit)}
		return report, nil
	}

	total := pass + fail
	ratio := float64(pass) / float64(total)
	report.Score = scoreFromRatio(ratio)
	report.Decision = decisionFromRatio(ratio)
	if fail > 0 {
		report.Concerns = []string{fmt.Sprintf("%d/%d tests failed", fail, total)}
		// Also surface the first few failing-test names if we can
		// extract them.
		if names := failingTestNames(string(out), lang); len(names) > 0 {
			report.Concerns = append(report.Concerns, names...)
		}
	}
	return report, nil
}

// testCommandFor returns the (cmd, args) tuple for the language's
// canonical test invocation. Node requires a "test" script in
// package.json; otherwise we report "no test command available".
func testCommandFor(lang detectedLang, workdir string) (string, []string, bool) {
	switch lang {
	case langGo:
		// -v emits "--- PASS: TestFoo" lines we need for per-test
		// counts. Without -v go test prints PASS/FAIL only for
		// failing tests, which would make a "2/3 passing" run look
		// like a "0/1 passing" run and over-report failures.
		// Trade-off: -v blows up output volume on big test suites,
		// but our tail-truncation cap keeps the runaway bounded.
		return "go", []string{"test", "-v", "./..."}, true
	case langRust:
		return "cargo", []string{"test"}, true
	case langPython:
		return "pytest", []string{"-q"}, true
	case langNode:
		if !hasNpmTestScript(workdir) {
			return "", nil, false
		}
		return "npm", []string{"test", "--silent"}, true
	default:
		return "", nil, false
	}
}

// hasNpmTestScript mirrors hasNpmBuildScript but looks for "test".
func hasNpmTestScript(workdir string) bool {
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
	_, ok := pkg.Scripts["test"]
	return ok
}

// Go test summary patterns. `go test` emits one of:
//
//	ok      pkg/path     0.123s
//	FAIL    pkg/path     0.123s
//	--- FAIL: TestFoo (0.00s)
//	--- PASS: TestFoo (0.00s)
//	PASS / FAIL on a line of its own
//
// We count --- PASS / --- FAIL lines for the granular ratio. Falls
// back to "ok"/"FAIL" package-summary counting if no per-test lines
// exist (e.g. -count=1 -run=... narrowed runs).
var goPassLineRE = regexp.MustCompile(`(?m)^\s*--- PASS:\s+(\S+)`)
var goFailLineRE = regexp.MustCompile(`(?m)^\s*--- FAIL:\s+(\S+)`)
var goPkgOkRE = regexp.MustCompile(`(?m)^ok\s+\S+`)
var goPkgFailRE = regexp.MustCompile(`(?m)^FAIL\s+\S+`)

// Rust test summary: "test result: ok. N passed; M failed; ..." or
// the unit "test foo::bar ... ok" / "... FAILED" lines.
var rustSummaryRE = regexp.MustCompile(`test result:.*?(\d+) passed.*?(\d+) failed`)
var rustFailingNameRE = regexp.MustCompile(`(?m)^test\s+(\S+)\s+\.\.\.\s+FAILED`)

// pytest summary: "===== N passed, M failed in 0.12s =====" or just
// "===== N passed in 0.12s =====" on a clean run.
var pytestSummaryRE = regexp.MustCompile(`(\d+)\s+passed`)
var pytestFailedRE = regexp.MustCompile(`(\d+)\s+failed`)
var pytestFailingNameRE = regexp.MustCompile(`(?m)^FAILED\s+(\S+)`)

// npm test counts vary by framework; we look for the common Mocha/
// Jest shapes: "N passing"/"N failing" or "Tests: N passed, M failed".
var npmPassingRE = regexp.MustCompile(`(\d+)\s+passing`)
var npmFailingRE = regexp.MustCompile(`(\d+)\s+failing`)
var npmJestPassRE = regexp.MustCompile(`Tests:.*?(\d+)\s+passed`)
var npmJestFailRE = regexp.MustCompile(`Tests:.*?(\d+)\s+failed`)

// countResults parses pass/fail counts from output. Returns (passed,
// failed); both zero means no counts could be extracted.
func countResults(output string, lang detectedLang) (int, int) {
	switch lang {
	case langGo:
		pass := len(goPassLineRE.FindAllStringIndex(output, -1))
		fail := len(goFailLineRE.FindAllStringIndex(output, -1))
		if pass == 0 && fail == 0 {
			// Per-package summary fallback.
			pass = len(goPkgOkRE.FindAllStringIndex(output, -1))
			fail = len(goPkgFailRE.FindAllStringIndex(output, -1))
		}
		return pass, fail
	case langRust:
		// Cargo can run several test binaries; sum each "test result"
		// summary line.
		pass, fail := 0, 0
		for _, m := range rustSummaryRE.FindAllStringSubmatch(output, -1) {
			if len(m) >= 3 {
				if n, err := strconv.Atoi(m[1]); err == nil {
					pass += n
				}
				if n, err := strconv.Atoi(m[2]); err == nil {
					fail += n
				}
			}
		}
		return pass, fail
	case langPython:
		pass := firstIntMatch(pytestSummaryRE, output)
		fail := firstIntMatch(pytestFailedRE, output)
		return pass, fail
	case langNode:
		// Try Jest shape first (more specific), then Mocha.
		jestPass := firstIntMatch(npmJestPassRE, output)
		jestFail := firstIntMatch(npmJestFailRE, output)
		if jestPass+jestFail > 0 {
			return jestPass, jestFail
		}
		return firstIntMatch(npmPassingRE, output), firstIntMatch(npmFailingRE, output)
	default:
		return 0, 0
	}
}

// firstIntMatch returns the first integer captured by re in s, or 0
// if no match.
func firstIntMatch(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// failingTestNames extracts the names of failing tests for inclusion
// in VerificationReport.Concerns. Caps at 5 names so a catastrophic
// failure doesn't blow out the concerns slice.
func failingTestNames(output string, lang detectedLang) []string {
	const maxNames = 5
	var matches [][]string
	switch lang {
	case langGo:
		matches = goFailLineRE.FindAllStringSubmatch(output, -1)
	case langRust:
		matches = rustFailingNameRE.FindAllStringSubmatch(output, -1)
	case langPython:
		matches = pytestFailingNameRE.FindAllStringSubmatch(output, -1)
	default:
		return nil
	}
	out := make([]string, 0, maxNames)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		out = append(out, strings.TrimSpace(m[1]))
		if len(out) >= maxNames {
			break
		}
	}
	return out
}

// Compile-time check: TestRunnerVerifier implements ToolGroundedVerifier.
var _ ToolGroundedVerifier = (*TestRunnerVerifier)(nil)
