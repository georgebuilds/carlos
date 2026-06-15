package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCCSessionCwd drops a CC session whose first record records the given
// cwd, under a fresh project store root. Returns (root, wire id).
func writeCCSessionCwd(t *testing.T, name, cwd string) (root, id string) {
	t.Helper()
	root = t.TempDir()
	proj := filepath.Join(root, "-enc-"+name)
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"type":"user","timestamp":"2026-06-13T10:00:00.000Z","cwd":%q,"message":{"role":"user","content":"hi there"}}`+"\n", cwd)
	if err := os.WriteFile(filepath.Join(proj, name+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, "cc:" + name
}

// freshCC returns a CC backend rooted at root with the clock nudged forward
// so freshly written files read as recent (inside the import window).
func freshCC(t *testing.T, root string) *CCBackend {
	t.Helper()
	b := newCCBackendAt(root)
	b.now = func() time.Time { return time.Now().Add(time.Minute) }
	return b
}

// TestRoster_CCScopedToOrigin: a user with an on-disk-but-not-origin CC
// session sees ZERO CC threads in GET /threads (plan WA-1).
func TestRoster_CCScopedToOrigin(t *testing.T) {
	root, id := writeCCSession(t, "onDisk-uuid", ccFixture)
	s, _, _ := newTestServer(t, "")
	s.Register(freshCC(t, root))

	rec := do(t, s, "GET", "/api/threads", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rec.Code, rec.Body.String())
	}
	var got []ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	for _, ts := range got {
		if ts.ID == id || ts.Backend == ccBackendName {
			t.Fatalf("on-disk-but-not-origin CC session leaked into roster: %+v", ts)
		}
	}
}

// TestRoster_WebOwnedCCAppears: once a CC session is marked origin (as an
// import/create would), it shows in the roster, resolved via GetThread.
func TestRoster_WebOwnedCCAppears(t *testing.T) {
	root, id := writeCCSession(t, "owned-uuid", ccFixture)
	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))
	if err := gs.MarkCCOrigin(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s, "GET", "/api/threads", nil)
	var got []ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	found := false
	for _, ts := range got {
		if ts.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("web-owned CC session missing from roster: %+v", got)
	}
}

// TestRoster_DirectOpenStillWorks: scoping the LIST does not block opening a
// non-origin session directly by id (GET /threads/{id}).
func TestRoster_DirectOpenStillWorks(t *testing.T) {
	root, id := writeCCSession(t, "direct-uuid", ccFixture)
	s, _, _ := newTestServer(t, "")
	s.Register(freshCC(t, root))

	rec := do(t, s, "GET", "/api/threads/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("direct open of non-origin session: %d (%s)", rec.Code, rec.Body.String())
	}
	var ts ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &ts)
	if ts.ID != id {
		t.Errorf("direct open id = %q, want %q", ts.ID, id)
	}
}

// TestImportable_ExcludesOrigin: GET /api/cc/importable lists recent on-disk
// sessions that are NOT yet origin-tracked.
func TestImportable_ExcludesOrigin(t *testing.T) {
	root, candID := writeCCSession(t, "cand-uuid", ccFixture)
	// A second session under the same root, already imported.
	proj := filepath.Join(root, "-Users-george-Code-anneal")
	ownedPath := filepath.Join(proj, "owned-uuid.jsonl")
	if err := os.WriteFile(ownedPath, []byte(ccFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	ownedID := "cc:owned-uuid"

	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))
	if err := gs.MarkCCOrigin(context.Background(), ownedID); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s, "GET", "/api/cc/importable", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("importable: %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Sessions []ThreadSummary `json:"sessions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	ids := map[string]bool{}
	for _, ts := range body.Sessions {
		ids[ts.ID] = true
	}
	if !ids[candID] {
		t.Errorf("import candidate %q missing from importable list: %+v", candID, body.Sessions)
	}
	if ids[ownedID] {
		t.Errorf("already-origin session %q should not be an import candidate", ownedID)
	}
}

// TestImport_MarksOriginResolvesRepoIdempotent covers POST /api/cc/import:
// it adopts the session (roster), resolves its repo from the cwd, is
// idempotent, and returns the summary with the repo overlay.
func TestImport_MarksOriginResolvesRepoIdempotent(t *testing.T) {
	// A session whose cwd is a REAL git dir so the repo resolves.
	repoDir := t.TempDir()
	mkGitDir(t, repoDir)
	root, id := writeCCSessionCwd(t, "imp-uuid", repoDir)

	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))

	rec := do(t, s, "POST", "/api/cc/import", map[string]any{"id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d (%s)", rec.Code, rec.Body.String())
	}
	var ts ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &ts)
	if ts.ID != id {
		t.Errorf("imported id = %q, want %q", ts.ID, id)
	}
	if ts.Repo == nil || ts.Repo.Root != filepath.Clean(repoDir) || ts.Repo.Name != filepath.Base(repoDir) {
		t.Errorf("imported repo = %+v, want %q", ts.Repo, repoDir)
	}

	// Origin recorded.
	origin, _ := gs.CCOriginSet(context.Background())
	if !origin[id] {
		t.Error("import did not mark the session web-origin")
	}
	// Repo persisted.
	m, _ := gs.RepoMap(context.Background())
	if m[id].Root != filepath.Clean(repoDir) {
		t.Errorf("repo map[%s] = %+v, want root %q", id, m[id], repoDir)
	}

	// Idempotent: re-import is 200, not an error.
	rec2 := do(t, s, "POST", "/api/cc/import", map[string]any{"id": id})
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-import: %d (%s), want idempotent 200", rec2.Code, rec2.Body.String())
	}
}

// TestImport_NotFound: importing an id with no on-disk session is a 404.
func TestImport_NotFound(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	s.Register(freshCC(t, t.TempDir())) // empty store

	rec := do(t, s, "POST", "/api/cc/import", map[string]any{"id": "cc:ghost-uuid"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("import unknown id = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// TestImport_BadRequest: a missing id is a 400.
func TestImport_BadRequest(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	s.Register(freshCC(t, t.TempDir()))
	rec := do(t, s, "POST", "/api/cc/import", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import without id = %d, want 400", rec.Code)
	}
}

// TestRepoOverlay_OnRoster: a thread with a stored repo reports it on
// GET /threads; one without omits it (plan WB-1).
func TestRepoOverlay_OnRoster(t *testing.T) {
	s, log, gs := newTestServer(t, "")
	seedThread(t, log, "withRepo", "has repo", "hi")
	seedThread(t, log, "noRepo", "no repo", "hi")
	if err := gs.SetThreadRepo(context.Background(), "withRepo", "/Code/anneal", "anneal"); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s, "GET", "/api/threads", nil)
	var got []ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	by := map[string]ThreadSummary{}
	for _, ts := range got {
		by[ts.ID] = ts
	}
	if by["withRepo"].Repo == nil || by["withRepo"].Repo.Name != "anneal" {
		t.Errorf("withRepo.Repo = %+v, want anneal", by["withRepo"].Repo)
	}
	if by["noRepo"].Repo != nil {
		t.Errorf("noRepo.Repo = %+v, want nil (omitted)", by["noRepo"].Repo)
	}
}

// TestRepoOmittedInJSON: the repo field is absent (not null) when unset.
func TestRepoOmittedInJSON(t *testing.T) {
	b, _ := json.Marshal(ThreadSummary{ID: "x"})
	if got := string(b); contains(got, "\"repo\"") {
		t.Errorf("marshaled summary %q should omit repo when nil", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
