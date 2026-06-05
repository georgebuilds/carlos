package agent_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// newArtifactTestLog opens a SQLite event log in a temp dir, seeds one
// agent row (so InsertArtifact's foreign-key constraint is satisfied),
// and returns the log + the seed agent's ID + the artifact basePath
// that the test should pass to ReadArtifact (and that WriteArtifact will
// pick up via CARLOS_ARTIFACT_BASE).
func newArtifactTestLog(t *testing.T) (*agent.SQLiteEventLog, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	// Seed an agent row so the artifacts.agent_id FK lands.
	ctx := context.Background()
	agentID := "agent-test-" + filepath.Base(dir)
	seedAgent(t, ctx, log, agentID, "test", agent.StateRunning, time.Now().UTC().Truncate(time.Millisecond))

	base := filepath.Join(dir, "artifacts")
	t.Setenv("CARLOS_ARTIFACT_BASE", base)
	return log, agentID, base
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestWriteArtifact_HappyPath covers the canonical write→insert→read
// loop: WriteArtifact returns a ref with the right hash, the file lands
// on disk at <basePath>/<sha256>, and ReadArtifact roundtrips the bytes.
func TestWriteArtifact_HappyPath(t *testing.T) {
	log, agentID, base := newArtifactTestLog(t)
	ctx := context.Background()
	content := []byte("hello carlos\n")

	ref, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindText, content)
	if err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	if ref.SHA256 != sha256Hex(content) {
		t.Fatalf("ref.SHA256 = %q, want %q", ref.SHA256, sha256Hex(content))
	}
	if ref.AgentID != agentID {
		t.Fatalf("ref.AgentID = %q, want %q", ref.AgentID, agentID)
	}
	if ref.Kind != agent.ArtifactKindText {
		t.Fatalf("ref.Kind = %q, want %q", ref.Kind, agent.ArtifactKindText)
	}
	if ref.Size != int64(len(content)) {
		t.Fatalf("ref.Size = %d, want %d", ref.Size, len(content))
	}
	if ref.Path != filepath.Join(base, ref.SHA256) {
		t.Fatalf("ref.Path = %q, want %q", ref.Path, filepath.Join(base, ref.SHA256))
	}
	if ref.ID == "" {
		t.Fatalf("ref.ID empty (expected ULID)")
	}
	if ref.CreatedAt.IsZero() {
		t.Fatalf("ref.CreatedAt zero")
	}

	// File on disk has the right bytes.
	got, err := agent.ReadArtifact(base, ref.SHA256)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("ReadArtifact got %q, want %q", got, content)
	}

	// Row inserted in artifacts table — verify directly via DB.
	var n int
	if err := log.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM artifacts WHERE id = ?`, ref.ID,
	).Scan(&n); err != nil {
		t.Fatalf("count row: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 row for ref.ID, got %d", n)
	}
}

// TestWriteArtifact_Idempotent verifies the dedup contract: same content
// twice → same blob on disk (one file), but two distinct rows so each
// agent's attribution is preserved.
func TestWriteArtifact_Idempotent(t *testing.T) {
	log, agentID, base := newArtifactTestLog(t)
	ctx := context.Background()
	content := []byte("dedup me")

	ref1, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindFile, content)
	if err != nil {
		t.Fatalf("WriteArtifact #1: %v", err)
	}
	ref2, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindFile, content)
	if err != nil {
		t.Fatalf("WriteArtifact #2: %v", err)
	}

	if ref1.SHA256 != ref2.SHA256 {
		t.Fatalf("dedup: sha mismatch %q vs %q", ref1.SHA256, ref2.SHA256)
	}
	if ref1.Path != ref2.Path {
		t.Fatalf("dedup: path mismatch %q vs %q", ref1.Path, ref2.Path)
	}
	if ref1.ID == ref2.ID {
		t.Fatalf("dedup: row IDs identical (%q); expected fresh ULID per row", ref1.ID)
	}

	// Exactly one blob file on disk (plus no temp files).
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var blobs, tmps int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			tmps++
			continue
		}
		blobs++
	}
	if blobs != 1 {
		t.Fatalf("expected 1 blob file, got %d (entries=%v)", blobs, entries)
	}
	if tmps != 0 {
		t.Fatalf("expected 0 .tmp files, got %d", tmps)
	}

	// Two rows in the artifacts table for the same sha.
	var n int
	if err := log.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM artifacts WHERE sha256 = ?`, ref1.SHA256,
	).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 rows for sha %s, got %d", ref1.SHA256, n)
	}
}

// TestWriteArtifact_NoTempLeftover asserts the atomic-write contract
// from the outside: after a successful WriteArtifact, no `.tmp.*` file
// remains in basePath. (We can't easily simulate a power-loss mid-write,
// but we can check the recipe's invariant on the success path.)
func TestWriteArtifact_NoTempLeftover(t *testing.T) {
	log, agentID, base := newArtifactTestLog(t)
	ctx := context.Background()

	for i, content := range [][]byte{
		[]byte("a"),
		[]byte("bb"),
		[]byte("ccc"),
		make([]byte, 1<<16), // 64KiB of zeros
	} {
		if _, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindOther, content); err != nil {
			t.Fatalf("WriteArtifact #%d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("leftover temp file in basePath: %s", e.Name())
		}
		// Filename should be 64 hex chars (sha256) with no extension.
		if len(e.Name()) != 64 {
			t.Fatalf("unexpected filename in basePath: %q (want 64-char sha)", e.Name())
		}
	}
}

