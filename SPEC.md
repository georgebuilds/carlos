# carlos — specification

This document is the contract for what carlos does and how it is wired. It is meant to be read alongside the code; line references point at the canonical implementation, not a paraphrase.

For the user-facing intro see [README.md](./README.md). For the LLM-discoverable summary see [docs/llms.txt](./docs/llms.txt).

## What carlos is

A pure-Go TUI agent. Single binary, under 16 MB ceiling. No CGO. Cross-compiled for darwin + linux × amd64 + arm64. Local-first: persistent state lives in `~/.carlos/`. Multi-provider: Anthropic, OpenAI, OpenRouter, Ollama, Gemini, with the Anthropic tool-use shape as canonical and the others normalized via adapters.

Carlos is not strictly a coding agent. General-purpose surface: file system, shell, web, Obsidian-flavored notes, scheduled runs, sub-agent supervision.

Two headline product bets:

1. Autonomous skill induction. The agent watches what you do and proposes reusable skills, which you review and edit before they enter the library.
2. First-class sub-agent supervision. Delegated agents are visible in a live manage view with intent, tool calls, progress, diffs, and spend. The user can join, redirect, or stop any of them mid-flight.

## Repository layout

```
cmd/carlos/         main TUI binary + daemon
internal/
  agent/            tool-use loop, event log, supervision, layered policy
  config/           ~/.carlos/config.yaml schema + onboarding state
  daemon/           background scheduler (UDS + launchd / systemd)
  gateway/          chat-surface adapters (ntfy, Telegram, Signal, custom)
  memory/           SQLite FTS5, summarizer, user model
  miniyaml/         hand-rolled YAML for frontmatter
  notes/            Obsidian vault index + cache
  projectctx/       per-project context loader (AGENTS.md, CLAUDE.md)
  providers/        anthropic / openai / openrouter / ollama / gemini (via oacompat)
  research/         decompose → search → fetch → read → synthesize → verify
  sandbox/          local + git-worktree execution
  schedule/         cron + NL grammar
  skills/           skill format, loader, inducer, judge, replay-eval
  theme/            light / dark / NO_COLOR / configurable accent
  tools/            bash, file ops, grep / glob, git_*, web_*, notes_*, obsidian_*
  tui/              bubbletea chat / manage / onboarding / slash registry
  usershell/        Phase U user-shell driver (! prefix, jobs, history)
  workspace/        Phase T-2 trusted-workspaces store + read-only bash classifier
docs/               GitHub Pages site + llms.txt
```

## Tool surface

28 tools registered by default via `tools.NewDefaultRegistryWithBaseDir`. The model sees the Anthropic-shaped JSON schema for each; adapters in the provider package translate for non-Anthropic providers.

### Read-only filesystem

- `read`, `grep`, `glob`, `ls` — sandboxed by `BaseDir` when carlos is running inside a `sandbox.Worktree`.

### Mutating filesystem

- `write`, `edit` — same BaseDir sandboxing, always prompt unless overridden by session "Always".

### Shell

- `bash` — runs commands via `bash -c`. Non-PTY by default; a separate `bash_pty` can be registered for interactive flows.

### Git (read-only)

- `git_status`, `git_diff`, `git_log`, `git_blame`, `git_show`.

### Web

- `web_fetch` — fetch + HTML→text. Configurable `UserAgent` and `RespectRobots` for use in research mode.
- `web_search` — Brave (if `BRAVE_API_KEY`), SearXNG (if `SEARXNG_URL`), or DuckDuckGo HTML fallback.
- `http_request` — method-parametric HTTP for JSON / REST / GraphQL / webhooks. Returns raw status + headers + body.

### Obsidian-flavored notes

Two families share one `*notes.Cache`:

- `notes_*` (7 tools): `notes_get`, `notes_search`, `notes_backlinks`, `notes_tagged`, `notes_neighbors`, `notes_recent`, `notes_resolve`. **Hard-pinned to the user's configured vault**. The schema does not accept a `vault:` field. The model cannot redirect these tools at an arbitrary path.
- `obsidian_*` (7 tools): same operations, `vault:` is **required**. The model must convince the user (via the approval prompt) to read each arbitrary vault.

The split is the trust anchor for layer-1 auto-approval (see permission model below).

## Approval / permission model

Implemented in `internal/agent/policy.go` as `LayeredApprover`. Wraps any concrete `Approver` (production wires the TUI overlay; headless wires stdin-prompt or `AutoApprover`). Three layers evaluated in order:

### Layer 1 — built-in allowlist (Phase T-1)

Hardcoded set of read-only-against-user-state tools. Auto-approved with reason `ReasonBuiltinAllow`:

