// mcp.go - the `carlos mcp` CLI surface (list / add / remove / help).
//
// Until now MCP servers could only be managed from inside the TUI (`/mcp
// add`) or the onboarding import screen; a bare `carlos mcp add ...` fell
// through to the default verb and launched the TUI, silently dropping the
// user's intent. This file gives MCP its own top-level command and renders
// every outcome - success or failure - as the same rounded-border box the
// research / please / farewell surfaces use, so the result reads like the
// rest of carlos rather than a stray stderr line.
//
// Parsing lives in internal/mcp.ParseAddSpec so this command and the TUI
// `/mcp add` accept exactly the same grammar.

package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/farewell"
	"github.com/georgebuilds/carlos/internal/mcp"
)

// errMCPReported is returned by runMCP after it has already rendered an error
// box to stdout. main's exit() recognises it and exits non-zero without
// printing a second "carlos: ..." line, so the box is the only output.
var errMCPReported = errors.New("mcp: reported")

// mcpCLIResult is the plan a subcommand produces: the rows to render and
// whether the run succeeded. Separating the plan from the rendering keeps the
// config IO unit-testable without capturing stdout.
type mcpCLIResult struct {
	rows []farewell.Message
	ok   bool
}

// runMCP dispatches `carlos mcp <verb>`, renders the resulting box to stdout,
// and maps a failed plan onto a silent non-zero exit.
func runMCP(args []string) error {
	res := mcpCLIPlan(args)
	mcpRenderBox(res.rows)
	if !res.ok {
		return errMCPReported
	}
	return nil
}

// mcpCLIPlan routes the verb and returns the box plan. A nil/short args slice
// (`carlos mcp`) lists, mirroring the TUI's `/mcp` default.
func mcpCLIPlan(args []string) mcpCLIResult {
	verb := ""
	var rest []string
	if len(args) > 0 {
		verb = strings.ToLower(strings.TrimSpace(args[0]))
		rest = args[1:]
	}
	switch verb {
	case "add":
		return planMCPAdd(rest)
	case "", "list", "ls":
		return planMCPList(rest)
	case "remove", "rm", "delete":
		return planMCPRemove(rest)
	case "help", "-h", "--help":
		return planMCPHelp()
	default:
		return mcpErr(fmt.Sprintf("unknown subcommand %q", verb), "usage: carlos mcp [list|add|remove|help]")
	}
}

// planMCPAdd parses the spec, appends it to the on-disk config, and reports
// the persisted target. Every failure mode (bad spec, no config, duplicate,
// save error) returns an error box rather than a bare stderr line.
func planMCPAdd(args []string) mcpCLIResult {
	sc, err := mcp.ParseAddSpec(args)
	if err != nil {
		return mcpErr(strings.TrimPrefix(err.Error(), "mcp add: "), "usage: carlos mcp add "+mcp.AddUsage)
	}
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mcpErr("no config yet", "run `carlos onboard` first, then add MCP servers")
		}
		return mcpErr("load config: "+err.Error(), "")
	}
	if !cfg.MCP.AddServer(sc) {
		return mcpErr(fmt.Sprintf("server %q already exists", sc.Name), "remove it first with `carlos mcp remove "+sc.Name+"`")
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return mcpErr("save config: "+err.Error(), "")
	}
	return mcpCLIResult{ok: true, rows: []farewell.Message{
		{Emoji: "✅", Text: fmt.Sprintf("added MCP server %q", sc.Name), Detail: mcpTargetLine(sc)},
		{Emoji: "🔌", Text: "restart carlos to connect", Detail: fmt.Sprintf("its tools register under the %q prefix", sc.Name+mcp.ToolNameSeparator)},
	}}
}

// planMCPList renders the configured servers (optionally frame-scoped via a
// leading -f/--frame). No live connection status: the CLI doesn't boot the
// servers, so the box describes config, not a session.
func planMCPList(args []string) mcpCLIResult {
	frame, _, err := parseLeadingFrameFlag(args)
	if err != nil {
		return mcpErr(err.Error(), "")
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mcpEmptyList()
		}
		return mcpErr("load config: "+err.Error(), "")
	}
	servers := cfg.MCP.ForFrame(frame)
	if len(servers) == 0 {
		return mcpEmptyList()
	}
	sorted := make([]mcp.ServerConfig, len(servers))
	copy(sorted, servers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	rows := make([]farewell.Message, 0, len(sorted)+1)
	header := fmt.Sprintf("%d MCP %s configured", len(sorted), plural(len(sorted), "server", "servers"))
	rows = append(rows, farewell.Message{Emoji: "📡", Text: header, Detail: "they connect on the next carlos start"})
	for _, s := range sorted {
		rows = append(rows, farewell.Message{Emoji: "•", Text: s.Name, Detail: mcpTargetLine(s)})
	}
	return mcpCLIResult{ok: true, rows: rows}
}

