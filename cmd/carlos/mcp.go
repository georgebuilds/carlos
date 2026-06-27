// mcp.go - the `carlos mcp` CLI surface. Deliberately mirrors
// `claude mcp` so a command copied from the internet works by swapping
// the binary name: `claude mcp add ...` -> `carlos mcp add ...`.
//
// carlos has ONE config (~/.carlos/config.yaml) with per-frame gating
// rather than Claude Code's local/project/user scopes, so `--scope` is
// accepted-and-ignored (with a note) for paste compatibility and the
// carlos-native `--frame` flag does the real gating. MCP OAuth flags are
// likewise accepted-and-ignored: carlos's MCP client has no OAuth.
//
// Servers connect at carlos startup (MCP is wired at boot), so add/remove
// take effect on the next run, same as the TUI `/mcp` slash.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/mcp"
)

// runMCP dispatches `carlos mcp <subcommand>`. With no subcommand it
// prints usage, mirroring `claude mcp`.
func runMCP(args []string) error {
	if len(args) == 0 {
		printMCPUsage()
		return nil
	}
	switch args[0] {
	case "add":
		return runMCPAdd(args[1:])
	case "add-json":
		return runMCPAddJSON(args[1:])
	case "list", "ls":
		return runMCPList(args[1:])
	case "get":
		return runMCPGet(args[1:])
	case "remove", "rm":
		return runMCPRemove(args[1:])
	case "help", "-h", "--help":
		printMCPUsage()
		return nil
	default:
		return fmt.Errorf("mcp: unknown subcommand %q (expected add | add-json | list | get | remove)", args[0])
	}
}

