package chat

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/workspace"
)

// emptyCwdModel wires a workspace policy whose cwd is empty (startup
// cwd resolution failed) so the trust slashes hit their "no workspace
// anchored" branch rather than the happy path.
func emptyCwdModel(t *testing.T) *Model {
	t.Helper()
	store := workspace.NewStore(filepath.Join(t.TempDir(), "t.json"))
	policy := workspace.NewPolicy(store, "")
	m := newTestModel(t)
	m.workspace = policy
	return m
}

func TestTrustSlashEnable_NoCwdAnchored(t *testing.T) {
	m := emptyCwdModel(t)
	st, ok := m.trustSlashEnable()().(statusMsg)
	if !ok || !strings.Contains(st.text, "no workspace anchored") {
		t.Errorf("/trust with empty cwd should warn no-anchor; got %+v", st)
	}
}

func TestTrustSlashDisable_NoCwdAnchored(t *testing.T) {
	m := emptyCwdModel(t)
	st, ok := m.trustSlashDisable()().(statusMsg)
	if !ok || !strings.Contains(st.text, "no workspace anchored") {
		t.Errorf("/untrust with empty cwd should warn no-anchor; got %+v", st)
	}
}
