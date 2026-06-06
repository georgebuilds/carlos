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

// stubWorkspacePolicy lets layer-2 tests inject decisions without
// dragging in the internal/workspace package.
type stubWorkspacePolicy struct {
	allow      bool
	calledWith []string
}

func (s *stubWorkspacePolicy) Allows(tool string, _ []byte) bool {
	s.calledWith = append(s.calledWith, tool)
	return s.allow
}

func TestSetWorkspacePolicy_AllowBypassesFallback(t *testing.T) {
	rec := &recordingApprover{allow: false}
	audit := &recordingAuditSink{}
	la := NewLayeredApprover(rec, nil, audit)
	la.SetWorkspacePolicy(&stubWorkspacePolicy{allow: true})

	if ok := la.ApproveToolCall("bash", []byte(`{"cmd":"git status"}`)); !ok {
		t.Errorf("workspace-allow should bypass fallback")
	}
	if rec.wasCalled() {
		t.Errorf("workspace-allow should NOT consult fallback")
	}
	snap := audit.snapshot()
	if len(snap) != 1 || snap[0].Reason != ReasonWorkspaceAllow {
		t.Errorf("audit reason = %v, want ReasonWorkspaceAllow", snap)
	}
}

func TestSetWorkspacePolicy_DenyFallsThrough(t *testing.T) {
	rec := &recordingApprover{allow: false}
	la := NewLayeredApprover(rec, nil, nil)
	la.SetWorkspacePolicy(&stubWorkspacePolicy{allow: false})

	if ok := la.ApproveToolCall("bash", []byte(`{"cmd":"git push"}`)); ok {
		t.Errorf("workspace-deny + fallback-deny should not allow")
	}
	if !rec.wasCalled() {
		t.Error("workspace-deny should fall through to fallback")
	}
}

func TestSetWorkspacePolicy_NilPolicySafe(t *testing.T) {
	rec := &recordingApprover{allow: false}
	la := NewLayeredApprover(rec, nil, nil)
	la.SetWorkspacePolicy(nil)
	if ok := la.ApproveToolCall("bash", []byte(`{"cmd":"ls"}`)); ok {
		t.Errorf("nil workspace policy + denying fallback should not allow")
	}
}

func TestSetWorkspacePolicy_BuiltinStillWins(t *testing.T) {
	// Layer-1 (builtin allow) must short-circuit before layer-2
	// is consulted — same tool name shouldn't be evaluated twice.
	rec := &recordingApprover{allow: false}
	ws := &stubWorkspacePolicy{allow: false}
	la := NewLayeredApprover(rec, []string{"notes_search"}, nil)
	la.SetWorkspacePolicy(ws)
	if !la.ApproveToolCall("notes_search", []byte(`{}`)) {
		t.Error("builtin should allow without consulting workspace")
	}
	if len(ws.calledWith) != 0 {
		t.Errorf("workspace policy should NOT see builtin-allowed tools; got %v", ws.calledWith)
	}
}

// Phase F-12 cross-frame approval. The detector intercepts write/edit
// inputs whose path falls inside a non-active frame's subtree, forcing
// the fallback to run and tagging the audit log with a cross-frame
// reason. Verified across the four interesting cases below.

func setupCrossFrame(t *testing.T) (*LayeredApprover, *recordingApprover, *recordingAuditSink) {
	t.Helper()
	rec := &recordingApprover{allow: true}
	sink := &recordingAuditSink{}
	la := NewLayeredApprover(rec, []string{"write", "edit", "read"}, sink)
	la.SetFrameSubtrees("personal", map[string]string{
		"personal": "/home/u/.carlos/frames/personal",
		"work":     "/home/u/.carlos/frames/work",
	})
	return la, rec, sink
}

func TestLayered_CrossFrame_PathInActiveFrameSkipsDetector(t *testing.T) {
	la, rec, sink := setupCrossFrame(t)
	ok := la.ApproveToolCall("write", []byte(`{"path":"/home/u/.carlos/frames/personal/notes/a.md","content":"x"}`))
	if !ok {
		t.Error("active-frame write should auto-approve via the builtin allow")
	}
	if rec.wasCalled() {
		t.Error("active-frame write should not hit the fallback")
	}
	d := sink.snapshot()
	if len(d) != 1 || d[0].Reason != ReasonBuiltinAllow {
		t.Errorf("want one builtin-allow decision; got %+v", d)
	}
}

