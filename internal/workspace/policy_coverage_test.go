package workspace

import "testing"

// TestIsReadOnly_TokenizesToEmptyDenies covers the `len(tokens) == 0`
// branch of IsReadOnly: a non-blank string made entirely of quote
// characters strips to zero tokens after tokenize and must deny rather
// than index tokens[0] out of range.
func TestIsReadOnly_TokenizesToEmptyDenies(t *testing.T) {
	for _, c := range []string{`""`, `''`, `"" ''`} {
		if IsReadOnly(c) {
			t.Errorf("IsReadOnly(%q) = true; quote-only input must deny", c)
		}
	}
}

// TestIsReadOnly_BareGitDenies covers the multi-tier `len(tokens) < 2`
// branch: `git` with no subcommand has no allowed first-positional and
// must deny.
func TestIsReadOnly_BareGitDenies(t *testing.T) {
	if IsReadOnly("git") {
		t.Error("IsReadOnly(\"git\") = true; bare git (no subcommand) must deny")
	}
}

// TestIsReadOnly_GitConfigWriteValue covers the config positionals>1
// write branch (`git config user.email VAL`) AND the read branch
// (`git config user.email`) for the positional gate.
func TestIsReadOnly_GitConfigPositionalGate(t *testing.T) {
	if IsReadOnly("git config user.email someone@example.com") {
		t.Error("git config with a value (2 positionals) is a write; must deny")
	}
	if !IsReadOnly("git config user.email") {
		t.Error("git config single-key read must allow")
	}
}

// TestExtractBashCmd_EmptyInput covers the len(input) == 0 early return
// in extractBashCmd: an empty envelope yields "" (deny-on-uncertainty)
// without reaching json.Unmarshal.
func TestExtractBashCmd_EmptyInput(t *testing.T) {
	if got := extractBashCmd(nil); got != "" {
		t.Errorf("extractBashCmd(nil) = %q, want empty", got)
	}
	if got := extractBashCmd([]byte{}); got != "" {
		t.Errorf("extractBashCmd([]) = %q, want empty", got)
	}
}

// TestExtractBashCmd_WellFormed and malformed inputs round-trip the
// success path and the parse-failure path of extractBashCmd.
func TestExtractBashCmd_WellFormedAndMalformed(t *testing.T) {
	if got := extractBashCmd([]byte(`{"cmd":"ls -la"}`)); got != "ls -la" {
		t.Errorf("extractBashCmd = %q, want \"ls -la\"", got)
	}
	if got := extractBashCmd([]byte(`{not json`)); got != "" {
		t.Errorf("extractBashCmd(garbled) = %q, want empty (deny)", got)
	}
}

// TestIsReadOnly_GitBranchInlineContains covers the inline
// `--contains=HEAD` value-flag branch in isReadOnlyGitPositional (the
// `=`-carrying form that must NOT be mistaken for a branch-name
// positional), alongside the bare-flag form for contrast.
func TestIsReadOnly_GitBranchInlineContains(t *testing.T) {
	if !IsReadOnly("git branch --contains=HEAD") {
		t.Error("git branch --contains=HEAD is a read form; must allow")
	}
	if !IsReadOnly("git branch --merged=main") {
		t.Error("git branch --merged=main is a read form; must allow")
	}
}

// TestIsReadOnly_GitRemoteFlagSkip covers the flag-skip `continue` in
// the remote positional scan: a leading flag before any sub-subcommand
// must be skipped so a read form still passes.
func TestIsReadOnly_GitRemoteFlagSkip(t *testing.T) {
	if !IsReadOnly("git remote -v show origin") {
		t.Error("git remote -v show origin is a read form; must allow")
	}
}

// TestPolicy_Allows_TrustedButStoreUntrusted exercises the IsTrusted()
// false fall-through inside Allows for a policy whose cwd is wired but
// never trusted: Allows must short-circuit to false before inspecting
// the tool/input.
func TestPolicy_Allows_UntrustedShortCircuits(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	s := NewStore(dir + "/t.json")
	p := NewPolicy(s, ws) // ws never trusted
	if p.Allows("bash", []byte(`{"cmd":"ls"}`)) {
		t.Error("untrusted policy must deny read-only bash too")
	}
}