// parseMCPAdd parses `carlos mcp add` arguments into a ServerConfig plus a
// list of human-readable warnings (for accepted-but-ignored flags). It is
// a pure function so the full flag matrix is table-testable.
//
// Grammar (matching `claude mcp add`):
//
//	[flags] <name> <command|url> [args...]
//	[flags] <name> -- <command> [args...]
//
// Flags may appear before or after the positionals (commander.js
// behavior). `--` ends flag parsing; everything after it is the verbatim
// stdio command + args. Flags accept both `--flag value` and `--flag=value`.
func parseMCPAdd(args []string) (mcp.ServerConfig, []string, error) {
	var (
		transport   string
		env         = map[string]string{}
		headers     = map[string]string{}
		frames      []string
		positionals []string
		passthrough []string
		warnings    []string
		sawDashDash bool
	)

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			passthrough = append(passthrough, args[i+1:]...)
			sawDashDash = true
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}

		// Flag, optionally in `--flag=value` form.
		name := a
		inlineVal := ""
		hasInline := false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name, inlineVal, hasInline = a[:eq], a[eq+1:], true
		}
		takeVal := func() (string, error) {
			if hasInline {
				return inlineVal, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("mcp add: flag %s needs a value", name)
			}
			i++
			return args[i], nil
		}

		switch name {
		case "-t", "--transport":
			v, err := takeVal()
			if err != nil {
				return mcp.ServerConfig{}, nil, err
			}
			transport = strings.ToLower(strings.TrimSpace(v))
		case "-e", "--env":
			v, err := takeVal()
			if err != nil {
				return mcp.ServerConfig{}, nil, err
			}
			k, val, ok := strings.Cut(v, "=")
			k = strings.TrimSpace(k)
			if !ok || k == "" {
				return mcp.ServerConfig{}, nil, fmt.Errorf("mcp add: --env expects KEY=value, got %q", v)
			}
			env[k] = val
		case "-H", "--header":
			v, err := takeVal()
			if err != nil {
				return mcp.ServerConfig{}, nil, err
			}
			k, val, ok := strings.Cut(v, ":")
			if !ok || strings.TrimSpace(k) == "" {
				return mcp.ServerConfig{}, nil, fmt.Errorf("mcp add: --header expects \"Name: value\", got %q", v)
			}
			headers[strings.TrimSpace(k)] = strings.TrimSpace(val)
		case "-f", "--frame":
			v, err := takeVal()
			if err != nil {
				return mcp.ServerConfig{}, nil, err
			}
			if f := strings.TrimSpace(v); f != "" {
				frames = append(frames, f)
			}
		case "-s", "--scope":
			v, err := takeVal()
			if err != nil {
				return mcp.ServerConfig{}, nil, err
			}
			warnings = append(warnings, fmt.Sprintf(
				"ignoring --scope %q: carlos uses one config with per-frame gating, not scopes (use --frame <name> to gate to a frame)", v))
		case "--client-id", "--callback-port":
			// Value-taking OAuth flags: consume the value so it isn't
			// mistaken for a positional, then ignore (carlos's MCP client
			// has no OAuth).
			if _, err := takeVal(); err != nil {
				return mcp.ServerConfig{}, nil, err
			}
			warnings = append(warnings, fmt.Sprintf("ignoring %s: carlos's MCP client does not support OAuth", name))
		case "--client-secret":
			// Boolean per `claude mcp add` (it prompts, or reads
			// MCP_CLIENT_SECRET); takes no value, so nothing to consume.
			warnings = append(warnings, "ignoring --client-secret: carlos's MCP client does not support OAuth")
		default:
			return mcp.ServerConfig{}, nil, fmt.Errorf(
				"mcp add: unknown flag %q (put the server command after `--`, e.g. carlos mcp add foo -- npx -y pkg)", name)
		}
	}

	if len(positionals) == 0 {
		return mcp.ServerConfig{}, nil, errors.New(
			"mcp add: usage - carlos mcp add [-t stdio|sse|http] [-e K=V] [-H \"H: v\"] <name> <command|url> [args...]")
	}

	sc := mcp.ServerConfig{Name: positionals[0]}
	if transport == "" {
		transport = mcp.TransportStdio
	}
	sc.Transport = transport
	if len(env) > 0 {
		sc.Env = env
	}
	if len(headers) > 0 {
		sc.Headers = headers
	}
	if len(frames) > 0 {
		sc.Frames = frames
	}

	switch transport {
	case mcp.TransportStdio:
		// `--` passthrough wins; otherwise the remaining positionals are
		// the command + args.
		cmdArgs := passthrough
		if !sawDashDash {
			cmdArgs = positionals[1:]
		} else if len(positionals) > 1 {
			// `foo bar -- cmd`: the `bar` between name and `--` is a paste
			// mistake; surface it rather than silently dropping it.
			return mcp.ServerConfig{}, nil, fmt.Errorf(
				"mcp add: unexpected arguments before `--`: %s", strings.Join(positionals[1:], " "))
		}
		if len(cmdArgs) == 0 {
			return mcp.ServerConfig{}, nil, errors.New(
				"mcp add: stdio server needs a command (e.g. carlos mcp add foo -- npx -y pkg)")
		}
		sc.Command = cmdArgs[0]
		if len(cmdArgs) > 1 {
			sc.Args = cmdArgs[1:]
		}
	case mcp.TransportHTTP, mcp.TransportSSE:
		url := ""
		switch {
		case len(positionals) > 1:
			url = positionals[1]
		case len(passthrough) > 0:
			url = passthrough[0]
		}
		if url == "" {
			return mcp.ServerConfig{}, nil, fmt.Errorf("mcp add: %s server needs a url", transport)
		}
		// A remote server takes exactly <name> <url>; extra tokens are a
		// paste mistake (e.g. a stray stdio command), not silently ignored.
		if len(positionals) > 2 {
			return mcp.ServerConfig{}, nil, fmt.Errorf(
				"mcp add: %s server takes a single url; unexpected: %s", transport, strings.Join(positionals[2:], " "))
		}
		sc.URL = url
	default:
		return mcp.ServerConfig{}, nil, fmt.Errorf(
			"mcp add: unknown transport %q (want stdio, sse, or http)", transport)
	}

	return sc, warnings, nil
}

