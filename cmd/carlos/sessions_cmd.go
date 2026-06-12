// `carlos sessions <list|rm> ...` - user-initiated chat-session
// management on the CLI side. The deletion primitive itself lives in
// internal/agent (DeleteSession); this file is the thin, testable CLI
// surface over it. Reads/writes the user's real state.db at
// ~/.carlos/state.db so it composes with the TUI and daemon, which
// project the same source of truth.
//
// Testability: the verb bodies are factored into runSessionsList and
// runSessionsRm, which take an explicit *agent.SQLiteEventLog plus
// injected io.Reader / io.Writer so the confirm prompt and the list
// output can be exercised without a TTY. runSessions is the thin
// arg-parser + state.db opener that wraps them for the real CLI.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/georgebuilds/carlos/internal/agent"
)

// runSessions dispatches `carlos sessions <list|rm> ...`. It opens the
// user's state.db once and hands the open log to the verb body. Kept
// thin so the testable logic stays in runSessionsList / runSessionsRm.
func runSessions(args []string) error {
	if len(args) == 0 {
		return errors.New("sessions: subcommand required (list | rm)")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("sessions: home dir: %w", err)
	}
	log, err := agent.OpenStateDB(filepath.Join(home, ".carlos", "state.db"))
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer log.Close()

	switch args[0] {
	case "list":
		return runSessionsList(log, os.Stdout)
	case "rm":
		rest := args[1:]
		assumeYes, rest := stripYesFlag(rest)
		if len(rest) == 0 {
			return errors.New("sessions rm: session id required (sessions rm <id> [-y])")
		}
		if len(rest) > 1 {
			return fmt.Errorf("sessions rm: expected a single id, got %d args", len(rest))
		}
		return runSessionsRm(log, rest[0], assumeYes, os.Stdin, os.Stdout)
	default:
		return fmt.Errorf("sessions: unknown subcommand %q (expected list | rm)", args[0])
	}
}

// stripYesFlag pulls a -y / --yes flag out of the argument list,
// reporting whether it was present and returning the remaining
// (non-flag) arguments. Order-independent so `rm -y <id>` and
// `rm <id> -y` both work.
func stripYesFlag(args []string) (assumeYes bool, rest []string) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			assumeYes = true
		default:
			rest = append(rest, a)
		}
	}
	return assumeYes, rest
}

// runSessionsList prints every top-level session, most-recent first,
// mirroring the output style of runApprovals: an id/state/count header
// line plus an indented preview line. Writes to out so tests can
// assert on the rendered text.
func runSessionsList(log *agent.SQLiteEventLog, out io.Writer) error {
	ctx := context.Background()
	sessions, err := agent.ListUserSessions(ctx, log, "")
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Fprintln(out, "no sessions yet - start one with `carlos`")
		return nil
	}
	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(out, "%s  [%s]  %d msg%s  %s\n",
			s.ID, s.State, s.UserMsgs, pluralS(s.UserMsgs), title)
		if s.Preview != "" {
			fmt.Fprintf(out, "         %s\n", s.Preview)
		}
	}
	return nil
}

// runSessionsRm hard-deletes the session identified by id after a
// confirmation step, then reports the result to out. The id may be an
// exact match or a unique short-id prefix against the listed sessions.
//
// Confirmation: unless assumeYes is set, it prints an irreversible
// warning that names the session and reads a y/N answer from in. Only
// an explicit yes proceeds; anything else (including EOF) leaves the
// session untouched and reports the cancellation.
//
// DeleteSession is called with force=false so a session another live
// process is actively driving is refused (ErrSessionLive). The guard
// errors are mapped to friendly messages for the caller to surface.
func runSessionsRm(log *agent.SQLiteEventLog, id string, assumeYes bool, in io.Reader, out io.Writer) error {
	ctx := context.Background()

	resolved, title, err := resolveSessionID(ctx, log, id)
	if err != nil {
		return err
	}

	if !assumeYes {
		label := title
		if label == "" {
			label = resolved
		}
		fmt.Fprintf(out, "permanently delete %q and its sub-agents? this cannot be undone. [y/N] ", label)
		if !readYes(in) {
			fmt.Fprintf(out, "cancelled, %s left untouched\n", resolved)
			return nil
		}
	}

	n, err := agent.DeleteSession(ctx, log, resolved, false)
	if err != nil {
		switch {
		case errors.Is(err, agent.ErrSessionNotFound):
			return fmt.Errorf("no session with id %q", resolved)
		case errors.Is(err, agent.ErrNotTopLevel):
			return fmt.Errorf("%q is a sub-agent, not a session, delete its parent session instead", resolved)
		case errors.Is(err, agent.ErrSessionLive):
			return fmt.Errorf("session %q is live in another process, close that session first", resolved)
		default:
			return err
		}
	}
	fmt.Fprintf(out, "deleted %s (%d agent row%s)\n", resolved, n, pluralS(n))
	return nil
}

// resolveSessionID maps a user-supplied id to a concrete session id and
// its title. An exact match wins immediately. Otherwise it tries a
// unique short-id prefix match against the listed sessions: zero
// matches returns ErrSessionNotFound (so the caller's "no session"
// message fires), and an ambiguous prefix is rejected with a hint to
// be more specific. The title is best-effort for the confirm copy.
func resolveSessionID(ctx context.Context, log *agent.SQLiteEventLog, id string) (string, string, error) {
	sessions, err := agent.ListUserSessions(ctx, log, "")
	if err != nil {
		return "", "", err
	}
	// Exact match first - the documented minimum.
	for _, s := range sessions {
		if s.ID == id {
			return s.ID, s.Title, nil
		}
	}
	// Short-id prefix as a nice-to-have. Must be unambiguous.
	var matches []agent.Session
	for _, s := range sessions {
		if id != "" && strings.HasPrefix(s.ID, id) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		// Defer to DeleteSession's own ErrSessionNotFound mapping by
		// passing the original id through unchanged.
		return id, "", nil
	case 1:
		return matches[0].ID, matches[0].Title, nil
	default:
		return "", "", fmt.Errorf("id prefix %q is ambiguous, matches %d sessions, use a longer prefix", id, len(matches))
	}
}

// readYes reads one line from in and reports whether it is an explicit
// yes ("y" or "yes", case-insensitive). EOF, an empty line, or
// anything else reads as no, so the default is the safe path.
func readYes(in io.Reader) bool {
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
