// Permissions policy engine (Phase T-1).
//
// Today's Approver decides per (tool, input) whether to ask the
// user. That worked when every prompt was hand-y/N - but the same
// noisy prompt fires for `notes_search` (reading your own configured
// vault) and for `bash` (running arbitrary commands). Once auto-
// approval lands we need a layered model that's auditable AND
// reversible. This file is that policy engine.
//
// # Three layers, evaluated in order
//
//  1. **Built-in** (hardcoded): tools that are read-only against
//     user-owned state. notes_* (configured vault only), read, grep,
//     glob, ls. Always allowed without prompting.
//  2. **Workspace trust** (Phase T-2 - placeholder hooks here): when
//     the user marked the current cwd as trusted, a small set of
//     read-only bash verbs (git status / diff / log, ls, cat, ...)
//     run without prompting. Anything else still prompts.
//  3. **Session "Always"** (today's behavior): the per-tool "Always"
//     cache from the TUIApprover. Last resort - user explicitly
//     opted in via "A" on a prompt this session.
//
// If none of the three matches, we fall through to the wrapped
// Approver (typically TUIApprover) which asks the user.
//
// # Why a separate type
//
// The Approver interface stays the same. LayeredApprover wraps any
// concrete Approver as its fallback - production wires
// TUIApprover, tests can inject a recording fake. The chat loop
// doesn't know layered-vs-single; it just calls ApproveToolCall.
package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
)

// LayeredApprover is the production approver wired by cmd/carlos.
// Construct via NewLayeredApprover; wraps a fallback Approver (the
// TUIApprover that owns the y/N overlay).
type LayeredApprover struct {
	// fallback is the Approver delegate when no built-in / workspace
	// / session policy matches. Required.
	fallback Approver

	// builtinAllow is the set of tool names auto-approved without
	// inspecting input. Hardcoded at construction; cannot be mutated
	// at runtime (use the wrapping policy layers for that).
	builtinAllow map[string]bool

	// workspaceRoots is the set of absolute paths the user has
	// trusted *for this session only*. Mutex-guarded so /trust slash
	// + first-launch prompt can both write. The disk side lives in
	// internal/workspace via the WorkspacePolicy plug below; this map
	// is just the in-memory snapshot LayeredApprover can read without
	// a disk hop on every tool call.
	mu             sync.RWMutex
	workspaceRoots map[string]bool

	// workspacePolicy is the Phase T-2 plug. When non-nil, layer 2
	// delegates to it. cmd/carlos wires a workspace.Policy here; the
	// agent package only sees the interface so internal/workspace can
	// own the disk schema + bash classifier without dragging file I/O
	// into the policy engine.
	workspacePolicy WorkspacePolicy

	// auditLog optionally captures every decision (allow + reason +
	// tool + input) so the manage view + /permissions overlay can
	// show "what was auto-approved." nil disables; LayeredApprover
	// works fine without it.
	auditLog AuditSink

	// Phase F-12 cross-frame detection. activeFrame names the frame
	// the session is currently in; frameSubtrees maps every known
	// frame name to its on-disk subtree (the per-frame paths.Root
	// from internal/frame). When a write/edit lands inside a subtree
	// that is NOT activeFrame's, the approval is forced through the
	// fallback (skipping builtin + workspace allow) AND recorded with
	// ReasonCrossFrameAllow/Deny. Empty subtrees disable the check
	// (legacy single-shelf mode).
	activeFrame   string
	frameSubtrees map[string]string
}

// WorkspacePolicy is the layer-2 plug point. Implementations live
// in internal/workspace; the interface stays here so the agent
// package doesn't import the disk schema.
type WorkspacePolicy interface {
	// Allows reports whether (tool, input) is permitted by the
	// workspace-trust layer. False is the safe default - any
	// uncertainty falls through to the prompt path.
	Allows(tool string, input []byte) bool
}

// AuditSink receives one notification per ApproveToolCall decision.
// Implementations should be fast + non-blocking - the chat loop
// calls into the approver synchronously.
type AuditSink interface {
	RecordDecision(d Decision)
}

// Decision is the audit-log entry. Reason names which layer fired;
// the manage view + /permissions can group on it.
type Decision struct {
	Tool    string
	Input   []byte
	Allowed bool
	Reason  DecisionReason
}

// DecisionReason is the structural why behind an Approve return.
// Kept as an enum so future analytics can group cleanly.
type DecisionReason string

