package agent

// Whitebox tests for verifier internals: error parsers, truncateTail
// edge cases, and the scoreFromRatio boundary conditions. Kept in the
// `agent` package (not `agent_test`) so we can reach the unexported
// helpers directly without exposing them just for tests.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFileTest(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func testCtx() context.Context { return context.Background() }

func TestTruncateTail_NoTruncation(t *testing.T) {
	buf := []byte("short")
	if got := truncateTail(buf, 100); string(got) != "short" {
		t.Errorf("truncateTail short = %q, want short", got)
	}
}

func TestTruncateTail_PreservesTail(t *testing.T) {
	// "ABCDEFGHIJ" truncated to 4 should keep "GHIJ".
	buf := []byte("ABCDEFGHIJ")
	got := truncateTail(buf, 4)
	if !strings.HasSuffix(string(got), "GHIJ") {
		t.Errorf("expected tail GHIJ in %q", got)
	}
	if !strings.Contains(string(got), "truncated") {
		t.Errorf("expected truncation marker in %q", got)
	}
}

func TestParseBuildErrors_Go(t *testing.T) {
	out := "./main.go:12:5: undefined: foo\nmain.go:24:3: cannot use bar\nrandom noise\n"
	concerns := parseBuildErrors(out, langGo)
	if len(concerns) != 2 {
		t.Fatalf("concerns = %v want 2", concerns)
	}
	if !strings.Contains(concerns[0], "undefined: foo") {
		t.Errorf("first concern = %q", concerns[0])
	}
}

func TestParseBuildErrors_Rust(t *testing.T) {
	out := "Compiling foo v0.1.0\nerror: expected `;`\nerror[E0308]: mismatched types\n"
	concerns := parseBuildErrors(out, langRust)
	if len(concerns) < 2 {
		t.Errorf("concerns = %v want >=2", concerns)
	}
}

func TestParseBuildErrors_PythonSyntaxError(t *testing.T) {
	out := "Listing foo.py\n  File \"x.py\", line 1\n    1 ===\n           ^\nSyntaxError: invalid syntax\n*** Error compiling 'broken.py'\n"
	concerns := parseBuildErrors(out, langPython)
	if len(concerns) < 1 {
		t.Errorf("concerns = %v", concerns)
	}
}

func TestParseBuildErrors_NodeNpmErr(t *testing.T) {
	out := "npm ERR! foo\nerror something broke\nstray line\n"
	concerns := parseBuildErrors(out, langNode)
	if len(concerns) < 2 {
		t.Errorf("concerns = %v want >=2", concerns)
	}
}

func TestParseBuildErrors_TruncatesAtMax(t *testing.T) {
	// 20 go-style error lines; output should cap at 8.
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("./main.go:1:1: err\n")
	}
	concerns := parseBuildErrors(b.String(), langGo)
	if len(concerns) > 8 {
		t.Errorf("concerns = %d, want <= 8", len(concerns))
	}
}

func TestCountResults_GoPerTest(t *testing.T) {
	out := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- FAIL: TestB (0.00s)\nPASS\nok      foo  0.001s\n"
	pass, fail := countResults(out, langGo)
	if pass != 1 || fail != 1 {
		t.Errorf("got pass=%d fail=%d want 1,1", pass, fail)
	}
}

func TestCountResults_GoPackageFallback(t *testing.T) {
	// No --- PASS/FAIL lines; should fall back to package counts.
	out := "ok      foo  0.001s\nok      bar  0.002s\nFAIL    qux  0.003s\n"
	pass, fail := countResults(out, langGo)
	if pass != 2 || fail != 1 {
		t.Errorf("got pass=%d fail=%d want 2,1", pass, fail)
	}
}

func TestCountResults_RustMultiline(t *testing.T) {
	out := "test result: ok. 5 passed; 1 failed; 0 ignored\ntest result: ok. 2 passed; 0 failed; 0 ignored\n"
	pass, fail := countResults(out, langRust)
	if pass != 7 || fail != 1 {
		t.Errorf("got pass=%d fail=%d want 7,1", pass, fail)
	}
}

