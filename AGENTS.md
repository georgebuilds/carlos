# carlos: agent onboarding

You are looking at the carlos repo. carlos is a pure-Go TUI agent. Single binary,
under 16 MB ceiling, no CGO. Cross-compiled for darwin + linux x amd64 + arm64.
General-purpose surface: file system, shell, web, Obsidian-flavored notes,
scheduled runs, sub-agent supervision.

The project name is lowercase: `carlos`, not `Carlos`.

## Layout

```
cmd/carlos/         main TUI binary + daemon
internal/
  agent/            tool-use loop, event log, supervision, layered policy
  config/           ~/.carlos/config.yaml schema + onboarding state
  daemon/           background scheduler (UDS + launchd / systemd)
  frame/            per-session frames (personal + N user-defined)
  gateway/          chat-surface adapters (ntfy, Telegram, Signal, custom)
  memory/           SQLite FTS5, summarizer, user model
  miniyaml/         hand-rolled YAML for frontmatter
  notes/            Obsidian vault index + cache
  projectctx/       per-project context loader (AGENTS.md, CLAUDE.md)
  providers/        anthropic / openai / openrouter / ollama / gemini
  research/         decompose -> search -> fetch -> read -> synthesize -> verify
  sandbox/          local + git-worktree execution
  schedule/         cron + NL grammar
  skills/           skill format, loader, inducer, judge, replay-eval
  theme/            light / dark / NO_COLOR / configurable accent
  tools/            bash, file ops, grep / glob, git_*, web_*, notes_*, obsidian_*
  tui/              bubbletea chat / manage / onboarding / slash registry
  usershell/        Phase U user-shell driver (! prefix, jobs, history)
  workspace/        Phase T-2 trusted-workspaces store + read-only bash classifier
skills/             bundled starter skills (calendar/, ...)
docs/               GitHub Pages site + llms.txt
```

## House rules

- No em-dashes in user-facing docs. Grep before reporting done.
- Lowercase project name in prose: `carlos`.
- Default to no comments. Only add one when the WHY is non-obvious; the WHAT is
  already in the code.
- Prefer editing existing files. Don't create new ones unless the change really
  doesn't belong anywhere that already exists.
- File modes: 0700 for dirs, 0600 for secret-bearing files (`config.yaml`,
  `state.db`, `trusted-workspaces.json`, artifact blobs), 0644 elsewhere.
- Atomic writes (temp + fsync + rename) for any file containing user state. See
  `internal/config/config.go` `Save` for the canonical recipe; the same shape
  shows up in `internal/agent/artifacts.go` `writeBlobAtomic` and
  `internal/workspace/store.go`.

## Test discipline

- `go test ./...` is the floor. ~2035 tests across 39 packages today; new code
  aims for 80%+ coverage on touched packages.
- `go vet ./...` must be clean.
- `go build ./...` must build cleanly for the four release targets
  (darwin + linux x amd64 + arm64). Cross-compile checks are cheap; run them
  when touching anything that imports OS-specific paths.

## Phase model

`SPEC.md` is authoritative; line-anchored references point at the canonical
implementation, not a paraphrase. Read it for the current state of:

- Phase F (frames)
- Phase G (gateway)
- Phase T (permissions: T-1 builtin allowlist, T-2 workspace trust, T-3 overlay)
- Phase U (user-shell)
- Phase C (capability taxonomy)
- Phase O (orchestrator modes)
- Phases 0-8 (foundation)

## Conventions

- **Slash commands** live in `internal/tui/slash/slash.go` in the `Builtins`
  list. Add a `Spec` row and wire the handler in the chat router.
- **Frames** are defined in `internal/frame`: `Frame`, `Config`, `Policy`. The
  active frame comes from `cfg.Frames.Active`; resolution order is in
  `frame/policy.go` `ResolveActive`.
- **Tools** register through `internal/tools`. The canonical constructor is
  `NewDefaultRegistryWithBaseDirAndFrames`; the older
  `NewDefaultRegistryWithBaseDir` delegates to it.
- **Approval** is layered in `internal/agent/policy.go` (`LayeredApprover`):
  builtin allowlist -> workspace trust -> session fallback. The cross-frame
  write detector lives in the same file. Audit reasons:
  `ReasonBuiltinAllow`, `ReasonWorkspaceAllow`, `ReasonSessionAllow`,
  `ReasonSessionDeny`, `ReasonCrossFrameAllow`.

## Where to look

- `SPEC.md` for the canonical spec with line-anchored references
- `README.md` for the user-facing intro
- `docs/llms.txt` for the LLM-readable summary
- `internal/agent/policy.go` for the approval layers
- `internal/frame/policy.go` for frame resolution
- `internal/workspace/bash.go` for the read-only bash classifier

## Commit + push

- Commit in logical groups. One slice per commit when possible.
- Run the full test suite before committing.
- Push to `origin/main` only when explicitly asked and the suite is green.
- No force-push to `main`.
