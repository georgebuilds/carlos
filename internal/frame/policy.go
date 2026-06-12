package frame

import (
	"path/filepath"
	"strings"
)

// Resolution describes how the active frame was chosen for a session.
// Returned by ResolveActive so callers (and tests) can audit the
// decision without re-running the matcher.
type Resolution struct {
	// Frame is the resolved frame name. Always non-empty when ResolveActive
	// returned ok=true.
	Frame string
	// Reason records which rule fired. One of the Reason* constants below.
	Reason string
	// Candidates is the list of frames whose CwdHints matched the input
	// cwd. Populated only when Reason is ReasonCwdHintMultiple - the chat
	// shell uses it to pre-highlight the matching tiles in the takeover.
	Candidates []string
	// Warning is a non-fatal advisory the caller should surface to the
	// user (chat header notice or stderr line at startup). Populated when
	// CARLOS_FRAME or -f names a frame that doesn't exist - the resolver
	// falls through to the next signal but the user typed something and
	// deserves to know it was ignored.
	Warning string
}

const (
	// ReasonEnv means the CARLOS_FRAME env var picked the frame. Highest
	// precedence - beats everything else, even an explicit -f flag at the
	// CLI (the CLI checks env first to keep cron + manual invocation
	// identical). When CARLOS_FRAME names a frame that doesn't exist the
	// resolver falls through to the next signal AND records a Warning on
	// the Resolution; callers surface that in the chat header / stderr.
	ReasonEnv = "env"
	// ReasonFlag means an explicit `-f <frame>` flag was passed. Same
	// unknown-name handling as ReasonEnv: a bad flag value falls through
	// and emits a Warning rather than booting a phantom frame.
	ReasonFlag = "flag"
	// ReasonCwdHintExact means exactly one frame's CwdHints matched the
	// session's cwd, so we picked that one automatically.
	ReasonCwdHintExact = "cwd_hint_exact"
	// ReasonCwdHintMultiple means more than one frame matched and we fell
	// back to the persisted active frame; the chat shell opens the
	// takeover with matching tiles highlighted.
	ReasonCwdHintMultiple = "cwd_hint_multiple"
	// ReasonPersistedActive means we used the active field from disk.
	ReasonPersistedActive = "persisted_active"
	// ReasonDefault means nothing else fired and we used the default
	// field (or "personal" if even that was empty).
	ReasonDefault = "default"
)

// Input gathers every signal ResolveActive consults. Pulled into its
// own struct so tests can build cases declaratively and so a future
// daemon path (scheduled run with its own frame:) doesn't need a new
// positional arg.
type Input struct {
	// Env is the value of CARLOS_FRAME at session start (empty when unset).
	Env string
	// Flag is an explicit -f value from the CLI (empty when unset).
	Flag string
	// Cwd is the symlink-resolved absolute working directory at session
	// start. Empty disables cwd-hint matching entirely (headless, cron).
	Cwd string
}

// ResolveActive returns the frame to apply for a session given the
// signals in input. The cfg argument supplies the frame list, the
// persisted active, and the default name; ResolveActive does not
// mutate either.
//
// Precedence (highest first):
//
//  1. Env (CARLOS_FRAME) - wins when it names a real frame. If the
//     named frame is unknown, the env signal is dropped, the resolver
//     falls through to the next signal, and the returned Resolution
//     carries a Warning the caller should surface to the user.
//  2. Flag (-f) - same shape as env, slightly lower precedence so
//     `CARLOS_FRAME=work carlos -f personal` honors the env. Same
//     unknown-name fall-through with a Warning.
//  3. Cwd-hint match - exact one match wins; multiple matches fall through
//     to persisted-active with the candidates surfaced.
//  4. Persisted active.
//  5. Default ("personal" when default is empty).
//
// Returns ok=false only when cfg has no frames at all (a brand-new install
// before MigrateFromLegacy has run); callers treat that as "run onboarding".
func ResolveActive(cfg *Config, input Input) (Resolution, bool) {
	if cfg == nil || len(cfg.List) == 0 {
		return Resolution{}, false
	}
	var warning string
	if input.Env != "" {
		if cfg.Find(input.Env) != nil {
			return Resolution{Frame: input.Env, Reason: ReasonEnv}, true
		}
		warning = "CARLOS_FRAME=" + input.Env + " unknown; falling through"
	}
	if input.Flag != "" {
		if cfg.Find(input.Flag) != nil {
			res := Resolution{Frame: input.Flag, Reason: ReasonFlag}
			res.Warning = warning
			return res, true
		}
		w := "-f " + input.Flag + " unknown; falling through"
		if warning != "" {
			warning = warning + "; " + w
		} else {
			warning = w
		}
	}
	if input.Cwd != "" {
		candidates := matchCwdHints(cfg, input.Cwd)
		switch len(candidates) {
		case 1:
			return Resolution{Frame: candidates[0], Reason: ReasonCwdHintExact, Warning: warning}, true
		default:
			if len(candidates) > 1 {
				return Resolution{
					Frame:      fallbackActive(cfg),
					Reason:     ReasonCwdHintMultiple,
					Candidates: candidates,
					Warning:    warning,
				}, true
			}
		}
	}
	if cfg.Active != "" {
		return Resolution{Frame: cfg.Active, Reason: ReasonPersistedActive, Warning: warning}, true
	}
	return Resolution{Frame: fallbackDefault(cfg), Reason: ReasonDefault, Warning: warning}, true
}

