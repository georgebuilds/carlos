package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"

	"github.com/georgebuilds/carlos/internal/usershell"
)

// S9: golden snapshots of the three user-shell TUI surfaces (jobs
// overlay, composer footer, transcript block) across the three target
// terminal sizes. These are pure render functions, so the snapshots are
// deterministic without spinning a bubbletea program - normalizeShellGolden
// strips ANSI color (profile-independent) and the one volatile token (a
// running job's live duration) before comparison.
//
// Regenerate with: go test ./internal/tui/chat/ -run TestUserShellGolden -update
//
// Terminal sizes are expressed as W×H for traceability with the roadmap
// S9 line; these renderers are width-driven (they don't clip vertically),
// so height is carried in the subtest name only.
var shellGoldenSizes = []struct {
	name string
	w    int
}{
	{"80x24", 80},
	{"120x40", 120},
	{"200x60", 200},
}

// fixedTime is an arbitrary frozen clock so terminal-job durations
// (EndedAt - StartedAt) render identically every run.
var fixedTime = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

// normalizeShellGolden strips color escapes (via the package's shared
// stripANSI) so the goldens are independent of lipgloss's process-global
// color profile - a sibling test (imagepaste_test.go) flips it to ANSI,
// and that state can leak across tests in one binary.
func normalizeShellGolden(s string) string {
	return stripANSI(s)
}

// fixtureRoster is a DETERMINISTIC overlay roster: a queued job plus the
// three recent terminal states (done / failed / cancelled), all with
// frozen timestamps. Running/background jobs are deliberately excluded -
// their row shows a live time.Since duration whose text length shifts the
// width-padded box border between runs, which no post-processing can pin.
// The running + background sections are covered by the substring-tolerant
// TestUserShellOverlay_LiveSections below instead.
func fixtureRoster() []usershell.Snapshot {
	start := fixedTime
	end := start.Add(2*time.Second + 300*time.Millisecond)
	return []usershell.Snapshot{
		{ID: "01QUEUED00000000000000003", Command: "go build ./...", State: usershell.StatePending, SubmittedAt: start},
		{ID: "01DONE0000000000000000004", Command: "go test ./...", State: usershell.StateDone, ExitCode: 0, StartedAt: start, EndedAt: end},
		{ID: "01FAIL0000000000000000005", Command: "make lint", State: usershell.StateFailed, ExitCode: 1, StartedAt: start, EndedAt: end},
		{ID: "01CANCEL00000000000000006", Command: "sleep 999", State: usershell.StateCancelled, ExitCode: -1, StartedAt: start, EndedAt: end},
	}
}

func TestUserShellGolden_JobsOverlay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	roster := fixtureRoster()
	for _, sz := range shellGoldenSizes {
		t.Run(sz.name, func(t *testing.T) {
			out := normalizeShellGolden(renderJobsOverlay(roster, "", false, 0, sz.w))
			golden.RequireEqual(t, []byte(out))
		})
	}
}

func TestUserShellGolden_JobsOverlayFilterMode(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	roster := fixtureRoster()
	for _, sz := range shellGoldenSizes {
		t.Run(sz.name, func(t *testing.T) {
			// cursor on the 2nd row + an active filter caret.
			out := normalizeShellGolden(renderJobsOverlay(roster, "go", true, 1, sz.w))
			golden.RequireEqual(t, []byte(out))
		})
	}
}

func TestUserShellGolden_JobsOverlayEmpty(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, sz := range shellGoldenSizes {
		t.Run(sz.name, func(t *testing.T) {
			out := normalizeShellGolden(renderJobsOverlay(nil, "", false, 0, sz.w))
			golden.RequireEqual(t, []byte(out))
		})
	}
}

func TestUserShellGolden_Footer(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Deterministic footer states only (the fg-running state shows a
	// live elapsed time; it's covered by the non-golden behavior tests).
	states := []struct {
		name string
		ctx  userShellFooterContext
	}{
		{"typing_prefix_only", userShellFooterContext{state: userShellFooterTypingShell, input: "!", hasShellMgr: true}},
		{"typing_command", userShellFooterContext{state: userShellFooterTypingShell, input: "!go test", hasShellMgr: true}},
		{"bg_only", userShellFooterContext{state: userShellFooterBgOnly, bgCount: 2, hasShellMgr: true}},
	}
	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			golden.RequireEqual(t, []byte(normalizeShellGolden(renderUserShellFooter(st.ctx))))
		})
	}
}

// TestUserShellOverlay_LiveSections covers the running + background
// overlay sections, which the golden roster excludes because their live
// duration is non-deterministic. Substring assertions tolerate the
// variable duration while still pinning the section headers, glyphs, and
// per-state labels.
func TestUserShellOverlay_LiveSections(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	start := fixedTime
	roster := []usershell.Snapshot{
		{ID: "01RUNNING0000000000000001", Command: "npm run dev", State: usershell.StateRunning, StartedAt: start},
		{ID: "01BG000000000000000000002", Command: "tail -f log", State: usershell.StateRunning, Backgrounded: true, StartedAt: start},
	}
	out := stripANSI(renderJobsOverlay(roster, "", false, 0, 120))
	for _, want := range []string{"Running", "Background", "▶", "⬡", "npm run dev", "tail -f log", "running "} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q\n%s", want, out)
		}
	}
}

func TestUserShellGolden_TranscriptEntry(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	entries := map[string]transcriptEntry{
		"done": {
			shellCommand: "go test ./...", shellOutput: "ok  carlos  2.3s\n",
			shellExitCode: 0, shellDuration: 2300 * time.Millisecond,
		},
		"failed": {
			shellCommand: "make lint", shellOutput: "lint: 3 issues found\n",
			shellExitCode: 1, shellDuration: 1500 * time.Millisecond,
		},
		"cancelled": {
			shellCommand: "sleep 999", shellCancelled: true, shellDuration: 4 * time.Second,
		},
		"running_bg": {
			shellCommand: "npm run dev", shellOutput: "listening on :3000\n",
			shellRunning: true, shellBackgrounded: true,
		},
		"truncated": {
			shellCommand: "cat big.log", shellOutput: "...last lines...\n",
			shellExitCode: 0, shellTruncated: 4096, shellDuration: 1 * time.Second,
		},
	}
	for _, sz := range shellGoldenSizes {
		for _, kind := range []string{"done", "failed", "cancelled", "running_bg", "truncated"} {
			t.Run(kind+"_"+sz.name, func(t *testing.T) {
				golden.RequireEqual(t, []byte(normalizeShellGolden(renderUserShellEntry(entries[kind], sz.w))))
			})
		}
	}
}
