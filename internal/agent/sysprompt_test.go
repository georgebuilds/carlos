package agent

import (
	"strings"
	"testing"
)

func TestSystemPrompt_NamesCarlosAndRulesOutModelName(t *testing.T) {
	out := SystemPrompt("", "", "")
	if !strings.Contains(strings.ToLower(out), "you are carlos") {
		t.Error("system prompt must establish identity (lowercase 'you are carlos')")
	}
	if !strings.Contains(strings.ToLower(out), "never name the model") {
		t.Error("system prompt must instruct the model NOT to reveal its underlying name")
	}
}

func TestSystemPrompt_NamesEveryToolFamily(t *testing.T) {
	out := SystemPrompt("", "", "")
	for _, family := range []string{"notes_*", "obsidian_*", "bash", "git_status", "web_fetch"} {
		if !strings.Contains(out, family) {
			t.Errorf("system prompt should mention tool family %q", family)
		}
	}
}

func TestSystemPrompt_IncludesRuntimeFieldsWhenProvided(t *testing.T) {
	out := SystemPrompt("george", "/Users/george/Code/carlos", "")
	if !strings.Contains(out, "george") {
		t.Error("system prompt should include user name when provided")
	}
	if !strings.Contains(out, "/Users/george/Code/carlos") {
		t.Error("system prompt should include cwd when provided")
	}
}

func TestSystemPrompt_OmitsRuntimeWhenEmpty(t *testing.T) {
	out := SystemPrompt("", "", "")
	if strings.Contains(out, "The user is") {
		t.Error("empty userName should omit the 'user is' line")
	}
	if strings.Contains(out, "current working directory") {
		t.Error("empty cwd should omit the cwd line")
	}
}

func TestSystemPrompt_AppendsProjectContextWhenProvided(t *testing.T) {
	out := SystemPrompt("", "", "# carlos house rules\n- be concise\n")
	if !strings.Contains(out, "Project context") {
		t.Error("non-empty projectCtx should produce 'Project context' section")
	}
	if !strings.Contains(out, "carlos house rules") {
		t.Error("projectCtx body should be appended verbatim")
	}
}

func TestSystemPrompt_OmitsProjectContextWhenBlank(t *testing.T) {
	out := SystemPrompt("", "", "   \n\t  ")
	if strings.Contains(out, "Project context") {
		t.Error("whitespace-only projectCtx should not produce a section")
	}
}

func TestSystemPromptWithFrame_IncludesFrameBlockWhenNamed(t *testing.T) {
	out := SystemPromptWithFrame("", "", "", FrameInfo{
		Name:   "work",
		Append: "Tone: precise. Brief mentions of stakeholders are fine.",
	})
	if !strings.Contains(out, "Frame: work.") {
		t.Errorf("expected frame sentence, got:\n%s", out)
	}
	if !strings.Contains(out, "Tone: precise.") {
		t.Errorf("expected frame append body, got:\n%s", out)
	}
}

func TestSystemPromptWithFrame_OmitsBlockWhenNameEmpty(t *testing.T) {
	out := SystemPromptWithFrame("", "", "", FrameInfo{Append: "ignored"})
	if strings.Contains(out, "Frame:") {
		t.Errorf("empty Name should suppress the frame block; got:\n%s", out)
	}
	if strings.Contains(out, "ignored") {
		t.Errorf("empty Name should suppress the append body; got:\n%s", out)
	}
}

func TestSystemPromptWithFrame_FrameBeforeRuntime(t *testing.T) {
	// Cache stability: chatBaseSystem → Frame block → Runtime block →
	// Project context. Reordering invalidates the per-frame cache
	// boundary, so this order is asserted.
	out := SystemPromptWithFrame("george", "/tmp", "", FrameInfo{Name: "personal"})
	frameAt := strings.Index(out, "Frame: personal")
	runtimeAt := strings.Index(out, "Runtime:")
	if frameAt < 0 || runtimeAt < 0 {
		t.Fatalf("missing markers; out:\n%s", out)
	}
	if !(frameAt < runtimeAt) {
		t.Errorf("Frame block must come before Runtime block")
	}
}
