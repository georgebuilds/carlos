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

func TestSystemPromptWithFrame_IncludesVaultPathAndSubtree(t *testing.T) {
	out := SystemPromptWithFrame("", "", "", FrameInfo{
		Name:         "personal",
		VaultPath:    "/Volumes/nas/carlos-vault",
		VaultSubtree: "personal",
	})
	for _, want := range []string{"Vault: /Volumes/nas/carlos-vault", "personal"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in sysprompt; got:\n%s", want, out)
		}
	}
}

func TestSystemPromptWithFrame_OmitsVaultBlockWhenEmpty(t *testing.T) {
	out := SystemPromptWithFrame("", "", "", FrameInfo{Name: "personal"})
	if strings.Contains(out, "Vault:") {
		t.Errorf("empty VaultPath should suppress the line; got:\n%s", out)
	}
}

func TestSystemPromptWithFrame_IncludesCwdHints(t *testing.T) {
	out := SystemPromptWithFrame("", "", "", FrameInfo{
		Name:     "work",
		CwdHints: []string{"~/Code/ludus", "~/Code/work*"},
	})
	if !strings.Contains(out, "Cwd hints for this frame: ~/Code/ludus, ~/Code/work*") {
		t.Errorf("cwd_hints not rendered correctly; got:\n%s", out)
	}
}

func TestSystemPromptWithFrame_IncludesCapabilitiesSorted(t *testing.T) {
	out := SystemPromptWithFrame("", "", "", FrameInfo{
		Name: "work",
		Capabilities: map[string]string{
			"email":    "fastmail-imap",
			"calendar": "caldav",
		},
	})
	if !strings.Contains(out, "calendar=caldav, email=fastmail-imap") {
		t.Errorf("capabilities should render sorted; got:\n%s", out)
	}
}

func TestSystemPromptWithFrame_NotesWriteHintInBase(t *testing.T) {
	// The static block of chatBaseSystem mentions notes_write as the
	// preferred tool for in-frame writes. Pin the copy so a future
	// edit doesn't silently drop the hint.
	out := SystemPrompt("", "", "")
	if !strings.Contains(out, "notes_write") {
		t.Errorf("base sysprompt should mention notes_write; got:\n%s", out)
	}
	if !strings.Contains(out, "prefer notes_write over the generic write tool") {
		t.Errorf("base sysprompt should prefer notes_write for in-frame writes; got:\n%s", out)
	}
}

func TestSystemPromptWithFrame_CommitAuthorAttributionInBase(t *testing.T) {
	// The static block of chatBaseSystem instructs the model to attribute
	// commits to carlos via `git commit --author=...` so GitHub's
	// Author/Committer split renders both avatars without polluting the
	// commit body with a Co-Authored-By trailer. Pin the email, the
	// `--author=` flag form, the "no Co-Authored-By trailer" guard, and
	// the AGENTS.md / CLAUDE.md override clause so a future edit doesn't
	// silently drop any of them.
	out := SystemPrompt("", "", "")
	if !strings.Contains(out, `--author="carlos <carlos@georgebuilds.com>"`) {
		t.Errorf("base sysprompt should attribute carlos via git commit --author=; got:\n%s", out)
	}
	if !strings.Contains(out, "Do NOT add a Co-Authored-By trailer") {
		t.Errorf("base sysprompt should explicitly suppress the Co-Authored-By trailer; got:\n%s", out)
	}
	if !strings.Contains(out, "AGENTS.md / CLAUDE.md") {
		t.Errorf("base sysprompt should defer to AGENTS.md / CLAUDE.md as the override channel; got:\n%s", out)
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
