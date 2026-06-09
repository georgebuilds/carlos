package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadTool returns file contents - optionally restricted to a 1-indexed
// line range - with a 64 KiB output cap. Binary files are refused via a
// null-byte sniff in the first 512 bytes, so the model never gets handed
// a UTF-8-mangled binary blob as "text".
//
// The 64 KiB cap is intentionally larger than BashTool's 8 KiB: file
// reads are a primary investigation path for a coding agent, so we want
// to fit a typical Go file's body without forcing a model to round-trip
// for the next chunk. Truncation is marked clearly so the model knows it
// only saw part of the file.
type ReadTool struct {
	// MaxOutputLen overrides the default 64 KiB cap.
	MaxOutputLen int
	// BaseDir, when non-empty, resolves relative `path` inputs against
	// this directory instead of the process cwd. Absolute paths are
	// honoured as-is. Used by `carlos please --worktree` so the model's
	// reads see the sandbox's view of the world. Zero-value = current
	// behaviour (cwd-relative).
	BaseDir string
}

// NewReadTool constructs a ReadTool with default 64 KiB cap.
func NewReadTool() *ReadTool { return &ReadTool{} }

func (*ReadTool) Name() string { return "read" }

func (*ReadTool) Description() string {
	return "Read a text file's contents, optionally restricted to a 1-indexed line range. Output is capped at 64 KiB; binary files are refused. Use when you need to inspect code, configuration, or any text artifact - prefer this over `bash cat` so output is properly framed and truncation is explicit."
}

func (*ReadTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Absolute or working-directory-relative path to the file to read."
			},
			"start_line": {
				"type": "integer",
				"description": "Optional 1-indexed first line to include. Default: 1."
			},
			"end_line": {
				"type": "integer",
				"description": "Optional 1-indexed last line to include (inclusive). Default: end of file."
			}
		},
		"required": ["path"]
	}`)
}

type readInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

const defaultReadCap = 64 * 1024

// binarySniffBytes is the leading window we inspect for null bytes. 512
// is what git uses for the same heuristic; if a file looks like text in
// its first 512 bytes it almost always is (the rare exception is a UTF-16
// file with a BOM beyond 512 - out of scope for v0).
const binarySniffBytes = 512

// Execute reads the file and returns its content (range-limited if asked,
// truncated at the output cap if needed). Errors returned cover parse,
// stat, and binary-refusal cases; "file doesn't exist" surfaces as an
// error so the agent can react.
func (t *ReadTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in readInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("read: parse input: %w", err)
	}
	if in.Path == "" {
		return nil, errors.New("read: empty path")
	}
	if in.StartLine < 0 || in.EndLine < 0 {
		return nil, errors.New("read: line numbers must be non-negative")
	}
	if in.StartLine > 0 && in.EndLine > 0 && in.EndLine < in.StartLine {
		return nil, errors.New("read: end_line < start_line")
	}

	cap := t.MaxOutputLen
	if cap <= 0 {
		cap = defaultReadCap
	}

	path, err := resolveBaseDir(t.BaseDir, in.Path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read: open %s: %w", path, err)
	}
	defer f.Close()

	// Binary sniff: read up to 512 bytes, scan for NUL. We then rewind
	// (via Seek) so the actual read starts from the top.
	sniff := make([]byte, binarySniffBytes)
	n, _ := io.ReadFull(f, sniff)
	if isBinary(sniff[:n]) {
		return nil, fmt.Errorf("read: %s: binary file; carlos refuses to read non-text content", path)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("read: rewind %s: %w", path, err)
	}

	var out bytes.Buffer
	br := bufio.NewReader(f)
	// Use scanner so we can apply the line range cheaply. A typical
	// source file is <10k lines; even at 100k lines, scan + skip is
	// microseconds.
	scanner := bufio.NewScanner(br)
	// Allow up to 1 MiB per line so generated/minified files don't crash
	// the scanner - they will still get truncated by the output cap.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	truncated := false
	for scanner.Scan() {
		lineNum++
		if in.StartLine > 0 && lineNum < in.StartLine {
			continue
		}
		if in.EndLine > 0 && lineNum > in.EndLine {
			break
		}
		line := scanner.Bytes()
		// Reserve 1 byte for the '\n' below; check the would-be size.
		if out.Len()+len(line)+1 > cap {
			truncated = true
			break
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read: scan %s: %w", path, err)
	}
	if truncated {
		fmt.Fprintf(&out, "[truncated at %d bytes - file is larger]\n", cap)
	}
	return out.Bytes(), nil
}

// isBinary reports whether b appears to be binary content. Any NUL byte
// in the sniff window is a strong signal (per git's textconv heuristic).
func isBinary(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

var _ Tool = (*ReadTool)(nil)
