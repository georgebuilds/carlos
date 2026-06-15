package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestCreateCwd reports the launch dir once drive is enabled.
func TestCreateCwd(t *testing.T) {
	b := newCCBackendAt(t.TempDir())
	if b.CreateCwd() != "" {
		t.Errorf("CreateCwd before drive = %q, want empty", b.CreateCwd())
	}
	b.EnableDrive(context.Background(), newEphemeralHub(), "http://127.0.0.1:0", "tok", "/bin/carlos")
	if b.CreateCwd() == "" {
		t.Error("CreateCwd after EnableDrive should be the launch dir (os.Getwd)")
	}
}

// TestImport_NoCCBackend: the import endpoints degrade gracefully when no
// Claude Code backend is registered.
func TestImport_NoCCBackend(t *testing.T) {
	s, _, _ := newTestServer(t, "") // carlos read-only only

	rec := do(t, s, "GET", "/api/cc/importable", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("importable without cc = %d, want 200 (empty)", rec.Code)
	}
	var body struct {
		Sessions []ThreadSummary `json:"sessions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Sessions) != 0 {
		t.Errorf("sessions without cc = %d, want 0", len(body.Sessions))
	}

	rec2 := do(t, s, "POST", "/api/cc/import", map[string]any{"id": "cc:x"})
	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("import without cc = %d, want 503", rec2.Code)
	}
}

// TestImportCandidates_MissingRoot returns an empty list (no error) when the
// project store directory does not exist.
func TestImportCandidates_MissingRoot(t *testing.T) {
	b := newCCBackendAt("") // no root at all
	got, err := b.ImportCandidates(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty-root candidates = %v/%v, want []/nil", got, err)
	}

	missing := newCCBackendAt(t.TempDir() + "/does-not-exist")
	got, err = missing.ImportCandidates(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("missing-root candidates = %v/%v, want []/nil", got, err)
	}
}

// TestSessionCwd_Misses: an unresolvable id returns ok=false.
func TestSessionCwd_Misses(t *testing.T) {
	b := freshCC(t, t.TempDir())
	if _, ok := b.sessionCwd("cc:nope"); ok {
		t.Error("sessionCwd on a missing session should report ok=false")
	}
}

// TestRoster_NoGroupStoreFallsBackToRegistry: with no group store, roster
// uses the unscoped registry fan-out (read-only deployments).
func TestRoster_NoGroupStoreFallsBackToRegistry(t *testing.T) {
	log, _ := newTestLog(t)
	seedThread(t, log, "t1", "one", "hi")
	s := NewServer(Options{Log: log, Token: testToken}) // no Groups

	got, err := s.roster(context.Background())
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	found := false
	for _, ts := range got {
		if ts.ID == "t1" {
			found = true
		}
	}
	if !found {
		t.Errorf("roster without group store missing carlos thread: %+v", got)
	}
}

// TestImportableCC_ReportsRepoOverlay: a candidate whose cwd is a real git
// dir carries its repo in the importable list.
func TestImportableCC_ReportsRepoOverlay(t *testing.T) {
	repoDir := t.TempDir()
	mkGitDir(t, repoDir)
	root, id := writeCCSessionCwd(t, "cand-repo", repoDir)
	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))
	// Pre-store the repo as an import would, so the overlay can stamp it.
	if err := gs.SetThreadRepo(context.Background(), id, repoDir, "x"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s, "GET", "/api/cc/importable", nil)
	var body struct {
		Sessions []ThreadSummary `json:"sessions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	for _, ts := range body.Sessions {
		if ts.ID == id {
			if ts.Repo == nil {
				t.Error("import candidate with stored repo should carry the repo overlay")
			}
			return
		}
	}
	t.Fatalf("candidate %q not in importable list: %+v", id, body.Sessions)
}

