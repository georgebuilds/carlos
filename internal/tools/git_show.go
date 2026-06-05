package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// GitShowTool runs `git show <ref> [-- <path>]`. Output is capped at
// 32 KiB (same as GitDiff — `git show` is effectively a diff plus a
// commit header).
type GitShowTool struct{}

// NewGitShowTool constructs the tool.
func NewGitShowTool() *GitShowTool { return &GitShowTool{} }

func (*GitShowTool) Name() string { return "git_show" }

func (*GitShowTool) Description() string {
	return "Run `git show <ref> [-- <path>]`. Use to view a commit's full message and diff, or to read a file's contents at a specific ref (e.g. ref=main:path/to/file)."
}

func (*GitShowTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"ref": {
				"type": "string",
				"description": "Git ref: sha, branch, tag, or expression like main:path/to/file."
			},
			"path": {
				"type": "string",
				"description": "Optional pathspec to limit the diff to."
			},
			"dir": {
				"type": "string",
				"description": "Optional working directory. Default: agent cwd."
			}
		},
		"required": ["ref"]
	}`)
}

type gitShowInput struct {
	Ref  string `json:"ref"`
	Path string `json:"path"`
	Dir  string `json:"dir"`
}

// Execute defers all ref validation to git; an invalid ref shows up as
// a non-zero exit in the output, which the model can read directly.
func (*GitShowTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in gitShowInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("git_show: parse input: %w", err)
	}
	if in.Ref == "" {
		return nil, errors.New("git_show: empty ref")
	}
	if err := requireGitRepo(ctx, in.Dir); err != nil {
		return nil, err
	}
	args := []string{"show", in.Ref}
	if in.Path != "" {
		args = append(args, "--", in.Path)
	}
	out, _, err := runGit(ctx, in.Dir, gitDiffMax, args...)
	return out, err
}

var _ Tool = (*GitShowTool)(nil)
