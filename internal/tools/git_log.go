package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// GitLogTool runs `git log --oneline -n <limit> [-- <path>]`. The
// oneline format gives sha + subject - enough for a model to navigate
// commits without flooding context with bodies. For full commit text
// use GitShow with the sha.
type GitLogTool struct{}

// NewGitLogTool constructs the tool.
func NewGitLogTool() *GitLogTool { return &GitLogTool{} }

func (*GitLogTool) Name() string { return "git_log" }

func (*GitLogTool) Description() string {
	return "Run `git log --oneline -n <limit> [-- <path>]`. Returns sha+subject pairs (one per line). Default limit is 20. Use to enumerate recent commits, optionally scoped to a path."
}

func (*GitLogTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Optional pathspec to filter commits to those touching this path."
			},
			"limit": {
				"type": "integer",
				"description": "Max number of commits to return (default 20, max 200)."
			},
			"dir": {
				"type": "string",
				"description": "Optional working directory. Default: agent cwd."
			}
		}
	}`)
}

type gitLogInput struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
	Dir   string `json:"dir"`
}

// Execute caps `limit` at 200 to prevent an unbounded model request
// from pulling thousands of lines into context.
func (*GitLogTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in gitLogInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("git_log: parse input: %w", err)
		}
	}
	if err := requireGitRepo(ctx, in.Dir); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	args := []string{"log", "--oneline", fmt.Sprintf("-n%d", limit)}
	if in.Path != "" {
		args = append(args, "--", in.Path)
	}
	out, _, err := runGit(ctx, in.Dir, gitMaxOutputBytes, args...)
	return out, err
}

var _ Tool = (*GitLogTool)(nil)
