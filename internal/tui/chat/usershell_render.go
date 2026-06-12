package chat

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// User-shell transcript block rendering (Phase U S5).
//
// Visual shape:
//
//	$ cargo test                                ← accent prompt, bold cmd
//	running 12 tests                            ← muted monospace body
//	test result: ok. 12 passed; 0 failed
//	✓ 0 · 4.2s                                  ← status badge (green / red)
//
// While running:
//
//	$ tail -f /tmp/foo
//	line 1
//	line 2
//	▎ running · ⌃z bg · ⌃c cancel               ← active hint
//
// Color discipline pulled from [[2026-06-05 How to Make a TUI Feel
// Awesome in 2026|TUI research §3]] - accent for the prompt + status
// badge, muted/subtle for the body, no decorative color.

// pathHyperlinkRe matches Go-style file:line:col references that
// the terminal can turn into OSC 8 hyperlinks. Examples:
//
//	main.go:42
//	internal/usershell/manager.go:123:45
//
// Conservative - single relative or absolute path tokens, no spaces.
// Misses paths-with-spaces (we don't try, since OSC 8 is a polish
// item - false positives are worse than missed positives).
var pathHyperlinkRe = regexp.MustCompile(
	`((?:\.\.?/|/)?[A-Za-z_][A-Za-z0-9_./-]*\.(?:go|md|ts|tsx|js|py|rs|sh|yaml|yml|json|txt|html|css))(?::\d+(?::\d+)?)?`,
)

// renderUserShellEntry produces one styled block for a user-shell
// transcript row. Width is the available column count; the renderer
// soft-wraps via lipgloss so the block fits within it.
func renderUserShellEntry(e transcriptEntry, width int) string {
	if width < 20 {
		width = 20
	}
	promptStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	cmdStyle := lipgloss.NewStyle().Foreground(colorAccent)
	bodyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	mutedStyle := lipgloss.NewStyle().Foreground(colorSubtle).Italic(true)

	var sb strings.Builder

	// Prompt line.
	sb.WriteString(promptStyle.Render("$"))
	sb.WriteString(" ")
	sb.WriteString(cmdStyle.Render(e.shellCommand))
	if e.shellBackgrounded {
		sb.WriteString("  ")
		sb.WriteString(mutedStyle.Render("(background)"))
	}
	sb.WriteString("\n")

	// Output body. Soft-wrap to width via lipgloss.
	if e.shellOutput != "" {
		body := bodyStyle.Width(width - 2).Render(annotatePaths(strings.TrimRight(e.shellOutput, "\n")))
		// Two-space indent so the body sits visually under the $
		// prompt without competing with it.
		body = indentEachLine(body, "  ")
		sb.WriteString(body)
		sb.WriteString("\n")
	}

	if e.shellTruncated > 0 {
		sb.WriteString("  ")
		sb.WriteString(mutedStyle.Render(fmt.Sprintf("(%d more bytes in artifact)", e.shellTruncated)))
		sb.WriteString("\n")
	}

	// Status row - running hint or completion badge.
	if e.shellRunning {
		runStyle := lipgloss.NewStyle().Foreground(colorAccent)
		keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		bodyKey := lipgloss.NewStyle().Foreground(colorMuted)
		sb.WriteString(runStyle.Render("▎"))
		sb.WriteString(" ")
		sb.WriteString(mutedStyle.Render("running"))
		if e.shellBackgrounded {
			sb.WriteString(mutedStyle.Render(" (bg)"))
		}
		sb.WriteString(bodyKey.Render(" · "))
		sb.WriteString(keyStyle.Render("⌃z"))
		sb.WriteString(bodyKey.Render(" bg · "))
		sb.WriteString(keyStyle.Render("⌃c"))
		sb.WriteString(bodyKey.Render(" cancel"))
		return sb.String()
	}

	sb.WriteString(renderUserShellBadge(e))
	if e.shellFailErr != "" {
		warn := lipgloss.NewStyle().Foreground(colorWarn).Italic(true)
		sb.WriteString("\n  ")
		sb.WriteString(warn.Render(e.shellFailErr))
	}
	return sb.String()
}

// renderUserShellBadge produces the per-status badge for a completed
// job. Discipline: ONE accent color per success/error path, no extra
// decoration. Duration shown only when ≥1s (sub-second jobs are
// "instant" - adding "0.4s" to every line is just noise).
func renderUserShellBadge(e transcriptEntry) string {
	mutedStyle := lipgloss.NewStyle().Foreground(colorMuted)
	switch {
	case e.shellCancelled:
		badge := lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("✗ cancelled")
		return badge + mutedStyle.Render(formatDurationSuffix(e.shellDuration))
	case e.shellExitCode != 0 || e.shellFailErr != "":
		txt := fmt.Sprintf("✗ %d", e.shellExitCode)
		badge := lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render(txt)
		return badge + mutedStyle.Render(formatDurationSuffix(e.shellDuration))
	default:
		badge := lipgloss.NewStyle().Foreground(colorOK).Bold(true).Render("✓ 0")
		return badge + mutedStyle.Render(formatDurationSuffix(e.shellDuration))
	}
}

// formatDurationSuffix emits " · 4.2s" when d >= 1s, otherwise the
// empty string. The leading " · " is part of the suffix so callers
// can concatenate without worrying about double separators.
func formatDurationSuffix(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	return " · " + formatDuration(d)
}

// indentEachLine prefixes every non-empty line in s with prefix.
// Preserves trailing newlines so a body that ends with "\n" doesn't
// suddenly grow a trailing indented blank.
func indentEachLine(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln != "" {
			lines[i] = prefix + ln
		}
	}
	return strings.Join(lines, "\n")
}

// annotatePaths wraps detected file paths in OSC 8 hyperlinks. The
// terminal turns them into clickable links in Kitty / iTerm2 /
// Ghostty / WezTerm / Windows Terminal / VS Code; unsupporting
// terminals see the visible text unchanged.
//
// Per the TUI research note §5, OSC 8 is now broadly supported and
// the right default for any TUI that emits file paths. The sequence
// shape: ESC]8;;<url>ESC\<text>ESC]8;;ESC\.
//
// We use file:// URLs (relative paths resolve against the user's
// cwd in most terminal click-handlers; absolute paths get the
// canonical file://<absolute> form).
func annotatePaths(s string) string {
	return pathHyperlinkRe.ReplaceAllStringFunc(s, func(match string) string {
		url := "file://"
		if !strings.HasPrefix(match, "/") {
			url += "./"
		}
		// Strip :line[:col] before constructing the URL - most
		// terminals handle the bare path; line/col is informational
		// in the display text.
		core := match
		if i := strings.Index(match, ":"); i > 0 {
			// Only strip when the suffix is line[:col] numeric - a
			// path like "foo:bar" without digits shouldn't lose the
			// colon part.
			rest := match[i+1:]
			if isNumericTail(rest) {
				core = match[:i]
			}
		}
		url += core
		// Shared OSC 8 emitter (linkify.go) - same sequence shape this
		// function shipped with; the transcript linkifier reuses it.
		return osc8(url, match)
	})
}

// isNumericTail reports whether s is "<digits>" or "<digits>:<digits>".
// Used by annotatePaths to decide whether a `path:N[:M]` suffix is a
// line/col reference (numeric) vs a path containing a colon.
func isNumericTail(s string) bool {
	if s == "" {
		return false
	}
	gotDigit := false
	gotColon := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			gotDigit = true
		case r == ':' && !gotColon && gotDigit:
			gotColon = true
			gotDigit = false
		default:
			return false
		}
	}
	return gotDigit
}
