package chat

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/tui/slash"
	"github.com/georgebuilds/carlos/internal/workspace"
)

func TestTrustSlash_NoPolicyWired(t *testing.T) {
	m := newTestModel(t)
	c, _ := slash.Parse("/trust")
	cmd := m.dispatchSlash(c)
	if cmd == nil {
		t.Fatal("dispatchSlash(/trust) returned nil")
	}
	msg := cmd().(statusMsg)
	if msg.kind != statusWarn || !strings.Contains(msg.text, "not wired") {
		t.Errorf("expected 'not wired' warn; got %+v", msg)
	}
}

func TestTrustSlash_TrustPersistsAndFlipsInSession(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	policy := workspace.NewPolicy(store, ws)
	if policy.IsTrusted() {
		t.Fatal("precondition: policy should start untrusted")
	}
	m := newTestModel(t)
	m.workspace = policy

	c, _ := slash.Parse("/trust")
	cmd := m.dispatchSlash(c)
	msg := cmd().(statusMsg)
	if msg.kind != statusInfo || !strings.Contains(msg.text, "trusted workspace") {
		t.Errorf("/trust status = %+v; want info+'trusted workspace'", msg)
	}
	if !policy.IsTrusted() {
		t.Error("/trust did not flip policy.IsTrusted()")
	}
	ok, _ := store.IsTrusted(ws)
	if !ok {
		t.Error("/trust did not persist to store")
	}
}

func TestTrustSlash_UntrustClearsBoth(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	_ = store.Trust(ws)
	policy := workspace.NewPolicy(store, ws)
	if !policy.IsTrusted() {
		t.Fatal("precondition: policy should start trusted")
	}
	m := newTestModel(t)
	m.workspace = policy

	c, _ := slash.Parse("/untrust")
	cmd := m.dispatchSlash(c)
	msg := cmd().(statusMsg)
	if msg.kind != statusInfo || !strings.Contains(msg.text, "untrusted workspace") {
		t.Errorf("/untrust status = %+v; want info+'untrusted workspace'", msg)
	}
	if policy.IsTrusted() {
		t.Error("/untrust did not clear policy.IsTrusted()")
	}
	ok, _ := store.IsTrusted(ws)
	if ok {
		t.Error("/untrust did not remove store entry")
	}
}

func TestTrustsSlash_EmptyListHint(t *testing.T) {
	dir := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	policy := workspace.NewPolicy(store, t.TempDir())
	m := newTestModel(t)
	m.workspace = policy

	c, _ := slash.Parse("/trusts")
	cmd := m.dispatchSlash(c)
	msg := cmd().(statusMsg)
	if msg.kind != statusInfo || !strings.Contains(msg.text, "no trusted workspaces") {
		t.Errorf("empty /trusts = %+v; want info+'no trusted workspaces'", msg)
	}
}

func TestTrustsSlash_RendersList(t *testing.T) {
	dir := t.TempDir()
	store := workspace.NewStore(filepath.Join(dir, "t.json"))
	a := t.TempDir()
	b := t.TempDir()
	_ = store.Trust(a)
	_ = store.Trust(b)
	policy := workspace.NewPolicy(store, t.TempDir())
	m := newTestModel(t)
	m.workspace = policy

	c, _ := slash.Parse("/trusts")
	cmd := m.dispatchSlash(c)
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.text, "2 trusted workspace") {
		t.Errorf("/trusts text missing count: %q", msg.text)
	}
	if !strings.Contains(msg.text, a) || !strings.Contains(msg.text, b) {
		t.Errorf("/trusts text missing entries: %q", msg.text)
	}
}

func TestTrustSlash_RegisteredInBuiltins(t *testing.T) {
	for _, name := range []string{"trust", "untrust", "trusts"} {
		if _, ok := slash.Lookup(name); !ok {
			t.Errorf("slash.Lookup(%q): not in Builtins", name)
		}
	}
}
