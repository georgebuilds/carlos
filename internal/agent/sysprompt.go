// sysprompt.go — the chat-facing system prompt builder.
//
// Until this landed, chatglue.Config.System was "" and providers
// answered identity questions with their built-in defaults
// ("My name is Gemini ..."). This file plugs that gap with a
// composed, minimal carlos identity prompt.
//
// Composition order:
//
//   1. Identity sentence: "you are carlos." Front-loaded so it
//      survives any later prose the model decides to ignore.
//   2. Per-tool-family overview: what notes_* vs obsidian_*
//      vs bash means, why notes_* doesn't accept a vault arg.
//   3. House conventions: brevity, the comment rule, the
//      Obsidian-flavored markdown link convention, etc.
//   4. Live project context appended by the caller (carlos walks
//      AGENTS.md / CLAUDE.md from cwd up to git root via
//      internal/projectctx).
//
// We keep it as a plain const + simple Go-string composition in
// v1. When the prompt grows past ~50 lines or starts wanting
// variable interpolation, promote to internal/agent/prompts/
// carlos.md + //go:embed — the DX pattern George flagged
// interest in but hasn't committed to.

package agent

import (
	"fmt"
	"strings"
)

// chatBaseSystem is the static portion. Provider, model, user name,
// and project context are appended by SystemPrompt().
const chatBaseSystem = `You are carlos, a local-first TUI coding agent that runs in the user's terminal. Your name is carlos regardless of the underlying model. If the user asks who you are, you are carlos — never name the model.

You have these tool families:

- notes_* (8 tools): query and WRITE the user's CONFIGURED Obsidian vault. Schema does not accept a vault argument; you cannot redirect these tools. Auto-approved without prompting. The write verb (notes_write) is constrained to the active frame's vault_subtree — for any note that belongs in the user's current frame, prefer notes_write over the generic write tool so the user doesn't have to approve each save.
- obsidian_* (7 tools): same READ operations against an ARBITRARY vault path the user has to approve per call. Use only when the user asks about a vault other than the configured one.
- read, grep, glob, ls: read-only filesystem. Sandboxed by carlos's base directory.
- write, edit: file mutation. Prompts the user.
- bash: shell command. Prompts unless the user has trusted the workspace, in which case a small read-only verb set (git status/diff/log/show/blame, ls, pwd, cat, head, tail, wc, file, which, echo) auto-approves.
- git_status, git_diff, git_log, git_blame, git_show: read-only git inspection.
- web_fetch, web_search, http_request: network access.

Conventions:

- Be concise. Default to no comments in code. Only add a comment when the WHY is non-obvious.
- Prefer editing existing files to creating new ones. Don't create README files unless explicitly asked.
- When you reference a file or location, write it as path:line so it can be clicked.
- Don't summarize what you just did at the end of every response; the user can read the diff.
- If you need to wait on the user, ask one specific question, not a survey.`

// SystemPrompt composes the runtime system prompt. Fields:
//
//   - userName: optional. When empty, the prompt skips the
//     "the user is X" sentence.
//   - cwd: optional. When set, included so the model knows what
//     "this project" means without grepping for it.
//   - projectCtx: optional. Pre-rendered AGENTS.md / CLAUDE.md
//     bundle from internal/projectctx, capped by the caller.
//
// Returns chatBaseSystem unchanged when all dynamic fields are
// empty (tests + the zero-config code path).
//
// For Phase F frame-aware composition, callers use SystemPromptWithFrame.
func SystemPrompt(userName, cwd, projectCtx string) string {
	return SystemPromptWithFrame(userName, cwd, projectCtx, FrameInfo{})
}

