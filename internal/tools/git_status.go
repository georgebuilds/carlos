package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// GitStatusTool runs `git status --porcelain=v2 --branch` and returns
// the (capped) output. Porcelain v2 is stable and trivial for the model
// to parse line-by-line, unlike the human-readable `git status` which
// changes wording between git versions.
//
// All git tools take an optional `dir` so the user can target a
// non-default working directory; absent it, git runs in the agent's cwd
// (which for sub-agents is the worktree backend's dir).
type GitStatusTool struct{}

// NewGitStatusTool constructs the tool with no state.
func NewGitStatusTool() *GitStatusTool { return &GitStatusTool{} }

func (*GitStatusTool) Name() string { return "git_status" }

func (*GitStatusTool) Description() string {
	return "Run `git status --porcelain=v2 --branch` in the repo. Output is the machine-readable v2 porcelain format (stable across git versions). Use when you need to know what's modified, staged, or untracked."
}

func (*GitStatusTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"dir": {
				"type": "string",
				"description": "Optional working directory. Default: agent cwd."
			}
		}
	}`)
}

type gitStatusInput struct {
	Dir string `json:"dir"`
}

// Execute checks that we're in a repo and shells out.
func (*GitStatusTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in gitStatusInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("git_status: parse input: %w", err)
		}
	}
	if err := requireGitRepo(ctx, in.Dir); err != nil {
		return nil, err
	}
	out, _, err := runGit(ctx, in.Dir, gitMaxOutputBytes, "status", "--porcelain=v2", "--branch")
	return out, err
}

var _ Tool = (*GitStatusTool)(nil)
