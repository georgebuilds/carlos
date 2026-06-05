package tools

import (
	"context"
	"errors"

	"github.com/georgebuilds/carlos/internal/config"
)

type Tool interface {
	Name() string
	Description() string
	Schema() []byte
	Execute(ctx context.Context, input []byte) ([]byte, error)
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Execute(ctx context.Context, name string, input []byte) ([]byte, error) {
	t, ok := r.Get(name)
	if !ok {
		return nil, errors.New("tools: unknown tool: " + name)
	}
	return t.Execute(ctx, input)
}

// All returns every registered tool in deterministic name order. Useful
// for callers that need to expose the tool list to a provider (e.g.
// Anthropic's `tools` array on /v1/messages).
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	// Sort by name so the order is stable across processes (map
	// iteration is randomized in Go).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Name() > out[j].Name(); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// NewDefaultRegistry constructs a Registry pre-populated with every
// tool shipped in this package. The foreground (cmd/carlos) is free to
// build its own Registry from scratch and pick & choose; this factory
// just saves the typing in the common case.
//
// Tools registered:
//   - bash              — shell command runner (non-PTY)
//   - read/write/edit   — file ops
//   - grep/glob         — search
//   - git_status / git_diff / git_log / git_blame / git_show — git read-only
//   - web_fetch / web_search — Phase 11 web access
//   - notes_get / notes_search / notes_backlinks / notes_tagged /
//     notes_neighbors / notes_recent / notes_resolve — Phase 12
//     Obsidian-aware vault queries (no-op envelope when no vault
//     configured AND no per-call override).
//
// The bash tool registered here uses the no-PTY default. Callers that
// want PTY can construct a separate BashTool{PTY:true} and register it
// under a distinct name (e.g. "bash_pty") to keep the two surfaces
// addressable from the model side.
//
// Vault is zero-valued: the seven notes_* tools register but reply with
// the "vault not configured" envelope until cmd/carlos hands them a
// VaultConfig via NewDefaultRegistryWithBaseDir.
func NewDefaultRegistry() *Registry {
	return NewDefaultRegistryWithBaseDir("", config.VaultConfig{})
}

// NewDefaultRegistryWithBaseDir is the slice-7f sandbox-aware variant,
// extended in Phase 12 with the VaultConfig for the notes_* tools.
//
// When baseDir is non-empty the bash + 5 file tools (read/write/edit/
// grep/glob) resolve relative path inputs against baseDir, landing
// inside a sandbox.Worktree the foreground opened. Absolute paths are
// honored as-is so a model that asks for /etc/hosts isn't silently
// redirected. Git tools are NOT BaseDir-sandboxed in v0 because they
// shell out to `git` which has its own -C semantics; a future slice
// can extend them.
//
// vaultCfg is the user's Obsidian vault settings. The seven notes_*
// tools share a single process-wide *notes.Cache constructed with
// vaultCfg.Exclude. When vaultCfg.Path is empty the tools still
// register but reply with the documented "vault not configured"
// envelope (unless the caller passes the per-call `vault:` override).
//
// Empty baseDir + zero VaultConfig → backwards-compatible behavior
// for legacy call sites.
func NewDefaultRegistryWithBaseDir(baseDir string, vaultCfg config.VaultConfig) *Registry {
	r := NewRegistry()
	bash := NewBashTool()
	bash.BaseDir = baseDir
	r.Register(bash)
	read := NewReadTool()
	read.BaseDir = baseDir
	r.Register(read)
	write := NewWriteTool()
	write.BaseDir = baseDir
	r.Register(write)
	edit := NewEditTool()
	edit.BaseDir = baseDir
	r.Register(edit)
	grep := NewGrepTool()
	grep.BaseDir = baseDir
	r.Register(grep)
	glob := NewGlobTool()
	glob.BaseDir = baseDir
	r.Register(glob)
	r.Register(NewGitStatusTool())
	r.Register(NewGitDiffTool())
	r.Register(NewGitLogTool())
	r.Register(NewGitBlameTool())
	r.Register(NewGitShowTool())
	// Phase 11a/b: web_fetch + web_search. Don't touch the local
	// filesystem, so BaseDir is irrelevant — register the same
	// instance for both the worktree-sandboxed and non-sandboxed
	// factories. web_search picks its backend from env at
	// construction (BRAVE_API_KEY → Brave; SEARXNG_URL → SearXNG;
	// else DuckDuckGo HTML fallback).
	r.Register(NewWebFetchTool())
	r.Register(NewWebSearchTool())
	// http_request: method-parametric HTTP for API consumption.
	// web_fetch handles human-readable web pages (GET + HTML→text);
	// http_request handles JSON / REST / GraphQL / webhooks with raw
	// status + headers + body and any verb. AllowPrivate flag mirrors
	// web_fetch so the two share one policy lever.
	httpReq := NewHTTPRequestTool()
	r.Register(httpReq)
	// Phase 12: notes_* tool family. One *notes.Cache shared across
	// the seven tools so a vault opened by one tool is reused by the
	// next. When vaultCfg.Path is empty + no per-call override is
	// supplied, each tool returns the "vault not configured"
	// envelope rather than crashing.
	nenv := newNotesEnv(vaultCfg)
	r.Register(NewNotesGetTool(nenv))
	r.Register(NewNotesSearchTool(nenv))
	r.Register(NewNotesBacklinksTool(nenv))
	r.Register(NewNotesTaggedTool(nenv))
	r.Register(NewNotesNeighborsTool(nenv))
	r.Register(NewNotesRecentTool(nenv))
	r.Register(NewNotesResolveTool(nenv))
	return r
}