func TestCountResults_Pytest(t *testing.T) {
	out := "=========================== 3 passed, 1 failed in 0.12s ===========================\n"
	pass, fail := countResults(out, langPython)
	if pass != 3 || fail != 1 {
		t.Errorf("got pass=%d fail=%d want 3,1", pass, fail)
	}
}

func TestCountResults_NodeMocha(t *testing.T) {
	out := "  5 passing (12ms)\n  2 failing\n"
	pass, fail := countResults(out, langNode)
	if pass != 5 || fail != 2 {
		t.Errorf("got pass=%d fail=%d want 5,2", pass, fail)
	}
}

func TestCountResults_NodeJest(t *testing.T) {
	out := "Tests:       2 failed, 8 passed, 10 total\n"
	pass, fail := countResults(out, langNode)
	if pass != 8 || fail != 2 {
		t.Errorf("got pass=%d fail=%d want 8,2", pass, fail)
	}
}

func TestCountResults_UnknownLang(t *testing.T) {
	p, f := countResults("anything", langUnknown)
	if p != 0 || f != 0 {
		t.Errorf("unknown lang got %d,%d", p, f)
	}
}

func TestFirstIntMatch_NoMatchReturnsZero(t *testing.T) {
	if got := firstIntMatch(goPassLineRE, "nothing here"); got != 0 {
		t.Errorf("got %d want 0", got)
	}
}

func TestFailingTestNames_Go(t *testing.T) {
	out := "--- FAIL: TestAlpha (0.00s)\n--- FAIL: TestBeta (0.00s)\n--- PASS: TestGamma\n"
	names := failingTestNames(out, langGo)
	if len(names) != 2 {
		t.Fatalf("names = %v want 2", names)
	}
	if names[0] != "TestAlpha" || names[1] != "TestBeta" {
		t.Errorf("names = %v", names)
	}
}

func TestFailingTestNames_RustAndPython(t *testing.T) {
	rustOut := "test foo::bar ... FAILED\ntest foo::baz ... FAILED\n"
	if names := failingTestNames(rustOut, langRust); len(names) != 2 {
		t.Errorf("rust names = %v", names)
	}
	pyOut := "FAILED tests/test_x.py::test_a - assert False\nFAILED tests/test_x.py::test_b\n"
	if names := failingTestNames(pyOut, langPython); len(names) != 2 {
		t.Errorf("python names = %v", names)
	}
}

func TestFailingTestNames_UnknownLang(t *testing.T) {
	if got := failingTestNames("x", langUnknown); got != nil {
		t.Errorf("unknown lang should yield nil, got %v", got)
	}
}

func TestFailingTestNames_CapsAt5(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("--- FAIL: TestX (0.00s)\n")
	}
	names := failingTestNames(b.String(), langGo)
	if len(names) > 5 {
		t.Errorf("expected cap at 5, got %d", len(names))
	}
}

func TestHasNpmBuildScript_TruePath(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"x","scripts":{"build":"webpack"}}`
	if err := writeFileTest(filepath.Join(dir, "package.json"), pkg); err != nil {
		t.Fatal(err)
	}
	if !hasNpmBuildScript(dir) {
		t.Error("expected hasNpmBuildScript = true")
	}
}

func TestHasNpmBuildScript_MalformedFalse(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileTest(filepath.Join(dir, "package.json"), "not json"); err != nil {
		t.Fatal(err)
	}
	if hasNpmBuildScript(dir) {
		t.Error("malformed package.json should return false")
	}
}

func TestHasNpmTestScript_TruePath(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"x","scripts":{"test":"jest"}}`
	if err := writeFileTest(filepath.Join(dir, "package.json"), pkg); err != nil {
		t.Fatal(err)
	}
	if !hasNpmTestScript(dir) {
		t.Error("expected hasNpmTestScript = true")
	}
}

func TestHasNpmTestScript_MalformedFalse(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileTest(filepath.Join(dir, "package.json"), "not json"); err != nil {
		t.Fatal(err)
	}
	if hasNpmTestScript(dir) {
		t.Error("malformed package.json should return false")
	}
}

