package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestRepoOverlay_NilGroups: the overlay no-ops (never panics) when no
// group store is wired, leaving Repo unset.
func TestRepoOverlay_NilGroups(t *testing.T) {
	s := &Server{} // groups == nil
	out := []ThreadSummary{{ID: "cc:x"}}
	s.repoOverlay(context.Background(), out)
	if out[0].Repo != nil {
		t.Errorf("repoOverlay with nil groups should no-op, got %+v", out[0].Repo)
	}
}

// Error-branch coverage for the import handlers (WA-2). The happy paths,
// 404, 400 and no-backend guards are covered in cc_origin_test.go /
// cc_origin_cover_test.go; these exercise the internal-error branches so
// the handlers clear the new-code coverage bar.

// TestImportable_ListError: a non-IsNotExist ReadDir failure inside
// ImportCandidates (the project root is a regular file, not a directory)
// surfaces as a 500.
func TestImportable_ListError(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "root-is-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, _ := newTestServer(t, "")
	s.Register(newCCBackendAt(notADir)) // ReadDir(notADir) -> ENOTDIR, not IsNotExist

	rec := do(t, s, "GET", "/api/cc/importable", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("importable with unreadable root = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}

// TestImportable_OriginSetFallback: when CCOriginSet errors (the group
// store handle is closed), the handler falls back to an empty origin set
// and still returns 200 with the candidates.
func TestImportable_OriginSetFallback(t *testing.T) {
	root, _ := writeCCSessionCwd(t, "imp-origin-err", t.TempDir())
	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))
	_ = gs.Close() // CCOriginSet (and the repo overlay) now error

	rec := do(t, s, "GET", "/api/cc/importable", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("importable with closed origin store = %d, want 200 fallback (%s)", rec.Code, rec.Body.String())
	}
}

// TestImport_MarkOriginError: MarkCCOrigin failing (closed store) surfaces
// as a 500. The session resolves on disk, so this is past the 404 path.
func TestImport_MarkOriginError(t *testing.T) {
	root, id := writeCCSessionCwd(t, "imp-mark-err", t.TempDir())
	s, _, gs := newTestServer(t, "")
	s.Register(freshCC(t, root))
	_ = gs.Close()

	rec := do(t, s, "POST", "/api/cc/import", map[string]any{"id": id})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("import with closed store = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}
