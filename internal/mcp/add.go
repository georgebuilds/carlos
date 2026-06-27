package mcp

import (
	"fmt"
	"strings"
)

// AddUsage is the one-line grammar shared by the CLI (`carlos mcp add`) and
// the TUI (`/mcp add`) so both surfaces document exactly what ParseAddSpec
// accepts.
const AddUsage = "<name> [-e KEY=VAL]... [-H \"Header: value\"]... [--frame NAME]... [--http|--sse <url>] [--] <command> [args...]"

// ParseAddSpec parses the tokenized arguments of an MCP "add" command into a
// ServerConfig. It backs both `carlos mcp add` (raw argv) and the TUI `/mcp
// add` (whitespace-split input) so the two entry points accept the same
// Claude Code-compatible grammar:
//
//	<name> [flags...] [--] <command> [args...]   stdio (the default)
//	<name> [flags...] --http <url> [flags...]    Streamable HTTP
//	<name> [flags...] --sse  <url> [flags...]    SSE
//
// Flags (any order, before the stdio command or around the --http/--sse url):
//
//	-e, --env KEY=VAL       environment override (repeatable)
//	-H, --header "K: V"     request header for http/sse (repeatable)
//	--frame NAME            gate the server to a frame (repeatable)
//	--http <url>            remote Streamable-HTTP transport
//	--sse  <url>            remote SSE transport
//	--                      end of flags; everything after is the stdio command
//
// The returned ServerConfig is validated, so a malformed spec (missing name,
// missing command/url, an unknown flag, conflicting transports, or headers on
// a stdio server) is reported here rather than as a confusing boot-time
// connect failure later. Every error is prefixed "mcp add: " so callers can
// surface it verbatim.
func ParseAddSpec(tokens []string) (ServerConfig, error) {
	toks := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if strings.TrimSpace(t) != "" {
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 {
		return ServerConfig{}, fmt.Errorf("mcp add: usage: %s", AddUsage)
	}

	name := toks[0]
	if strings.HasPrefix(name, "-") {
		return ServerConfig{}, fmt.Errorf("mcp add: first argument must be a server name, got flag %q", name)
	}
	sc := ServerConfig{Name: name}
	rest := toks[1:]

	value := func(flag string, i int) (string, error) {
		if i+1 >= len(rest) {
			return "", fmt.Errorf("mcp add: %s needs a value", flag)
		}
		return rest[i+1], nil
	}

	var cmdTokens []string
	i := 0
loop:
	for i < len(rest) {
		tok := rest[i]
		switch {
		case tok == "--":
			cmdTokens = append(cmdTokens, rest[i+1:]...)
			break loop
		case tok == "-e" || tok == "--env":
			v, err := value(tok, i)
			if err != nil {
				return ServerConfig{}, err
			}
			k, val, err := splitKV(v, "=", "env override", "KEY=VALUE")
			if err != nil {
				return ServerConfig{}, err
			}
			if sc.Env == nil {
				sc.Env = map[string]string{}
			}
			sc.Env[k] = val
			i += 2
		case tok == "-H" || tok == "--header":
			v, err := value(tok, i)
			if err != nil {
				return ServerConfig{}, err
			}
			k, val, err := splitKV(v, ":", "header", "\"Name: value\"")
			if err != nil {
				return ServerConfig{}, err
			}
			if sc.Headers == nil {
				sc.Headers = map[string]string{}
			}
			sc.Headers[k] = strings.TrimSpace(val)
			i += 2
		case tok == "--frame":
			v, err := value(tok, i)
			if err != nil {
				return ServerConfig{}, err
			}
			sc.Frames = append(sc.Frames, v)
			i += 2
		case tok == "--http" || tok == "--sse":
			v, err := value(tok, i)
			if err != nil {
				return ServerConfig{}, err
			}
			if sc.Transport != "" {
				return ServerConfig{}, fmt.Errorf("mcp add: %s conflicts with an earlier --%s", strings.TrimPrefix(tok, "--"), sc.Transport)
			}
			if tok == "--http" {
				sc.Transport = TransportHTTP
			} else {
				sc.Transport = TransportSSE
			}
			sc.URL = v
			i += 2
		case strings.HasPrefix(tok, "-"):
			return ServerConfig{}, fmt.Errorf("mcp add: unknown flag %q; use --http or --sse for remote servers, or -- before a stdio command", tok)
		default:
			// First bare token starts the stdio command; everything after
			// it (its own flags included) is the command's argv.
			cmdTokens = append(cmdTokens, rest[i:]...)
			break loop
		}
	}

	switch sc.TransportKind() {
	case TransportHTTP, TransportSSE:
		if len(cmdTokens) > 0 {
			return ServerConfig{}, fmt.Errorf("mcp add: %s server takes a url, not a command (%q)", sc.TransportKind(), strings.Join(cmdTokens, " "))
		}
	default: // stdio
		if len(sc.Headers) > 0 {
			return ServerConfig{}, fmt.Errorf("mcp add: -H/--header only applies to --http or --sse servers")
		}
		if len(cmdTokens) == 0 {
			return ServerConfig{}, fmt.Errorf("mcp add: stdio server %q needs a command (e.g. `-- npx -y @scope/pkg`)", name)
		}
		sc.Command = cmdTokens[0]
		if len(cmdTokens) > 1 {
			sc.Args = cmdTokens[1:]
		}
	}

	if err := sc.Validate(); err != nil {
		return ServerConfig{}, err
	}
	return sc, nil
}

// splitKV splits "key<sep>value" for -e and -H. label/shape are woven into
// the error so a malformed token names its own fix.
func splitKV(s, sep, label, shape string) (string, string, error) {
	k, v, ok := strings.Cut(s, sep)
	if !ok || strings.TrimSpace(k) == "" {
		return "", "", fmt.Errorf("mcp add: %s %q must be %s", label, s, shape)
	}
	return strings.TrimSpace(k), v, nil
}