func TestCompilerCommandFor_AllLangs(t *testing.T) {
	dir := t.TempDir()
	// Plain languages get their command from the map without filesystem
	// peeks.
	for _, lang := range []detectedLang{langGo, langRust, langPython} {
		cmd, args, ok := compilerCommandFor(lang, dir)
		if !ok {
			t.Errorf("lang %s should have a build command", lang)
		}
		if cmd == "" || len(args) == 0 {
			t.Errorf("lang %s: cmd=%q args=%v", lang, cmd, args)
		}
	}
	// Node WITHOUT build script: ok=false.
	if _, _, ok := compilerCommandFor(langNode, dir); ok {
		t.Error("node without build script should return ok=false")
	}
	// Unknown lang: ok=false.
	if _, _, ok := compilerCommandFor(langUnknown, dir); ok {
		t.Error("unknown lang should return ok=false")
	}
}

func TestTestCommandFor_AllLangs(t *testing.T) {
	dir := t.TempDir()
	for _, lang := range []detectedLang{langGo, langRust, langPython} {
		cmd, args, ok := testCommandFor(lang, dir)
		if !ok {
			t.Errorf("lang %s should have test cmd", lang)
		}
		if cmd == "" || len(args) == 0 {
			t.Errorf("lang %s: cmd=%q args=%v", lang, cmd, args)
		}
	}
	if _, _, ok := testCommandFor(langNode, dir); ok {
		t.Error("node without test script should return ok=false")
	}
	if _, _, ok := testCommandFor(langUnknown, dir); ok {
		t.Error("unknown lang should return ok=false")
	}
}

func TestScoreFromRatio_BoundariesMore(t *testing.T) {
	cases := []struct {
		ratio float64
		want  int
	}{
		{0.0, 1},
		{0.05, 1}, // very low maps to 1
		{0.5, 6},  // mid
		{1.0, 10},
		{-0.1, 1},  // clamps low
		{1.5, 10},  // clamps high
		{0.95, 10}, // round-up
	}
	for _, tc := range cases {
		got := scoreFromRatio(tc.ratio)
		if got != tc.want {
			t.Errorf("scoreFromRatio(%v) = %d want %d", tc.ratio, got, tc.want)
		}
	}
}

func TestDecisionFromRatio_BoundariesMore(t *testing.T) {
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
	for _, tc := range cases {
		got := decisionFromRatio(tc.ratio)
		if got != tc.want {
			t.Errorf("decisionFromRatio(%v) = %s want %s", tc.ratio, got, tc.want)
		}
	}
}

func TestFormatJudgeModelID_AllCombinations(t *testing.T) {
	cases := []struct {
		p, m string
		want string
	}{
		{"", "", ""},
		{"openai", "", "openai"},
		{"", "gpt-5", "gpt-5"},
		{"openai", "gpt-5", "openai:gpt-5"},
	}
	for _, tc := range cases {
		got := formatJudgeModelID(tc.p, tc.m)
		if got != tc.want {
			t.Errorf("formatJudgeModelID(%q,%q) = %q want %q", tc.p, tc.m, got, tc.want)
		}
	}
}

func TestExtractJSON_NoObjectReturnsEmpty(t *testing.T) {
	if got := extractJSON("nothing here"); got != "" {
		t.Errorf("got %q want empty", got)
	}
}