const (
	// ReasonBuiltinAllow - tool is in the hardcoded read-only
	// builtins set.
	ReasonBuiltinAllow DecisionReason = "builtin-allow"
	// ReasonWorkspaceAllow - tool + input matches the trusted-
	// workspace policy (Phase T-2).
	ReasonWorkspaceAllow DecisionReason = "workspace-allow"
	// ReasonSessionAllow - wrapped Approver returned true (TUI
	// "Always" cache OR user pressed y).
	ReasonSessionAllow DecisionReason = "session-allow"
	// ReasonSessionDeny - wrapped Approver returned false.
	ReasonSessionDeny DecisionReason = "session-deny"
	// ReasonCrossFrameAllow - write/edit landed inside another frame's
	// subtree and the user explicitly approved. Phase F-12. Bypasses
	// the builtin + workspace shortcuts so the user always sees a
	// cross-frame write distinctly.
	ReasonCrossFrameAllow DecisionReason = "cross-frame-allow"
	// ReasonCrossFrameDeny - same path but the user rejected.
	ReasonCrossFrameDeny DecisionReason = "cross-frame-deny"
)

// DefaultBuiltinAllow is the initial hardcoded auto-approve set.
// Discipline: every entry here is **read-only against user-owned
// state**. Adding a new tool here MUST come with a justification
// comment and a security review (today: just an issue, but the
// principle stands).
//
// Configured-vault notes_* tools are scoped to cfg.Vault.Path by
// construction (the schema doesn't accept a `vault:` field anymore)
// so silent reads can't escape the user's intended boundary.
var DefaultBuiltinAllow = []string{
	// Configured-vault Obsidian tools. The read family is auto-
	// approved because the trust anchor is the configuration
	// boundary set during onboarding. notes_write is also auto-
	// approved because it is constrained to the active frame's
	// vault_subtree and writes nowhere else (cross-frame and
	// out-of-vault attempts are rejected before disk).
	"notes_search",
	"notes_get",
	"notes_neighbors",
	"notes_recent",
	"notes_resolve",
	"notes_backlinks",
	"notes_tagged",
	"notes_write",
	// Read-only introspection of carlos's own state (vault path,
	// frames, capabilities, providers). Returns local data only;
	// no network egress, no file mutation.
	"carlos_about",
	// Read-only access to the loaded skill library: list every
	// frame-scoped skill or fetch a single skill's body so the
	// model can follow its instructions. Pure read; the body may
	// then instruct the model to call gated tools (web_fetch,
	// bash, http_request, …), which still route through the
	// approver as usual.
	"skill_use",
	// Generic read-only filesystem tools.
	"read",
	"grep",
	"glob",
	"ls",
	// Read-only git inspection.
	"git_status",
	"git_diff",
	"git_log",
	"git_blame",
	"git_show",
}

// NewLayeredApprover wraps fallback with the layered policy. allow
// is the hardcoded auto-approve set (typically DefaultBuiltinAllow);
// pass an empty slice for tests that want strict prompting.
func NewLayeredApprover(fallback Approver, allow []string, audit AuditSink) *LayeredApprover {
	if fallback == nil {
		// Defensive: an approver with no fallback would silently
		// deny anything not in the allow list. AutoApprover is the
		// least-surprising fallback.
		fallback = AutoApprover{}
	}
	set := make(map[string]bool, len(allow))
	for _, name := range allow {
		set[name] = true
	}
	return &LayeredApprover{
		fallback:       fallback,
		builtinAllow:   set,
		workspaceRoots: map[string]bool{},
		auditLog:       audit,
	}
}

// ApproveToolCall implements Approver. Walks the layers in order:
// builtin → workspace → fallback. Audit log fires on every return.
//
// Phase F-12: when a mutating tool (write, edit) targets a path that
// belongs to a non-active frame's subtree, the layered shortcuts are
// skipped and the fallback prompts. The decision is recorded with the
// cross-frame reason so the audit log and /permissions overlay can
// surface the boundary crossing distinctly.
func (l *LayeredApprover) ApproveToolCall(name string, input []byte) bool {
	if cross := l.crossFrameTarget(name, input); cross != "" {
		ok := l.fallback.ApproveToolCall(name, input)
		reason := ReasonCrossFrameDeny
		if ok {
			reason = ReasonCrossFrameAllow
		}
		l.record(name, input, ok, reason)
		return ok
	}
	if l.builtinAllow[name] {
		l.record(name, input, true, ReasonBuiltinAllow)
		return true
	}
	if l.workspaceAllows(name, input) {
		l.record(name, input, true, ReasonWorkspaceAllow)
		return true
	}
	ok := l.fallback.ApproveToolCall(name, input)
	reason := ReasonSessionDeny
	if ok {
		reason = ReasonSessionAllow
	}
	l.record(name, input, ok, reason)
	return ok
}

// SetFrameSubtrees plugs the Phase F-12 cross-frame detector. active
// is the current session's frame name; subtrees maps every frame name
// to its on-disk subtree root (typically frame.PathsFor(home,name).Root).
// Pass a nil/empty map to disable the check entirely.
//
// INVARIANT: the stored frameSubtrees map MUST NOT be mutated in
// place after the swap below. crossFrameTarget snapshots the map
// pointer under RLock and then iterates it WITHOUT the lock held -
// safe only because every writer (today: this function) installs a
// freshly-allocated copy, so the in-flight reader keeps iterating
// the old immutable map. If a future caller adds an in-place
// modifier, the iteration becomes a data race.
func (l *LayeredApprover) SetFrameSubtrees(active string, subtrees map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.activeFrame = active
	if subtrees == nil {
		l.frameSubtrees = nil
		return
	}
	cp := make(map[string]string, len(subtrees))
	for k, v := range subtrees {
		cp[k] = v
	}
	l.frameSubtrees = cp
}