```
notes_search, notes_get, notes_neighbors, notes_recent,
notes_resolve, notes_backlinks, notes_tagged,
read, grep, glob, ls,
git_status, git_diff, git_log, git_blame, git_show
```

Adding to this list requires a justification comment and review. The trust anchor for `notes_*` is the configuration boundary set during onboarding, not the contents of a tool argument.

### Layer 2 — workspace trust (Phase T-2)

Delegates to a `WorkspacePolicy` plugged via `LayeredApprover.SetWorkspacePolicy`. The production implementation lives in `internal/workspace`:

- `Store`: persistent JSON file at `~/.carlos/trusted-workspaces.json`. 0600 file, 0700 directory, atomic temp-fsync-rename writes. Each entry is `{path, trusted_at}`; paths are absolute and symlink-resolved.
- `Policy`: per-session view of the cwd's trust status. Holds the normalized cwd captured at session boot; `/trust` and `/untrust` slash commands flip it in-session AND persist via `Store`.
- `IsReadOnly`: curated bash-verb classifier (see below).

When the cwd is trusted, the only thing the policy adds beyond layer 1 is a curated set of read-only bash invocations. Everything else still prompts.

#### v1 read-only bash allowlist

Inclusion is opinionated. The full list:

- Filesystem reads: `ls`, `pwd`, `cat`, `head`, `tail`, `wc`, `file`, `which`, `echo`
- Git inspection subcommands: `status`, `diff`, `log`, `show`, `blame`, `branch`, `ls-files`, `ls-tree`, `rev-parse`, `describe`, `remote`, `config` (read forms)

Explicitly OUT and intentionally so:

