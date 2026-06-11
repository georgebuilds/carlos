package chat

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/usershell"
)

func TestJobsRowGlyph_AllSections(t *testing.T) {
	cases := []struct {
		name string
		row  jobsRow
		want string
	}{
		{"running", jobsRow{section: jobsSectionRunning}, "▶"},
		{"queued", jobsRow{section: jobsSectionQueued}, "▸"},
		{"background", jobsRow{section: jobsSectionBackground}, "⬡"},
		{
			"recent-cancelled",
			jobsRow{section: jobsSectionRecent, snap: usershell.Snapshot{State: usershell.StateCancelled}},
			"✗",
		},
		{
			"recent-nonzero-exit",
			jobsRow{section: jobsSectionRecent, snap: usershell.Snapshot{State: usershell.StateFailed, ExitCode: 2}},
			"✗",
		},
		{
			"recent-ok",
			jobsRow{section: jobsSectionRecent, snap: usershell.Snapshot{State: usershell.StateDone, ExitCode: 0}},
			"✓",
		},
		{"unknown", jobsRow{section: jobsSection(99)}, " "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jobsRowGlyph(c.row)
			if !strings.Contains(got, c.want) {
				t.Errorf("glyph for %s = %q want to contain %q", c.name, got, c.want)
			}
		})
	}
}

// TestRenderJobsOverlay_RecentSectionRendersBadges paints a full
// overlay with terminal-state jobs so the recent-section glyph branches
// are exercised through the render path too.
func TestRenderJobsOverlay_RecentSectionRendersBadges(t *testing.T) {
	jobs := []usershell.Snapshot{
		snap("01HOK", "true", usershell.StateDone, false),
		snap("01HFAIL", "false", usershell.StateFailed, false),
		snap("01HCANCEL", "sleep", usershell.StateCancelled, false),
	}
	jobs[1].ExitCode = 1
	out := renderJobsOverlay(jobs, "", false, 0, 80)
	if !strings.Contains(out, "Recent") {
		t.Errorf("recent section header missing; got:\n%s", out)
	}
	if !strings.Contains(out, "✓") || !strings.Contains(out, "✗") {
		t.Errorf("recent badges missing; got:\n%s", out)
	}
}
