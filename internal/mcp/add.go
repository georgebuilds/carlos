package mcp

import (
	"fmt"
	"strings"
)

// AddUsage is the one-line grammar shared by the CLI (`carlos mcp add`) and
// the TUI (`/mcp add`). It mirrors Claude Code's `claude mcp add`.
const AddUsage = "[-t stdio|sse|http] [-e KEY=VAL]... [-H \"Header: value\"]... [--frame NAME]... <name> <command|url> [args...]"

// ParseAddSpec parses the tokenized arguments of an MCP "add" command into a
// ServerConfig. It backs both `carlos mcp add` (raw argv) and the TUI `/mcp
// add` (whitespace-split input), and matches Claude Code's `claude mcp add`
// grammar so muscle memory and copy-pasted commands work unchanged:
//
//	mcp add [options] <name> <command> [args...]   stdio (the default)
//	mcp add -t http <name> <url>                   Streamable HTTP
//	mcp add -t sse  <name> <url>                   SSE
//
// Like Claude Code (commander.js), options may appear before OR after the
// name; the first non-option token is the name. For stdio the next positional
// is the command and everything after it is passed through verbatim as argv
// (so `npx -y @scope/pkg` keeps its -y). For http/sse the next positional is
// the URL.
//
// Options:
//
//	-t, --transport <stdio|sse|http>   transport type (default stdio)
//	-e, --env KEY=VAL                  environment override (repeatable)
//	-H, --header "Name: value"         request header for http/sse (repeatable)
//	-s, --scope <scope>                accepted for Claude Code parity; ignored
//	                                   (carlos scopes by frame, not scope)
//	--frame NAME                       gate the server to a frame (repeatable)
//	--                                 end of options; the rest is command+args
//
// carlos also accepts `--http <url>` / `--sse <url>` as aliases (url as the
// flag value) for back-compat. Long options accept the `--flag=value` form.
//
// The returned ServerConfig is validated, so a malformed spec (missing name,
// missing command/url, unknown flag, conflicting transports, or headers on a
// stdio server) is reported here rather than as a confusing boot-time connect
// failure later. Every error is prefixed "mcp add: ".
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

	var (
		env         = map[string]string{}
		headers     = map[string]string{}
		frames      []string
		transport   string
		transportOK bool
		urlFromFlag string
		positionals []string
		afterDouble bool
	)

	setTransport := func(v string) error {
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case TransportStdio, TransportSSE, TransportHTTP:
		default:
			return fmt.Errorf("mcp add: unknown transport %q (want stdio, sse, or http)", v)
		}
		if transportOK && transport != v {
			return fmt.Errorf("mcp add: conflicting transports %q and %q", transport, v)
		}
		transport, transportOK = v, true
		return nil
	}

	i := 0
	for i < len(toks) {
		t := toks[i]
		if afterDouble {
			positionals = append(positionals, t)
			i++
			continue
		}

		// Split the `--flag=value` form so it shares the value-taking cases.
		flag, inline, hasInline := t, "", false
		if strings.HasPrefix(t, "--") {
			if eq := strings.IndexByte(t, '='); eq >= 0 {
				flag, inline, hasInline = t[:eq], t[eq+1:], true
			}
		}
		// takeValue returns the option's value (inline or the next token) and
		// how many tokens it consumed.
		takeValue := func() (string, int, error) {
			if hasInline {
				return inline, 1, nil
			}
			if i+1 >= len(toks) {
				return "", 0, fmt.Errorf("mcp add: %s needs a value", flag)
			}
			return toks[i+1], 2, nil
		}

		switch {
		case t == "--":
			afterDouble = true
			i++
		case flag == "-e" || flag == "--env":
			v, n, err := takeValue()
			if err != nil {
				return ServerConfig{}, err
			}
			k, val, err := splitKV(v, "=", "env override", "KEY=VALUE")
			if err != nil {
				return ServerConfig{}, err
			}
			env[k] = val
			i += n
		case flag == "-H" || flag == "--header":
			v, n, err := takeValue()
			if err != nil {
				return ServerConfig{}, err
			}
			k, val, err := splitKV(v, ":", "header", "\"Name: value\"")
			if err != nil {
				return ServerConfig{}, err
			}
			headers[k] = strings.TrimSpace(val)
			i += n
		case flag == "-t" || flag == "--transport":
			v, n, err := takeValue()
			if err != nil {
				return ServerConfig{}, err
			}
			if err := setTransport(v); err != nil {
				return ServerConfig{}, err
			}
			i += n
		case flag == "-s" || flag == "--scope":
			// Accepted for Claude Code parity; carlos has no scope concept
			// (it gates servers by frame), so the value is consumed and
			// ignored rather than erroring on a familiar flag.
			_, n, err := takeValue()
			if err != nil {
				return ServerConfig{}, err
			}
			i += n
		case flag == "--frame":
			v, n, err := takeValue()
			if err != nil {
				return ServerConfig{}, err
			}
			frames = append(frames, v)
			i += n
		case flag == "--http" || flag == "--sse":
			v, n, err := takeValue()
			if err != nil {
				return ServerConfig{}, err
			}
			tk := TransportHTTP
			if flag == "--sse" {
				tk = TransportSSE
			}
			if err := setTransport(tk); err != nil {
				return ServerConfig{}, err
			}
			urlFromFlag = v
			i += n
		case strings.HasPrefix(t, "-") && t != "-":
			return ServerConfig{}, fmt.Errorf("mcp add: unknown flag %q; see `carlos mcp add` usage", t)
		default:
			positionals = append(positionals, t)
			i++
			// For a stdio server the second positional is the command, and
			// everything after it is verbatim argv (so command flags like
			// `npx -y` aren't parsed as our options). http/sse take only a
			// url positional, so they don't grab the rest.
			if len(positionals) >= 2 && urlFromFlag == "" {
				kind := transport
				if kind == "" {
					kind = TransportStdio
				}
				if kind == TransportStdio {
					positionals = append(positionals, toks[i:]...)
					i = len(toks)
				}
			}
		}
	}

	if len(positionals) == 0 {
		return ServerConfig{}, fmt.Errorf("mcp add: missing server name; usage: %s", AddUsage)
	}

	kind := transport
	if kind == "" {
		kind = TransportStdio
	}
	sc := ServerConfig{Name: positionals[0]}
	if len(env) > 0 {
		sc.Env = env
	}
	if len(frames) > 0 {
		sc.Frames = frames
	}

	switch kind {
	case TransportHTTP, TransportSSE:
		sc.Transport = kind
		if len(headers) > 0 {
			sc.Headers = headers
		}
		switch {
		case urlFromFlag != "":
			sc.URL = urlFromFlag
			if len(positionals) > 1 {
				return ServerConfig{}, fmt.Errorf("mcp add: %s server takes only a url, got extra %q", kind, strings.Join(positionals[1:], " "))
			}
		case len(positionals) >= 2:
			sc.URL = positionals[1]
			if len(positionals) > 2 {
				return ServerConfig{}, fmt.Errorf("mcp add: %s server takes a single url, got extra %q", kind, strings.Join(positionals[2:], " "))
			}
		default:
			return ServerConfig{}, fmt.Errorf("mcp add: %s server %q needs a url (e.g. `-t %s %s https://host/mcp`)", kind, sc.Name, kind, sc.Name)
		}
	default: // stdio
		if len(headers) > 0 {
			return ServerConfig{}, fmt.Errorf("mcp add: -H/--header only applies to --transport http or sse servers")
		}
		if len(positionals) < 2 {
			return ServerConfig{}, fmt.Errorf("mcp add: stdio server %q needs a command (e.g. `%s -- npx -y @scope/pkg`)", sc.Name, sc.Name)
		}
		// A URL where a command is expected almost always means the user put
		// the transport flag after the url (e.g. `add name <url> -t http`),
		// which - matching Claude Code's pass-through - swallowed it as argv.
		// Catch that helpfully rather than persisting a junk stdio server.
		if cmd := positionals[1]; strings.HasPrefix(cmd, "http://") || strings.HasPrefix(cmd, "https://") {
			return ServerConfig{}, fmt.Errorf("mcp add: %q looks like a url; for a remote server put the transport first: `-t http %s %s`", cmd, sc.Name, cmd)
		}
		sc.Command = positionals[1]
		if len(positionals) > 2 {
			sc.Args = positionals[2:]
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
