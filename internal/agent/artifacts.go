// Phase 3 slice 3d — content-addressable artifact store.
//
// Sub-agents return deliverables (file contents, diffs, plans, skill
// PROPOSALs, research notes) to their parent via on-disk artifacts; the
// parent only sees a lightweight ArtifactRef. This is the architectural
// "avoid game of telephone" pattern from the 2026-06-04 supervisor
// decisions: the full transcript stays in the child's event log, but the
// child's structured output is materialized as a hash-addressed blob the
// parent can stream, diff, or hand to a viewer.
//
// # Layout
//
//	<basePath>/<sha256>            -- the blob, mode 0600
//	<basePath>/<sha256>.tmp.<rand> -- in-flight write; renamed atomically
//
// Default basePath: ~/.carlos/artifacts/. Overridable via
// CARLOS_ARTIFACT_BASE (same convention as CARLOS_CONFIG), primarily for
// tests. The design hand-wavily suggested ~/.carlos/runs/<session>/artifacts/
// but `session` is not a v0 concept (the supervisor research notes called
// this out); a flat content-addressable store is the v0 shape and a
// future runs/ layout can grow on top with hard links or symlinks if
// session-scoped GC becomes interesting.
//
// # Why content-addressable
//
//   - Dedup. Two children that happen to generate byte-identical output
//     (a regenerated test file, an empty diff, a stock "no changes
//     needed" plan) share one blob. The row in `artifacts` is per-agent
//     so attribution is preserved.
//   - Integrity. The filename IS the hash; readers verify on read by
//     re-hashing if they want stronger guarantees. We never trust a row's
//     sha256 against a renamed blob — the row is metadata, the file is
//     truth.
//   - GC story. A future "drop blobs not referenced by any non-pruned
//     agent row" sweep is trivial: list <basePath>, subtract the set of
//     sha256s still referenced from `artifacts`. No path-mangling, no
//     session-tree walks.
//
// # Atomic writes
//
// Same recipe as internal/config/config.go.Save: open a unique temp file
// under basePath, write+fsync, then rename onto <basePath>/<sha256>. If
// the destination already exists we discard the temp — sha256 collision
// is cryptographic, not coincidental, so the existing file IS the same
// content. Concurrent writers of identical content race only on the
// rename; both end up with the same final path.
//
// # Phase 3e (Agent tool) handoff
//
// When a spawned child finishes, the parent's SpawnResult will carry an
// []ArtifactRef. The child's outermost loop calls WriteArtifact(...) for
// each typed deliverable (one for the diff, one for the plan, etc.) and
// hands the slice back. Parents that want to render to the user open the
// blob via ReadArtifact(basePath, ref.SHA256).
//
// # Phase 6 (skill induction) handoff
//
// Induced skills are emitted as artifacts of kind "skill_proposal".
// They surface in manage-mode's approval queue alongside plans and
// diffs (DESIGN § Manage mode: "no separate skills view"); the user
// reviews via the same UX. Approval flips a skill_proposal artifact into
// a real file under .agents/skills/ or .claude/skills/ depending on the
// user's SkillsConfig.Convention.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"
)

// ArtifactKind values. Mirrors the design § Storage list. The set is
// open (the `kind` column is TEXT, not an enum) so callers may pass other
// strings, but sticking to these keeps the manage-mode roster's filter
// chips stable.
const (
	ArtifactKindFile          = "file"
	ArtifactKindDiff          = "diff"
	ArtifactKindPlan          = "plan"
	ArtifactKindSkillProposal = "skill_proposal"
	ArtifactKindResearch      = "research"
	ArtifactKindText          = "text"
	ArtifactKindOther         = "other"
)

// ErrArtifactNotFound is returned by ReadArtifact when no blob exists at
// the addressed path. Callers should treat it as "the row pointed at
// nothing on disk" — a recoverable miss, not a corrupt log.
var ErrArtifactNotFound = errors.New("artifact: not found")

