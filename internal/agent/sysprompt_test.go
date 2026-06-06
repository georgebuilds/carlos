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
