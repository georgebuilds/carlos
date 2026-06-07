package memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAppendSummary_PersistsFrame(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, Summary{
		AgentID: "a1",
		Text:    "first work session",
		Frame:   "work",
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchInFrame(ctx, "work", "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].Frame != "work" {
		t.Errorf("Frame = %q, want work", hits[0].Frame)
	}
}

func TestSearchInFrame_FiltersToOneFrame(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, row := range []Summary{
		{AgentID: "a1", Text: "alpha personal note", Frame: "personal"},
		{AgentID: "a1", Text: "alpha work note", Frame: "work"},
		{AgentID: "a1", Text: "alpha research note", Frame: "research"},
	} {
		if _, err := s.AppendSummary(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.SearchInFrame(ctx, "alpha", "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Frame != "work" {
		t.Errorf("frame-scoped search wrong: %+v", hits)
	}
	// Empty frame returns every match.
	all, _ := s.SearchInFrame(ctx, "alpha", "", 10)
	if len(all) != 3 {
		t.Errorf("cross-frame search returned %d, want 3", len(all))
	}
}

func TestRecentInFrame_FiltersToOneFrame(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []Summary{
		{AgentID: "a1", Text: "p1", Frame: "personal", ClosedAt: now.Add(-1 * time.Hour)},
		{AgentID: "a1", Text: "w1", Frame: "work", ClosedAt: now.Add(-2 * time.Hour)},
		{AgentID: "a1", Text: "p2", Frame: "personal", ClosedAt: now.Add(-3 * time.Hour)},
	}
	for _, row := range rows {
		if _, err := s.AppendSummary(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.RecentInFrame(ctx, "personal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("got %d, want 2", len(hits))
	}
	for _, h := range hits {
		if h.Frame != "personal" {
			t.Errorf("hit with wrong frame: %+v", h)
		}
	}
}

func TestLegacySearch_ReturnsAllFrames(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _ = s.AppendSummary(ctx, Summary{AgentID: "a", Text: "alpha p", Frame: "personal"})
	_, _ = s.AppendSummary(ctx, Summary{AgentID: "a", Text: "alpha w", Frame: "work"})

	hits, err := s.Search(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("legacy two-arg Search should return all frames; got %d", len(hits))
	}
}

func TestMigration_AddsFrameColumnToLegacyDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")
	// Hand-roll a legacy summaries table (no frame column) so we exercise
	// the migration path explicitly.
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE summaries (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  agent_id TEXT NOT NULL,
		  closed_at INTEGER NOT NULL,
		  text TEXT NOT NULL,
		  tokens INTEGER NOT NULL DEFAULT 0,
		  source_seq INTEGER NOT NULL DEFAULT 0
		);
		INSERT INTO summaries(agent_id, closed_at, text, tokens, source_seq)
		  VALUES ('legacy', 0, 'pre-frames row', 0, 0);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Reopen via the production OpenStore so the migration runs.
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore on legacy db: %v", err)
	}
	defer s.Close()

	// The legacy row should still be there + return Frame="" (the
	// migration default).
	hits, err := s.RecentSummaries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].Frame != "" {
		t.Errorf("legacy row Frame = %q, want empty", hits[0].Frame)
	}
	// And a fresh insert with frame works.
	if _, err := s.AppendSummary(context.Background(), Summary{
		AgentID: "fresh", Text: "after migration", Frame: "work",
	}); err != nil {
		t.Fatalf("post-migration AppendSummary: %v", err)
	}
}

func TestMigration_SecondRunIsNoOp(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()
	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	// Insert + read to confirm the second open's migration didn't break
	// the column.
	if _, err := s2.AppendSummary(context.Background(), Summary{
		AgentID: "x", Text: "reopened", Frame: "personal",
	}); err != nil {
		t.Fatalf("AppendSummary after second migrate: %v", err)
	}
}
