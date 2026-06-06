package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/usershell"
)

func snap(id, cmd string, st usershell.State, bg bool) usershell.Snapshot {
	return usershell.Snapshot{
		ID:           id,
		Command:      cmd,
		State:        st,
		Backgrounded: bg,
		SubmittedAt:  time.Now().UTC(),
		StartedAt:    time.Now().UTC().Add(-5 * time.Second),
	}
}

func TestBuildJobsRows_SectionsInOrder(t *testing.T) {
	jobs := []usershell.Snapshot{
		snap("01HDONE", "true", usershell.StateDone, false),
		snap("01HBG", "tail", usershell.StateRunning, true),
		snap("01HQ", "next", usershell.StatePending, false),
		snap("01HFG", "vim", usershell.StateRunning, false),
	}
	rows := buildJobsRows(jobs, "")
	if len(rows) != 4 {
		t.Fatalf("rows: want 4 got %d", len(rows))
	}
	wantSections := []jobsSection{
		jobsSectionRunning,
		jobsSectionQueued,
		jobsSectionBackground,
		jobsSectionRecent,
	}
	for i, want := range wantSections {
		if rows[i].section != want {
			t.Errorf("rows[%d].section = %v want %v", i, rows[i].section, want)
		}
	}
}

func TestBuildJobsRows_FilterSubstring(t *testing.T) {
	jobs := []usershell.Snapshot{
		snap("01HVIM", "vim file.go", usershell.StateRunning, false),
		snap("01HTAIL", "tail -f /tmp", usershell.StateRunning, true),
		snap("01HCAR", "cargo test", usershell.StateDone, false),
	}
	rows := buildJobsRows(jobs, "cargo")
	if len(rows) != 1 || rows[0].snap.ID != "01HCAR" {
		t.Errorf("filter cargo: %+v", rows)
	}
	rows = buildJobsRows(jobs, "VIM") // case-insensitive
	if len(rows) != 1 || rows[0].snap.ID != "01HVIM" {
		t.Errorf("filter VIM: %+v", rows)
	}
}

func TestJobsSection_String(t *testing.T) {
	cases := map[jobsSection]string{
		jobsSectionRunning:    "Running",
		jobsSectionQueued:     "Queued",
		jobsSectionBackground: "Background",
		jobsSectionRecent:     "Recent",
		jobsSection(99):       "",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("section %v string: got %q want %q", s, got, want)
		}
	}
}

func TestFormatRowDuration(t *testing.T) {
	s := snap("x", "y", usershell.StateRunning, false)
	if got := formatRowDuration(s); !strings.HasPrefix(got, "running ") {
		t.Errorf("running prefix: %q", got)
	}
	p := snap("x", "y", usershell.StatePending, false)
	if got := formatRowDuration(p); got != "queued" {
		t.Errorf("pending: %q", got)
	}
	d := snap("x", "y", usershell.StateDone, false)
	d.StartedAt = time.Now().Add(-2 * time.Second)
	d.EndedAt = time.Now()
	if got := formatRowDuration(d); !strings.HasSuffix(got, "s") {
		t.Errorf("terminal: %q", got)
	}
}

func TestRenderJobsOverlay_EmptyHasCallToAction(t *testing.T) {
	out := renderJobsOverlay(nil, "", false, 0, 80)
	if !strings.Contains(out, "no jobs") {
		t.Errorf("empty CTA missing: %s", out)
	}
	if !strings.Contains(out, "!<cmd>") {
		t.Errorf("empty CTA hint missing: %s", out)
	}
}

func TestRenderJobsOverlay_NoMatchesHint(t *testing.T) {
	jobs := []usershell.Snapshot{
		snap("01H", "vim", usershell.StateRunning, false),
	}
	out := renderJobsOverlay(jobs, "no-such", false, 0, 80)
	if !strings.Contains(out, "no matches") {
		t.Errorf("no-matches hint missing: %s", out)
	}
}

func TestRenderJobsOverlay_HasFooter(t *testing.T) {
	jobs := []usershell.Snapshot{
		snap("01H", "vim", usershell.StateRunning, false),
	}
	out := renderJobsOverlay(jobs, "", false, 0, 80)
	for _, want := range []string{"enter", "esc", "/", "↑/↓"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q: %s", want, out)
		}
	}
}

func TestRenderJobsOverlay_SectionsHeaderPresent(t *testing.T) {
	jobs := []usershell.Snapshot{
		snap("01HFG", "vim", usershell.StateRunning, false),
		snap("01HBG", "tail", usershell.StateRunning, true),
	}
	out := renderJobsOverlay(jobs, "", false, 0, 80)
	if !strings.Contains(out, "Running") {
		t.Errorf("Running header missing: %s", out)
	}
	if !strings.Contains(out, "Background") {
		t.Errorf("Background header missing: %s", out)
	}
}