func runMCPAdd(args []string) error {
	sc, warnings, err := parseMCPAdd(args)
	if err != nil {
		return err
	}
	if err := sc.Validate(); err != nil {
		return err
	}
	cfg, cfgPath, err := loadConfigForMCP("add")
	if err != nil {
		return err
	}
	if !cfg.MCP.AddServer(sc) {
		return fmt.Errorf("mcp add: a server named %q already exists (remove it with `carlos mcp remove %s`)", sc.Name, sc.Name)
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("mcp add: save config: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "carlos: mcp:", w)
	}
	fmt.Printf("Added %s MCP server %q. It connects on the next carlos start.\n", sc.TransportKind(), sc.Name)
	return nil
}

// mcpJSON is the lenient shape of `mcp add-json`'s payload. Claude Code
// uses `type` for the transport; we also accept `transport` so either
// spelling round-trips.
type mcpJSON struct {
	Type      string            `json:"type"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Frames    []string          `json:"frames"`
}

func runMCPAddJSON(args []string) error {
	if len(args) < 2 {
		return errors.New("mcp add-json: usage - carlos mcp add-json <name> '<json>'")
	}
	name := args[0]
	var j mcpJSON
	if err := json.Unmarshal([]byte(args[1]), &j); err != nil {
		return fmt.Errorf("mcp add-json: invalid JSON: %w", err)
	}
	transport := j.Type
	if transport == "" {
		transport = j.Transport
	}
	sc := mcp.ServerConfig{
		Name:      name,
		Transport: strings.ToLower(strings.TrimSpace(transport)),
		Command:   j.Command,
		Args:      j.Args,
		Env:       j.Env,
		URL:       j.URL,
		Headers:   j.Headers,
		Frames:    j.Frames,
	}
	if err := sc.Validate(); err != nil {
		return fmt.Errorf("mcp add-json: %w", err)
	}
	cfg, cfgPath, err := loadConfigForMCP("add-json")
	if err != nil {
		return err
	}
	if !cfg.MCP.AddServer(sc) {
		return fmt.Errorf("mcp add-json: a server named %q already exists", name)
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("mcp add-json: save config: %w", err)
	}
	fmt.Printf("Added %s MCP server %q. It connects on the next carlos start.\n", sc.TransportKind(), name)
	return nil
}

func runMCPList(args []string) error {
	cfg, _, err := loadConfigForMCP("list")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("No MCP servers configured. Add one with `carlos mcp add`.")
			return nil
		}
		return err
	}
	if len(cfg.MCP.Servers) == 0 {
		fmt.Println("No MCP servers configured. Add one with `carlos mcp add`.")
		return nil
	}
	servers := append([]mcp.ServerConfig(nil), cfg.MCP.Servers...)
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	for _, s := range servers {
		line := fmt.Sprintf("%s: %s", s.Name, mcpTargetCLI(s))
		if len(s.Frames) > 0 {
			line += "  [frames: " + strings.Join(s.Frames, ",") + "]"
		}
		fmt.Println(line)
	}
	return nil
}

func runMCPGet(args []string) error {
	if len(args) != 1 {
		return errors.New("mcp get: usage - carlos mcp get <name>")
	}
	name := args[0]
	cfg, _, err := loadConfigForMCP("get")
	if err != nil {
		return err
	}
	for _, s := range cfg.MCP.Servers {
		if s.Name != name {
			continue
		}
		fmt.Printf("%s\n", s.Name)
		fmt.Printf("  transport: %s\n", s.TransportKind())
		switch s.TransportKind() {
		case mcp.TransportHTTP, mcp.TransportSSE:
			fmt.Printf("  url: %s\n", s.URL)
		default:
			fmt.Printf("  command: %s\n", s.Command)
			if len(s.Args) > 0 {
				fmt.Printf("  args: %s\n", strings.Join(s.Args, " "))
			}
		}
		if len(s.Env) > 0 {
			fmt.Printf("  env: %s\n", strings.Join(sortedKeys(s.Env), ", "))
		}
		if len(s.Headers) > 0 {
			fmt.Printf("  headers: %s\n", strings.Join(sortedKeys(s.Headers), ", "))
		}
		if len(s.Frames) > 0 {
			fmt.Printf("  frames: %s\n", strings.Join(s.Frames, ", "))
		}
		return nil
	}
	return fmt.Errorf("mcp get: no server named %q", name)
}

func runMCPRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("mcp remove: usage - carlos mcp remove <name>")
	}
	name := args[0]
	cfg, cfgPath, err := loadConfigForMCP("remove")
	if err != nil {
		return err
	}
	out := cfg.MCP.Servers[:0]
	found := false
	for _, s := range cfg.MCP.Servers {
		if s.Name == name {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return fmt.Errorf("mcp remove: no server named %q", name)
	}
	cfg.MCP.Servers = out
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("mcp remove: save config: %w", err)
	}
	fmt.Printf("Removed MCP server %q.\n", name)
	return nil
}

// loadConfigForMCP loads the config for an mcp subcommand, turning a
// missing-file error into a clear "run onboard first" message for the
// write paths. The list path special-cases os.ErrNotExist itself (empty
// state), so it gets the raw error back to inspect.
func loadConfigForMCP(verb string) (*config.Config, string, error) {
	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if verb == "list" {
			return nil, cfgPath, err // caller inspects os.ErrNotExist
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, cfgPath, fmt.Errorf("mcp %s: no config yet - run `carlos onboard` first", verb)
		}
		return nil, cfgPath, fmt.Errorf("mcp %s: load config: %w", verb, err)
	}
	return cfg, cfgPath, nil
}

// mcpTargetCLI renders the transport + connection target for a list row.
func mcpTargetCLI(s mcp.ServerConfig) string {
	switch s.TransportKind() {
	case mcp.TransportHTTP, mcp.TransportSSE:
		return s.TransportKind() + " " + s.URL
	default:
		cmd := s.Command
		if len(s.Args) > 0 {
			cmd += " " + strings.Join(s.Args, " ")
		}
		return "stdio " + cmd
	}
}

// sortedKeys returns the map keys in stable order (env/header names are
// printed, not their secret values).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func printMCPUsage() {
	fmt.Println(`carlos mcp - manage Model Context Protocol servers (mirrors ` + "`claude mcp`" + `)

Usage:
  carlos mcp add [flags] <name> <command|url> [args...]   add a server
  carlos mcp add-json <name> '<json>'                     add a server from a JSON blob
  carlos mcp list                                         list configured servers
  carlos mcp get <name>                                   show one server's config
  carlos mcp remove <name>                                remove a server

add flags:
  -t, --transport <stdio|sse|http>   transport (default stdio)
  -e, --env KEY=value                environment variable (repeatable)
  -H, --header "Name: value"         HTTP header for sse/http (repeatable)
  -f, --frame <name>                 gate the server to a frame (repeatable; carlos-native)
  -s, --scope <local|user|project>   accepted for claude compatibility; ignored (carlos uses frames)
  --                                 everything after is the verbatim stdio command + args

Examples:
  carlos mcp add github -e GITHUB_TOKEN=ghp_xxx -- npx -y @modelcontextprotocol/server-github
  carlos mcp add --transport sse linear https://mcp.linear.app/sse
  carlos mcp add --transport http notion https://mcp.notion.com/mcp -H "Authorization: Bearer xxx"
  carlos mcp add-json weather '{"type":"stdio","command":"npx","args":["-y","weather-mcp"]}'

Servers connect on the next carlos start.`)
}
