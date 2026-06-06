# carlos

A pure-Go TUI agent. Single binary, under 15 MB.

Not strictly a coding agent. General-purpose: file system, shell, web, notes, schedules. Two headline features:

- **Autonomous skill induction.** Carlos watches what you do and turns repeated patterns into reusable skills, which you review and edit like code.
- **First-class sub-agent supervision.** When carlos delegates, you see every delegated agent in a live supervisor view: their intent, tool calls, progress, diffs, and spend. Join, redirect, or stop any of them mid-flight.

Plays nice with Claude Code: loads its skill library, honors your `CLAUDE.md`, familiar slash commands. Skills you write in either tool show up in both.

## Status

Alpha. Dogfood-ready, not yet v1. Site: [georgebuilds.github.io/carlos](https://georgebuilds.github.io/carlos/).

- ~1170 tests across 28 packages
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

Anthropic, OpenAI, OpenRouter, Ollama. All first-class from day one. Tool-use canonical shape is Anthropic's; adapters normalize others. Capability map honestly exposes what's lost when downgrading (caching, parallel tool use, structured output, vision).

## What's inside v0

1. A single binary, under 15 MB.
2. Four providers, one shape.
3. A memory that lives in plain markdown. Read it in Obsidian, grep it in your shell. Specialized tools query it 10× to 100× more token-efficiently than grepping and globbing.
4. Work and life on separate shelves. Two contexts, never crossed.
5. Research that goes hard. Many readers fan out, one synthesis returns, every source on file.
6. Proposes new skills, never publishes them. Learned from use, kept only if you approve.
7. Plays nice with Claude Code.
8. MCP draft compliant from day one. Built against the next Model Context Protocol spec, not the old one.

## Layout

```
cmd/carlos/      main TUI binary + daemon
internal/
  agent/         tool-use loop, event log, supervision, approval queue
  tui/           bubbletea chat / manage / onboarding views
  providers/     anthropic, openai, openrouter, ollama
  tools/         bash, file, grep, web_fetch, web_search, http_request, notes_*
  skills/        skill format, loader, induction + replay-eval
  memory/        SQLite FTS5, summarizer, user model
  notes/         Obsidian-aware vault tools (Goldmark + miniyaml)
  research/      decompose → search → fetch → read → synthesize → verify
  sandbox/       local + git-worktree
  schedule/      cron + NL grammar + scheduled-run execution
  config/        onboarding state, provider keys, user prefs
  theme/         light / dark / NO_COLOR / configurable accent
  daemon/        background scheduler (UDS + launchd/systemd)
  miniyaml/      tiny hand-rolled YAML for frontmatter
  projectctx/    per-project context (CLAUDE.md, working tree)
  gateway/       chat surface adapter
docs/            coming-soon site (served via GitHub Pages)
```

## License

GPL-3.0-or-later. See [LICENSE](./LICENSE).
