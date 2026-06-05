package manage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// fakeLister returns a canned pending list for the test.
func fakeLister(items []agent.PendingApproval, err error) approvalLister {
	return func(ctx context.Context) ([]agent.PendingApproval, error) {
		return items, err
	}
}

// recordingResolver captures calls so we can assert what the pane
// dispatched. The returned func records the (id, note, accept) tuple
// and returns the stored err.
type recorderCall struct {
	id     string
	note   string
	accept bool
}

func recordingResolver(err error) (approvalResolver, *[]recorderCall) {
	var calls []recorderCall
	r := func(ctx context.Context, id, note string, accept bool) error {
		calls = append(calls, recorderCall{id: id, note: note, accept: accept})
		return err
	}
	return r, &calls
}

func mkPending(id, title, kind string, agentID string, age time.Duration) agent.PendingApproval {
	return agent.PendingApproval{
		AgentID: agentID,
		Title:   title,
		Ref: agent.ArtifactRef{
			ID:      id,
			Path:    "/tmp/" + id,
			Kind:    kind,
			SHA256:  "deadbeef",
			Size:    256,
			AgentID: agentID,
		},
		ProposedAt: time.Now().Add(-age),
	}
}

func TestApprovalsPane_RendersEmptyState(t *testing.T) {
	p := approvalsPane{fetched: time.Now()}
	out := p.render(100, 20)
	if !strings.Contains(out, "no pending approvals") {
		t.Errorf("empty-state copy missing:\n%s", out)
	}
}

func TestApprovalsPane_RendersList(t *testing.T) {
	p := approvalsPane{
		fetched: time.Now(),
		pending: []agent.PendingApproval{
			mkPending("abc12345", "plan: extract helper", "plan", "agent-1", 2*time.Minute),
			mkPending("def67890", "skill: react-test-debug", "skill_proposal", "agent-2", 1*time.Hour),
		},
	}
	out := p.render(120, 30)
	for _, must := range []string{"plan: extract helper", "skill: react-test-debug", "abc12345", "def67890", "[plan]", "[skill_proposal]"} {
		if !strings.Contains(out, must) {
			t.Errorf("missing %q in render:\n%s", must, out)
		}
	}
}

func TestApprovalsPane_CursorClampsAfterFetch(t *testing.T) {
	p := approvalsPane{cursor: 99}
	p.applyFetch(fetchApprovalsMsg{
		pending: []agent.PendingApproval{mkPending("a", "x", "k", "agent", time.Minute)},
		t:       time.Now(),
	})
	if p.cursor != 0 {
		t.Errorf("cursor not clamped: %d", p.cursor)
	}
}

func TestApprovalsPane_MoveCursorRespectsBounds(t *testing.T) {
	p := approvalsPane{pending: []agent.PendingApproval{
		mkPending("a", "x", "k", "agent", time.Minute),
		mkPending("b", "y", "k", "agent", time.Minute),
	}}
	p.moveCursor(-5)
	if p.cursor != 0 {
		t.Errorf("cursor underflow: %d", p.cursor)
	}
	p.moveCursor(99)
	if p.cursor != 1 {
		t.Errorf("cursor overflow: %d", p.cursor)
	}
}

func TestApprovalsPane_FetchErrorSurfaces(t *testing.T) {
	p := approvalsPane{}
	p.applyFetch(fetchApprovalsMsg{err: errors.New("boom"), t: time.Now()})
	out := p.render(80, 20)
	if !strings.Contains(out, "boom") {
		t.Errorf("fetch error not surfaced:\n%s", out)
	}
}

func TestModel_AKeyOpensApprovalsView(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil)
	// No lister wired — should surface a status line.
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	if m.view == viewApprovals {
		t.Error("should NOT have switched view without a lister")
	}
	if !strings.Contains(m.status, "not wired") {
		t.Errorf("expected 'not wired' status, got %q", m.status)
	}

	// With a lister, switching works.
	m = New(staticSnapshot{}, nil, nil).WithApprovals(
		fakeLister([]agent.PendingApproval{
			mkPending("art-1", "test title", "plan", "agent-a", time.Minute),
		}, nil),
		nil,
	)
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	if m.view != viewApprovals {
		t.Errorf("view didn't switch to approvals: %v", m.view)
	}
}

func TestModel_EscFromApprovalsReturnsToRoster(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil).WithApprovals(
		fakeLister(nil, nil), nil,
	)
	m.view = viewApprovals
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.view != viewRoster {
		t.Error("ESC didn't return to roster view")
	}
}

func TestModel_ApprovalsView_YDispatchesAccept(t *testing.T) {
	res, calls := recordingResolver(nil)
	m := New(staticSnapshot{}, nil, nil).WithApprovals(
		fakeLister([]agent.PendingApproval{
			mkPending("art-x", "needs review", "plan", "agent-a", time.Second),
		}, nil),
		res,
	)
	m.view = viewApprovals
	m.approvals.pending = []agent.PendingApproval{
		mkPending("art-x", "needs review", "plan", "agent-a", time.Second),
	}
	_, cmd := updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("y should have returned a tea.Cmd")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("cmd returned nil msg")
	}
	if len(*calls) != 1 {
		t.Fatalf("resolver calls: want 1 got %d", len(*calls))
	}
	if (*calls)[0].id != "art-x" || (*calls)[0].accept != true {
		t.Errorf("resolver call: %+v", (*calls)[0])
	}
}

func TestModel_ApprovalsView_ROpensRejectOverlay(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil).WithApprovals(
		fakeLister(nil, nil), nil,
	)
	m.view = viewApprovals
	m.approvals.pending = []agent.PendingApproval{
		mkPending("art-y", "questionable", "plan", "agent-a", time.Second),
	}
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.overlay != overlayRejectReason {
		t.Errorf("overlay didn't open: %v", m.overlay)
	}
}

func TestModel_FooterSwapsForApprovalsView(t *testing.T) {
	m := New(staticSnapshot{}, nil, nil).WithApprovals(
		fakeLister(nil, nil), nil,
	)
	m.width, m.height = 120, 40
	rosterFooter := m.renderFooter(120)
	if !strings.Contains(rosterFooter, "steer") {
		t.Error("roster footer should mention steer")
	}
	m.view = viewApprovals
	apprFooter := m.renderFooter(120)
	if !strings.Contains(apprFooter, "accept") {
		t.Errorf("approvals footer should mention accept:\n%s", apprFooter)
	}
	if strings.Contains(apprFooter, "steer") {
		t.Error("approvals footer should NOT mention steer")
	}
}

// updateModel is the test helper that dispatches a msg through the
// model and returns the (typed) model + cmd. Same shape as the
// manage_test.go helpers (which we don't import from a test file).
func updateModel(m *Model, msg tea.Msg) (*Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(*Model), cmd
}

// staticSnapshot is the minimal SnapshotSource for tests that don't
// care about the agents projection.
type staticSnapshot struct{}

func (staticSnapshot) Snapshot(context.Context) ([]agent.AgentRow, error) {
	return nil, nil
}