func TestExtractJSON_BalancedBraceInString(t *testing.T) {
	// Brace inside a string literal must NOT count toward nesting.
	got := extractJSON(`prefix {"text":"a } b","score":5} suffix`)
	if got != `{"text":"a } b","score":5}` {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSON_EscapedBackslash(t *testing.T) {
	// Escape sequences inside strings should leave braces inert.
	got := extractJSON(`prefix {"k":"a\\\"} } b"} suffix`)
	if got == "" {
		t.Error("expected non-empty result on escaped string")
	}
}

func TestExtractJSON_UnbalancedReturnsEmpty(t *testing.T) {
	if got := extractJSON("{ unbalanced"); got != "" {
		t.Errorf("got %q want empty", got)
	}
}

func TestComposeJudgePrompt_IncludesMetadata(t *testing.T) {
	ref := ArtifactRef{
		Kind:    "plan",
		Size:    42,
		AgentID: "child-1",
		SHA256:  "abc123",
	}
	got := composeJudgePrompt(ref, []byte("body content"))
	if !strings.Contains(got, "kind: plan") {
		t.Errorf("missing kind: %s", got)
	}
	if !strings.Contains(got, "size: 42") {
		t.Errorf("missing size: %s", got)
	}
	if !strings.Contains(got, "producer: child-1") {
		t.Errorf("missing producer: %s", got)
	}
	if !strings.Contains(got, "sha256: abc123") {
		t.Errorf("missing sha256: %s", got)
	}
	// Content body must appear too.
	if !strings.Contains(got, "body content") {
		t.Errorf("missing body: %s", got)
	}
}

func TestComposeJudgePrompt_AppendsNewlineWhenMissing(t *testing.T) {
	ref := ArtifactRef{Kind: "x"}
	got := composeJudgePrompt(ref, []byte("no trailing newline"))
	// The body block must end with ```\n; ensure we don't get a missing
	// trailing newline before the fence.
	if !strings.Contains(got, "no trailing newline\n```") {
		t.Errorf("expected appended newline; got %q", got)
	}
}

// --- URL refetcher internals: isHTTPURL, classifyStatuses, fetch flow.

func TestIsHTTPURL_DiscriminatesHTTPSAndOther(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http://x.com", true},
		{"https://x.com", true},
		{"https:/x.com", false}, // missing slash
		{"ftp://x.com", false},
		{"x.com", false},
		{"/path/only", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isHTTPURL(tc.in); got != tc.want {
			t.Errorf("isHTTPURL(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestClassifyStatuses_HealthyAndBroken(t *testing.T) {
	statuses := []urlStatus{
		{URL: "u1", Status: 200},
		{URL: "u2", Status: 301},
		{URL: "u3", Status: 404},
		{URL: "u4", Status: 500},
		{URL: "u5", Status: 0, Err: "network error"},
	}
	healthy, broken := classifyStatuses(statuses)
	if healthy != 2 {
		t.Errorf("healthy = %d want 2", healthy)
	}
	if len(broken) != 3 {
		t.Errorf("broken = %d want 3", len(broken))
	}
}

func TestCloseResponse_NilSafe(t *testing.T) {
	closeResponse(nil)
	closeResponse(&http.Response{})
}

// TestURLRefetcherVerifier_NoURLsAccepts hits the "no URLs in content"
// short-circuit which the existing tests already cover; we additionally
// exercise an end-to-end fetch via a local httptest server so the
// happy-path runs without flakiness.
func TestURLRefetcherVerifier_AllHealthyAccepts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	content := []byte("see " + srv.URL + " for details")
	v := NewURLRefetcherVerifier()
	rep, err := v.Verify(testCtx(), "", content)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Decision != VerificationAccept {
		t.Errorf("decision = %s want accept; report = %+v", rep.Decision, rep)
	}
	if rep.Score != 10 {
		t.Errorf("score = %d want 10", rep.Score)
	}
}

func TestURLRefetcherVerifier_HeadFallsBackToGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	content := []byte("link: " + srv.URL)
	v := NewURLRefetcherVerifier()
	rep, err := v.Verify(testCtx(), "", content)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Decision != VerificationAccept {
		t.Errorf("got %s; report=%+v", rep.Decision, rep)
	}
}

func TestURLRefetcherVerifier_BrokenURLRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	content := []byte("see " + srv.URL)
	v := NewURLRefetcherVerifier()
	rep, _ := v.Verify(testCtx(), "", content)
	if rep.Decision == VerificationAccept {
		t.Errorf("server error should not accept; report=%+v", rep)
	}
	if len(rep.Concerns) == 0 {
		t.Errorf("expected concerns; report=%+v", rep)
	}
}
