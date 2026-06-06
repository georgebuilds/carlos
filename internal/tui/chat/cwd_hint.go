package chat

import (
	"os"
	"path/filepath"
	"strings"
)

// tryInterceptCdCommand parses the shell command for a `cd <path>` shape
// and, when matched, applies it as a Manager.SetCwd in-process so the
// new working directory persists across foreground jobs. Returns
// (handled, statusText). When handled is false the caller hands cmd
// to the Manager as usual.
//
// Phase F-8. The standard `!cd` shape dies with the subshell, so the
// chat surface intercepts the common case ("cd <path>" with no chained
// commands or shell metacharacters) and threads it through the
// Manager's cwd state. This also gives F-8 a reliable trigger for the
// cwd-hint footer hint.
func (m *Model) tryInterceptCdCommand(cmd string) (bool, string) {
	if m.usershell == nil {
		return false, ""
	}
	trimmed := strings.TrimSpace(cmd)
	if !strings.HasPrefix(trimmed, "cd") {
		return false, ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "cd"))
	// "cd" alone is "cd $HOME"; "cd path" with no metacharacters is
	// what we handle. Anything with `;`, `&`, `|`, redirects, etc.
	// falls through to the shell so the user's compound command runs.
	if strings.ContainsAny(rest, ";&|<>`$()") {
		return false, ""
	}
	dest := rest
	if dest == "" || dest == "~" {
		dest = osHomeDir()
		if dest == "" {
			return false, ""
		}
	}
	if strings.HasPrefix(dest, "~/") {
		if home := osHomeDir(); home != "" {
			dest = filepath.Join(home, dest[2:])
		}
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(m.usershell.Cwd(), dest)
	}
	dest = filepath.Clean(dest)
	info, err := os.Stat(dest)
	if err != nil {
		return true, "cd: " + dest + ": " + err.Error()
	}
	if !info.IsDir() {
		return true, "cd: " + dest + ": not a directory"
	}
	m.usershell.SetCwd(dest)
	m.refreshCwdHint(dest)
	return true, "cwd is now " + dest
}

// refreshCwdHint runs the Phase F-8 hint logic against a fresh cwd. The
// hint is set when the cwd matches another frame's cwd_hints AND the
// match wasn't already reported for this path AND the user hasn't
// locked hints via Ctrl+L.
func (m *Model) refreshCwdHint(cwd string) {
	if m.hintsLocked {
		m.footerHint = ""
		return
	}
	if m.frame.MatchCwd == nil {
		m.footerHint = ""
		return
	}
	target := m.frame.MatchCwd(cwd)
	if target == "" || target == m.frame.Active {
		m.footerHint = ""
		return
	}
	if m.hintSeen == nil {
		m.hintSeen = map[string]bool{}
	}
	key := target + ":" + cwd
	if m.hintSeen[key] {
		m.footerHint = ""
		return
	}
	m.hintSeen[key] = true
	m.footerHint = "you are in " + cwd + " which matches frame `" + target + "`. Ctrl+F to switch, Ctrl+L to mute."
}

// lockCwdHints flips the once-per-session lock so the F-8 hint stops
// firing after the user has acknowledged it. Bound to Ctrl+L in the
// chat key handler.
func (m *Model) lockCwdHints() {
	m.hintsLocked = true
	m.footerHint = ""
}

// osHomeDir is split out so tests can rely on os.UserHomeDir() while
// keeping the rest of the package free of os imports.
func osHomeDir() string {
	if d, err := os.UserHomeDir(); err == nil {
		return d
	}
	return ""
}
