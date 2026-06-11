package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunSearch is the entry point for the `carlos memory search`
// foreground verb. It opens the user's ~/.carlos/state.db, runs the
// FTS5 query, and prints results to stdout in a single-line-per-row
// format suitable for terminal review.
//
// Defaults:
//   - limit <= 0 → 10
//   - dbPath empty → ~/.carlos/state.db (CARLOS_STATE_DB env override)
//
// Output shape per row:
//
//	<RFC3339 closed_at>  [<agent_id short>]  <text first 200 chars>
//
// The cmd/carlos foreground wiring will call this after parsing
// `memory search <query>`; we keep the surface here so the foreground
// agent's change is a one-liner.
func RunSearch(query string, limit int) error {
	return RunSearchTo(os.Stdout, query, limit, "", AnyFrames())
}

// RunSearchInFrame is the frame-aware variant. The FrameFilter
// argument is required - construct via AnyFrames() / InFrame(name)
// / Unframed().
func RunSearchInFrame(query string, filter FrameFilter, limit int) error {
	return RunSearchTo(os.Stdout, query, limit, "", filter)
}

// RunSearchTo is the testable variant of RunSearch. It accepts an
// io.Writer (for capture in tests) and an explicit dbPath ("" falls
// back to the resolution rules in RunSearch). The filter argument
// scopes the FTS5 query - see FrameFilter for the rules.
func RunSearchTo(out io.Writer, query string, limit int, dbPath string, filter FrameFilter) error {
	if strings.TrimSpace(query) == "" {
		return errors.New("memory: search: empty query")
	}
	path, err := resolveDBPath(dbPath)
	if err != nil {
		return err
	}
	store, err := OpenStore(path)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hits, err := store.SearchInFrame(ctx, query, filter, limit)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		fmt.Fprintln(out, "no matches.")
		return nil
	}
	for _, h := range hits {
		fmt.Fprintln(out, formatSearchHit(h))
	}
	return nil
}

// formatSearchHit renders one summary as a single line:
//
//	<RFC3339>  [<agent-id first 8 chars>]  [<frame>]  <text first 200 chars>
//
// Newlines inside the summary are collapsed to spaces so the line
// stays scannable. Empty frame is suppressed (legacy / unframed rows
// still render compactly).
func formatSearchHit(h Summary) string {
	short := h.AgentID
	if len(short) > 8 {
		short = short[:8]
	}
	text := strings.ReplaceAll(h.Text, "\n", " ")
	if r := []rune(text); len(r) > 200 {
		text = string(r[:200]) + "…"
	}
	if h.Frame != "" {
		return fmt.Sprintf("%s  [%s]  [%s]  %s",
			h.ClosedAt.Format(time.RFC3339), short, h.Frame, text)
	}
	return fmt.Sprintf("%s  [%s]  %s",
		h.ClosedAt.Format(time.RFC3339), short, text)
}

// resolveDBPath picks the state.db path per RunSearch's documented
// precedence. Surfaces a useful error if neither HOME nor an explicit
// path are available.
func resolveDBPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("CARLOS_STATE_DB"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("memory: cannot locate state.db (no home dir): %w", err)
	}
	return filepath.Join(home, ".carlos", "state.db"), nil
}