// ArtifactRef is the lightweight pointer the parent receives from a
// child. It carries enough to render a roster entry (kind, size,
// created_at) and to load the blob (path, sha256). The parent never
// receives the raw bytes — that's the whole point of the artifact
// pattern.
type ArtifactRef struct {
	ID        string    // ULID from the artifacts row
	AgentID   string    // who produced it
	Path      string    // absolute path to the on-disk blob
	Kind      string    // file | diff | plan | skill_proposal | research | text | other
	SHA256    string    // lowercase hex, 64 chars
	Size      int64     // bytes
	CreatedAt time.Time // UTC, ms-truncated to match the rest of the log
}

// artifactWriter is the subset of *SQLiteEventLog needed by
// WriteArtifact. Pulled out so tests that don't want a real SQLite
// backend (or that want to inject failures) can substitute. Currently
// only SQLiteEventLog satisfies it; that's fine.
type artifactWriter interface {
	InsertArtifact(ctx context.Context, a Artifact) error
}

// ArtifactBasePath returns the absolute directory under which all blobs
// live. Precedence:
//
//  1. $CARLOS_ARTIFACT_BASE (test override)
//  2. <home>/.carlos/artifacts/
//
// home is typically os.UserHomeDir(); pass "" to let the function decide
// via the standard library (an empty home + missing env yields the
// relative ".carlos/artifacts/" fallback — the same shape config.go uses
// for its YAML).
func ArtifactBasePath(home string) string {
	if env := os.Getenv("CARLOS_ARTIFACT_BASE"); env != "" {
		return env
	}
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			home = h
		}
	}
	if home == "" {
		return filepath.Join(".carlos", "artifacts")
	}
	return filepath.Join(home, ".carlos", "artifacts")
}

// MkdirArtifactBase ensures basePath exists at mode 0700. Artifacts may
// contain secrets (API responses, draft diffs against private code), so
// the directory mode matches ~/.carlos itself. Idempotent. If the
// directory already exists with looser perms we tighten it via Chmod —
// best-effort, errors there are swallowed because the caller asked for
// "ensure it exists", not "audit perms".
func MkdirArtifactBase(basePath string) error {
	if basePath == "" {
		return errors.New("artifact: MkdirArtifactBase called with empty basePath")
	}
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		return fmt.Errorf("artifact: mkdir %s: %w", basePath, err)
	}
	_ = os.Chmod(basePath, 0o700)
	return nil
}

// WriteArtifact hashes content, writes it to <basePath>/<sha256>
// atomically if not already present, inserts a row into the `artifacts`
// table attributing the blob to agentID, and returns the resulting
// ArtifactRef.
//
// basePath is resolved via ArtifactBasePath at the call site (callers
// hold their own copy so tests can substitute). MkdirArtifactBase is
// invoked here so callers don't have to remember the ordering on first
// use.
//
// Idempotency: calling WriteArtifact twice with the same content reuses
// the existing blob (no second file appears on disk) and inserts a new
// row each time. The two rows carry distinct ULIDs but the same sha256,
// so the cross-agent attribution graph stays intact. This matches
// DESIGN's "blobs DO NOT live in SQLite rows" invariant — duplicate
// content is normal, the row is the authoritative reference.
//
// Errors:
//   - nil log returns an error immediately; we don't write a blob with
//     no row to point at it (would-be orphan on disk).
//   - filesystem errors propagate wrapped with the offending path.
//   - row insert errors propagate wrapped; the blob is left on disk
//     (the next attempt will reuse it, no leak).
func WriteArtifact(ctx context.Context, log artifactWriter, agentID, kind string, content []byte) (ArtifactRef, error) {
	if log == nil {
		return ArtifactRef{}, errors.New("artifact: WriteArtifact called with nil log")
	}
	if agentID == "" {
		return ArtifactRef{}, errors.New("artifact: WriteArtifact called with empty agentID")
	}
	if kind == "" {
		return ArtifactRef{}, errors.New("artifact: WriteArtifact called with empty kind")
	}

	basePath := ArtifactBasePath("")
	if err := MkdirArtifactBase(basePath); err != nil {
		return ArtifactRef{}, err
	}

	sum := sha256.Sum256(content)
	hexSum := hex.EncodeToString(sum[:])
	dest := filepath.Join(basePath, hexSum)

	// Fast path: blob already present. Stat is cheap and avoids the temp
	// dance entirely for the dedup case.
	if _, err := os.Stat(dest); err == nil {
		// Existing file is, by construction, the same content. Skip the
		// write; still insert the row so per-agent attribution lands.
	} else if !errors.Is(err, os.ErrNotExist) {
		return ArtifactRef{}, fmt.Errorf("artifact: stat %s: %w", dest, err)
	} else {
		if err := writeBlobAtomic(dest, content); err != nil {
			return ArtifactRef{}, err
		}
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	rowID, err := newArtifactRowID(now)
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("artifact: mint row id: %w", err)
	}

	row := Artifact{
		ID:        rowID,
		AgentID:   agentID,
		Path:      dest,
		Kind:      kind,
		SHA256:    hexSum,
		CreatedAt: now,
	}
	if err := log.InsertArtifact(ctx, row); err != nil {
		return ArtifactRef{}, err
	}

	return ArtifactRef{
		ID:        rowID,
		AgentID:   agentID,
		Path:      dest,
		Kind:      kind,
		SHA256:    hexSum,
		Size:      int64(len(content)),
		CreatedAt: now,
	}, nil
}