- Build / test / install tools: `cargo`, `npm`, `yarn`, `pnpm`, `go test`, `go build`, `make`, `cmake`, `bazel`, `ninja`. These can execute arbitrary code paths via scripts, plugins, codegen.
- Mutating filesystem: `rm`, `mv`, `cp`, `mkdir`, `touch`, `chmod`, `chown`.
- Anything with shell metacharacters: `;`, `&&`, `||`, `|`, `>`, `<`, `` ` ``, `$(`, `>>`, `<<`. The classifier rejects the whole string when any are present.
- Hidden write forms behind flags: `git config --add`, `--unset`, `git branch -D` are denied at the flag level.

The classifier is at `internal/workspace/bash.go:IsReadOnly`. Bias is "deny on uncertainty".

### Layer 3 — session fallback

The wrapped `Approver`. In the chat path that is the bubbletea TUI overlay. The user's response is cached as a session "Always" entry inside the TUIApprover, so repeated calls to the same tool don't re-prompt.

Every decision (allow + reason + tool + truncated input) can be captured by an `AuditSink` passed at construction. Reasons: `ReasonBuiltinAllow`, `ReasonWorkspaceAllow`, `ReasonSessionAllow`, `ReasonSessionDeny`. The `/permissions` overlay (Phase T-3, pending) renders this history.

## Slash commands

Owned by `internal/tui/slash`. The composer routes any line starting with `/` to `Parse` + `dispatchSlash`. Naming mirrors Claude Code where a verb exists in both tools.

Mirrored from Claude Code: `/clear`, `/help`, `/exit` (alias `/quit`, `/q`), `/compact`, `/model`, `/review`.

Carlos-specific:

- `/insights [topic]` — what carlos has learned about the user
- `/skills [list|review|edit <name>]` — skill library
- `/memory <query>` — FTS5 over summarized memory
- `/schedule [list|add|rm]` — manage scheduled runs (writes to config.yaml)
- `/daemon [enable|disable|status]` — background daemon control
- `/agents` — open the manage-mode supervisor view
- `/research <question>` — deep-research orchestrator
- `/resume` — past-session picker
- `/shell <cmd>`, `/jobs`, `/fg <id>`, `/bg <id>` — Phase U user-shell (also accessible via `!` prefix)
- `/trust`, `/untrust`, `/trusts` — Phase T-2 workspace trust

## Onboarding

Six-screen flow on first launch, owned by `internal/tui/onboarding`. State persists to `~/.carlos/config.yaml`:

1. Welcome
2. Name
3. Provider (Anthropic, OpenAI, OpenRouter, Ollama, Gemini)
4. Model picker (provider-aware dropdown)
5. Daemon enable
6. Vault path (optional Obsidian vault for `notes_*`)
7. Done

Additional screens shown when relevant: gateway wizard (when daemon is enabled), skills enable.

## Multi-provider

Implemented in `internal/providers/`. The Anthropic tool-use schema is canonical. Adapters:

- Anthropic — first-class. Prompt caching, parallel tool use, vision, structured output.
- OpenAI — via `oacompat` (Chat Completions wire shape). System prompt is injected as the first message.
- OpenRouter — same `oacompat` path with vendor-specific tweaks.
- Gemini — native `gemini` provider plus `oacompat` for OpenAI-compatible models.
- Ollama — `/api/chat` with leading role=system message.

`Capabilities()` exposes what each provider supports: `ParallelToolUse`, `PromptCaching`, `Vision`, `StructuredOutput`. The TUI surfaces this when the model is announced.

## Memory + skills

- **Memory**: SQLite FTS5 over markdown summaries. The `/compact` verb summarizes the current chat and replaces the model's context with the summary, freeing space for new turns.
- **Skills**: `internal/skills`. The inducer watches transcripts and proposes new skills; the judge ranks proposals; the curator queues them for user review. Replay-eval (`internal/skills/skillwire`) runs the original conversation with and without the proposed skill to measure outcome delta. Skills you write in Claude Code show up in carlos and vice versa.

## Research engine

`internal/research`. Pipeline: decompose question → fan-out search → fetch sources → read → synthesize → verify.

- `decomposeSystem` is a one-paragraph system prompt that returns one sub-query per line.
- `synthesizeSystem` is the writing-phase prompt; verifier judges the artifact.
- `WebFetchAdapter` wraps the `web_fetch` tool. `UserAgent` and `RespectRobots` overrides let research mode get past polite-bot 403s and `Disallow: /` listings (set by `cmd/carlos`).
- Output is a markdown report saved to `~/.carlos/research/<slug>-<unix-ts>.md`.

Live status panel in the chat header during research; rendered by the same status sink the user-shell uses.

## User-shell (Phase U)

The `!` prefix in the composer (or `/shell`) routes to `internal/usershell.Manager`. PTY exec (via `creack/pty`), ring buffer, queue, background pool. Per-job output file at `~/.carlos/jobs/<job-id>.log`. Slash verbs: `/shell`, `/jobs` (overlay, also Ctrl+J), `/fg <id>`, `/bg <id>` (Ctrl+Z foregrounds). Separate history file at `~/.carlos/shell-history`.

Events (`EvtUserShellStart`, `EvtUserShellEnd`) land in the same SQLite event log the chat reads; the next model turn sees them via the context projection.

## Session resume (Phase R)

- `carlos -c` — continue the most recent session
- `carlos -r` — open the past-session picker
- `/resume` — same picker from inside chat

Sessions are keyed by ULID. The event log is the durable record; the chat rebuilds the transcript and the model history from it.

## Daemon + gateway

`internal/daemon` runs a background scheduler on a UDS, registered via launchd (macOS) or systemd user unit (Linux). `internal/gateway` adapts external chat surfaces:

- **ntfy** — publish + HMAC-signed callbacks
- **Telegram** — long-poll, inline keyboard, MarkdownV2
- **Signal** — stub
- **Custom** — pluggable adapter contract

Approval routing bridges the agent's approval queue to whichever surfaces are wired.

## Schedules

`internal/schedule`. Cron + NL grammar (`/schedule add "every weekday at 9am" <prompt>`). The daemon executes scheduled runs and posts results through the gateway. Schedules persist in `~/.carlos/config.yaml`.

## File system layout (`~/.carlos/`)

```
config.yaml                 user prefs, provider keys, schedules, vault config
state.db                    SQLite event log + memory FTS5
trusted-workspaces.json     Phase T-2 trust store (0600, atomic writes)
shell-history               Phase U separate history (~/.zsh_history-style)
jobs/<job-id>.log           per-job shell output (Phase U)
research/<slug>-<ts>.md     research reports (Phase 11)
skills/<name>.md            user-approved skill library
agent-pools/                sub-agent worktrees + state
```

Permissions: directory 0700, files containing secrets 0600.

## Pending

- **Phase T-3**: `/permissions` overlay rendering the layered policy state, audit log, and the workspace-trust file. Designed against the TUI research notes (command-palette pattern, three-tier visual hierarchy, "/" filter, always-visible footer, semantic color, empty states with CTA).
- First-launch trust prompt overlay: lives alongside T-3 so the two share styling and key-binding conventions.

## Build + release

- Go toolchain version pinned by `go.mod` (currently `1.26.3`).
- `goreleaser` v2 builds darwin + linux × amd64 + arm64 on a `v*` tag push. `CGO_ENABLED=0`, `-trimpath`, `-s -w`.
- Homebrew tap at `georgebuilds/homebrew-tap`; `Formula/carlos.rb` is auto-updated on release.
- See `.github/workflows/release.yml` for the tag-driven pipeline.
- See `.github/workflows/ci.yml` for the PR-gated test pipeline.

## License

GPL-3.0-or-later. See [LICENSE](./LICENSE).
