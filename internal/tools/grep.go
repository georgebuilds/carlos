package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// GrepTool recursively searches files under `root` for `pattern`. Default
// is literal substring search; `regex: true` switches to Go regexp.
// Results are formatted `path:line:text` (ripgrep-ish) and capped at 100
// hits to keep the model's context window bounded.
//
// By default grep respects .gitignore (uses the same Ignorer the Glob
// tool does). This matters: a coding agent that grep-walks node_modules
// or vendor/ on every search burns a huge amount of latency and easily
// finds false positives. Set `respect_gitignore: false` to override.
type GrepTool struct {
	// BaseDir, when non-empty, is used as the default `root` when the
	// model omits one (instead of cwd). When the model passes an
	// explicit relative `root`, it's resolved against BaseDir.
	// Absolute roots are honoured as-is. Used by `carlos please
	// --worktree` to keep searches inside the sandbox. Zero-value =
	// current behaviour (cwd-relative).
	BaseDir string
}

// NewGrepTool constructs a GrepTool.
func NewGrepTool() *GrepTool { return &GrepTool{} }

func (*GrepTool) Name() string { return "grep" }

func (*GrepTool) Description() string {
	return "Recursively search files under `root` (default cwd) for `pattern`. Default is literal substring; set regex=true for Go regexp syntax. Results format: path:line:text, capped at 100 hits. Honours .gitignore by default; set respect_gitignore=false to override. Use when you need to locate symbols, error strings, or any literal across the repo."
}

func (*GrepTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Search pattern (literal substring by default, regex when regex=true)."
			},
			"root": {
				"type": "string",
				"description": "Root directory to recurse from. Default: current working directory."
			},
			"include": {
				"type": "string",
				"description": "Optional glob restricting the files searched (e.g. \"*.go\"). Matches against the basename."
			},
			"regex": {
				"type": "boolean",
				"description": "When true, pattern is parsed as a Go regexp. Default: false (literal substring)."
			},
			"respect_gitignore": {
				"type": "boolean",
				"description": "When true (default), .gitignore is honoured."
			}
		},
		"required": ["pattern"]
	}`)
}

type grepInput struct {
	Pattern          string `json:"pattern"`
	Root             string `json:"root"`
	Include          string `json:"include"`
	Regex            bool   `json:"regex"`
	RespectGitignore *bool  `json:"respect_gitignore,omitempty"`
}

// maxGrepHits bounds the number of result lines returned. 100 is enough
// for "show me where X is defined" while still small enough to render in
// a single conversation turn.
const maxGrepHits = 100

// maxGrepLineLen truncates absurdly long lines (minified bundles etc).
// The first 512 chars are normally plenty to tell a hit from a miss.
const maxGrepLineLen = 512

// Execute walks the requested tree, matches per-line, and renders the
// hits. Respects ctx cancel between files (the WalkDir callback bails
// when ctx is done).
func (t *GrepTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in grepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("grep: parse input: %w", err)
	}
	if in.Pattern == "" {
		return nil, errors.New("grep: empty pattern")
	}
	root := in.Root
	if root == "" {
		if t.BaseDir != "" {
			root = t.BaseDir
		} else {
			var err error
			root, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("grep: getwd: %w", err)
			}
		}
	} else {
		root = resolveBaseDir(t.BaseDir, root)
	}

	respect := true
	if in.RespectGitignore != nil {
		respect = *in.RespectGitignore
	}

	var matcher func([]byte) bool
	if in.Regex {
		re, err := regexp.Compile(in.Pattern)
		if err != nil {
			return nil, fmt.Errorf("grep: compile regex: %w", err)
		}
		matcher = re.Match
	} else {
		needle := []byte(in.Pattern)
		matcher = func(line []byte) bool { return bytes.Contains(line, needle) }
	}

	var ig Ignorer
	if respect {
		var err error
		ig, err = LoadIgnorer(root)
		if err != nil {
			return nil, fmt.Errorf("grep: load gitignore: %w", err)
		}
	}

	var out bytes.Buffer
	hits := 0
	truncated := false

	walk := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Permission-denied or stat-failed entries are skipped
			// rather than aborting the whole grep.
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if in.Include != "" {
			matched, mErr := filepath.Match(in.Include, filepath.Base(path))
			if mErr != nil || !matched {
				return nil
			}
		}
		// Respect ctx between files; we don't pre-empt mid-file because
		// a single file scan is bounded by its size and cheap.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if hits >= maxGrepHits {
			truncated = true
			return fs.SkipAll
		}
		grepFile(path, matcher, &out, &hits)
		return nil
	}

	var walkErr error
	if respect {
		walkErr = WalkRespectingGitignore(root, ig, walk)
	} else {
		walkErr = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return walk(path, nil, err)
			}
			// Even without gitignore we still skip .git so we never
			// recurse into pack files.
			if d.IsDir() && d.Name() == ".git" && path != root {
				return fs.SkipDir
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return walk(path, nil, infoErr)
			}
			return walk(path, info, nil)
		})
	}
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		// Surface ctx cancel as an infra error so the agent loop can
		// react; everything else has been swallowed inside walk.
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return nil, walkErr
		}
	}

	if truncated {
		fmt.Fprintf(&out, "\n[truncated at %d hits]\n", maxGrepHits)
	}
	if hits == 0 {
		out.WriteString("(no matches)\n")
	}
	return out.Bytes(), nil
}

// grepFile scans one file line-by-line, writing any hits in `path:N:txt`
// form into out. Increments *hits per match (caller enforces the cap).
// Files that fail to open are silently skipped - a coding agent doesn't
// need a flood of "permission denied" entries cluttering its view.
func grepFile(path string, match func([]byte) bool, out *bytes.Buffer, hits *int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// Cheap binary sniff: if the first 512 bytes look binary, skip the
	// file. We don't want to dump base64-looking noise from a .pdf or
	// .png into the model's view.
	sniff := make([]byte, binarySniffBytes)
	n, _ := f.Read(sniff)
	if isBinary(sniff[:n]) {
		return
	}
	// Rewind to start so we scan the whole file (including the sniff).
	if _, err := f.Seek(0, 0); err != nil {
		return
	}

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for s.Scan() {
		lineNo++
		line := s.Bytes()
		if !match(line) {
			continue
		}
		text := line
		if len(text) > maxGrepLineLen {
			text = text[:maxGrepLineLen]
		}
		// Replace any trailing CR for Windows-formatted files.
		text = bytes.TrimRight(text, "\r")
		fmt.Fprintf(out, "%s:%d:%s\n", path, lineNo, text)
		*hits++
		if *hits >= maxGrepHits {
			return
		}
	}
}

var _ Tool = (*GrepTool)(nil)
