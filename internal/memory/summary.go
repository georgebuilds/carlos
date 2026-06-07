package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Summary is one closed-conversation summary, FTS5-indexed by Text.
//
// AgentID names the producing agent (root or sub); the summarizer
// hook (called from the agent loop on conversation close) is what
// stamps this. ClosedAt is the wall-clock close time in UTC ms;
// SourceSeq is the last events.seq covered by the summary (so a
// future incremental summarizer can pick up where the previous one
// stopped). Frame is the active frame at conversation close (Phase
// F-13); empty string is the legacy single-shelf value.
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
// — both are required for a useful summary, and silently inserting a
// blank row would pollute the FTS index.
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
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO summaries(agent_id, closed_at, text, tokens, source_seq, frame)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sum.AgentID, closedAt.UnixMilli(), sum.Text, sum.Tokens, sum.SourceSeq, sum.Frame,
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

// Search runs an FTS5 MATCH against the summaries index and returns
// rows ordered by closed_at DESC (newest first). If limit <= 0 we
// default to 10.
//
// The query is passed straight to FTS5 — callers may use the full
// FTS5 query grammar (quoted phrases, AND/OR/NOT, NEAR/N, prefix*).
// Bad-syntax queries surface the FTS5 error wrapped.
//
// Legacy callers using two-arg Search get every frame's hits. Use
// SearchInFrame to scope a search.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]Summary, error) {
	return s.SearchInFrame(ctx, query, "", limit)
}

// SearchInFrame runs an FTS5 MATCH and filters to the named frame.
// Empty frame returns every match (the legacy cross-frame behaviour).
// Phase F-13. The summaries_by_frame index makes the per-frame scan
// cheap even on large stores.
func (s *Store) SearchInFrame(ctx context.Context, query, frame string, limit int) ([]Summary, error) {
	if s == nil {
		return nil, errors.New("memory: nil store")
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("memory: Search: empty query")
	}
	if limit <= 0 {
		limit = 10
	}
	if frame == "" {
		rows, err := s.db.QueryContext(ctx, `
			SELECT id, agent_id, closed_at, text, tokens, source_seq, frame
			  FROM summaries
			 WHERE id IN (SELECT rowid FROM summaries_fts WHERE summaries_fts MATCH ?)
			 ORDER BY closed_at DESC
			 LIMIT ?`,
			query, limit,
		)
		if err != nil {
			return nil, fmt.Errorf("memory: search: %w", err)
		}
		defer rows.Close()
		return scanSummaries(rows)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, agent_id, closed_at, text, tokens, source_seq, frame
		  FROM summaries
		 WHERE frame = ?
		   AND id IN (SELECT rowid FROM summaries_fts WHERE summaries_fts MATCH ?)
		 ORDER BY closed_at DESC
		 LIMIT ?`,
		frame, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("memory: search: %w", err)
	}
	defer rows.Close()
	return scanSummaries(rows)
}

// RecentSummaries returns the top-N summaries by closed_at DESC. Used
// by the agent boot path to seed working memory ("here's what we
// last talked about") without invoking FTS5.
//
// Legacy two-arg callers get every frame's hits. Use RecentInFrame to
// scope by frame.
func (s *Store) RecentSummaries(ctx context.Context, limit int) ([]Summary, error) {
	return s.RecentInFrame(ctx, "", limit)
}

// RecentInFrame returns the top-N summaries scoped to one frame.
// Empty frame returns every frame's rows. Phase F-13.
func (s *Store) RecentInFrame(ctx context.Context, frame string, limit int) ([]Summary, error) {
	if s == nil {
		return nil, errors.New("memory: nil store")
	}
	if limit <= 0 {
		limit = 10
	}
	if frame == "" {
		rows, err := s.db.QueryContext(ctx, `
			SELECT id, agent_id, closed_at, text, tokens, source_seq, frame
			  FROM summaries
			 ORDER BY closed_at DESC
			 LIMIT ?`, limit,
		)
		if err != nil {
			return nil, fmt.Errorf("memory: recent summaries: %w", err)
		}
		defer rows.Close()
		return scanSummaries(rows)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, agent_id, closed_at, text, tokens, source_seq, frame
		  FROM summaries
		 WHERE frame = ?
		 ORDER BY closed_at DESC
		 LIMIT ?`, frame, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("memory: recent summaries: %w", err)
	}
	defer rows.Close()
	return scanSummaries(rows)
}

// scanSummaries is the shared row-scan loop for Search and
// RecentSummaries. Both queries select the same column list in the
// same order — keep them in lockstep.
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
		)
		if err := rows.Scan(&sum.ID, &sum.AgentID, &closeMs, &sum.Text, &sum.Tokens, &sum.SourceSeq, &sum.Frame); err != nil {
			return nil, fmt.Errorf("memory: scan summary: %w", err)
		}
		sum.ClosedAt = time.UnixMilli(closeMs).UTC()
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
