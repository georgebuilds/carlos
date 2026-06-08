// Package slash owns carlos's TUI slash-command vocabulary.
//
// We follow Claude Code's syntax and naming conventions deliberately:
// users coming from Claude Code should be able to type `/clear`, `/help`,
// `/exit` without thinking. Carlos-specific commands (e.g. `/insights`)
// extend the same `/<verb>[ args...]` shape so the muscle memory carries.
//
// Slash commands are TUI-only directives - they are NOT prompts sent to
// the model. The TUI's input handler peeks at the first character: a
// leading `/` routes to Parse + the command registry; anything else is a
// model-bound message.
package slash

import (
	"errors"
	"strings"
)

// Command is the parsed shape of a slash command line.
type Command struct {
	// Name is the verb (e.g. "clear"). Always lower-cased, no leading "/".
	Name string
	// Args is the rest of the line after the verb, trimmed. Commands that
	// take structured args parse this themselves.
	Args string
}

// ErrNotSlash is returned by Parse when the input doesn't begin with "/".
// Callers use this to fall back to treating the input as a model prompt.
var ErrNotSlash = errors.New("slash: not a slash command")

// Parse splits a raw input line into a Command. Returns ErrNotSlash for
// non-slash input. Verb is normalized to lower-case so `/Clear` and
// `/clear` are the same command.
func Parse(line string) (Command, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return Command{}, ErrNotSlash
	}
	body := strings.TrimPrefix(line, "/")
	verb, args, _ := strings.Cut(body, " ")
	return Command{
		Name: strings.ToLower(strings.TrimSpace(verb)),
		Args: strings.TrimSpace(args),
	}, nil
}

// Spec describes a built-in command for the registry + help screen.
// Handlers live wherever they need to (TUI mode switches, agent calls);
// the registry only owns the verb→spec mapping.
type Spec struct {
	Name        string
	ArgsHint    string // e.g. "[query]"; empty for no args
	Description string
}

// Builtins is the initial command set. Naming mirrors Claude Code where a
// command already exists there; carlos-specific verbs follow the same
// `/<verb>` shape.
//
// This is the contract; SPEC § Slash commands is the user-facing doc.
var Builtins = []Spec{
	// Mirrored from Claude Code (same name + behavior, so muscle memory carries).
	{Name: "clear", Description: "clear the chat buffer (keeps the conversation; just clears the view)"},
	{Name: "help", Description: "show available slash commands"},
	{Name: "exit", Description: "exit carlos (alias: /quit, /q)"},
	{Name: "quit", Description: "alias for /exit"},
	{Name: "compact", Description: "summarize the conversation and shed older context"},
	{Name: "model", ArgsHint: "[provider:model]", Description: "switch the active model; no args lists options"},
	{Name: "review", Description: "open the manage-mode approval queue (plans, diffs, skill proposals)"},

	// carlos-specific verbs.
	{Name: "insights", ArgsHint: "[topic]", Description: "show what carlos has learned about you and your work; topical filter optional"},
	{Name: "skills", ArgsHint: "[list|review|edit <name>]", Description: "inspect or edit the skill library"},
	{Name: "memory", ArgsHint: "<query>", Description: "search persistent memory (FTS5 over summaries)"},
	{Name: "schedule", ArgsHint: "[list|add|rm]", Description: "manage scheduled runs"},
	{Name: "daemon", ArgsHint: "[enable|disable|status]", Description: "manage the background daemon"},
	{Name: "agents", Description: "switch focus to the manage-mode supervisor view"},
	{Name: "research", ArgsHint: "<question>", Description: "deep-research a question; web-searches, fetches sources, synthesizes a cited report"},

	// Phase U - user-shell verbs. Provide a slash alternative to
	// the "!"-prefix submit so users who prefer slashes get parity.
	{Name: "shell", ArgsHint: "<cmd>", Description: "run a shell command in your context (same as !cmd)"},
	{Name: "jobs", Description: "toggle the shell-jobs overlay (same as Ctrl+J)"},
	{Name: "fg", ArgsHint: "<job-id>", Description: "foreground a background shell job"},
	{Name: "bg", ArgsHint: "<job-id>", Description: "background the running shell job (same as Ctrl+Z)"},
	{Name: "resume", Description: "pick a past chat session to resume (Phase R)"},

	// Phase T-2 - workspace trust. Trust enables auto-approval for
	// a curated set of read-only bash verbs (git status/diff/log/…,
	// ls, pwd, cat, head, tail, …). Everything else still prompts.
	{Name: "trust", Description: "trust the current workspace for read-only bash auto-approval"},
	{Name: "untrust", Description: "remove trust from the current workspace"},
	{Name: "trusts", Description: "list trusted workspaces"},

	// Phase T-3 - open the layered-policy overlay: built-in
	// allowlist plus trusted workspaces, with tab to switch and /
	// to filter.
	{Name: "permissions", Description: "open the permissions overlay (built-in + workspace-trust state)"},

	// Phase F - frames. `/frame` echoes the active frame; `/frame
	// list` enumerates available frames; `/frame switch <name>`
	// persists a new active frame (provider/model take effect at
	// next session start until the live-swap slice lands).
	{Name: "frame", ArgsHint: "[list|switch <name>|new [name]]", Description: "show or switch the active frame (Phase F)"},

	// Phase C-7 - list user-facing capabilities wired in the active
	// frame: capability -> backend -> skills that deliver it.
	{Name: "capabilities", Description: "list wired capabilities (calendar, etc.) in the active frame"},

	// Orchestrator-mode: show or switch the active frame's mode
	// (solo / tight / orchestrator). Persisted alongside the frame.
	{Name: "mode", ArgsHint: "[solo|tight|orchestrator]", Description: "show or set the active frame's orchestrator mode"},

	// Identity surface: print frame, mode, provider, model. Useful
	// after a /frame switch to confirm the live swap.
	{Name: "whoami", Description: "show the active frame, mode, provider, and model"},

	// MCP v1 - list configured MCP servers and the tools they
	// contributed at boot. Handler ships in a follow-on slice (see
	// chat.dispatchSlash); for now the verb appears in the palette so
	// users can discover it.
	{Name: "mcp", Description: "list configured MCP servers and their tools"},
}

