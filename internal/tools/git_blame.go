package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// GitBlameTool runs `git blame [-L start,end] <path>`. A path that has
// no history (untracked or never committed) is refused with a clear
// error so the model doesn't waste a turn on an empty result.
type GitBlameTool struct{}

// NewGitBlameTool constructs the tool.
func NewGitBlameTool() *GitBlameTool { return &GitBlameTool{} }

func (*GitBlameTool) Name() string { return "git_blame" }

func (*GitBlameTool) Description() string {
	return "Run `git blame [-L start,end] <path>`. Use to find which commit last touched a given line range. Refuses paths with no git history."
}

func (*GitBlameTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to blame."
			},
			"start_line": {
				"type": "integer",
				"description": "Optional 1-indexed first line. Default: 1."
			},
			"end_line": {
				"type": "integer",
				"description": "Optional 1-indexed last line. Default: end of file."
			},
			"dir": {
				"type": "string",
				"description": "Optional working directory. Default: agent cwd."
			}
		},
		"required": ["path"]
	}`)
}

type gitBlameInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Dir       string `json:"dir"`
}

// Execute first verifies the path is tracked (git log --oneline -n1).
// An empty log means no commits touch the path → blame would be empty
// or error; we return an explicit error so the model can pick a
// different approach.
func (*GitBlameTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in gitBlameInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("git_blame: parse input: %w", err)
	}
	if in.Path == "" {
		return nil, errors.New("git_blame: empty path")
	}
	if in.StartLine < 0 || in.EndLine < 0 {
		return nil, errors.New("git_blame: line numbers must be non-negative")
	}
	if in.StartLine > 0 && in.EndLine > 0 && in.EndLine < in.StartLine {
		return nil, errors.New("git_blame: end_line < start_line")
	}
	if err := requireGitRepo(ctx, in.Dir); err != nil {
		return nil, err
	}
	// Sanity-check the path has history.
	logOut, logExit, logErr := runGit(ctx, in.Dir, 1024, "log", "--oneline", "-n1", "--", in.Path)
	if logErr != nil {
		return nil, fmt.Errorf("git_blame: log probe: %w", logErr)
	}
	if logExit != 0 || len(bytes.TrimSpace(logOut)) == 0 {
		return nil, fmt.Errorf("git_blame: %s has no git history", in.Path)
	}

	args := []string{"blame"}
	if in.StartLine > 0 || in.EndLine > 0 {
		s := in.StartLine
		if s == 0 {
			s = 1
		}
		if in.EndLine > 0 {
			args = append(args, fmt.Sprintf("-L%d,%d", s, in.EndLine))
		} else {
			args = append(args, fmt.Sprintf("-L%d,+1000", s))
		}
	}
	args = append(args, "--", in.Path)
	out, _, err := runGit(ctx, in.Dir, gitMaxOutputBytes, args...)
	return out, err
}

var _ Tool = (*GitBlameTool)(nil)
