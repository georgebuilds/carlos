package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

func TestGroupStore_HideUnhide(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	if err := gs.Hide(ctx, "cc:x"); err != nil {
		t.Fatal(err)
	}
	if err := gs.Hide(ctx, "cc:x"); err != nil { // idempotent
		t.Fatalf("re-hide: %v", err)
	}
	set, _ := gs.HiddenSet(ctx)
	if !set["cc:x"] || len(set) != 1 {
		t.Errorf("hidden set = %v, want {cc:x}", set)
	}
	if err := gs.Unhide(ctx, "cc:x"); err != nil {
		t.Fatal(err)
	}
	set2, _ := gs.HiddenSet(ctx)
	if len(set2) != 0 {
		t.Errorf("hidden set after unhide = %v, want empty", set2)
	}
}

// hiddenOf fetches the roster and returns id -> Hidden.
func hiddenOf(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	rec := do(t, s, "GET", "/api/threads", nil)
	var list []ThreadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, ts := range list {
		m[ts.ID] = ts.Hidden
	}
	return m
}

func TestThreads_HideOverlayAndEndpoint(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "one", "hi")
	seedThread(t, log, "t2", "two", "yo")

	if h := hiddenOf(t, s); h["t1"] || h["t2"] {
		t.Fatalf("nothing should be hidden initially: %v", h)
	}
	// hide t1
	if rec := do(t, s, "POST", "/api/threads/t1/hide", nil); rec.Code != 204 {
		t.Fatalf("hide: %d", rec.Code)
	}
	h := hiddenOf(t, s)
	if !h["t1"] || h["t2"] {
		t.Errorf("after hide t1: %v, want only t1 hidden", h)
	}
	// unhide t1
	if rec := do(t, s, "DELETE", "/api/threads/t1/hide", nil); rec.Code != 204 {
		t.Fatalf("unhide: %d", rec.Code)
	}
	if hiddenOf(t, s)["t1"] {
		t.Error("t1 should be visible again after unhide")
	}
}

func TestCCBackend_DeleteRemovesSessionFile(t *testing.T) {
	root, id := writeCCSession(t, "del-uuid", ccFixture)
	b := newCCBackendAt(root)
	path := filepath.Join(root, "-Users-george-Code-anneal", "del-uuid.jsonl")

	n, err := b.Delete(id)
	if err != nil || n != 1 {
		t.Fatalf("Delete = (%d,%v), want (1,nil)", n, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("session file still present after delete: %v", err)
	}
	// deleting a gone session reports not found
	if _, err := b.Delete(id); !errors.Is(err, agent.ErrSessionNotFound) {
		t.Errorf("second delete err = %v, want ErrSessionNotFound", err)
	}
}

// The hide endpoint is reachable through the auth-wrapped handler with a
// path-encoded cc: id (the colon is a valid path char).
func TestThreads_HideAcceptsPrefixedIds(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	req := httptest.NewRequest("POST", "/api/threads/cc:abc-123/hide", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("hide cc id: %d (%s)", rec.Code, rec.Body.String())
	}
}