// Lookup returns the Spec for name (case-insensitive), or (Spec{}, false).
func Lookup(name string) (Spec, bool) {
	name = strings.ToLower(name)
	for _, s := range Builtins {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Filter is the autocomplete brain for the TUI's "slash mode": given the
// raw textarea value, return the matching specs, the verb fragment under
// the cursor, and a flag telling the caller whether the user has typed
// past the verb into args-entry territory.
//
// Three regimes:
//
//   - Just "/" (or "/" + whitespace): full Builtins list. The popup
//     reads as a discoverable command palette.
//   - "/<prefix>": case-insensitive prefix match on Spec.Name. Ordered
//     by Builtins (which is the curated reading order in the help
//     panel, so the popup matches what users already know).
//   - "/<verb> ...": treat the verb as locked in. Returns the exact-
//     match Spec (one entry) and inArgs=true so the caller pivots from
//     "narrow down" mode into "show me how to fill this in" mode.
//
// Non-slash input returns an empty matches slice and inArgs=false. The
// caller uses len(matches)==0 + verb!="" to detect "user typed a verb
// that doesn't exist" and surface a hint.
func Filter(line string) (matches []Spec, verb string, inArgs bool) {
	line = strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(line, "/") {
		return nil, "", false
	}
	body := strings.TrimPrefix(line, "/")
	rawVerb, _, hasSpace := strings.Cut(body, " ")
	verb = strings.ToLower(strings.TrimSpace(rawVerb))
	if hasSpace {
		// Locked into one verb; the popup should pivot to args hint.
		// Empty matches signals "verb is unknown" - the renderer can
		// still show a 'unknown command' line.
		for _, s := range Builtins {
			if s.Name == verb {
				return []Spec{s}, verb, true
			}
		}
		return nil, verb, true
	}
	if verb == "" {
		out := make([]Spec, len(Builtins))
		copy(out, Builtins)
		return out, "", false
	}
	for _, s := range Builtins {
		if strings.HasPrefix(s.Name, verb) {
			matches = append(matches, s)
		}
	}
	return matches, verb, false
}

// Ghost returns the ghost-text completion to render after the user's
// cursor in slash mode, given the current textarea value and the
// currently-selected suggestion. Two regimes mirror [Filter]:
//
//   - Verb-completion mode: the user is mid-typing a verb. Returns the
//     remaining characters of spec.Name. Empty if the verb is already
//     complete (covers /clear, /help, etc. where there's no more to
//     suggest until a space lands).
//   - Args-hint mode: the user has typed "/<verb> " (or further).
//     Returns spec.ArgsHint, optionally prefixed with a space so the
//     ghost reads as a continuation. Empty when the spec takes no args.
//
// Returns "" whenever no useful suggestion exists; the caller skips
// rendering in that case.
func Ghost(line string, spec Spec) string {
	if spec.Name == "" {
		return ""
	}
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	body := strings.TrimPrefix(trimmed, "/")
	rawVerb, after, hasSpace := strings.Cut(body, " ")
	verb := strings.ToLower(rawVerb)
	if !hasSpace {
		// Verb-completion: dim out the remainder of the name.
		if !strings.HasPrefix(spec.Name, verb) {
			return ""
		}
		remainder := spec.Name[len(verb):]
		// When the verb is fully typed but no space yet, hint the
		// args inline so the user sees what comes next without
		// pressing space first.
		if remainder == "" && spec.ArgsHint != "" {
			return " " + spec.ArgsHint
		}
		return remainder
	}
	// Args-hint mode: the spec is locked in. Only render the hint if
	// the user hasn't already started typing args (we don't want to
	// overlay over their text).
	if strings.TrimSpace(after) != "" {
		return ""
	}
	return spec.ArgsHint
}
