package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/skills"
)

func TestSkillUseTool_Identity(t *testing.T) {
	tool := NewSkillUseTool(nil, "personal")
	if tool.Name() != "skill_use" {
		t.Errorf("wrong tool name: %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("description must not be empty")
	}
	if len(tool.Schema()) == 0 {
		t.Error("schema must not be empty")
	}
}

func TestSkillUseTool_NilLibrary(t *testing.T) {
	tool := NewSkillUseTool(nil, "personal")
	_, err := tool.Execute(context.Background(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "no skill library") {
		t.Errorf("expected library-not-loaded error; got %v", err)
	}
}

func TestSkillUseTool_BadInputErrors(t *testing.T) {
	lib := &skills.Library{
		Active: []*skills.Skill{{Name: "calendar", Description: "d", Body: "b"}},
	}
	tool := NewSkillUseTool(lib, "personal")
	_, err := tool.Execute(context.Background(), []byte("not-json"))
	if err == nil || !strings.Contains(err.Error(), "parse input") {
		t.Errorf("expected parse error; got %v", err)
	}
}

func TestSkillUseTool_ListMode(t *testing.T) {
	lib := &skills.Library{
		Active: []*skills.Skill{
			{Name: "calendar", Description: "calendar entry point"},
			{Name: "calendar-caldav", Description: "talk to CalDAV"},
		},
	}
	tool := NewSkillUseTool(lib, "personal")
	out, err := tool.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("list mode failed: %v", err)
	}
	var resp skillUseListResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Skills) != 2 {
		t.Errorf("expected 2 skills; got %d", len(resp.Skills))
	}
	if resp.Frame != "personal" {
		t.Errorf("expected frame=personal; got %q", resp.Frame)
	}
	// Alphabetical order.
	if resp.Skills[0].Name != "calendar" || resp.Skills[1].Name != "calendar-caldav" {
		t.Errorf("expected alphabetical order; got %+v", resp.Skills)
	}
}

func TestSkillUseTool_BodyMode(t *testing.T) {
	lib := &skills.Library{
		Active: []*skills.Skill{
			{
				Name:        "calendar-caldav",
				Description: "talk to CalDAV",
				Backend:     "caldav",
				Body:        "## How to talk to CalDAV\n\nstep 1...",
			},
		},
	}
	tool := NewSkillUseTool(lib, "personal")
	out, err := tool.Execute(context.Background(), []byte(`{"name":"calendar-caldav"}`))
	if err != nil {
		t.Fatalf("body mode failed: %v", err)
	}
	var resp skillUseBodyResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Name != "calendar-caldav" {
		t.Errorf("wrong name: %q", resp.Name)
	}
	if resp.Body == "" {
		t.Error("body should not be empty")
	}
	if resp.Backend != "caldav" {
		t.Errorf("backend missing; got %q", resp.Backend)
	}
}

func TestSkillUseTool_NotFoundReturnsCatalog(t *testing.T) {
	lib := &skills.Library{
		Active: []*skills.Skill{
			{Name: "calendar", Description: "d", Body: "b"},
			{Name: "code-review", Description: "d2", Body: "b2"},
		},
	}
	tool := NewSkillUseTool(lib, "personal")
	_, err := tool.Execute(context.Background(), []byte(`{"name":"nope"}`))
	if err == nil {
		t.Fatal("expected not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		t.Errorf("expected 'not found' in error; got %q", msg)
	}
	if !strings.Contains(msg, "calendar") || !strings.Contains(msg, "code-review") {
		t.Errorf("expected available-list in error; got %q", msg)
	}
}

// TestSkillUseTool_FrameFilter pins that a skill scoped to a
// different frame is invisible to skill_use.
func TestSkillUseTool_FrameFilter(t *testing.T) {
	lib := &skills.Library{
		Active: []*skills.Skill{
			{Name: "personal-only", Description: "d", Body: "b", Frames: []string{"personal"}},
			{Name: "work-only", Description: "d", Body: "b", Frames: []string{"work"}},
		},
	}
	tool := NewSkillUseTool(lib, "personal")
	_, err := tool.Execute(context.Background(), []byte(`{"name":"work-only"}`))
	if err == nil {
		t.Error("work-only skill should be invisible in personal frame")
	}
}