// ReadArtifact reads the blob at <basePath>/<sha256>. Returns
// ErrArtifactNotFound (wrapped via errors.Is-friendly sentinel) if the
// file is missing — callers can distinguish "file gone" from "fs error".
func ReadArtifact(basePath, sha256Hex string) ([]byte, error) {
	if basePath == "" {
		return nil, errors.New("artifact: ReadArtifact called with empty basePath")
	}
	if sha256Hex == "" {
		return nil, errors.New("artifact: ReadArtifact called with empty sha256")
	}
	path := filepath.Join(basePath, sha256Hex)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrArtifactNotFound, path)
		}
		return nil, fmt.Errorf("artifact: read %s: %w", path, err)
	}
	return b, nil
}

// writeBlobAtomic implements the temp+fsync+rename dance. dest is the
// final <basePath>/<sha256> path; we derive the temp name in the same
// directory so rename(2) stays on the same filesystem (POSIX guarantees
// atomic rename only within a single fs).
//
// If the destination materializes between our Stat above and our Rename
// here (concurrent writers, identical content), Rename overwrites it on
// POSIX with the same bytes — the result is byte-for-byte equivalent
// because the sha matches, so atomicity is preserved. We could
// alternately use os.Link to atomically claim the slot, but Rename keeps
// us symmetric with config.go.
func writeBlobAtomic(dest string, content []byte) error {
	dir := filepath.Dir(dest)
	suffix, err := randomSuffix()
	if err != nil {
		return fmt.Errorf("artifact: random suffix: %w", err)
	}
	tmp := dest + ".tmp." + suffix

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("artifact: open tmp %s: %w", tmp, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("artifact: write tmp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("artifact: fsync tmp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("artifact: close tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("artifact: rename %s -> %s: %w", tmp, dest, err)
	}
	_ = dir // silence unused warning if filepath.Dir gets inlined away
	return nil
}

// randomSuffix returns 16 hex chars of crypto/rand entropy. Used to
// disambiguate concurrent temp files in the same basePath.
func randomSuffix() (string, error) {
	var buf [8]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// newArtifactRowID mints a ULID stamped with `now`. We use the same
// monotonic-entropy reader the rest of the package will use (sliding the
// generator into a package-level var here so the artifacts ULIDs sort by
// creation time the same way the agents-table ULIDs do — useful when a
// future roster query orders artifacts by id).
func newArtifactRowID(now time.Time) (string, error) {
	u, err := ulid.New(uint64(now.UnixMilli()), artifactULIDEntropy)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// artifactULIDEntropy is the monotonic entropy source for artifact row
// IDs. Crypto-random base, monotonic increment within the same
// millisecond — same recipe as ulid_test.go validates for the agents
// table.
var artifactULIDEntropy = ulid.Monotonic(rand.Reader, 0)
