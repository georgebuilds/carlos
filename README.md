# carlos

A pure-Go TUI agent. Single binary, under 16 MB.

Not strictly a coding agent. General-purpose: file system, shell, web, notes, schedules. Two headline features:

- **Autonomous skill induction.** Carlos watches what you do and turns repeated patterns into reusable skills, which you review and edit like code.
- **First-class sub-agent supervision.** When carlos delegates, you see every delegated agent in a live supervisor view: their intent, tool calls, progress, diffs, and spend. Join, redirect, or stop any of them mid-flight.

Plays nice with Claude Code: loads its skill library, honors your `CLAUDE.md`, familiar slash commands. Skills you write in either tool show up in both.

## Frames

Personal, work, side projects. Each has its own glyph, color, rules, and provider. Switch with Ctrl+F. He never writes across the line unless you ask.

A frame bundles a glyph, an accent from an eight-name palette, a provider and model, and a `system_prompt_append` line that shifts tone. The active frame paints a colored pill in the chat header. Resolution at session boot walks `CARLOS_FRAME`, then `-f <name>`, then `cwd_hints`, then the persisted active, then the default. Cross-frame reads are free; cross-frame writes prompt.

## Permissions

Three layers, evaluated in order. A built-in allowlist auto-approves the read-only tools (notes, read, grep, glob, ls, git inspection). A workspace-trust layer adds a curated read-only bash classifier when the cwd is trusted via `/trust`. Anything else falls through to a session prompt, with "Always" choices remembered until you `/clear`. The `/permissions` overlay shows the layered state, the audit log, and the trusted-workspaces file.

## Gateway

The daemon owns the gateway broker. Four adapters: ntfy and Telegram ship today, Signal is a stub, custom is a pluggable seam. Routing lives under `gateway:` in `~/.carlos/config.yaml` with per-event channel rules. `/schedule` adds cron entries with a small natural-language grammar (`every weekday at 9am`); the daemon runs them and posts results through whichever adapter the routing block selects. See the vault SPEC for the wire shapes.

## Status

Alpha. Dogfood-ready, not yet v1. Site: [georgebuilds.github.io/carlos](https://georgebuilds.github.io/carlos/).

- ~1900 tests across 39 packages
- Cross-compiled for darwin + linux × amd64 + arm64
- Pure Go, no CGO, single binary

## Install

```
brew install georgebuilds/tap/carlos
```

Or grab a tarball from [Releases](https://github.com/georgebuilds/carlos/releases) and drop `carlos` into your `$PATH`.

## Build from source

```
go build ./cmd/carlos
./carlos
```

First launch runs a six-screen onboarding (welcome, name, provider, model, daemon enable, done). State lives in `~/.carlos/`.

## Multi-provider

Anthropic, OpenAI, OpenRouter, Ollama, Gemini. All first-class from day one. Tool-use canonical shape is Anthropic's; adapters normalize others. Capability map honestly exposes what's lost when downgrading (caching, parallel tool use, structured output, vision).

## What's inside v0

1. A single binary, under 16 MB.
2. Five providers, one shape.
3. A memory that lives in plain markdown. Read it in Obsidian, grep it in your shell. Specialized tools query it 10× to 100× more token-efficiently than grepping and globbing.
4. Many frames, one carlos. Personal, work, side projects with their own glyphs, colors, rules, and providers. Cross-frame writes always prompt.
5. Research that goes hard. Many readers fan out, one synthesis returns, every source on file.
6. Proposes new skills, never publishes them. Learned from use, kept only if you approve.
7. Plays nice with Claude Code.
8. MCP draft compliant from day one. Built against the next Model Context Protocol spec, not the old one.

## Layout

```
cmd/carlos/      main TUI binary + daemon
internal/
  agent/         tool-use loop, event log, supervision, approval queue
  frame/         per-session frames (personal + N user-defined)
  tui/           bubbletea chat / manage / onboarding views
  providers/     anthropic, openai, openrouter, ollama, gemini
  tools/         bash, file, grep, web_fetch, web_search, http_request, notes_*
  skills/        skill format, loader, induction + replay-eval
  memory/        SQLite FTS5, summarizer, user model
  notes/         Obsidian-aware vault tools (Goldmark + miniyaml)
  research/      decompose, search, fetch, read, synthesize, verify
  sandbox/       local + git-worktree
  schedule/      cron + NL grammar + scheduled-run execution
  config/        onboarding state, provider keys, user prefs
  theme/         light / dark / NO_COLOR / configurable accent
  daemon/        background scheduler (UDS + launchd/systemd)
  workspace/     trusted-workspaces store + read-only bash classifier
  miniyaml/      tiny hand-rolled YAML for frontmatter
  projectctx/    per-project context (CLAUDE.md, working tree)
  gateway/       chat-surface adapters (ntfy, Telegram, Signal stub, custom)
  usershell/     ! prefix shell driver, jobs overlay, history
skills/          bundled starter skills (calendar/, ...)
docs/            GitHub Pages site + llms.txt
```

## License

GPL-3.0-or-later. See [LICENSE](./LICENSE).
