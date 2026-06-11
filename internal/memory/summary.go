package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrBadQuery is returned (wrapped) when Search's FTS5 MATCH fails
// because the user's query string violates the FTS5 grammar (unmatched
// quote, bare operator, etc.). Callers may errors.Is against this to
// render a user-fixable hint instead of a generic "database error".
var ErrBadQuery = errors.New("memory: bad FTS5 query syntax")

// ErrInvalidFrameFilter is returned by the read APIs when a caller
// passes a zero-value FrameFilter. The zero value is intentionally
// invalid so an uninitialized filter (e.g. a struct field never set)
// fails loudly rather than silently behaving like "any frame".
var ErrInvalidFrameFilter = errors.New("memory: invalid FrameFilter - construct via AnyFrames/InFrame/Unframed")

// isFTS5SyntaxError sniffs the error string returned by
// modernc.org/sqlite for the two shapes FTS5 actually emits:
//
//	SQL logic error: fts5: syntax error near "..." (1)
//	SQL logic error: unterminated string (1)
//
// Either shape means the caller's query is malformed, not that the DB
// is sick. We pair-sniff on substrings rather than the full prefix so
// a future SQLite patch that adjusts the wording stays detectable.
func isFTS5SyntaxError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "fts5:") && strings.Contains(msg, "syntax error") {
		return true
	}
	if strings.Contains(msg, "unterminated string") {
		return true
	}
	return false
}

// Summary is one closed-conversation summary, FTS5-indexed by Text.
//
// AgentID names the producing agent (root or sub); the summarizer
// hook (called from the agent loop on conversation close) is what
// stamps this. ClosedAt is the wall-clock close time in UTC ms;
// SourceSeq is the last events.seq covered by the summary (so a
// future incremental summarizer can pick up where the previous one
// stopped).
//
// Frame is the active frame at conversation close. The struct uses a
// plain string for ergonomic display; the storage layer maps "" to
// SQL NULL (unframed) and any non-empty string to a CHECK-constrained
// non-empty column. Callers therefore see a consistent "" both for
// rows that predate frames and for rows written outside any active
// frame.
type Summary struct {
	ID        int64
	AgentID   string
	ClosedAt  time.Time
	Text      string
	Tokens    int
	SourceSeq int64
	Frame     string
}

// AppendSummary inserts one summary row. The AFTER INSERT trigger on
// `summaries` mirrors the text into `summaries_fts` automatically;
// callers do NOT need to touch the FTS table.
//
// Returns the new row id. Empty AgentID or empty Text return errors
// - both are required for a useful summary, and silently inserting a
// blank row would pollute the FTS index.
//
// Frame storage rule: an empty sum.Frame is stored as SQL NULL
// (unframed); a non-empty sum.Frame is stored verbatim. The empty
// string never lands in the column - the table's CHECK constraint
// would reject it - so on-disk values are unambiguous.
func (s *Store) AppendSummary(ctx context.Context, sum Summary) (int64, error) {
	if s == nil {
		return 0, errors.New("memory: nil store")
	}
	if sum.AgentID == "" {
		return 0, errors.New("memory: AppendSummary: empty agent_id")
	}
	if strings.TrimSpace(sum.Text) == "" {
		return 0, errors.New("memory: AppendSummary: empty text")
	}
	closedAt := sum.ClosedAt
	if closedAt.IsZero() {
		closedAt = time.Now().UTC()
	}
	closedAt = closedAt.UTC().Truncate(time.Millisecond)
	frame := sql.NullString{String: sum.Frame, Valid: sum.Frame != ""}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO summaries(agent_id, closed_at, text, tokens, source_seq, frame)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sum.AgentID, closedAt.UnixMilli(), sum.Text, sum.Tokens, sum.SourceSeq, frame,
	)
	if err != nil {
		return 0, fmt.Errorf("memory: insert summary: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("memory: last insert id: %w", err)
	}
	return id, nil
}

