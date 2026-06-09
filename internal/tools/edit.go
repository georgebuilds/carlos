package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// EditTool performs an exact-string find-and-replace on a single file.
// Exact-match (no regex) is a deliberate choice: regex edits are easy to
// mis-author and surprisingly hard to bound, and the model can always
// run the `grep` tool first to verify uniqueness before editing.
//
// Validation: the actual match count must equal `expect_match_count`
// (default 1). If they differ, the edit is REFUSED with an error that
// names both numbers - this catches "I thought there was one match but
// there are three" mistakes before they touch the disk.
//
// Binary refusal mirrors ReadTool: a NUL-byte sniff on first 512 bytes
// stops us from accidentally corrupting a binary blob.
type EditTool struct {
	// BaseDir, when non-empty, resolves relative `path` inputs against
	// this directory. Absolute paths are honoured as-is. Used by
	// `carlos please --worktree` so edits land inside the sandbox.
	// Zero-value = current behaviour (cwd-relative).
	BaseDir string
}

// NewEditTool constructs an EditTool. Like WriteTool it carries no
// per-instance state.
func NewEditTool() *EditTool { return &EditTool{} }

func (*EditTool) Name() string { return "edit" }

func (*EditTool) Description() string {
	return "Replace an exact string in a file. Validates that `search` matches exactly `expect_match_count` times (default 1); refuses the edit otherwise. Use when you need to surgically modify an existing file - for whole-file replacement use `write`."
}

func (*EditTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to the file to modify."
			},
			"search": {
				"type": "string",
				"description": "Exact substring to find (no regex). Must be present exactly expect_match_count times."
			},
			"replace": {
				"type": "string",
				"description": "String to substitute for each match."
			},
			"expect_match_count": {
				"type": "integer",
				"description": "Number of matches expected (default 1). Edit is refused if actual count differs."
			}
		},
		"required": ["path", "search", "replace"]
	}`)
}

type editInput struct {
	Path             string `json:"path"`
	Search           string `json:"search"`
	Replace          string `json:"replace"`
	ExpectMatchCount *int   `json:"expect_match_count,omitempty"`
}

// Execute performs the edit and returns a human-readable receipt
// summarising what changed.
func (t *EditTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in editInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("edit: parse input: %w", err)
	}
	if in.Path == "" {
		return nil, errors.New("edit: empty path")
	}
	if in.Search == "" {
		return nil, errors.New("edit: empty search string")
	}
	want := 1
	if in.ExpectMatchCount != nil {
		want = *in.ExpectMatchCount
	}
	if want < 0 {
		return nil, errors.New("edit: expect_match_count must be non-negative")
	}

	path, err := resolveBaseDir(t.BaseDir, in.Path)
	if err != nil {
		return nil, fmt.Errorf("edit: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("edit: open %s: %w", path, err)
	}
	// Same binary heuristic the Read tool uses.
	sniffLen := binarySniffBytes
	if len(data) < sniffLen {
		sniffLen = len(data)
	}
	if isBinary(data[:sniffLen]) {
		return nil, fmt.Errorf("edit: %s: binary file; carlos refuses to modify non-text content", path)
	}

	got := bytes.Count(data, []byte(in.Search))
	if got != want {
		return nil, fmt.Errorf("edit: %s: search matched %d times, expected %d (use grep to verify before editing)", path, got, want)
	}
	if want == 0 {
		// Nothing to do; write nothing, report nothing changed. Returning
		// success here means a defensive precondition check (expect 0
		// matches) won't blow up the calling agent loop.
		return []byte(fmt.Sprintf("no matches to replace in %s\n", path)), nil
	}

	updated := strings.ReplaceAll(string(data), in.Search, in.Replace)
	if updated == string(data) {
		// Should be unreachable given got>0, but guard anyway.
		return []byte(fmt.Sprintf("no changes written to %s\n", path)), nil
	}
	if err := atomicWrite(path, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("edited %s: replaced %d occurrence(s)\n", path, got)), nil
}

var _ Tool = (*EditTool)(nil)
