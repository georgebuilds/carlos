package workspace

import (
	"path/filepath"
	"testing"
)

func TestPolicy_NilSafe(t *testing.T) {
	var p *Policy
	if p.Allows("bash", []byte(`{"cmd":"ls"}`)) {
		t.Error("nil Policy must deny")
	}
	if p.IsTrusted() {
		t.Error("nil Policy must report untrusted")
	}
	if p.Cwd() != "" {
		t.Error("nil Policy Cwd must be empty")
	}
	if p.Store() != nil {
		t.Error("nil Policy Store must be nil")
	}
	p.SetTrusted(true) // no panic
}

func TestPolicy_ZeroStoreDenies(t *testing.T) {
	p := NewPolicy(nil, "/tmp")
	if p.Allows("bash", []byte(`{"cmd":"ls"}`)) {
		t.Error("nil store: Allows should deny")
	}
}

func TestPolicy_UntrustedCwdDenies(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	p := NewPolicy(s, ws)
	if p.Allows("bash", []byte(`{"cmd":"ls"}`)) {
		t.Error("untrusted cwd: Allows should deny")
	}
}

func TestPolicy_TrustedCwdAllowsReadOnlyBash(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	if err := s.Trust(ws); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	p := NewPolicy(s, ws)
	if !p.Allows("bash", []byte(`{"cmd":"git status"}`)) {
		t.Errorf("trusted cwd should allow `git status`")
	}
	if !p.Allows("bash", []byte(`{"cmd":"ls -la"}`)) {
		t.Errorf("trusted cwd should allow `ls -la`")
	}
}

func TestPolicy_TrustedCwdDeniesWriteBash(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	_ = s.Trust(ws)
	p := NewPolicy(s, ws)
	if p.Allows("bash", []byte(`{"cmd":"git commit -m x"}`)) {
		t.Error("write bash must deny even in trusted cwd")
	}
	if p.Allows("bash", []byte(`{"cmd":"rm -rf node_modules"}`)) {
		t.Error("rm must deny even in trusted cwd")
	}
}

func TestPolicy_NonBashToolsDenied(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	_ = s.Trust(ws)
	p := NewPolicy(s, ws)
	// `edit` and `write` are file mutations — never auto-approved
	// by workspace trust; they must still prompt.
	if p.Allows("edit", []byte(`{"path":"main.go","contents":"..."}`)) {
		t.Error("edit must not be workspace-allowed")
	}
	if p.Allows("write", []byte(`{"path":"foo","contents":"bar"}`)) {
		t.Error("write must not be workspace-allowed")
	}
}

func TestPolicy_SetTrustedFlipsAllowance(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	p := NewPolicy(s, ws)
	if p.IsTrusted() {
		t.Fatal("precondition: should start untrusted")
	}
	p.SetTrusted(true)
	if !p.IsTrusted() {
		t.Error("SetTrusted(true) didn't take effect")
	}
	if !p.Allows("bash", []byte(`{"cmd":"git diff"}`)) {
		t.Error("SetTrusted(true): should allow `git diff`")
	}
	p.SetTrusted(false)
	if p.Allows("bash", []byte(`{"cmd":"git diff"}`)) {
		t.Error("SetTrusted(false): should re-deny")
	}
}

func TestPolicy_GarbledBashInputDenies(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	_ = s.Trust(ws)
	p := NewPolicy(s, ws)
	if p.Allows("bash", []byte(`{not-json`)) {
		t.Error("garbled JSON should deny (bias toward prompt)")
	}
	if p.Allows("bash", []byte(`{}`)) {
		t.Error("empty cmd should deny")
	}
}

func TestPolicy_CwdNormalized(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(filepath.Join(dir, "t.json"))
	_ = s.Trust(ws)
	// Construct policy via a path with a redundant trailing slash;
	// the cwd should normalize back to the canonical form.
	p := NewPolicy(s, ws+string(filepath.Separator))
	if !p.IsTrusted() {
		t.Errorf("policy cwd should normalize to trusted entry; cwd=%q", p.Cwd())
	}
}
