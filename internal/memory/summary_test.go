package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/memory"
)

// TestAppendSummary_AndSearch verifies the round-trip: insert two
// summaries, search for a token that matches one, get that one back.
// This is the canonical proof that FTS5 is wired correctly on
// modernc.org/sqlite (the AFTER INSERT trigger populates the index).
func TestAppendSummary_AndSearch(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	id1, err := s.AppendSummary(ctx, memory.Summary{
		AgentID:  "agent-1",
		ClosedAt: time.Now().UTC().Add(-2 * time.Hour),
		Text:     "Discussed the carlos memory subsystem and FTS5 schema.",
		Tokens:   42,
	})
	if err != nil {
		t.Fatalf("AppendSummary 1: %v", err)
	}
	if id1 == 0 {
		t.Errorf("first id should be non-zero, got %d", id1)
	}
	id2, err := s.AppendSummary(ctx, memory.Summary{
		AgentID:  "agent-2",
		ClosedAt: time.Now().UTC().Add(-1 * time.Hour),
		Text:     "Reviewed a pull request about lipgloss styling.",
		Tokens:   30,
	})
	if err != nil {
		t.Fatalf("AppendSummary 2: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("second id should be > first; got %d <= %d", id2, id1)
	}

	hits, err := s.Search(ctx, "FTS5", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search FTS5: want 1 hit, got %d (%+v)", len(hits), hits)
	}
	if hits[0].ID != id1 {
		t.Errorf("Search FTS5: want id %d, got %d", id1, hits[0].ID)
	}
	if hits[0].AgentID != "agent-1" || hits[0].Tokens != 42 {
		t.Errorf("Search FTS5: row hydration wrong: %+v", hits[0])
	}
}

// TestSearch_NoMatchesReturnsEmpty verifies that a query against an
// empty FTS5 index returns nil/empty without erroring.
func TestSearch_NoMatchesReturnsEmpty(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	hits, err := s.Search(ctx, "nothingmatchesthis", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits, got %d", len(hits))
	}
}

// TestSearch_EmptyQueryRejected verifies the input-validation guard
// (an empty MATCH would crash FTS5).
func TestSearch_EmptyQueryRejected(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Search(context.Background(), "   ", 10); err == nil {
		t.Error("expected error on empty query")
	}
}

// TestSearch_DefaultLimit verifies that limit <= 0 → 10 (the
// documented default) — i.e. an 11-row index returns at most 10
// without an explicit limit.
func TestSearch_DefaultLimit(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if _, err := s.AppendSummary(ctx, memory.Summary{
			AgentID:  "a",
			ClosedAt: time.Now().UTC().Add(time.Duration(-i) * time.Minute),
			Text:     "keyword present in every row",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	hits, err := s.Search(ctx, "keyword", 0) // 0 → default
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 10 {
		t.Errorf("default limit: want 10, got %d", len(hits))
	}
}

// TestRecentSummaries_OrderedByClosedAtDesc verifies the documented
// ordering: newest first.
func TestRecentSummaries_OrderedByClosedAtDesc(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	// Insert out of order so we don't accidentally rely on PK ordering.
	for _, ago := range []time.Duration{3 * time.Hour, 1 * time.Hour, 2 * time.Hour} {
		if _, err := s.AppendSummary(ctx, memory.Summary{
			AgentID:  "a",
			ClosedAt: now.Add(-ago),
			Text:     "row " + ago.String(),
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	hits, err := s.RecentSummaries(ctx, 10)
	if err != nil {
		t.Fatalf("RecentSummaries: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("want 3 rows, got %d", len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].ClosedAt.Before(hits[i].ClosedAt) {
			t.Errorf("rows out of order at i=%d: %v before %v", i, hits[i-1].ClosedAt, hits[i].ClosedAt)
		}
	}
}

// TestRecentSummaries_Limit verifies the LIMIT clause works.
func TestRecentSummaries_Limit(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := s.AppendSummary(ctx, memory.Summary{
			AgentID: "a", Text: "row",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	hits, err := s.RecentSummaries(ctx, 2)
	if err != nil {
		t.Fatalf("RecentSummaries: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("want 2 rows, got %d", len(hits))
	}
}

// TestAppendSummary_RejectsEmptyAgentID guards against polluting the
// FTS index with rows that have no producer attribution.
func TestAppendSummary_RejectsEmptyAgentID(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.AppendSummary(context.Background(), memory.Summary{
		AgentID: "", Text: "x",
	})
	if err == nil {
		t.Error("expected error on empty agent_id")
	}
}

// TestAppendSummary_RejectsEmptyText guards against blank rows in
// the FTS index (which would match every query).
func TestAppendSummary_RejectsEmptyText(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.AppendSummary(context.Background(), memory.Summary{
		AgentID: "a", Text: "   ",
	})
	if err == nil {
		t.Error("expected error on empty text")
	}
}

// TestSearch_SubstringTokenMatch verifies a more interesting FTS5
// query: a quoted phrase + a word that's only in one row. Proves we
// can use the FTS5 grammar through the API.
func TestSearch_SubstringTokenMatch(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, memory.Summary{
		AgentID: "a", Text: "we shipped the gpu backend on a tuesday",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendSummary(ctx, memory.Summary{
		AgentID: "a", Text: "we talked about lipgloss colors",
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(ctx, "gpu", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("gpu MATCH: want 1, got %d", len(hits))
	}
}

// TestSearch_BadFTS5QueryReturnsErrBadQuery verifies that malformed
// FTS5 query strings (unmatched quote, bare operator, etc.) are wrapped
// with the ErrBadQuery sentinel so CLI consumers can render a
// user-fixable hint rather than a generic "database error".
//
// The driver (modernc.org/sqlite) emits two error shapes for these:
//
//	SQL logic error: fts5: syntax error near "..." (1)
//	SQL logic error: unterminated string (1)
//
// Both must satisfy errors.Is(err, ErrBadQuery).
func TestSearch_BadFTS5QueryReturnsErrBadQuery(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, memory.Summary{
		AgentID: "a", Text: "seed row so the table is non-empty",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name  string
		query string
	}{
		{"unmatched_quote", `"unclosed`},
		{"bare_operator_AND", `AND`},
		{"lone_quote", `"`},
		{"trailing_OR", `a OR`},
		{"unbalanced_paren", `((`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Search(ctx, tc.query, 10)
			if err == nil {
				t.Fatalf("query %q: expected error, got nil", tc.query)
			}
			if !errors.Is(err, memory.ErrBadQuery) {
				t.Errorf("query %q: errors.Is(err, ErrBadQuery)=false; err=%v", tc.query, err)
			}
		})
	}
}

// TestSearch_BadFTS5QueryInFrameReturnsErrBadQuery covers the
// frame-scoped Search path (SearchInFrame with a non-empty frame),
// which executes a different SQL statement than the cross-frame path.
func TestSearch_BadFTS5QueryInFrameReturnsErrBadQuery(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, memory.Summary{
		AgentID: "a", Frame: "personal", Text: "frame-scoped seed row",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := s.SearchInFrame(ctx, `"unclosed`, "personal", 10)
	if err == nil {
		t.Fatal("expected error on bad FTS5 query")
	}
	if !errors.Is(err, memory.ErrBadQuery) {
		t.Errorf("errors.Is(err, ErrBadQuery)=false; err=%v", err)
	}
}

// TestSearch_HappyPathStillWorks is a trivial smoke that the new
// error-classification wrap does not regress well-formed queries.
func TestSearch_HappyPathStillWorks(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, memory.Summary{
		AgentID: "a", Text: "the carlos memory subsystem speaks FTS5",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hits, err := s.Search(ctx, "carlos", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
	if errors.Is(err, memory.ErrBadQuery) {
		t.Errorf("happy path should not be classified as ErrBadQuery")
	}
}