func TestLayered_CrossFrame_PathInOtherFrameForcesFallback(t *testing.T) {
	la, rec, sink := setupCrossFrame(t)
	ok := la.ApproveToolCall("write", []byte(`{"path":"/home/u/.carlos/frames/work/notes/a.md","content":"x"}`))
	if !ok {
		t.Error("recorded approver returns true; want true")
	}
	if !rec.wasCalled() {
		t.Error("cross-frame write MUST consult the fallback even when builtin allow has the tool")
	}
	d := sink.snapshot()
	if len(d) != 1 || d[0].Reason != ReasonCrossFrameAllow {
		t.Errorf("want ReasonCrossFrameAllow; got %+v", d)
	}
}

func TestLayered_CrossFrame_FallbackDenyRecordsCrossDeny(t *testing.T) {
	rec := &recordingApprover{allow: false}
	sink := &recordingAuditSink{}
	la := NewLayeredApprover(rec, []string{"write"}, sink)
	la.SetFrameSubtrees("personal", map[string]string{
		"personal": "/home/u/.carlos/frames/personal",
		"work":     "/home/u/.carlos/frames/work",
	})
	ok := la.ApproveToolCall("write", []byte(`{"path":"/home/u/.carlos/frames/work/notes/a.md","content":"x"}`))
	if ok {
		t.Error("rejected fallback should propagate")
	}
	d := sink.snapshot()
	if len(d) != 1 || d[0].Reason != ReasonCrossFrameDeny {
		t.Errorf("want ReasonCrossFrameDeny; got %+v", d)
	}
}

func TestLayered_CrossFrame_NonMutatingToolIgnored(t *testing.T) {
	la, rec, _ := setupCrossFrame(t)
	if !la.ApproveToolCall("read", []byte(`{"path":"/home/u/.carlos/frames/work/notes/a.md"}`)) {
		t.Error("read in cross-frame path should still be evaluated by other layers")
	}
	if rec.wasCalled() {
		t.Error("read isn't on the cross-frame list; fallback should not be forced")
	}
}

func TestLayered_CrossFrame_DisabledWhenNoSubtrees(t *testing.T) {
	rec := &recordingApprover{allow: true}
	sink := &recordingAuditSink{}
	la := NewLayeredApprover(rec, []string{"write"}, sink)
	// No SetFrameSubtrees call — detector stays off.
	la.ApproveToolCall("write", []byte(`{"path":"/anything/at/all.md","content":"x"}`))
	d := sink.snapshot()
	if len(d) != 1 || d[0].Reason != ReasonBuiltinAllow {
		t.Errorf("legacy single-shelf decision should be builtin-allow; got %+v", d)
	}
}

func TestLayered_CrossFrame_BoundaryGuardsAgainstPrefixCollision(t *testing.T) {
	rec := &recordingApprover{allow: true}
	sink := &recordingAuditSink{}
	la := NewLayeredApprover(rec, []string{"write"}, sink)
	la.SetFrameSubtrees("personal", map[string]string{
		"personal": "/root/a",
		"shadow":   "/root/a-extra",
	})
	// /root/a-extra/x must NOT match the personal frame (no leading sep after prefix).
	la.ApproveToolCall("write", []byte(`{"path":"/root/a-extra/x.md","content":"x"}`))
	d := sink.snapshot()
	if len(d) != 1 || d[0].Reason != ReasonCrossFrameAllow {
		t.Errorf("want ReasonCrossFrameAllow (target is shadow frame); got %+v", d)
	}
}

func TestPathInside(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/root/a/x.md", "/root/a", true},
		{"/root/a", "/root/a", true},
		{"/root/a-extra/x.md", "/root/a", false},
		{"/root/b/x.md", "/root/a", false},
		{"/elsewhere", "/root/a", false},
	}
	for _, c := range cases {
		if got := pathInside(c.path, c.root); got != c.want {
			t.Errorf("pathInside(%q,%q) = %v, want %v", c.path, c.root, got, c.want)
		}
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
