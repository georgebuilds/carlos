package skillwire_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/skills/skillwire"
)

// fakeInducer is a scripted ResearchInducer: it returns the configured
// proposal/error and records the transcript + existing descriptions it
// was handed so tests can assert the adapter composed the call right.
type fakeInducer struct {
	proposal *skills.Proposal
	err      error

	calls          int
	lastTranscript string
	lastExisting   []string
	lastInduced    []string
}

func (f *fakeInducer) Induce(_ context.Context, transcript string, existing []string, opts skills.InducerOptions) (*skills.Proposal, error) {
	f.calls++
	f.lastTranscript = transcript
	f.lastExisting = existing
	f.lastInduced = opts.InducedFrom
	return f.proposal, f.err
}

func goodResearchReport() *research.Report {
	return &research.Report{
		Question: "How widely is WebGPU supported across browsers in 2026?",
		Query: research.Query{
			Sub: []string{"which browsers ship it?", "what versions first shipped?"},
		},
		Sources: []research.Source{
			{ID: "s1", URL: "https://developer.mozilla.org/x"},
			{ID: "s2", URL: "https://en.wikipedia.org/wiki/WebGPU"},
		},
		Synthesis: strings.Repeat("WebGPU reached broad availability by 2026. ", 6),
	}
}

func collectDiag() (func(string), *[]string) {
	var msgs []string
	return func(m string) { msgs = append(msgs, m) }, &msgs
}

// Happy path: the inducer returns a proposal; the adapter queues it
// through the existing approval pipeline (skill_proposal artifact +
// pending approval), attributed to the research session's agent ID.
func TestResearchProposer_QueuesProposal(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "r-agent")

	fi := &fakeInducer{proposal: &skills.Proposal{
		Name:        "research-webgpu-compat",
		Description: "Use when researching browser feature support timelines",
		Body:        "1. decompose by browser\n2. cross-check release notes\n",
	}}
	diag, msgs := collectDiag()
	p := skillwire.NewResearchSkillProposer(log, nil, "model-x", []string{"Use when existing"}, diag)
	p.Inducer = fi

	p.ProposeFromResearch(ctx, "r-agent", goodResearchReport())

	if fi.calls != 1 {
		t.Fatalf("inducer calls = %d want 1", fi.calls)
	}
	if !strings.Contains(fi.lastTranscript, "Research session summary") {
		t.Errorf("transcript should be the session summary, got %q", fi.lastTranscript)
	}
	if len(fi.lastExisting) != 1 || fi.lastExisting[0] != "Use when existing" {
		t.Errorf("existing descriptions not forwarded: %v", fi.lastExisting)
	}
	if len(fi.lastInduced) != 1 || fi.lastInduced[0] != "r-agent" {
		t.Errorf("InducedFrom lineage not set to agentID: %v", fi.lastInduced)
	}

	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 queued approval, got %d", len(pending))
	}
	if pending[0].Title != "skill: research-webgpu-compat" {
		t.Errorf("queued title = %q", pending[0].Title)
	}
	if !diagContains(*msgs, "queued") {
		t.Errorf("expected a 'queued' diagnostic, got %v", *msgs)
	}
}

// Inducer returns (nil, nil): the run was not a reusable procedure. The
// adapter swallows it to a diagnostic and queues nothing.
func TestResearchProposer_NotReusable(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "r-agent")

	fi := &fakeInducer{proposal: nil, err: nil}
	diag, msgs := collectDiag()
	p := skillwire.NewResearchSkillProposer(log, nil, "", nil, diag)
	p.Inducer = fi

	p.ProposeFromResearch(ctx, "r-agent", goodResearchReport())

	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("nothing should queue when the run is not reusable, got %d", len(pending))
	}
	if !diagContains(*msgs, "not reusable") {
		t.Errorf("expected 'not reusable' diagnostic, got %v", *msgs)
	}
}

