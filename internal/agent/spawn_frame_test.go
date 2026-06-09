// Phase F-12 (Fix 4): subagent frame inheritance contract tests.
//
// The frames audit (2026-06-09 frames integration audit.md, §"Subagent
// frame inheritance not audited") flagged that a parent agent running
// in frame X could potentially bypass the cross-frame WRITE prompt by
// delegating the write to a subagent. Before the fix the supervisor's
// runChild hardcoded AutoApprover{} for every child, so a child's
// write/edit calls never hit the LayeredApprover.crossFrameTarget
// detector at all - the parent could ask the child to write into frame
// Y's subtree and the user would never see the prompt.
//
// These tests pin the post-fix contract:
//
//  1. When the supervisor has a sub-agent approver wired (a
//     LayeredApprover with active frame + subtree map), a spawned
//     child's write into a non-active frame's subtree triggers the
//     fallback approver AND records the cross-frame audit reason.
//  2. The same child writing into its own (active) frame's subtree does
//     NOT consult the fallback - the builtin allow short-circuits.
//  3. SetSubAgentApprover / SubAgentApprover round-trip cleanly,
//     including the nil-clear case that restores AutoApprover.
//  4. The fallback approver decision propagates - a deny on a
//     cross-frame write produces "(rejected by user)" in the
//     tool_result the child sees.
//
// The test provider scripts a single write tool_use turn (turn 1) and
// an end_turn turn (turn 2). The frame map shape mirrors what cmd/carlos
// installs in production (frame.PathsFor(home,name).Root).
package agent_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// frameWriteProvider scripts exactly one tool_use(write) turn followed
// by one end_turn turn. The write's path field is parameterised so the
// same provider can target either the active or a foreign frame's
// subtree depending on the test.
type frameWriteProvider struct {
	mu         sync.Mutex
	writePath  string
	turn       int
	lastResult []byte
}

func (*frameWriteProvider) Name() string                         { return "frame-write" }
func (*frameWriteProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (p *frameWriteProvider) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.turn++
	turn := p.turn
	// Capture the most recent tool_result block so a test can confirm
	// the child observed the approver's "(rejected by user)" string on
	// a deny path. The last message is always the user-role tool_result
	// reply (after turn 1 fires).
	if turn > 1 && len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1]
		for _, b := range last.Content {
			if b.Kind == "tool_result" {
				p.lastResult = append([]byte{}, b.ToolResult...)
			}
		}
	}
	p.mu.Unlock()
	ch := make(chan providers.Event, 4)
	go func() {
		defer close(ch)
		if turn == 1 {
			ch <- providers.Event{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tu-write-1", Name: "write"}}
			input := []byte(`{"path":"` + p.writePath + `","content":"hi"}`)
			ch <- providers.Event{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tu-write-1", Name: "write", Input: input}}
			ch <- providers.Event{Kind: providers.EventStopReason, Stop: "tool_use"}
			return
		}
		ch <- providers.Event{Kind: providers.EventTextDelta, Text: "done"}
		ch <- providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"}
	}()
	return ch, nil
}

// fakeWriteTool is a no-op stand-in for the real internal/tools.write
// tool. We only care that the approver gates the call - the test never
// inspects the tool's side effect, so Execute is a stub that returns a
// constant byte slice.
type fakeWriteTool struct{}

func (fakeWriteTool) Name() string                                      { return "write" }
func (fakeWriteTool) Description() string                               { return "fake write" }
func (fakeWriteTool) Schema() []byte                                    { return []byte(`{}`) }
func (fakeWriteTool) Execute(_ context.Context, in []byte) ([]byte, error) { return []byte(`{"ok":true}`), nil }

// recordingApproverTSF (test-side fake) captures every ApproveToolCall
// invocation so the test can assert (a) it was called (i.e. the builtin
// allow short-circuit was bypassed by the cross-frame detector) and
// (b) the configured decision propagated.
type recordingApproverTSF struct {
	mu       sync.Mutex
	allow    bool
	called   bool
	lastTool string
	lastIn   []byte
}

func (r *recordingApproverTSF) ApproveToolCall(name string, input []byte) bool {
	r.mu.Lock()
	r.called = true
	r.lastTool = name
	r.lastIn = append([]byte{}, input...)
	r.mu.Unlock()
	return r.allow
}

func (r *recordingApproverTSF) wasCalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// recordingAuditTSF mirrors policy_test's recordingAuditSink but lives
// in the external _test package alongside the spawn frame tests.
type recordingAuditTSF struct {
	mu  sync.Mutex
	dec []agent.Decision
}

func (s *recordingAuditTSF) RecordDecision(d agent.Decision) {
	s.mu.Lock()
	s.dec = append(s.dec, d)
	s.mu.Unlock()
}

func (s *recordingAuditTSF) snapshot() []agent.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agent.Decision, len(s.dec))
	copy(out, s.dec)
	return out
}

