// policy.go - the workspace-trust gate plugged into LayeredApprover.
//
// A Policy is the read side of the trust store, paired with the
// read-only bash classifier. The chat path constructs one Policy per
// session (the chat's cwd is fixed for the session's lifetime), wires
// it into LayeredApprover via SetWorkspacePolicy, and the policy
// engine consults it for layer-2 allowances:
//
//	1. builtin   ← tool name in hardcoded allowlist (Phase T-1)
//	2. workspace ← THIS layer - only fires when cwd is in store
//	3. fallback  ← TUI prompt
//
// The Allows hook is called for every tool call. Performance: each
// call does at most one map lookup + one string scan against a tiny
// metacharacter table. No disk I/O on the hot path; the store cache
// is populated on Load.

package workspace

import (
	"encoding/json"
)

// Policy is the WorkspacePolicy implementation. Construct via
// NewPolicy. Thread-safe; the underlying Store is concurrent.
type Policy struct {
	store *Store
	// cwd is the directory the session anchored to, normalized
	// once at construction. The Allows hook compares against it
	// rather than re-resolving os.Getwd on each call - the chat
	// loop's cwd is fixed for the session.
	cwd string
	// trustedAtStart caches the cwd's trust status at session
	// boot. The /trust slash flips this in-memory immediately so
	// the model sees the new policy on its next call; the on-disk
	// write happens in the slash handler.
	trustedAtStart bool
}

// NewPolicy returns a Policy for the given store anchored at cwd.
// cwd is normalized (absolute + symlink-resolved); the trust check
// then reuses the normalized form. A nil store or empty cwd disables
// the policy (Allows always returns false).
func NewPolicy(store *Store, cwd string) *Policy {
	if store == nil || cwd == "" {
		return &Policy{}
	}
	norm, err := normalize(cwd)
	if err != nil {
		return &Policy{store: store}
	}
	trusted, _ := store.IsTrusted(norm)
	return &Policy{
		store:          store,
		cwd:            norm,
		trustedAtStart: trusted,
	}
}

// Allows reports whether (tool, input) is permitted by the workspace
// layer. Returns false for any of:
//   - nil/empty Policy (store wasn't wired)
//   - cwd isn't in the trust store
//   - tool isn't bash (only bash needs per-input inspection at this
//     layer; the read-only filesystem tools are already in the
//     hardcoded builtin allowlist)
//   - bash input has shell metachars / unknown verb / write subcmd
func (p *Policy) Allows(tool string, input []byte) bool {
	if p == nil || p.store == nil || p.cwd == "" {
		return false
	}
	if !p.IsTrusted() {
		return false
	}
	if tool != "bash" {
		return false
	}
	cmd := extractBashCmd(input)
	return IsReadOnly(cmd)
}

// IsTrusted reports whether the policy's anchored cwd is currently
// trusted. Combines the boot-time snapshot with any in-session /trust
// toggles via the SetTrusted hook below.
func (p *Policy) IsTrusted() bool {
	if p == nil {
		return false
	}
	return p.trustedAtStart
}

// SetTrusted updates the in-session view of the cwd's trust status.
// Used by the /trust + /untrust slash handlers so the next tool call
// sees the new state without a chat restart. Disk persistence is the
// caller's responsibility - slash handlers call store.Trust /
// store.Untrust separately.
func (p *Policy) SetTrusted(v bool) {
	if p == nil {
		return
	}
	p.trustedAtStart = v
}

// Cwd returns the normalized cwd this policy is anchored to. Empty
// when the policy was constructed without a valid cwd. Used by slash
// handlers + the first-launch prompt to know which root to persist.
func (p *Policy) Cwd() string {
	if p == nil {
		return ""
	}
	return p.cwd
}

// Store returns the underlying trust store so callers (slash
// handlers, manage view) can List / Trust / Untrust without
// re-constructing the file path.
func (p *Policy) Store() *Store {
	if p == nil {
		return nil
	}
	return p.store
}

// extractBashCmd pulls the "cmd" field out of a bash tool input
// envelope. Mirrors the shape of internal/tools/bash.go's bashInput.
// Returns "" on parse failure so the policy bias remains "deny on
// uncertainty."
func extractBashCmd(input []byte) string {
	if len(input) == 0 {
		return ""
	}
	var m struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	return m.Cmd
}