// Inducer returns an error: the adapter swallows it to a diagnostic and
// never panics or queues anything. (Best-effort contract.)
func TestResearchProposer_InducerError(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "r-agent")

	fi := &fakeInducer{err: errors.New("provider exploded")}
	diag, msgs := collectDiag()
	p := skillwire.NewResearchSkillProposer(log, nil, "", nil, diag)
	p.Inducer = fi

	p.ProposeFromResearch(ctx, "r-agent", goodResearchReport())

	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("inducer error should queue nothing, got %d", len(pending))
	}
	if !diagContains(*msgs, "induction error") {
		t.Errorf("expected 'induction error' diagnostic, got %v", *msgs)
	}
}

// Queue error: the inducer returns a proposal but the queue write
// fails (the agent ID has no agents row, so the artifacts foreign key
// rejects it). The adapter swallows the error to a diagnostic and never
// propagates it. (Best-effort contract.)
func TestResearchProposer_QueueError(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	// Deliberately do NOT seed "ghost-agent": WriteArtifact's FK fails.

	fi := &fakeInducer{proposal: &skills.Proposal{
		Name:        "doomed-skill",
		Description: "Use when the queue write is going to fail",
		Body:        "step\n",
	}}
	diag, msgs := collectDiag()
	p := skillwire.NewResearchSkillProposer(log, nil, "", nil, diag)
	p.Inducer = fi

	p.ProposeFromResearch(ctx, "ghost-agent", goodResearchReport())

	if !diagContains(*msgs, "queue error") {
		t.Errorf("expected a swallowed 'queue error' diagnostic, got %v", *msgs)
	}
	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a failed queue write should leave nothing pending, got %d", len(pending))
	}
}

// Guard rails: nil receiver / missing dependencies / missing inputs are
// all no-ops that never panic.
func TestResearchProposer_GuardRails(t *testing.T) {
	ctx := context.Background()
	report := goodResearchReport()

	t.Run("nil receiver", func(t *testing.T) {
		var p *skillwire.ResearchSkillProposer
		p.ProposeFromResearch(ctx, "r", report) // must not panic
	})

	t.Run("nil log", func(t *testing.T) {
		diag, msgs := collectDiag()
		p := &skillwire.ResearchSkillProposer{Inducer: &fakeInducer{}, Diag: diag}
		p.ProposeFromResearch(ctx, "r", report)
		if !diagContains(*msgs, "not fully wired") {
			t.Errorf("want 'not fully wired' diag, got %v", *msgs)
		}
	})

	t.Run("nil inducer", func(t *testing.T) {
		log := newLog(t)
		diag, msgs := collectDiag()
		p := &skillwire.ResearchSkillProposer{Log: log, Diag: diag}
		p.ProposeFromResearch(ctx, "r", report)
		if !diagContains(*msgs, "not fully wired") {
			t.Errorf("want 'not fully wired' diag, got %v", *msgs)
		}
	})

	t.Run("empty agentID", func(t *testing.T) {
		log := newLog(t)
		fi := &fakeInducer{}
		diag, msgs := collectDiag()
		p := &skillwire.ResearchSkillProposer{Log: log, Inducer: fi, Diag: diag}
		p.ProposeFromResearch(ctx, "", report)
		if fi.calls != 0 {
			t.Error("empty agentID should short-circuit before the inducer")
		}
		if !diagContains(*msgs, "missing agentID") {
			t.Errorf("want 'missing agentID' diag, got %v", *msgs)
		}
	})

	t.Run("nil report", func(t *testing.T) {
		log := newLog(t)
		fi := &fakeInducer{}
		p := &skillwire.ResearchSkillProposer{Log: log, Inducer: fi}
		p.ProposeFromResearch(ctx, "r", nil) // must not panic; no diag sink set
		if fi.calls != 0 {
			t.Error("nil report should short-circuit before the inducer")
		}
	})
}

func diagContains(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}