// setupFrameSpawn builds a supervisor whose sub-agent approver is a
// LayeredApprover wired with active frame "work" and a two-frame
// subtree map {personal: <tmp>/personal, work: <tmp>/work}. Returns
// the supervisor, the recording fallback, the audit sink, and the two
// frame roots.
func setupFrameSpawn(t *testing.T, dbDir string, fallbackAllow bool) (*agent.Supervisor, *recordingApproverTSF, *recordingAuditTSF, string, string) {
	t.Helper()
	personalRoot := filepath.Join(dbDir, "frames", "personal")
	workRoot := filepath.Join(dbDir, "frames", "work")
	rec := &recordingApproverTSF{allow: fallbackAllow}
	sink := &recordingAuditTSF{}
	// "write" goes into the builtin allow set on purpose: that mirrors
	// production (notes_write is builtin-allowed, write is NOT in
	// DefaultBuiltinAllow). For this test we add "write" to confirm the
	// cross-frame detector bypasses the builtin shortcut just like
	// production does for notes_write's write-class siblings.
	layered := agent.NewLayeredApprover(rec, []string{"write"}, sink)
	layered.SetFrameSubtrees("work", map[string]string{
		"personal": personalRoot,
		"work":     workRoot,
	})
	log, err := agent.OpenStateDB(filepath.Join(dbDir, "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { agent.CloseStateDB(log) })
	base := tools.NewRegistry()
	base.Register(fakeWriteTool{})
	sup := agent.NewSupervisor(log, &frameWriteProvider{writePath: "placeholder"}, base)
	t.Cleanup(sup.Shutdown)
	sup.SetSubAgentApprover(layered)
	return sup, rec, sink, personalRoot, workRoot
}

// TestSpawn_FrameInheritance_CrossFrameWriteTripsPrompt is the headline
// regression test: a parent whose active frame is "work" spawns a child
// that attempts to write into "personal"'s subtree. The supervisor's
// installed sub-agent approver MUST be consulted (the LayeredApprover's
// cross-frame detector bypasses the builtin allow shortcut) AND must
// record ReasonCrossFrameAllow. Without the Fix 4 plumbing the child
// runs under AutoApprover and the audit sink stays empty.
func TestSpawn_FrameInheritance_CrossFrameWriteTripsPrompt(t *testing.T) {
	dir := t.TempDir()
	sup, rec, sink, personalRoot, _ := setupFrameSpawn(t, dir, true /* fallback allows */)
	// Re-wire provider with the target path now that we know the root.
	target := filepath.Join(personalRoot, "notes", "a.md")
	prov := &frameWriteProvider{writePath: target}
	// Swap the supervisor's provider by spawning with an override.
	contract := agent.SpawnContract{
		Objective:        "cross-frame write",
		ToolAllowlist:    []string{"write"},
		MaxTurns:         4,
		OverrideProvider: prov,
	}
	_, ch, err := sup.Spawn(context.Background(), "", contract)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out")
	}
	if !rec.wasCalled() {
		t.Fatalf("cross-frame write MUST consult the parent approver's fallback; never called")
	}
	if rec.lastTool != "write" {
		t.Errorf("fallback saw tool=%q, want write", rec.lastTool)
	}
	decisions := sink.snapshot()
	if len(decisions) == 0 {
		t.Fatalf("expected at least one audited decision; got none")
	}
	// Walk decisions to find the write one - the child may also emit
	// approvals for non-write tools the future enriches. Today only the
	// write is gated, so the slice is length 1.
	var found bool
	for _, d := range decisions {
		if d.Tool != "write" {
			continue
		}
		found = true
		if d.Reason != agent.ReasonCrossFrameAllow {
			t.Errorf("cross-frame write audit reason = %v, want ReasonCrossFrameAllow", d.Reason)
		}
		if !d.Allowed {
			t.Errorf("fallback returned allow=true; decision.Allowed should mirror that")
		}
	}
	if !found {
		t.Errorf("no write decision in audit log; got %+v", decisions)
	}
}

// TestSpawn_FrameInheritance_IntraFrameWriteSkipsPrompt is the
// counterpart: a child writing into the active frame's own subtree
// (here "work") MUST short-circuit via the builtin allow and never hit
// the fallback. This pins the contract that the cross-frame detector
// only fires for foreign-subtree targets - intra-frame writes are
// silent (the user already opted into that frame).
func TestSpawn_FrameInheritance_IntraFrameWriteSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	sup, rec, sink, _, workRoot := setupFrameSpawn(t, dir, true /* fallback allows; irrelevant */)
	target := filepath.Join(workRoot, "notes", "a.md")
	prov := &frameWriteProvider{writePath: target}
	contract := agent.SpawnContract{
		Objective:        "intra-frame write",
		ToolAllowlist:    []string{"write"},
		MaxTurns:         4,
		OverrideProvider: prov,
	}
	_, ch, err := sup.Spawn(context.Background(), "", contract)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out")
	}
	if rec.wasCalled() {
		t.Errorf("intra-frame write must NOT consult the fallback; called with tool=%q", rec.lastTool)
	}
	decisions := sink.snapshot()
	var found bool
	for _, d := range decisions {
		if d.Tool != "write" {
			continue
		}
		found = true
		if d.Reason != agent.ReasonBuiltinAllow {
			t.Errorf("intra-frame write audit reason = %v, want ReasonBuiltinAllow", d.Reason)
		}
	}
	if !found {
		t.Errorf("no write decision in audit log; got %+v", decisions)
	}
}

