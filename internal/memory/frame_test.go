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

func TestSearchInFrame_AnyFrameReturnsAll(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, _ = s.AppendSummary(ctx, Summary{AgentID: "a", Text: "alpha p", Frame: "personal"})
	_, _ = s.AppendSummary(ctx, Summary{AgentID: "a", Text: "alpha w", Frame: "work"})

	hits, err := s.SearchInFrame(ctx, "alpha", AnyFrame, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("AnyFrame sentinel should return all frames; got %d", len(hits))
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
	hits, err := s.RecentInFrame(context.Background(), AnyFrame, 10)
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

// TestSearchInFrame_LegacyFallthrough proves the audit-required
// behaviour: rows stamped with the empty-string legacy frame value
// surface for every active-frame query. Users with summaries written
// pre-frames must not lose recall on those rows when they later adopt
// frames.
func TestSearchInFrame_LegacyFallthrough(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, row := range []Summary{
		{AgentID: "a", Text: "alpha legacy row", Frame: ""},
		{AgentID: "a", Text: "alpha work row", Frame: "work"},
		{AgentID: "a", Text: "alpha personal row", Frame: "personal"},
	} {
		if _, err := s.AppendSummary(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, active := range []string{"work", "personal", "research", "anything"} {
		hits, err := s.SearchInFrame(ctx, "alpha", active, 10)
		if err != nil {
			t.Fatalf("active=%q: %v", active, err)
		}
		var sawLegacy bool
		for _, h := range hits {
			if h.Frame == "" {
				sawLegacy = true
			}
		}
		if !sawLegacy {
			t.Errorf("active=%q: legacy frame='' row was hidden; hits=%+v", active, hits)
		}
	}
}

// TestRecentInFrame_LegacyFallthrough mirrors the FTS5 path: legacy
// rows surface for any active-frame query on the recency path too.
// Both code paths must agree.
func TestRecentInFrame_LegacyFallthrough(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, row := range []Summary{
		{AgentID: "a", Text: "legacy row", Frame: ""},
		{AgentID: "a", Text: "work row", Frame: "work"},
		{AgentID: "a", Text: "personal row", Frame: "personal"},
	} {
		if _, err := s.AppendSummary(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, active := range []string{"work", "personal", "research"} {
		hits, err := s.RecentInFrame(ctx, active, 10)
		if err != nil {
			t.Fatalf("active=%q: %v", active, err)
		}
		var sawLegacy bool
		for _, h := range hits {
			if h.Frame == "" {
				sawLegacy = true
			}
		}
		if !sawLegacy {
			t.Errorf("active=%q: legacy frame='' row was hidden; hits=%+v", active, hits)
		}
	}
}

// TestSearchInFrame_BlocksOtherFrame is the integrity proof: a summary
// stamped under `work` MUST NOT surface when the active frame is
// `personal`, and vice versa. This is the cross-frame leak the audit
// caught.
func TestSearchInFrame_BlocksOtherFrame(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, Summary{
		AgentID: "a", Text: "secret work plan", Frame: "work",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendSummary(ctx, Summary{
		AgentID: "a", Text: "personal todo list", Frame: "personal",
	}); err != nil {
		t.Fatal(err)
	}

	// Active=personal must not see work.
	hits, err := s.SearchInFrame(ctx, "secret", "personal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("personal active should not see work-framed match; got %+v", hits)
	}

	// Active=work must not see personal.
	hits, err = s.SearchInFrame(ctx, "personal", "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Frame == "personal" {
			t.Errorf("work active leaked personal-framed row: %+v", h)
		}
	}

	// Active=work DOES see work (the positive case).
	hits, err = s.SearchInFrame(ctx, "secret", "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Frame != "work" {
		t.Errorf("work active should see its own row; got %+v", hits)
	}
}

// TestRecentInFrame_BlocksOtherFrame proves the recency path applies
// the same isolation as the FTS5 path. Belt + braces: every recall
// avenue must filter.
func TestRecentInFrame_BlocksOtherFrame(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.AppendSummary(ctx, Summary{
		AgentID: "a", Text: "w1", Frame: "work",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendSummary(ctx, Summary{
		AgentID: "a", Text: "p1", Frame: "personal",
	}); err != nil {
		t.Fatal(err)
	}

	hits, err := s.RecentInFrame(ctx, "personal", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Frame == "work" {
			t.Errorf("personal active leaked work row: %+v", h)
		}
	}

	hits, err = s.RecentInFrame(ctx, "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Frame == "personal" {
			t.Errorf("work active leaked personal row: %+v", h)
		}
	}
}

// TestAnyFrame_SentinelReturnsEverything pins the explicit sentinel
// contract: passing memory.AnyFrame (the empty string) means "no
// filter, give me every frame's rows." Used by the CLI when no -f
// flag is supplied.
func TestAnyFrame_SentinelReturnsEverything(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, row := range []Summary{
		{AgentID: "a", Text: "alpha w", Frame: "work"},
		{AgentID: "a", Text: "alpha p", Frame: "personal"},
		{AgentID: "a", Text: "alpha r", Frame: "research"},
		{AgentID: "a", Text: "alpha legacy", Frame: ""},
	} {
		if _, err := s.AppendSummary(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	// FTS5 path.
	hits, err := s.SearchInFrame(ctx, "alpha", AnyFrame, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 {
		t.Errorf("AnyFrame Search: want 4 hits, got %d", len(hits))
	}
	// Recency path.
	hits, err = s.RecentInFrame(ctx, AnyFrame, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 {
		t.Errorf("AnyFrame Recent: want 4 hits, got %d", len(hits))
	}
}

// TestMidConversationSwitch_RecallFollowsActiveFrame simulates the
// Ctrl+F mid-conversation switch the user can perform. Frames are
// session-scoped, not conversation-scoped: a summary written under
// `work` must vanish from recall when the user switches active to
// `personal`, and reappear when they switch back. Without this the
// model would blend cross-frame context after a switch.
func TestMidConversationSwitch_RecallFollowsActiveFrame(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Turn 1: user is in `work`, conversation closes, a summary lands.
	if _, err := s.AppendSummary(ctx, Summary{
		AgentID:  "root",
		ClosedAt: time.Now().UTC().Add(-1 * time.Hour),
		Text:     "discussed sprint planning and Q3 OKRs",
		Frame:    "work",
	}); err != nil {
		t.Fatal(err)
	}

	// Ctrl+F: switch to personal. The same transcript continues, but
	// recall must not surface the work summary now.
	hits, err := s.RecentInFrame(ctx, "personal", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Frame == "work" {
			t.Errorf("after switch to personal, work summary still surfaces: %+v", h)
		}
	}
	fts, err := s.SearchInFrame(ctx, "sprint", "personal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fts) != 0 {
		t.Errorf("after switch, FTS5 recall under personal leaked work hit: %+v", fts)
	}

	// Ctrl+F back to work: the summary must reappear (the conversation
	// is unchanged; only the lens shifted).
	hits, err = s.RecentInFrame(ctx, "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawWork bool
	for _, h := range hits {
		if h.Frame == "work" {
			sawWork = true
		}
	}
	if !sawWork {
		t.Errorf("after switch back to work, work summary did not reappear; hits=%+v", hits)
	}
	fts, err = s.SearchInFrame(ctx, "sprint", "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fts) != 1 {
		t.Errorf("after switch back to work, FTS5 lost the row; hits=%+v", fts)
	}
}
