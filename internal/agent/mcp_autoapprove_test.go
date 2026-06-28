package agent

import "testing"

func TestMCPAutoApprove_AllowsConfiguredServer(t *testing.T) {
	fb := &recordingApprover{allow: false} // would deny if consulted
	l := NewLayeredApprover(fb, nil, nil)
	l.SetMCPAutoApprove(map[string]bool{"digitalocean": true})

	if !l.ApproveToolCall("digitalocean__droplet_list", []byte(`{}`)) {
		t.Error("auto-approved server tool should allow without fallback")
	}
	if fb.wasCalled() {
		t.Error("fallback must not be consulted for an auto-approved MCP tool")
	}
}

func TestMCPAutoApprove_FallsThroughForOtherServers(t *testing.T) {
	fb := &recordingApprover{allow: true}
	l := NewLayeredApprover(fb, nil, nil)
	l.SetMCPAutoApprove(map[string]bool{"digitalocean": true})

	if !l.ApproveToolCall("home-tools__do_thing", []byte(`{}`)) {
		t.Error("fallback allow=true should allow")
	}
	if !fb.wasCalled() {
		t.Error("non-auto-approved MCP server must consult the fallback")
	}
}

func TestMCPAutoApprove_BuiltinUnaffected(t *testing.T) {
	fb := &recordingApprover{allow: true}
	l := NewLayeredApprover(fb, nil, nil)
	l.SetMCPAutoApprove(map[string]bool{"digitalocean": true})
	// "bash" has no "__" so the MCP layer never matches; fallback decides.
	l.ApproveToolCall("bash", []byte(`{"cmd":"ls"}`))
	if !fb.wasCalled() {
		t.Error("built-in tool should fall through to fallback")
	}
}

func TestMCPAutoApprove_NilSnapshotDisables(t *testing.T) {
	fb := &recordingApprover{allow: false}
	l := NewLayeredApprover(fb, nil, nil)
	l.SetMCPAutoApprove(nil)
	if l.ApproveToolCall("digitalocean__x", []byte(`{}`)) {
		t.Error("no snapshot => MCP tool must defer to fallback (deny here)")
	}
	if !fb.wasCalled() {
		t.Error("fallback should be consulted when no auto-approve set")
	}
}

func TestMCPAutoApprove_AuditReason(t *testing.T) {
	audit := &recordingAuditSink{}
	l := NewLayeredApprover(&recordingApprover{}, nil, audit)
	l.SetMCPAutoApprove(map[string]bool{"srv": true})
	l.ApproveToolCall("srv__tool", []byte(`{}`))
	got := audit.snapshot()
	if len(got) != 1 || got[0].Reason != ReasonMCPAutoApprove {
		t.Errorf("audit=%+v want one ReasonMCPAutoApprove", got)
	}
}