// TestReadArtifact_NotFound exercises the ErrArtifactNotFound sentinel.
func TestReadArtifact_NotFound(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "artifacts")
	if err := agent.MkdirArtifactBase(base); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 64-char hex that won't exist.
	missing := strings.Repeat("0", 64)
	_, err := agent.ReadArtifact(base, missing)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, agent.ErrArtifactNotFound) {
		t.Fatalf("expected ErrArtifactNotFound, got %v", err)
	}
}

// TestArtifactPermissions verifies the privacy posture: blobs are mode
// 0600 and the base directory is mode 0700. Artifacts may carry secrets
// (provider responses, draft diffs of private code), so wide-open perms
// would be a leak.
func TestArtifactPermissions(t *testing.T) {
	log, agentID, base := newArtifactTestLog(t)
	ctx := context.Background()

	ref, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindFile, []byte("perm check"))
	if err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	fi, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatalf("stat blob: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("blob mode = %o, want 0600", perm)
	}

	di, err := os.Stat(base)
	if err != nil {
		t.Fatalf("stat base: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("base dir mode = %o, want 0700", perm)
	}
}

// TestWriteArtifact_ConcurrentSameContent races N goroutines writing the
// same bytes. All should succeed, final state should have exactly one
// blob file and N rows. This exercises both the race-on-rename window
// and the row-insert serialization at the SQLite layer.
func TestWriteArtifact_ConcurrentSameContent(t *testing.T) {
	log, agentID, base := newArtifactTestLog(t)
	ctx := context.Background()

	const N = 16
	content := []byte("concurrent content")
	expected := sha256Hex(content)

	var wg sync.WaitGroup
	errs := make(chan error, N)
	refs := make(chan agent.ArtifactRef, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := agent.WriteArtifact(ctx, log, agentID, agent.ArtifactKindFile, content)
			if err != nil {
				errs <- err
				return
			}
			refs <- ref
		}()
	}
	wg.Wait()
	close(errs)
	close(refs)

	for err := range errs {
		t.Fatalf("concurrent WriteArtifact: %v", err)
	}

	seenIDs := map[string]struct{}{}
	for ref := range refs {
		if ref.SHA256 != expected {
			t.Fatalf("ref sha %q != expected %q", ref.SHA256, expected)
		}
		if _, dup := seenIDs[ref.ID]; dup {
			t.Fatalf("duplicate row id %q across goroutines", ref.ID)
		}
		seenIDs[ref.ID] = struct{}{}
	}
	if len(seenIDs) != N {
		t.Fatalf("expected %d distinct row IDs, got %d", N, len(seenIDs))
	}

	// One blob file in basePath, no temps.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var blobs int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("leftover temp: %s", e.Name())
		}
		blobs++
	}
	if blobs != 1 {
		t.Fatalf("expected 1 blob file post-race, got %d", blobs)
	}

	// N rows in artifacts table.
	var n int
	if err := log.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM artifacts WHERE sha256 = ?`, expected,
	).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != N {
		t.Fatalf("expected %d rows, got %d", N, n)
	}
}

// TestArtifactBasePath_EnvOverride proves CARLOS_ARTIFACT_BASE wins
// over the computed default — the seam tests use to keep blobs in
// t.TempDir().
func TestArtifactBasePath_EnvOverride(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", "/tmp/explicit-override")
	got := agent.ArtifactBasePath("/home/whoever")
	if got != "/tmp/explicit-override" {
		t.Fatalf("env override ignored: got %q", got)
	}
}

// TestArtifactBasePath_DefaultsToHome covers the unset-env path: a
// supplied home goes to <home>/.carlos/artifacts.
func TestArtifactBasePath_DefaultsToHome(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", "")
	got := agent.ArtifactBasePath("/home/whoever")
	want := filepath.Join("/home/whoever", ".carlos", "artifacts")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestMkdirArtifactBase_Idempotent runs Mkdir twice; second call must
// not error. Mode stays 0700 after both.
func TestMkdirArtifactBase_Idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "carlos-artifacts")
	if err := agent.MkdirArtifactBase(dir); err != nil {
		t.Fatalf("mkdir #1: %v", err)
	}
	// Tighten a deliberately loose perms to test the chmod path.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("loosen perms: %v", err)
	}
	if err := agent.MkdirArtifactBase(dir); err != nil {
		t.Fatalf("mkdir #2: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("perm %o, want 0700", perm)
	}
}

// TestWriteArtifact_NilLog and friends cover the input validation
// branches so a misuse fails fast rather than producing an orphan blob.
func TestWriteArtifact_NilLog(t *testing.T) {
	_, err := agent.WriteArtifact(context.Background(), nil, "a", agent.ArtifactKindText, []byte("x"))
	if err == nil {
		t.Fatalf("expected error for nil log")
	}
}

func TestWriteArtifact_EmptyAgentID(t *testing.T) {
	log, _, _ := newArtifactTestLog(t)
	_, err := agent.WriteArtifact(context.Background(), log, "", agent.ArtifactKindText, []byte("x"))
	if err == nil {
		t.Fatalf("expected error for empty agentID")
	}
}

func TestWriteArtifact_EmptyKind(t *testing.T) {
	log, agentID, _ := newArtifactTestLog(t)
	_, err := agent.WriteArtifact(context.Background(), log, agentID, "", []byte("x"))
	if err == nil {
		t.Fatalf("expected error for empty kind")
	}
}

// TestReadArtifact_EmptyInputs guards the cheap-misuse branches.
func TestReadArtifact_EmptyInputs(t *testing.T) {
	if _, err := agent.ReadArtifact("", "abc"); err == nil {
		t.Fatalf("expected error for empty basePath")
	}
	if _, err := agent.ReadArtifact("/tmp", ""); err == nil {
		t.Fatalf("expected error for empty sha")
	}
}