// planMCPRemove deletes a server by name and persists the change.
func planMCPRemove(args []string) mcpCLIResult {
	name := ""
	if len(args) > 0 {
		name = strings.TrimSpace(args[0])
	}
	if name == "" {
		return mcpErr("remove needs a server name", "usage: carlos mcp remove <name>")
	}
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return mcpErr(fmt.Sprintf("no server named %q", name), "no config yet - nothing to remove")
		}
		return mcpErr("load config: "+err.Error(), "")
	}
	kept := cfg.MCP.Servers[:0]
	removed := false
	for _, s := range cfg.MCP.Servers {
		if s.Name == name {
			removed = true
			continue
		}
		kept = append(kept, s)
	}
	if !removed {
		return mcpErr(fmt.Sprintf("no server named %q", name), "list servers with `carlos mcp list`")
	}
	cfg.MCP.Servers = kept
	if err := config.Save(cfgPath, cfg); err != nil {
		return mcpErr("save config: "+err.Error(), "")
	}
	return mcpCLIResult{ok: true, rows: []farewell.Message{
		{Emoji: "🗑️", Text: fmt.Sprintf("removed MCP server %q", name), Detail: "restart carlos to apply"},
	}}
}

// planMCPHelp renders the command's usage as a box so even `carlos mcp help`
// matches the surface's look.
func planMCPHelp() mcpCLIResult {
	return mcpCLIResult{ok: true, rows: []farewell.Message{
		{Emoji: "📡", Text: "carlos mcp - manage Model Context Protocol servers (Claude Code grammar)"},
		{Emoji: "•", Text: "carlos mcp list [-f <frame>]", Detail: "show configured servers"},
		{Emoji: "•", Text: "carlos mcp add <name> <command> [args...]", Detail: "add a stdio server (the default transport)"},
		{Emoji: "•", Text: "carlos mcp add -t http <name> <url>", Detail: "add a Streamable-HTTP server (-t sse for SSE)"},
		{Emoji: "•", Text: "carlos mcp add ... -e KEY=VAL", Detail: "env override; -H \"H: v\" for http/sse headers (repeatable)"},
		{Emoji: "•", Text: "carlos mcp add ... --frame <name>", Detail: "gate the server to a frame (repeatable)"},
		{Emoji: "•", Text: "carlos mcp remove <name>", Detail: "remove a server"},
		{Emoji: "🔌", Text: "servers connect at startup", Detail: "add/remove take effect on the next carlos run"},
	}}
}

// mcpEmptyList is the no-servers state, with a copy-paste add line.
func mcpEmptyList() mcpCLIResult {
	return mcpCLIResult{ok: true, rows: []farewell.Message{
		{Emoji: "📡", Text: "no MCP servers configured", Detail: "add one with `carlos mcp add <name> -- <command>`"},
	}}
}

// mcpErr builds a single-row error box plan (ok = false).
func mcpErr(text, detail string) mcpCLIResult {
	return mcpCLIResult{ok: false, rows: []farewell.Message{
		{Emoji: "⚠️", Text: text, Detail: detail},
	}}
}

// mcpTargetLine renders a server's transport target plus env/frame badges as
// the dim detail line under its name.
func mcpTargetLine(s mcp.ServerConfig) string {
	var b strings.Builder
	switch s.TransportKind() {
	case mcp.TransportHTTP, mcp.TransportSSE:
		b.WriteString(s.TransportKind() + " " + s.URL)
	default:
		b.WriteString("stdio " + s.Command)
		if len(s.Args) > 0 {
			b.WriteString(" " + strings.Join(s.Args, " "))
		}
	}
	if n := len(s.Env); n > 0 {
		b.WriteString(fmt.Sprintf("  ·  %d env", n))
	}
	if n := len(s.Headers); n > 0 {
		b.WriteString(fmt.Sprintf("  ·  %d %s", n, plural(n, "header", "headers")))
	}
	if len(s.Frames) > 0 {
		b.WriteString("  ·  frames: " + strings.Join(s.Frames, ","))
	}
	return b.String()
}

// plural picks the singular/plural label for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// mcpRenderBox prints the rows as one rounded-border box on stdout, reusing
// farewell.Panel so the styling matches the farewell / research surfaces.
func mcpRenderBox(rows []farewell.Message) {
	if len(rows) == 0 {
		return
	}
	panel := farewell.New()
	for _, r := range rows {
		panel.AddWithDetail(r.Emoji, r.Text, r.Detail)
	}
	out := panel.Render(mcpBoxWidth(), loadPickerPalette())
	if out == "" {
		return
	}
	fmt.Fprintln(os.Stdout, out)
}

// mcpBoxWidth picks the box width from stdout's TTY (the box is written
// there), clamped like the farewell panel and falling back when stdout is
// piped to a file.
func mcpBoxWidth() int {
	fd := int(os.Stdout.Fd())
	if term.IsTerminal(fd) {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return clampFarewellWidth(w)
		}
	}
	return farewellWidthFallback
}