// fallbackActive returns cfg.Active when set, else fallbackDefault.
// Pulled out so ReasonCwdHintMultiple and other rules share the same
// "default-when-nothing-else" decision.
func fallbackActive(cfg *Config) string {
	if cfg.Active != "" {
		return cfg.Active
	}
	return fallbackDefault(cfg)
}

// fallbackDefault returns cfg.Default when set, else the first listed
// frame's name, else the literal DefaultPersonalName. Guarantees a
// non-empty string when called on a cfg with at least one frame.
func fallbackDefault(cfg *Config) string {
	if cfg.Default != "" {
		return cfg.Default
	}
	if len(cfg.List) > 0 {
		return cfg.List[0].Name
	}
	return DefaultPersonalName
}

// matchCwdHints returns the names of every frame whose CwdHints match
// the supplied cwd. Match rule:
//
//   - Hint without any glob meta-character (`*` `?` `[`) is treated as a
//     path prefix - useful for the common `~/Code/anneal` case.
//   - Hint with glob meta is fed to filepath.Match against the cwd
//     itself, then against every parent directory walking up.
//
// Both forms tolerate a leading `~/` which is resolved against $HOME by
// callers - ResolveActive doesn't expand tilde itself because the cwd
// passed in is already absolute.
func matchCwdHints(cfg *Config, cwd string) []string {
	var out []string
	for _, f := range cfg.List {
		for _, hint := range f.CwdHints {
			if hintMatches(hint, cwd) {
				out = append(out, f.Name)
				break
			}
		}
	}
	return out
}

// hintMatches is the rule used by matchCwdHints. Split out so unit tests
// can pin the exact semantics without going through the whole resolver.
func hintMatches(hint, cwd string) bool {
	if hint == "" || cwd == "" {
		return false
	}
	if hasGlob(hint) {
		if ok, _ := filepath.Match(hint, cwd); ok {
			return true
		}
		// Walk up the tree so a hint like "~/Code/ludus*" still matches
		// cwd "~/Code/ludus/web/src". filepath.Match is a single-segment
		// matcher, not a recursive one.
		dir := cwd
		for {
			parent := filepath.Dir(dir)
			if parent == dir {
				return false
			}
			if ok, _ := filepath.Match(hint, parent); ok {
				return true
			}
			dir = parent
		}
	}
	// Plain prefix match. Require a path separator immediately after to
	// avoid "~/Code/ann" matching "~/Code/anneal" by accident.
	if !strings.HasSuffix(hint, string(filepath.Separator)) {
		hint += string(filepath.Separator)
	}
	cwdSlashed := cwd
	if !strings.HasSuffix(cwd, string(filepath.Separator)) {
		cwdSlashed = cwd + string(filepath.Separator)
	}
	return strings.HasPrefix(cwdSlashed, hint)
}

// hasGlob reports whether s contains any of filepath.Match's meta
// characters. Used by hintMatches to choose between glob and prefix
// modes.
func hasGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}