// TestSpawn_FrameInheritance_FallbackDenyPropagates closes the loop:
// when the parent approver's fallback denies a cross-frame write, the
// child's loop must receive "(rejected by user)" as the tool_result
// just as the parent would. This pins the deny path so a future
// refactor can't accidentally route the child's approval result
// somewhere other than the standard loop path.
func TestSpawn_FrameInheritance_FallbackDenyPropagates(t *testing.T) {
	dir := t.TempDir()
	sup, _, sink, personalRoot, _ := setupFrameSpawn(t, dir, false /* fallback denies */)
	target := filepath.Join(personalRoot, "notes", "a.md")
	prov := &frameWriteProvider{writePath: target}
	contract := agent.SpawnContract{
		Objective:        "cross-frame write that gets denied",
		ToolAllowlist:    []string{"write"},
		MaxTurns:         4,
		OverrideProvider: prov,
	}
	_, ch, err := sup.Spawn(context.Background(), "", contract)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out")
	}
	decisions := sink.snapshot()
	var found bool
	for _, d := range decisions {
		if d.Tool != "write" {
			continue
		}
		found = true
		if d.Reason != agent.ReasonCrossFrameDeny {
			t.Errorf("deny audit reason = %v, want ReasonCrossFrameDeny", d.Reason)
		}
		if d.Allowed {
			t.Errorf("deny decision should not be Allowed=true")
		}
	}
	if !found {
		t.Errorf("no write decision in audit log; got %+v", decisions)
	}
	// Provider's second-turn capture: the user-role tool_result that
	// followed the denied write should carry the "(rejected by user)"
	// string the loop synthesises on deny.
	prov.mu.Lock()
	defer prov.mu.Unlock()
	if string(prov.lastResult) != "(rejected by user)" {
		t.Errorf("child tool_result = %q, want \"(rejected by user)\"", string(prov.lastResult))
	}
}

// TestSupervisor_SubAgentApprover_RoundTrips locks in the
// Set/SubAgentApprover plumbing, including the nil-clear case that
// restores AutoApprover for tests and headless callers without a
// layered approver.
func TestSupervisor_SubAgentApprover_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	log, err := agent.OpenStateDB(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer agent.CloseStateDB(log)
	sup := agent.NewSupervisor(log, scriptedTextProvider{text: "ok"}, nil)
	defer sup.Shutdown()

	if sup.SubAgentApprover() != nil {
		t.Errorf("default sub-agent approver should be nil (falls back to AutoApprover)")
	}

	rec := &recordingApproverTSF{allow: true}
	sup.SetSubAgentApprover(rec)
	if got := sup.SubAgentApprover(); got != rec {
		t.Errorf("SubAgentApprover returned %v, want %v", got, rec)
	}

	sup.SetSubAgentApprover(nil)
	if sup.SubAgentApprover() != nil {
		t.Errorf("nil clear failed; SubAgentApprover = %v", sup.SubAgentApprover())
	}
}