// FrameFilter selects which summary rows the read API returns. It is
// a closed sum type: construct via AnyFrames() / InFrame(name) /
// Unframed(). The zero value is intentionally an invalid filter so an
// uninitialized FrameFilter passed by mistake fails fast with
// ErrInvalidFrameFilter instead of silently behaving like "any
// frame".
//
// Filter semantics:
//
//   - AnyFrames(): no filter; every row (named + unframed) matches.
//     Used by the `carlos memory search` CLI when invoked without
//     -f / --unframed so a script gets the full corpus.
//   - InFrame(name): the named frame's rows PLUS legacy unframed
//     rows (frame IS NULL) fall through. Mirrors the audit-required
//     contract that summaries written before frames remain reachable
//     under any active frame.
//   - Unframed(): only frame IS NULL rows. Lets a user (or operator)
//     surface the legacy / no-frame corpus without dragging in a
//     specific named frame's rows.
type FrameFilter struct {
	kind frameKind
	name string
}

// frameKind is the FrameFilter discriminator. Kept unexported so
// callers cannot construct an invalid filter literal.
type frameKind uint8

const (
	frameInvalid frameKind = iota
	frameAny
	frameNamed
	frameUnframed
)

// AnyFrames returns a FrameFilter that matches every summary row,
// regardless of frame. This is the default for the `carlos memory
// search` CLI when no -f / --unframed flag is supplied.
func AnyFrames() FrameFilter { return FrameFilter{kind: frameAny} }

// InFrame returns a FrameFilter that matches rows stamped under the
// named frame plus legacy unframed rows (frame IS NULL). Panics if
// name is empty because the empty string is not a valid frame name;
// callers wanting unframed-only rows must use Unframed().
func InFrame(name string) FrameFilter {
	if name == "" {
		panic("memory: InFrame requires non-empty name; use AnyFrames() or Unframed()")
	}
	return FrameFilter{kind: frameNamed, name: name}
}

// Unframed returns a FrameFilter that matches only rows with NULL
// frame (legacy + no-active-frame). Lets operators inspect the
// untagged corpus without pulling in any named frame's rows.
func Unframed() FrameFilter { return FrameFilter{kind: frameUnframed} }

// predicate returns the SQL fragment and bind args to AND into a
// WHERE clause. An empty fragment means "no filter"; callers compose
// the surrounding query. Returns (frag="", args=nil, ok=true) for
// AnyFrames; an unconstrained args slice for Unframed; a single arg
// for InFrame. Returns ok=false for the zero value so callers can
// raise ErrInvalidFrameFilter.
func (f FrameFilter) predicate() (frag string, args []any, ok bool) {
	switch f.kind {
	case frameAny:
		return "", nil, true
	case frameNamed:
		return "(frame = ? OR frame IS NULL)", []any{f.name}, true
	case frameUnframed:
		return "frame IS NULL", nil, true
	}
	return "", nil, false
}

// composeSearchSQL returns the SQL for the FTS5-matching summary
// query under the given filter fragment. The filter fragment may be
// empty (AnyFrames) - in that case only the FTS5 subquery + ORDER +
// LIMIT clauses appear in the WHERE.
func composeSearchSQL(filterFrag string) string {
	var where strings.Builder
	where.WriteString("WHERE id IN (SELECT rowid FROM summaries_fts WHERE summaries_fts MATCH ?)")
	if filterFrag != "" {
		where.WriteString(" AND ")
		where.WriteString(filterFrag)
	}
	return `SELECT id, agent_id, closed_at, text, tokens, source_seq, frame
		  FROM summaries
		 ` + where.String() + `
		 ORDER BY closed_at DESC
		 LIMIT ?`
}

