// Phase 5 slice 5d — language/toolchain detection for tool-grounded
// verifier adapters.
//
// The Compiler and TestRunner adapters need to pick the right canonical
// build/test command for the project living under workdir. We detect by
// project marker files (go.mod, package.json, Cargo.toml, pyproject.toml,
// etc.) — same approach as every other "polyglot project detector"
// because it's the only signal that survives across repos that share the
// same file extensions in different ecosystems.
//
// Detection is shallow (workdir top-level only) on purpose. A monorepo
// with multiple sub-projects is out of scope for v0 — the foreground
// integrator can call Verify per worktree if it cares to break a repo
// down. Mirrors the discipline in internal/sandbox/worktree.go which
// also pins one project per worktree.
package agent

import (
	"os"
	"path/filepath"
)

// detectedLang names a project's primary toolchain. Strings are stable;
// VerificationReport.JudgeModel embeds them as "compiler:<lang>" /
// "tests:<lang>" so the queue UI can show which adapter ran.
type detectedLang string

const (
	langUnknown detectedLang = ""
	langGo      detectedLang = "go"
	langNode    detectedLang = "node"
	langRust    detectedLang = "rust"
	langPython  detectedLang = "python"
)

// projectMarker maps a top-level file (or directory) name to the
// language that owns it. Order in detectLanguage matters when multiple
// markers coexist (e.g. a Node frontend in a Go repo) — Go wins by
// virtue of being checked first, mirroring the carlos repo's own
// convention.
var projectMarker = map[string]detectedLang{
	"go.mod":         langGo,
	"package.json":   langNode,
	"Cargo.toml":     langRust,
	"pyproject.toml": langPython,
	"setup.py":       langPython,
	"pytest.ini":     langPython,
}

// detectLanguage returns the first matched language for workdir, or
// langUnknown if none of the markers exist. We walk markers in a fixed
// priority order so the result is deterministic.
//
// Priority: Go → Rust → Python → Node. Go and Rust are typed/compiled
// languages where a build pass is a strong signal; Node and Python are
// dynamic, so we deprioritize them — but we still try them if no
// compiled-language marker is present.
func detectLanguage(workdir string) detectedLang {
	priority := []struct {
		marker string
		lang   detectedLang
	}{
		{"go.mod", langGo},
		{"Cargo.toml", langRust},
		{"pyproject.toml", langPython},
		{"setup.py", langPython},
		{"pytest.ini", langPython},
		{"package.json", langNode},
	}
	for _, p := range priority {
		if fileExists(filepath.Join(workdir, p.marker)) {
			return p.lang
		}
	}
	return langUnknown
}

// fileExists reports whether path exists as a regular file or directory.
// Errors are swallowed (treated as non-existent) — the detector is
// best-effort and a stat failure on a marker path is functionally the
// same as the marker being absent.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