// FrameInfo carries the per-frame fields the system prompt needs.
// Pulled out of internal/frame.Frame so this package stays free of the
// frame import (avoids a cycle the chatglue layer would otherwise hit).
//
// All fields optional. An empty FrameInfo makes SystemPromptWithFrame
// behave identically to the legacy SystemPrompt — the per-frame block is
// emitted only when Name is non-empty.
type FrameInfo struct {
	// Name is the frame's user-visible identifier ("personal", "work").
	Name string
	// Append is the verbatim per-frame addition (e.g. "Personal frame.
	// Tone: relaxed."). Trimmed and added under a "Frame:" header.
	Append string
	// Mode is the orchestrator-mode default for the frame: "solo",
	// "tight", or "orchestrator". Surfaced in the Frame block so the
	// model knows whether to delegate aggressively or stay single-track.
	Mode string
	// VaultPath is the absolute path to the configured Obsidian vault.
	// Surfaced so notes_write + the model's mental model of "where do
	// notes go for this user" both reference the same location.
	VaultPath string
	// VaultSubtree is the active frame's slice of the vault, relative
	// to VaultPath. Empty means the whole vault.
	VaultSubtree string
	// CwdHints is the active frame's cwd_hints list, surfaced so the
	// model can reason about which paths belong to this frame without
	// guessing. Empty list = the frame is path-agnostic.
	CwdHints []string
	// Capabilities is the active frame's capability -> backend map
	// (e.g. {"calendar": "caldav"}). Surfaced so the model knows which
	// backend skill to delegate to without re-reading config.
	Capabilities map[string]string
}

// SystemPromptWithFrame composes the runtime system prompt and folds in
// the active frame's name + system_prompt_append. The frame sentence
// lands BEFORE the Runtime block so the prefix-cache boundary is stable
// across frame switches: the chatBaseSystem prefix stays cached even
// when the frame changes.
func SystemPromptWithFrame(userName, cwd, projectCtx string, fi FrameInfo) string {
	var b strings.Builder
	b.WriteString(chatBaseSystem)
	if name := strings.TrimSpace(fi.Name); name != "" {
		b.WriteString("\n\nFrame: ")
		b.WriteString(name)
		if mode := strings.TrimSpace(fi.Mode); mode != "" {
			b.WriteString(" (")
			b.WriteString(mode)
			b.WriteString(" mode)")
		}
		b.WriteString(".")
		switch strings.TrimSpace(fi.Mode) {
		case "orchestrator":
			b.WriteString("\nDelegate aggressively: split large problems across sub-agents (Agent tool) and synthesize the results. Prefer parallel sub-agents over sequential self-work when the task has independent parts.")
		case "tight":
			b.WriteString("\nSingle-task focus: do not chase tangents. Surface side-quests as notes for the user instead of pursuing them.")
		case "solo":
			b.WriteString("\nDo the work yourself. Sub-agent delegation is opt-in: only spawn an Agent when the user explicitly asks or the task is plainly beyond a single session.")
		}
		// Where things live: ground the agent in the active frame's
		// concrete paths so `notes_write` calls land in the right
		// subtree without guessing, and so the model can reason about
		// cross-frame writes (use the generic write tool with an
		// absolute path for those — it will prompt for confirmation).
		var where strings.Builder
		if vp := strings.TrimSpace(fi.VaultPath); vp != "" {
			fmt.Fprintf(&where, "\n- Vault: %s", vp)
			if sub := strings.TrimSpace(fi.VaultSubtree); sub != "" {
				fmt.Fprintf(&where, " (this frame's subtree: %s)", sub)
			}
		}
		if len(fi.CwdHints) > 0 {
			fmt.Fprintf(&where, "\n- Cwd hints for this frame: %s", strings.Join(fi.CwdHints, ", "))
		}
		if len(fi.Capabilities) > 0 {
			parts := make([]string, 0, len(fi.Capabilities))
			for k, v := range fi.Capabilities {
				parts = append(parts, k+"="+v)
			}
			sortStrings(parts)
			fmt.Fprintf(&where, "\n- Capabilities wired for this frame: %s", strings.Join(parts, ", "))
		}
		if where.Len() > 0 {
			b.WriteString(where.String())
		}
		if app := strings.TrimSpace(fi.Append); app != "" {
			b.WriteString("\n")
			b.WriteString(app)
		}
	}
	if userName != "" || cwd != "" {
		b.WriteString("\n\nRuntime:\n")
		if userName != "" {
			b.WriteString("\n- The user is ")
			b.WriteString(userName)
			b.WriteString(".")
		}
		if cwd != "" {
			b.WriteString("\n- The current working directory is ")
			b.WriteString(cwd)
			b.WriteString(".")
		}
	}
	if projectCtx = strings.TrimSpace(projectCtx); projectCtx != "" {
		b.WriteString("\n\nProject context (AGENTS.md / CLAUDE.md, walked up from cwd):\n\n")
		b.WriteString(projectCtx)
	}
	return b.String()
}
