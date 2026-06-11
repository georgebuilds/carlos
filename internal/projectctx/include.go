package projectctx

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// includeRe matches a Claude-Code-style `@<path>` directive.
//
// Conservative rules:
//   - Must be at the start of a line (after optional whitespace).
//   - `@` is followed by a non-whitespace path; the path runs until
//     end-of-line OR the next whitespace.
//   - Email-like tokens (`@example.com`) are rejected by requiring the
//     path to look path-ish: it must contain `/`, start with `.` or `~`,
//     OR end with a known doc extension (.md/.txt). Otherwise we'd
//     mangle prose that happens to mention `@somebody`.
//
// We DO NOT match `@<path>` inside fenced code blocks (``` ... ```) -
// that detection happens in expandIncludes line-by-line, not in this
// regex.
var includeRe = regexp.MustCompile(`^\s*@(\S+)\s*$`)

// includeWarn is a single include-expansion problem.
//
// user is the human-facing line surfaced on Context.Warnings (and from
// there into the TUI). inContext reports whether expandIncludes also
// left a stub in the expanded content for this failure: a missing target
// is surfaced to the user but deliberately kept OUT of the model context
// (it is almost always a stale local pointer the model can't act on, e.g.
// a CLAUDE.md that says @AGENTS.md when no AGENTS.md was ever created), so
// it sets inContext=false; an unreadable-but-present include keeps its
// stub so the model still sees the gap.
type includeWarn struct {
	user      string
	inContext bool
}

// expandIncludes recursively resolves `@<path>` directives in content.
//
// baseDir is the directory of the file that wrote the include - paths
// are resolved relative to it (matches Claude Code semantics). fromFile
// is the display name of the file being expanded, used to phrase
// warnings ("<fromFile> references <target>, which doesn't exist"). `~`
// is home-expanded. depth/maxDepth bound recursion. seenPaths is the
// resolution stack used for cycle detection: an include of a path
// already on the stack is replaced with
// "[project context: cycle detected - <path>]" rather than re-expanded.
//
// Returned warnings are advisory; callers can log them but the expanded
// content is always safe to use (failed includes are either replaced
// inline with descriptive stubs or, for missing targets, dropped).
func expandIncludes(content, baseDir, fromFile string, depth, maxDepth int, seenPaths map[string]bool) (string, []includeWarn) {
	if depth > maxDepth {
		return fmt.Sprintf("[project context: include depth %d exceeds cap %d]", depth, maxDepth), nil
	}

	var warnings []includeWarn
	var out strings.Builder
	inFence := false

	for _, line := range splitKeepNewlines(content) {
		// Toggle fence state on lines that START with ``` (optional lang).
		// We deliberately don't try to parse tildes (~~~) - markdown
		// parsers vary; the conservative choice is to only honor ``` and
		// accept that ~~~ blocks may have @-paths expanded. Real-world
		// AGENTS.md files universally use ```.
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out.WriteString(line)
			continue
		}
		if inFence {
			out.WriteString(line)
			continue
		}

		m := includeRe.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil || !looksLikePath(m[1]) {
			out.WriteString(line)
			continue
		}

		incPath := resolveIncludePath(m[1], baseDir)
		if seenPaths[incPath] {
			out.WriteString(fmt.Sprintf("[project context: cycle detected - %s]\n", incPath))
			continue
		}
		data, err := os.ReadFile(incPath)
		if err != nil {
			if os.IsNotExist(err) {
				// Missing target: surface a clear, user-facing warning but
				// keep it OUT of the model context. Drop the @line entirely
				// instead of writing a stub - the model can't act on a stale
				// local pointer, and the user sees it in the TUI instead.
				warnings = append(warnings, includeWarn{
					user:      fmt.Sprintf("Your %s references %s, which doesn't exist", fromFile, m[1]),
					inContext: false,
				})
				continue
			}
			// Present but unreadable (permissions, a directory, IO): keep
			// the inline stub so the model still sees the gap, and surface
			// the detail to the user.
			stub := fmt.Sprintf("[project context: include failed - %s: %v]\n", m[1], err)
			out.WriteString(stub)
			warnings = append(warnings, includeWarn{
				user:      fmt.Sprintf("%s includes %s, which couldn't be read: %v", fromFile, m[1], err),
				inContext: true,
			})
			continue
		}

		// Push, recurse, pop - pop is essential so siblings don't
		// poison each other (only a true cycle along the current
		// resolution path should trip the guard).
		seenPaths[incPath] = true
		expanded, subWarnings := expandIncludes(string(data), filepath.Dir(incPath), filepath.Base(incPath), depth+1, maxDepth, seenPaths)
		delete(seenPaths, incPath)

		out.WriteString(expanded)
		if !strings.HasSuffix(expanded, "\n") {
			out.WriteString("\n")
		}
		warnings = append(warnings, subWarnings...)
	}

	return out.String(), warnings
}

// resolveIncludePath expands ~ and turns a relative path into an absolute
// one rooted at baseDir. Already-absolute paths pass through unchanged.
func resolveIncludePath(p, baseDir string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			// Handles both "~" and "~/foo" - strip the leading "~" then
			// join. "~user" syntax is not supported (rare in practice and
			// Go's stdlib doesn't provide a cross-platform resolver).
			rest := strings.TrimPrefix(p, "~")
			rest = strings.TrimPrefix(rest, "/")
			return filepath.Join(home, rest)
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}

// looksLikePath filters out `@username` / `@example.com` prose that
// shouldn't be treated as an include. The heuristic: a real include
// either contains a `/`, starts with `.` or `~`, or ends with a markdown
// doc extension.
func looksLikePath(p string) bool {
	if strings.ContainsRune(p, '/') {
		return true
	}
	if strings.HasPrefix(p, ".") || strings.HasPrefix(p, "~") {
		return true
	}
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".txt") || strings.HasSuffix(lower, ".markdown")
}

// splitKeepNewlines splits s on '\n' but preserves the trailing newline
// on each non-final line so we can reassemble without losing formatting.
func splitKeepNewlines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