// composeRecentSQL returns the SQL for the order-by-closed-at recall
// query under the given filter fragment. Empty fragment yields an
// unfiltered SELECT.
func composeRecentSQL(filterFrag string) string {
	var where string
	if filterFrag != "" {
		where = "WHERE " + filterFrag
	}
	return `SELECT id, agent_id, closed_at, text, tokens, source_seq, frame
		  FROM summaries
		 ` + where + `
		 ORDER BY closed_at DESC
		 LIMIT ?`
}

// SearchInFrame runs an FTS5 MATCH against the summaries index and
// returns rows ordered by closed_at DESC (newest first). If limit
// <= 0 we default to 10.
//
// Frame semantics are encoded by the FrameFilter argument (see
// AnyFrames / InFrame / Unframed for the rules). A zero-value
// FrameFilter returns ErrInvalidFrameFilter rather than silently
// matching anything.
//
// The query is passed straight to FTS5 - callers may use the full
// FTS5 query grammar (quoted phrases, AND/OR/NOT, NEAR/N, prefix*).
// Bad-syntax queries surface the FTS5 error wrapped in ErrBadQuery.
//
// The summaries_by_frame index makes the per-frame scan cheap even
// on large stores.
func (s *Store) SearchInFrame(ctx context.Context, query string, filter FrameFilter, limit int) ([]Summary, error) {
	if s == nil {
		return nil, errors.New("memory: nil store")
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("memory: Search: empty query")
	}
	if limit <= 0 {
		limit = 10
	}
	frag, args, ok := filter.predicate()
	if !ok {
		return nil, ErrInvalidFrameFilter
	}
	// Bind order matches composeSearchSQL: FTS5 MATCH first, then any
	// filter args (currently 0 or 1), then LIMIT.
	bind := make([]any, 0, 2+len(args))
	bind = append(bind, query)
	bind = append(bind, args...)
	bind = append(bind, limit)
	rows, err := s.db.QueryContext(ctx, composeSearchSQL(frag), bind...)
	if err != nil {
		if isFTS5SyntaxError(err) {
			return nil, fmt.Errorf("%w: %v", ErrBadQuery, err)
		}
		return nil, fmt.Errorf("memory: search: %w", err)
	}
	defer rows.Close()
	return scanSummaries(rows)
}

// RecentInFrame returns the top-N summaries by closed_at DESC. Used
// by the agent boot / recall path to seed working memory ("here's
// what we last talked about") without invoking FTS5.
//
// Frame semantics mirror SearchInFrame exactly - the same FrameFilter
// kinds and the same legacy-fallthrough rule. A zero-value filter
// returns ErrInvalidFrameFilter.
func (s *Store) RecentInFrame(ctx context.Context, filter FrameFilter, limit int) ([]Summary, error) {
	if s == nil {
		return nil, errors.New("memory: nil store")
	}
	if limit <= 0 {
		limit = 10
	}
	frag, args, ok := filter.predicate()
	if !ok {
		return nil, ErrInvalidFrameFilter
	}
	bind := make([]any, 0, 1+len(args))
	bind = append(bind, args...)
	bind = append(bind, limit)
	rows, err := s.db.QueryContext(ctx, composeRecentSQL(frag), bind...)
	if err != nil {
		return nil, fmt.Errorf("memory: recent summaries: %w", err)
	}
	defer rows.Close()
	return scanSummaries(rows)
}

// scanSummaries is the shared row-scan loop for Search and
// RecentSummaries. Both queries select the same column list in the
// same order - keep them in lockstep. NULL frame columns hydrate to
// Summary.Frame = "".
func scanSummaries(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Summary, error) {
	var out []Summary
	for rows.Next() {
		var (
			sum     Summary
			closeMs int64
			frame   sql.NullString
		)
		if err := rows.Scan(&sum.ID, &sum.AgentID, &closeMs, &sum.Text, &sum.Tokens, &sum.SourceSeq, &frame); err != nil {
			return nil, fmt.Errorf("memory: scan summary: %w", err)
		}
		sum.ClosedAt = time.UnixMilli(closeMs).UTC()
		if frame.Valid {
			sum.Frame = frame.String
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
