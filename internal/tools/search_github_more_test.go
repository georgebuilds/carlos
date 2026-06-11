package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestParseGHResults_EmptyAndNull — gh emits "[]" or "null" for an empty
// result set; both yield a clean zero-length slice.
func TestParseGHResults_EmptyAndNull(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", "   "} {
		got, err := parseGHResults("code", []byte(raw))
		if err != nil {
			t.Errorf("parseGHResults(%q) err = %v", raw, err)
		}
		if len(got) != 0 {
			t.Errorf("parseGHResults(%q) = %d rows, want 0", raw, len(got))
		}
	}
}

// TestParseGHResults_UnsupportedKind — a kind that slipped past upstream
// validation hits the defensive default branch.
func TestParseGHResults_UnsupportedKind(t *testing.T) {
	if _, err := parseGHResults("gists", []byte(`[{}]`)); err == nil ||
		!strings.Contains(err.Error(), "unsupported kind") {
		t.Errorf("want unsupported-kind error, got %v", err)
	}
}

// TestParseGHResults_Dispatch — each kind decodes into normalised rows
// with 1-indexed ranks and the right fields populated.
func TestParseGHResults_Dispatch(t *testing.T) {
	code, err := parseGHResults("code", []byte(`[
		{"path":"a.go","url":"https://x/a","repository":{"nameWithOwner":"o/r"},
		 "textMatches":[{"fragment":"  func A() {}  "}]}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 1 || code[0].Title != "a.go" || code[0].Repo != "o/r" ||
		code[0].Snippet != "func A() {}" || code[0].Rank != 1 {
		t.Errorf("code row wrong: %+v", code[0])
	}

	repos, err := parseGHResults("repos", []byte(`[
		{"fullName":"o/r","description":"d","url":"https://x/r","stargazersCount":42}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].Title != "o/r" || repos[0].Snippet != "d" ||
		repos[0].Extra["stars"] != 42 {
		t.Errorf("repo row wrong: %+v", repos[0])
	}

	issues, err := parseGHResults("issues", []byte(`[
		{"title":"bug","url":"https://x/i","state":"open","number":7,
		 "repository":{"nameWithOwner":"o/r"}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if issues[0].Title != "bug" || issues[0].Snippet != "open #7" {
		t.Errorf("issue row wrong: %+v", issues[0])
	}
}

// TestParseGHResults_MalformedJSON — each kind surfaces a decode error
// for non-array JSON.
func TestParseGHResults_MalformedJSON(t *testing.T) {
	for _, kind := range []string{"code", "repos", "issues", "prs"} {
		if _, err := parseGHResults(kind, []byte(`{"not":"an array"}`)); err == nil {
			t.Errorf("kind %q: expected decode error on object payload", kind)
		}
	}
}

// TestGHSearch_RunnerError — a runner failure (e.g. gh not authenticated)
// surfaces through Execute rather than being swallowed.
func TestGHSearch_RunnerError(t *testing.T) {
	r := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return nil, errors.New("gh: authentication required")
	}}
	tool := &GHSearchTool{Runner: r}
	raw := []byte(`{"query":"x","kind":"code"}`)
	if _, err := tool.Execute(context.Background(), raw); err == nil ||
		!strings.Contains(err.Error(), "authentication") {
		t.Errorf("want runner error to surface, got %v", err)
	}
}

// TestGHSearch_MalformedRunnerOutput — non-JSON output from gh surfaces a
// decode error.
func TestGHSearch_MalformedRunnerOutput(t *testing.T) {
	r := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return []byte("this is not json"), nil
	}}
	tool := &GHSearchTool{Runner: r}
	raw := []byte(`{"query":"x","kind":"code"}`)
	if _, err := tool.Execute(context.Background(), raw); err == nil {
		t.Error("expected a decode error for malformed gh output")
	}
}

// TestGHSearch_NilRunner — a tool with no Runner reports a clear error
// rather than dereferencing a nil interface.
func TestGHSearch_NilRunner(t *testing.T) {
	tool := &GHSearchTool{}
	if _, err := tool.Execute(context.Background(), []byte(`{"query":"x"}`)); err == nil ||
		!strings.Contains(err.Error(), "no runner") {
		t.Errorf("want no-runner error, got %v", err)
	}
}

// TestGHSearch_BatchedQueries — the batched `queries` form returns one
// block per query with results parsed per call.
func TestGHSearch_BatchedQueries(t *testing.T) {
	r := &fakeGHRunner{Respond: func(args []string) ([]byte, error) {
		return []byte(fakeGHReposJSON), nil
	}}
	tool := &GHSearchTool{Runner: r}
	raw, _ := json.Marshal(map[string]any{
		"queries": []string{"alpha", "beta"},
		"kind":    "repos",
	})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("batched gh_search: %v", err)
	}
	var resp ghSearchBatchedOutput
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Backend != "github" || resp.Kind != "repos" {
		t.Errorf("envelope = %+v", resp)
	}
	if len(resp.Blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(resp.Blocks))
	}
	for _, b := range resp.Blocks {
		if b.Error != "" || b.Count == 0 {
			t.Errorf("block %q should have parsed results: %+v", b.Query, b)
		}
	}
}

// TestRealGHRunner_ErrorPath exercises realGHRunner against the real gh
// binary using a bogus subcommand so the call exits non-zero and the
// stderr-bearing error is produced. Skipped when gh is not installed.
func TestRealGHRunner_ErrorPath(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not on PATH")
	}
	_, err := realGHRunner{}.Run(context.Background(), []string{"definitely-not-a-real-subcommand-xyz"})
	if err == nil {
		t.Fatal("expected an error from a bogus gh subcommand")
	}
	if !strings.Contains(err.Error(), "gh:") {
		t.Errorf("error should be wrapped with gh prefix: %v", err)
	}
}
