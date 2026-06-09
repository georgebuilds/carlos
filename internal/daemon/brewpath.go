package daemon

import (
	"os"
	"path/filepath"
)

// resolveBinaryPath returns the path the launchd plist / systemd
// unit should bake into ExecStart. Priority:
//
//  1. If os.Executable() resolves under a Homebrew Cellar segment,
//     return the stable symlink at "<homebrew-prefix>/bin/<exe>"
//     so the unit re-execs the CURRENT brew-installed version on
//     every restart — `brew upgrade carlos` then just works on the
//     next unit restart, instead of pinning the daemon to whatever
//     version was installed at `carlos daemon enable` time.
//
//  2. Otherwise fall back to the fully symlink-resolved path, which
//     is what we always did pre-v0.7.1. Right answer for `go install`
//     binaries, distro packages, or manually-copied builds — none of
//     which have a stable symlink we could safely re-target.
//
// The returned path is verified to exist (Stat) so a brew install
// whose symlink moved doesn't bake a dangling path into the unit.
func resolveBinaryPath() (string, error) {
	raw, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		// Couldn't resolve symlinks; the raw path is still usable
		// for the unit (launchd / systemd don't care if a symlink
		// hop sits between unit and binary).
		resolved = raw
	}
	if sym := brewSymlinkFor(resolved); sym != "" {
		return sym, nil
	}
	return resolved, nil
}

// brewSymlinkFor returns the stable Homebrew symlink for resolved
// when resolved sits under "<prefix>/Cellar/<formula>/<version>/...",
// or "" otherwise. The returned symlink is verified to exist via
// Stat — a brew install whose bin/ link was removed externally
// falls back to the resolved Cellar path rather than baking a
// dangling reference into the unit.
//
// Layout:
//
//	/opt/homebrew/Cellar/carlos/0.7.0/bin/carlos   ← resolved
//	/opt/homebrew/bin/carlos                       ← stable symlink
//	└── prefix ──┘                                 ← walk up from resolved
//
// Walk up from the binary path; when we land in a directory named
// "Cellar", its parent is the homebrew prefix. The stable symlink
// is "<prefix>/bin/<basename-of-resolved>".
func brewSymlinkFor(resolved string) string {
	if resolved == "" {
		return ""
	}
	p := resolved
	for {
		parent := filepath.Dir(p)
		if parent == p {
			// Hit the filesystem root without finding Cellar.
			return ""
		}
		if filepath.Base(parent) == "Cellar" {
			prefix := filepath.Dir(parent)
			sym := filepath.Join(prefix, "bin", filepath.Base(resolved))
			if _, err := os.Stat(sym); err != nil {
				return ""
			}
			return sym
		}
		p = parent
	}
}
