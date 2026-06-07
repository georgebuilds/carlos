// first_trust.go - Phase T-2 follow-on: a one-time prompt that offers to
// trust the cwd on first chat launch when the workspace looks like a
// real project (`.git`, `go.mod`, `package.json`, `Cargo.toml`, etc.)
// and isn't already trusted.
//
// Visual shape mirrors the /permissions overlay (T-3): rounded border in
// the accent color, three options on a single row (y / n / esc), one
// line of context above. Surfaces via the same `approval` slot in
// renderInner so it can't render alongside an active modal approval.
//
// Dismissal is session-only: pressing `n` or `esc` suppresses the prompt
// for the rest of the session but the next carlos launch will prompt
// again. The user can flip to "never" by typing `/untrust` which
// already records intent, or by editing `~/.carlos/trusted-workspaces.json`.

package chat

import (
	"os"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"
)

// projectMarkers are the directory entries that, when present, make a
// cwd look like a real project worth prompting about. Anything not on
// this list (e.g. /tmp) won't trigger the first-launch prompt.
var projectMarkers = []string{
	".git",
	"go.mod",
	"package.json",
	"Cargo.toml",
	"pyproject.toml",
	"requirements.txt",
	"pom.xml",
	"build.gradle",
	"Gemfile",
	"composer.json",
	"deno.json",
	"Makefile",
}

// shouldOfferFirstTrustPrompt is the gate: workspace must be wired, cwd
// non-empty, NOT already trusted, and the cwd must look like a project.
// Returns false in test paths that don't pass a workspace.
func (m *Model) shouldOfferFirstTrustPrompt() bool {
	if m.workspace == nil {
		return false
	}
	if m.workspace.IsTrusted() {
		return false
	}
	cwd := m.workspace.Cwd()
	if cwd == "" {
		return false
	}
	for _, marker := range projectMarkers {
		if _, err := os.Stat(filepath.Join(cwd, marker)); err == nil {
			return true
		}
	}
	return false
}

// initFirstTrustPrompt is called once during chat Init. Sets the flag
// when the gate passes; downstream rendering owns the rest.
func (m *Model) initFirstTrustPrompt() {
	if m.firstTrustDismissed {
		return
	}
	if m.shouldOfferFirstTrustPrompt() {
		m.showFirstTrust = true
	}
}

// handleFirstTrustKey routes y/n/esc when the prompt is open. Returns
// true when the key was consumed so the caller's textarea sees nothing.
// y trusts the workspace via the same store the /trust slash uses;
// n / esc dismiss for the session.
func (m *Model) handleFirstTrustKey(key string) bool {
	if !m.showFirstTrust {
		return false
	}
	switch key {
	case "y", "Y":
		if cmd := m.trustSlashEnable(); cmd != nil {
			m.queuedCmds = append(m.queuedCmds, cmd)
		}
		m.showFirstTrust = false
		m.firstTrustDismissed = true
		m.rerenderViewport()
		return true
	case "n", "N", "esc":
		m.showFirstTrust = false
		m.firstTrustDismissed = true
		m.rerenderViewport()
		return true
	}
	return false
}

// renderFirstTrustPrompt is the bordered panel rendered when the user
// is in a project dir + untrusted on first launch. Lives in the same
// overlay slot as approval / permissions; precedence below approval +
// switcher so a modal approval always wins.
func renderFirstTrustPrompt(cwd string, w int) string {
	if w < 40 {
		w = 40
	}
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	header := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render("trust this workspace?")
	body := hintStyle.Render("cwd: " + cwd)
	hintLine := hintStyle.Render(
		"trusting auto-approves a small set of read-only bash commands (ls, pwd, cat, git status/diff/log, etc.) for this cwd. nothing else changes; mutating tools still prompt.",
	)
	actions := keyStyle.Render("y") + hintStyle.Render(" trust  ") +
		keyStyle.Render("n") + hintStyle.Render(" not now  ") +
		keyStyle.Render("esc") + hintStyle.Render(" skip")

	stack := lipgloss.JoinVertical(lipgloss.Left, header, "", body, hintLine, "", actions)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(w - 2)
	return box.Render(stack)
}
