package agent

import (
	"sync"
	"testing"
)

// recordingApprover collects decision calls so layered tests can
// distinguish "fallback consulted" from "fallback bypassed".
type recordingApprover struct {
	mu     sync.Mutex
	calls  []string
	allow  bool
	called bool
}

func (r *recordingApprover) ApproveToolCall(name string, _ []byte) bool {
	r.mu.Lock()
	r.called = true
	r.calls = append(r.calls, name)
	r.mu.Unlock()
	return r.allow
}

func (r *recordingApprover) wasCalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// recordingAuditSink captures decisions so tests can assert on the
// reason field.
type recordingAuditSink struct {
	mu        sync.Mutex
	decisions []Decision
}

func (s *recordingAuditSink) RecordDecision(d Decision) {
	s.mu.Lock()
	s.decisions = append(s.decisions, d)
	s.mu.Unlock()
}

func (s *recordingAuditSink) snapshot() []Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Decision, len(s.decisions))
	copy(out, s.decisions)
	return out
}

func TestLayeredApprover_BuiltinAllowBypassesFallback(t *testing.T) {
	fallback := &recordingApprover{allow: false}
	la := NewLayeredApprover(fallback, []string{"notes_get"}, nil)
	if !la.ApproveToolCall("notes_get", []byte(`{"note":"x"}`)) {
		t.Error("notes_get in builtin set should be approved")
	}
	if fallback.wasCalled() {
		t.Error("builtin-allow must short-circuit; fallback was consulted")
	}
}

func TestLayeredApprover_NonBuiltinDelegates(t *testing.T) {
	fallback := &recordingApprover{allow: true}
	la := NewLayeredApprover(fallback, []string{"notes_get"}, nil)
	if !la.ApproveToolCall("bash", []byte(`{"cmd":"ls"}`)) {
		t.Error("non-builtin should delegate to fallback (which allows)")
	}
	if !fallback.wasCalled() {
		t.Error("fallback should have been consulted for non-builtin")
	}
}

func TestLayeredApprover_NonBuiltinFallbackDenyReturnsFalse(t *testing.T) {
	fallback := &recordingApprover{allow: false}
	la := NewLayeredApprover(fallback, []string{"notes_get"}, nil)
	if la.ApproveToolCall("bash", []byte(`{}`)) {
		t.Error("fallback deny should propagate")
	}
}

func TestLayeredApprover_AuditCapturesReason(t *testing.T) {
	fallback := &recordingApprover{allow: true}
	sink := &recordingAuditSink{}
	la := NewLayeredApprover(fallback, []string{"notes_get"}, sink)

	la.ApproveToolCall("notes_get", []byte(`{}`))
	la.ApproveToolCall("bash", []byte(`{}`))
	fallback.allow = false
	la.ApproveToolCall("write", []byte(`{}`))

	got := sink.snapshot()
	if len(got) != 3 {
		t.Fatalf("audit count: want 3 got %d", len(got))
	}
	if got[0].Reason != ReasonBuiltinAllow {
		t.Errorf("audit[0].Reason = %v", got[0].Reason)
	}
	if got[1].Reason != ReasonSessionAllow {
		t.Errorf("audit[1].Reason = %v", got[1].Reason)
	}
	if got[2].Reason != ReasonSessionDeny || got[2].Allowed {
		t.Errorf("audit[2]: %+v", got[2])
	}
}

func TestLayeredApprover_NilFallbackDefaultsToAuto(t *testing.T) {
	la := NewLayeredApprover(nil, []string{"notes_get"}, nil)
	if !la.ApproveToolCall("anything-not-in-the-list", []byte(`{}`)) {
		t.Error("nil fallback should default to AutoApprover (always allow)")
	}
}

func TestLayeredApprover_BuiltinAllowListSortedSnapshot(t *testing.T) {
	la := NewLayeredApprover(AutoApprover{}, []string{"z", "a", "m"}, nil)
	got := la.BuiltinAllowList()
	want := []string{"a", "m", "z"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("BuiltinAllowList()[%d]: want %q got %q", i, want[i], got[i])
		}
	}
}

func TestLayeredApprover_TrustWorkspaceLifecycle(t *testing.T) {
	la := NewLayeredApprover(AutoApprover{}, nil, nil)
	if la.IsWorkspaceTrusted("/foo") {
		t.Error("fresh approver should not trust anything")
	}
	la.TrustWorkspace("/foo")
	la.TrustWorkspace("/bar")
	if !la.IsWorkspaceTrusted("/foo") || !la.IsWorkspaceTrusted("/bar") {
		t.Error("TrustWorkspace did not persist")
	}
	got := la.TrustedWorkspaces()
	want := []string{"/bar", "/foo"} // sorted
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("TrustedWorkspaces()[%d]: want %q got %q", i, want[i], got[i])
		}
	}
	la.UntrustWorkspace("/foo")
	if la.IsWorkspaceTrusted("/foo") {
		t.Error("UntrustWorkspace did not remove")
	}
}

func TestLayeredApprover_TrustEmptyIsNoop(t *testing.T) {
	la := NewLayeredApprover(AutoApprover{}, nil, nil)
	la.TrustWorkspace("")
	if len(la.TrustedWorkspaces()) != 0 {
		t.Error("empty trust string should be a no-op")
	}
}

func TestDefaultBuiltinAllow_ContainsReadOnlyBuiltins(t *testing.T) {
	required := []string{
		"notes_search", "notes_get", "notes_neighbors", "notes_recent",
		"notes_resolve", "notes_backlinks", "notes_tagged",
		"read", "grep", "glob", "ls",
		"git_status", "git_diff", "git_log", "git_blame", "git_show",
	}
	have := map[string]bool{}
	for _, n := range DefaultBuiltinAllow {
		have[n] = true
	}
	for _, n := range required {
		if !have[n] {
			t.Errorf("DefaultBuiltinAllow missing %q", n)
		}
	}
}

func TestDefaultBuiltinAllow_DoesNotContainWriteTools(t *testing.T) {
	mustNotInclude := []string{"bash", "edit", "write", "http_request"}
	have := map[string]bool{}
	for _, n := range DefaultBuiltinAllow {
		have[n] = true
	}
	for _, n := range mustNotInclude {
		if have[n] {
			t.Errorf("DefaultBuiltinAllow MUST NOT include %q — write-class tool", n)
		}
	}
}

func TestExtractInputField(t *testing.T) {
	cases := map[string]struct {
		input []byte
		field string
		want  string
	}{
		"present":   {[]byte(`{"path":"/a/b"}`), "path", "/a/b"},
		"trim":      {[]byte(`{"path":"  /x  "}`), "path", "/x"},
		"missing":   {[]byte(`{"path":"/a"}`), "cmd", ""},
		"non-str":   {[]byte(`{"n":42}`), "n", ""},
		"empty":     {nil, "path", ""},
		"bad-json":  {[]byte(`not json`), "path", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := extractInputField(tc.input, tc.field); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSortStrings(t *testing.T) {
	in := []string{"banana", "apple", "cherry", ""}
	sortStrings(in)
	want := []string{"", "apple", "banana", "cherry"}
	for i := range want {
		if in[i] != want[i] {
			t.Errorf("sortStrings[%d]: want %q got %q", i, want[i], in[i])
		}
	}
}
