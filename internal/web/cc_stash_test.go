package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestGetThread_StashSurvivesUntilFileLands pins the new-session fix: a CC
// session created via the web "+ new" flow has no JSONL until its first
// turn, but is live (driver attached). GetThread must surface the stashed
// create-summary in that window so the thread stays in the roster instead
// of vanishing on the next poll, then hand off to the file once it lands.
func TestGetThread_StashSurvivesUntilFileLands(t *testing.T) {
	root := t.TempDir()
	b := freshCC(t, root)
	b.EnableDrive(context.Background(), newEphemeralHub(), "http://127.0.0.1:0", "tok", "/bin/carlos")
	const uuid = "stash-uuid"
	id := "cc:" + uuid

	// Simulate the create stash directly (CreateThread spawns a real
	// claude subprocess, unsuitable for a unit test; the stash write it
	// performs is what we exercise here).
	b.driveMu.Lock()
	b.created[id] = ThreadSummary{
		ID: id, Title: "new claude code session", State: "running",
		Attached: true, Backend: ccBackendName,
	}
	b.driveMu.Unlock()

	// Before the file exists, GetThread surfaces the stash.
	got, ok, err := b.GetThread(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetThread before file = (ok=%v err=%v), want the stash", ok, err)
	}
	if got.Title != "new claude code session" || got.State != "running" {
		t.Errorf("stashed summary = %+v", got)
	}

	// The JSONL lands in the backend's project store.
	proj := filepath.Join(root, "-enc-"+uuid)
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","timestamp":"2026-06-13T10:00:00.000Z","cwd":"/tmp","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, uuid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Now GetThread resolves from disk and drops the stale stash.
	if _, ok, _ := b.GetThread(context.Background(), id); !ok {
		t.Fatal("GetThread after file should resolve from disk")
	}
	b.driveMu.Lock()
	_, stashed := b.created[id]
	b.driveMu.Unlock()
	if stashed {
		t.Error("stash must be dropped once the file exists")
	}

	// Detach clears a still-file-less stash, so a created-but-never-run
	// session leaves the roster on detach.
	const id2 = "cc:never-ran"
	b.driveMu.Lock()
	b.created[id2] = ThreadSummary{ID: id2}
	b.driveMu.Unlock()
	_ = b.Detach(id2)
	b.driveMu.Lock()
	_, lingering := b.created[id2]
	b.driveMu.Unlock()
	if lingering {
		t.Error("Detach should clear a file-less stash")
	}
}
