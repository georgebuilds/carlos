package farewell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsBrewInstall reports whether the running binary lives inside a
// Homebrew Cellar, indicating the user installed carlos via brew (or
// the goreleaser tap). We resolve any symlink hop first because the
// canonical install path is /opt/homebrew/bin/carlos →
// /opt/homebrew/Cellar/carlos/<ver>/bin/carlos, and the same shape
// applies under /usr/local on Intel Macs.
//
// Returns false on any error or when no Cellar segment shows up in
// the path. Cheap enough to call from the hot startup path; results
// don't change during the session.
func IsBrewInstall() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// Symlink resolution failed; check the raw path anyway, since
		// a manually-copied binary still has the Cellar segment.
		resolved = exe
	}
	return pathLooksLikeBrew(resolved)
}

// pathLooksLikeBrew is the testable predicate behind IsBrewInstall:
// returns true when any path segment is named "Cellar", the
// homebrew install marker on both Intel (/usr/local/Cellar/...) and
// Apple Silicon (/opt/homebrew/Cellar/...). Pulled out as a pure
// function so we can exercise the matching logic without mocking
// os.Executable.
func pathLooksLikeBrew(p string) bool {
	if p == "" {
		return false
	}
	p = filepath.Clean(p)
	parts := strings.Split(p, string(filepath.Separator))
	for _, segment := range parts {
		if segment == "Cellar" {
			return true
		}
	}
	return false
}

// CheckBrewUpdate runs `brew outdated --formula --quiet` and returns
// true when "carlos" is in the output (i.e. an update is pending).
// The probe is timeout-gated by the caller's ctx; on any error or
// timeout we return false rather than guessing — false negatives are
// strictly better than nagging users about updates that may not
// actually exist.
//
// Wrapper rationale: brew CLI is the only authoritative source for
// "is this formula outdated"; the alternative (parsing the tap's
// GitHub formula and comparing semver) is fragile because brew may
// be pinned, the user may have HOMEBREW_NO_AUTO_UPDATE set, etc.
// Shelling out keeps the logic right.
//
// Output-format note: `brew outdated --quiet` prints
// formula.full_installed_specified_name, which is the bare name for
// core-tap formulae ("carlos") but the fully-qualified path for
// custom-tap installs ("georgebuilds/tap/carlos"). carlos ships via
// georgebuilds/tap, so the match must accept either shape — the
// pre-fix exact-equality check silently missed every tap user, which
// is the whole production install base.
func CheckBrewUpdate(ctx context.Context, formula string) bool {
	if formula == "" {
		formula = "carlos"
	}
	cmd := exec.CommandContext(ctx, "brew", "outdated", "--formula", "--quiet")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if matchOutdatedLine(strings.TrimSpace(line), formula) {
			return true
		}
	}
	return false
}

// matchOutdatedLine reports whether a single `brew outdated --quiet`
// row refers to the given formula. Accepts the bare formula name
// ("carlos") and any tap-qualified form ("user/tap/carlos"). We
// require the slash form so an unrelated formula whose name happens
// to end with the substring "carlos" doesn't trip a false positive.
func matchOutdatedLine(line, formula string) bool {
	if line == "" || formula == "" {
		return false
	}
	if line == formula {
		return true
	}
	return strings.HasSuffix(line, "/"+formula)
}
