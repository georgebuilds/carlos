package agent

import (
	"strings"
	"testing"
)

// Frames-audit §"Mid-conversation switch edge cases" pins.
// Three semantic decisions, captured here so a regression flags
// against the documented behavior:
//
//  1. Sysprompt rebuild on Ctrl+F — next turn outbound MUST carry the
//     new frame's block. swapLoop (cmd/carlos/runtime_tui.go:528) calls
//     SystemPromptWithFrame against the new frame's FrameInfo; this
//     test pins the building block.
//
//  2. Approver subtree refresh on switch — SetFrameSubtrees called by
//     swapLoop (runtime_tui.go:547-553) MUST change which paths the
//     cross-frame detector treats as foreign. Pin in policy_test.go
//     (TestSetFrameSubtrees_RefreshesActiveAfterSwap).
//
//  3. Capture-at-issue for tool calls — covered by
//     TestSnapshotAtFrame_* in policy_test.go and
//     TestLoop_HandleUserMessage_SnapshotsApprover in
//     internal/tui/chatglue/chatglue_test.go.

// Item 1 pin. Sysprompt for two different frames must differ in the
// frame name AND the SystemPromptAppend body — proves the swap path
// can't accidentally hold a stale frame block by reusing the same
// FrameInfo struct or cached output.
func TestFrameSwitchPin_SyspromptDiffersAcrossFrames(t *testing.T) {
	work := SystemPromptWithFrame("Boss", "/home/u", "", FrameInfo{
		Name:   "work",
		Append: "Tone: precise. Stakeholders matter.",
	})
	personal := SystemPromptWithFrame("Boss", "/home/u", "", FrameInfo{
		Name:   "personal",
		Append: "Tone: casual. It's just us here.",
	})
	if work == personal {
		t.Fatal("sysprompts for different frames must not be byte-identical; mid-conversation switch would feed model the wrong frame block")
	}
	if !strings.Contains(work, "Frame: work.") || !strings.Contains(work, "Stakeholders matter") {
		t.Errorf("work sysprompt missing frame name or append body:\n%s", work)
	}
	if !strings.Contains(personal, "Frame: personal.") || !strings.Contains(personal, "just us here") {
		t.Errorf("personal sysprompt missing frame name or append body:\n%s", personal)
	}
	// Cross-pollination guard: each sysprompt must NOT carry the OTHER
	// frame's append body. A bug where swapLoop reused an in-place
	// FrameInfo struct would leak the previous append text here.
	if strings.Contains(work, "just us here") {
		t.Errorf("work sysprompt leaked personal's append body:\n%s", work)
	}
	if strings.Contains(personal, "Stakeholders matter") {
		t.Errorf("personal sysprompt leaked work's append body:\n%s", personal)
	}
}

// Empty FrameInfo (legacy single-shelf mode) must NOT include any frame
// block — pins the swapLoop precondition that an unconfigured session
// degrades gracefully.
func TestFrameSwitchPin_EmptyFrameInfoOmitsBlock(t *testing.T) {
	out := SystemPromptWithFrame("Boss", "/home/u", "", FrameInfo{})
	if strings.Contains(out, "Frame:") {
		t.Errorf("empty FrameInfo must produce a frameless sysprompt; got:\n%s", out)
	}
}
