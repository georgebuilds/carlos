package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// GitDiffTool runs `git diff [base] -- [path]`. With no args it returns
// the working-tree diff against HEAD (matches what a developer typing
// `git diff HEAD` would see).
//
// Diff output is rarely tiny: 32 KiB cap (4x the default) so a typical
// PR-sized change fits in one turn.
type GitDiffTool struct{}

// NewGitDiffTool constructs the tool.
func NewGitDiffTool() *GitDiffTool { return &GitDiffTool{} }

func (*GitDiffTool) Name() string { return "git_diff" }

func (*GitDiffTool) Description() string {
	return "Run `git diff [base] -- [path]`. Default base is HEAD (working-tree changes). Use when you want to review what changed before describing it back to the user, or to inspect a specific commit (pass base=<sha>~)."
}

func (*GitDiffTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Optional pathspec to scope the diff. Default: whole tree."
			},
			"base": {
				"type": "string",
				"description": "Optional base ref (e.g. HEAD~3, main, sha). Default: HEAD."
			},
			"dir": {
				"type": "string",
				"description": "Optional working directory. Default: agent cwd."
			}
		}
	}`)
}

type gitDiffInput struct {
	Path string `json:"path"`
	Base string `json:"base"`
	Dir  string `json:"dir"`
}

// gitDiffMax is GitDiff's output cap; larger than the default 8 KiB
// because diffs are normally read in full.
const gitDiffMax = 32 * 1024

// Execute composes the args and shells out.
func (*GitDiffTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in gitDiffInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("git_diff: parse input: %w", err)
		}
	}
	if err := requireGitRepo(ctx, in.Dir); err != nil {
		return nil, err
	}
	args := []string{"diff"}
	if in.Base != "" {
		args = append(args, in.Base)
	} else {
		args = append(args, "HEAD")
	}
	if in.Path != "" {
		args = append(args, "--", in.Path)
	}
	out, _, err := runGit(ctx, in.Dir, gitDiffMax, args...)
	return out, err
}

var _ Tool = (*GitDiffTool)(nil)
