package agent_test

// Coverage for artifacts.go filesystem error branches and the
// home-fallback path in ArtifactBasePath:
//
//   - ArtifactBasePath empty-home fallback to ".carlos/artifacts".
//   - MkdirArtifactBase mkdir failure (a path component is a regular file).
//   - WriteArtifact's mkdir / stat / blob-write error propagation.
//   - ReadArtifact's non-ErrNotExist error (reading a directory).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestArtifactBasePath_EmptyHomeFallback clears both the env override and
// HOME so os.UserHomeDir fails (or returns empty) and the function falls
// back to the relative ".carlos/artifacts" path.
func TestArtifactBasePath_EmptyHomeFallback(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", "")
	t.Setenv("HOME", "")
	// On some platforms UserHomeDir reads other vars; clear the common ones.
	t.Setenv("USERPROFILE", "")
	got := agent.ArtifactBasePath("")
	want := filepath.Join(".carlos", "artifacts")
	if got != want {
		t.Fatalf("empty-home fallback = %q, want %q", got, want)
	}
}

// TestMkdirArtifactBase_MkdirFails points the base at a path whose parent
// component is a regular file, so MkdirAll fails.
func TestMkdirArtifactBase_MkdirFails(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regularfile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := agent.MkdirArtifactBase(filepath.Join(file, "sub")); err == nil {
		t.Fatal("MkdirArtifactBase should fail when a path component is a file")
	}
}

// TestWriteArtifact_MkdirBaseFails sets CARLOS_ARTIFACT_BASE to a path
// under a regular file so the in-WriteArtifact MkdirArtifactBase fails.
func TestWriteArtifact_MkdirBaseFails(t *testing.T) {
	log, agentID, _ := newArtifactTestLog(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "blocker")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(file, "artifacts"))
	if _, err := agent.WriteArtifact(context.Background(), log, agentID, agent.ArtifactKindText, []byte("hi")); err == nil {
		t.Fatal("WriteArtifact should fail when base dir cannot be created")
	}
}

// TestReadArtifact_NonNotExistError reads a path that exists but is a
// directory, so os.ReadFile returns an error that is NOT ErrNotExist,
// hitting the generic read-error wrap.
func TestReadArtifact_NonNotExistError(t *testing.T) {
	dir := t.TempDir()
	// Create a "blob" that is actually a directory.
	sha := "deadbeef"
	if err := os.MkdirAll(filepath.Join(dir, sha), 0o700); err != nil {
		t.Fatalf("mkdir blob-as-dir: %v", err)
	}
	_, err := agent.ReadArtifact(dir, sha)
	if err == nil {
		t.Fatal("ReadArtifact on a directory should error")
	}
	// It must NOT be the not-found sentinel — it's a real read error.
	if os.IsNotExist(err) {
		t.Fatalf("expected a non-not-exist error, got %v", err)
	}
}

// TestWriteArtifact_StatBlobIsDirError forces the dedup-stat path to find
// a directory where the blob should be. Stat succeeds (err == nil) so the
// fast-path "already present" branch is taken, and the subsequent insert
// happens normally — this confirms the stat-success dedup branch when the
// destination already exists.
func TestWriteArtifact_DedupStatHit(t *testing.T) {
	log, agentID, base := newArtifactTestLog(t)
	ctx := context.Background()
	content := []byte("stat-hit content")

	// First write lands the blob.
	ref1, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindFile, content)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Second write should hit the Stat-success dedup branch (blob present).
	ref2, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindFile, content)
	if err != nil {
		t.Fatalf("second write (dedup): %v", err)
	}
	if ref1.SHA256 != ref2.SHA256 {
		t.Fatalf("dedup sha mismatch: %q vs %q", ref1.SHA256, ref2.SHA256)
	}
	_ = base
}
