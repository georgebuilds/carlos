package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// GlobTool returns paths matching a glob pattern, honouring `**`
// recursion (which stdlib `filepath.Glob` does NOT support - hence the
// custom walker). Like GrepTool, it respects .gitignore by default.
//
// Patterns are interpreted relative to `root` (default cwd). The full
// pattern syntax is the same one used by the gitignore matcher: `*`, `?`,
// `**`. Anchoring rules are simplified - a pattern without a leading `/`
// is treated as a "match anywhere" wildcard, while a leading `/` anchors
// it to root.
type GlobTool struct {
	// BaseDir, when non-empty, is the default `root` when the model
	// omits one (instead of cwd). An explicit relative `root` resolves
	// against BaseDir; absolute roots are honoured as-is. Used by
	// `carlos please --worktree` to keep listings inside the sandbox.
	// Zero-value = current behaviour (cwd-relative).
	BaseDir string
}

// NewGlobTool constructs a GlobTool.
func NewGlobTool() *GlobTool { return &GlobTool{} }

func (*GlobTool) Name() string { return "glob" }

func (*GlobTool) Description() string {
	return "List paths matching a glob pattern. Supports `*`, `?`, and `**` (recursive). Honours .gitignore by default. Use when you need to enumerate files by name pattern (\"all Go test files\", \"every JSON config\"). For *content* search use `grep`."
}

func (*GlobTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Glob pattern. Supports *, ?, ** (recursive). Examples: \"*.go\", \"**/*_test.go\", \"src/**/main.go\"."
			},
			"root": {
				"type": "string",
				"description": "Root directory to glob from. Default: current working directory."
			},
			"respect_gitignore": {
				"type": "boolean",
				"description": "When true (default), .gitignore is honoured."
			}
		},
		"required": ["pattern"]
	}`)
}

type globInput struct {
	Pattern          string `json:"pattern"`
	Root             string `json:"root"`
	RespectGitignore *bool  `json:"respect_gitignore,omitempty"`
}

const maxGlobResults = 1000

// Execute walks the tree (gitignore-aware by default) and returns each
// path that matches `pattern`. Output is one path per line; capped at
// 1000 entries with a truncation marker. The cap is generous because
// listings are cheaper context-wise than file contents.
func (t *GlobTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in globInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("glob: parse input: %w", err)
	}
	if in.Pattern == "" {
		return nil, errors.New("glob: empty pattern")
	}
	root := in.Root
	if root == "" {
		if t.BaseDir != "" {
			root = t.BaseDir
		} else {
			var err error
			root, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("glob: getwd: %w", err)
			}
		}
	} else {
		resolved, err := resolveBaseDir(t.BaseDir, root)
		if err != nil {
			return nil, fmt.Errorf("glob: %w", err)
		}
		root = resolved
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("glob: abs %s: %w", root, err)
	}

	respect := true
	if in.RespectGitignore != nil {
		respect = *in.RespectGitignore
	}

	// Anchored vs not - leading `/` means "match from root", otherwise
	// treat as match-anywhere by prepending `**/` (gitignore-style).
	pat := in.Pattern
	anchored := false
	if len(pat) > 0 && pat[0] == '/' {
		anchored = true
		pat = pat[1:]
	}

	var ig Ignorer
	if respect {
		ig, err = LoadIgnorer(absRoot)
		if err != nil {
			return nil, fmt.Errorf("glob: load gitignore: %w", err)
		}
	}

	var out bytes.Buffer
	count := 0
	truncated := false

	visit := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // permission denied etc.; skip.
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if path == absRoot {
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		var matched bool
		if anchored {
			matched = matchGlob(pat, relSlash)
		} else {
			// Try matching pattern against full rel; if that fails,
			// also try every suffix path (this gives the "match
			// anywhere" feel `**/x` does).
			if matchGlob(pat, relSlash) {
				matched = true
			} else if matchGlob("**/"+pat, relSlash) {
				matched = true
			}
		}
		if !matched {
			return nil
		}
		if count >= maxGlobResults {
			truncated = true
			return fs.SkipAll
		}
		out.WriteString(relSlash)
		if info != nil && info.IsDir() {
			out.WriteByte('/')
		}
		out.WriteByte('\n')
		count++
		return nil
	}

	var walkErr error
	if respect {
		walkErr = WalkRespectingGitignore(absRoot, ig, visit)
	} else {
		walkErr = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return visit(path, nil, err)
			}
			if d.IsDir() && d.Name() == ".git" && path != absRoot {
				return fs.SkipDir
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return visit(path, nil, infoErr)
			}
			return visit(path, info, nil)
		})
	}
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return nil, walkErr
		}
	}

	if truncated {
		fmt.Fprintf(&out, "\n[truncated at %d results]\n", maxGlobResults)
	}
	if count == 0 {
		out.WriteString("(no matches)\n")
	}
	return out.Bytes(), nil
}

var _ Tool = (*GlobTool)(nil)