// crossFrameTarget reports the frame name a write/edit input would
// land in IF that frame is NOT the active one. Returns "" when the
// target is in the active frame, when no frame mapping is configured,
// or when the tool isn't one of the mutating-with-path family.
func (l *LayeredApprover) crossFrameTarget(name string, input []byte) string {
	if name != "write" && name != "edit" {
		return ""
	}
	l.mu.RLock()
	active := l.activeFrame
	subtrees := l.frameSubtrees
	l.mu.RUnlock()
	if len(subtrees) == 0 {
		return ""
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.Path == "" {
		return ""
	}
	absPath, err := filepath.Abs(in.Path)
	if err != nil {
		return ""
	}
	for fname, root := range subtrees {
		if root == "" || fname == active {
			continue
		}
		if pathInside(absPath, root) {
			return fname
		}
	}
	return ""
}

// pathInside reports whether path is inside root. Both must be
// absolute and clean; uses a separator-anchored prefix check so
// "/root/a" does not match "/root-other/...".
func pathInside(path, root string) bool {
	if path == root {
		return true
	}
	if !strings.HasPrefix(path, root) {
		return false
	}
	rest := path[len(root):]
	if rest == "" {
		return true
	}
	return rest[0] == filepath.Separator
}

// record is a small helper so the call sites stay one line. Skips
// when no audit sink is wired.
func (l *LayeredApprover) record(name string, input []byte, allowed bool, reason DecisionReason) {
	if l.auditLog == nil {
		return
	}
	l.auditLog.RecordDecision(Decision{
		Tool:    name,
		Input:   input,
		Allowed: allowed,
		Reason:  reason,
	})
}

// BuiltinAllowList returns a copy of the current builtin allow set
// (sorted). Used by /permissions to render the layered view.
func (l *LayeredApprover) BuiltinAllowList() []string {
	out := make([]string, 0, len(l.builtinAllow))
	for name := range l.builtinAllow {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// TrustWorkspace adds root to the trusted workspaces set. Phase T-2
// wires the disk persistence; today this just lives in-memory for
// the session.
func (l *LayeredApprover) TrustWorkspace(root string) {
	if root == "" {
		return
	}
	l.mu.Lock()
	l.workspaceRoots[root] = true
	l.mu.Unlock()
}

// UntrustWorkspace removes root from the trusted set.
func (l *LayeredApprover) UntrustWorkspace(root string) {
	l.mu.Lock()
	delete(l.workspaceRoots, root)
	l.mu.Unlock()
}

// TrustedWorkspaces returns a sorted snapshot of the trusted roots.
func (l *LayeredApprover) TrustedWorkspaces() []string {
	l.mu.RLock()
	out := make([]string, 0, len(l.workspaceRoots))
	for r := range l.workspaceRoots {
		out = append(out, r)
	}
	l.mu.RUnlock()
	sortStrings(out)
	return out
}

// IsWorkspaceTrusted reports whether root is in the trusted set.
// Used by the first-launch prompt to know whether to ask.
func (l *LayeredApprover) IsWorkspaceTrusted(root string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.workspaceRoots[root]
}

// workspaceAllows is the Phase T-2 policy gate. Delegates to the
// plugged WorkspacePolicy (typically a *workspace.Policy from
// internal/workspace). When no policy is wired the layer is a no-op
// and the call falls through to the fallback approver.
func (l *LayeredApprover) workspaceAllows(name string, input []byte) bool {
	l.mu.RLock()
	policy := l.workspacePolicy
	l.mu.RUnlock()
	if policy == nil {
		return false
	}
	return policy.Allows(name, input)
}

// SetWorkspacePolicy plugs a Phase T-2 policy into layer 2. Pass nil
// to clear (tests + the headless code path do this). Safe to call
// at any time; subsequent ApproveToolCall hits see the new policy.
func (l *LayeredApprover) SetWorkspacePolicy(p WorkspacePolicy) {
	l.mu.Lock()
	l.workspacePolicy = p
	l.mu.Unlock()
}

// sortStrings - insertion sort; the slices we care about top out at
// a couple dozen entries, so dragging in sort.Strings would be
// overkill.
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// extractInputField is a small helper used by the workspace policy
// to peek at common path-ish fields in a tool's input JSON without
// declaring per-tool input structs in the policy package. Returns
// "" when the field is missing or not a string.
func extractInputField(input []byte, field string) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}