// TestGroups_LiveGatedCountThroughHandler: GET /api/groups counts only
// members in the live roster (plan WB-3). A cc id assigned to a group counts
// only while it is web-owned (in the roster).
func TestGroups_LiveGatedCountThroughHandler(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken})
	seedThread(t, log, "carlosThread", "c", "hi")

	g, _ := gs.Create(context.Background(), "mix")
	mustSet(t, gs, "carlosThread", &g.ID)
	// A cc id grouped but NOT web-owned: it is not in the roster, so it must
	// not inflate the count (live-gating, no agents join).
	mustSet(t, gs, "cc:not-owned", &g.ID)

	rec := do(t, s, "GET", "/api/groups", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("groups: %d", rec.Code)
	}
	var groups []Group
	_ = json.Unmarshal(rec.Body.Bytes(), &groups)
	if len(groups) != 1 || groups[0].Threads != 1 {
		t.Fatalf("group counts = %+v, want one group with 1 live member", groups)
	}
}

// TestRoster_OriginIdWithNoFileSkipped: a web-origin id whose session file
// has since vanished is silently dropped from the roster (ccOriginRoster).
func TestRoster_OriginIdWithNoFileSkipped(t *testing.T) {
	root, present := writeCCSession(t, "present-uuid", ccFixture)
	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))
	ctx := context.Background()
	if err := gs.MarkCCOrigin(ctx, present); err != nil {
		t.Fatal(err)
	}
	// An origin id with no backing file on disk.
	if err := gs.MarkCCOrigin(ctx, "cc:vanished-uuid"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s, "GET", "/api/threads", nil)
	var got []ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	for _, ts := range got {
		if ts.ID == "cc:vanished-uuid" {
			t.Error("vanished origin id should be skipped from the roster")
		}
	}
	found := false
	for _, ts := range got {
		if ts.ID == present {
			found = true
		}
	}
	if !found {
		t.Error("present origin session missing from roster")
	}
}

// TestRosterIDs_ErrorFallsBackToEmpty: when the roster cannot be built (the
// event log is closed), rosterIDs returns an empty set rather than panicking,
// so group counts gracefully read zero.
func TestRosterIDs_ErrorFallsBackToEmpty(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken})
	_ = log.Close() // carlos ListThreads now errors; CC origin set is empty

	ids := s.rosterIDs(context.Background())
	if len(ids) != 0 {
		t.Errorf("rosterIDs after log close = %v, want empty", ids)
	}
}

// TestImport_NonGitCwdNoRepo: importing a session whose cwd is not in a git
// tree still succeeds (marks origin), just with no repo overlay.
func TestImport_NonGitCwdNoRepo(t *testing.T) {
	nonGit := t.TempDir() // a real dir, but no .git anywhere up
	root, id := writeCCSessionCwd(t, "nogit-uuid", nonGit)
	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))

	rec := do(t, s, "POST", "/api/cc/import", map[string]any{"id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("import non-git session: %d (%s)", rec.Code, rec.Body.String())
	}
	var ts ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &ts)
	if ts.Repo != nil {
		t.Errorf("non-git import repo = %+v, want nil", ts.Repo)
	}
	origin, _ := gs.CCOriginSet(context.Background())
	if !origin[id] {
		t.Error("non-git import should still mark origin")
	}
}

// TestImportCandidates_SkipsNonJSONLAndSubdirs: a project dir with a stray
// non-jsonl file and a subdirectory yields only the real session.
func TestImportCandidates_SkipsNonJSONLAndSubdirs(t *testing.T) {
	root, _ := writeCCSession(t, "real-uuid", ccFixture)
	proj := root + "/-Users-george-Code-anneal"
	if err := os.WriteFile(proj+"/notes.txt", []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(proj+"/subdir", 0o755); err != nil {
		t.Fatal(err)
	}
	b := freshCC(t, root)
	got, err := b.ImportCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("candidates = %d, want 1 (non-jsonl + subdir skipped)", len(got))
	}
}

// TestImportable_StaleSessionExcluded: a session older than the import
// window is not an import candidate.
func TestImportable_StaleSessionExcluded(t *testing.T) {
	root, _ := writeCCSession(t, "stale-uuid", ccFixture)
	b := newCCBackendAt(root)
	// Clock far in the future so the file reads as older than the window.
	b.now = func() time.Time { return time.Now().Add(60 * 24 * time.Hour) }
	got, err := b.ImportCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("stale session should be excluded, got %d candidates", len(got))
	}
}
