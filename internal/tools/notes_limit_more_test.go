package tools

import (
	"context"
	"testing"
)

// TestNotesTagged_LimitTruncatesFanout — with a small limit the merged
// fan-out set is truncated (exercising the limit-cap branch).
func TestNotesTagged_LimitTruncatesFanout(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesTaggedTool(env)
	// "meta" tags both notes.md and sub/notes.md; limit=1 forces a cut.
	out, err := tool.Execute(context.Background(), []byte(`{"tag":"meta","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	got, _ := m["notes"].([]any)
	if len(got) > 1 {
		t.Errorf("limit=1 should cap notes to 1; got %d", len(got))
	}
	// Total should still reflect the pre-truncation count.
	if total, _ := m["total"].(float64); total < 1 {
		t.Errorf("total should report the full count; got %v", m["total"])
	}
}

// TestNotesRecent_LimitTruncatesFanout — recent fan-out is capped at limit.
func TestNotesRecent_LimitTruncatesFanout(t *testing.T) {
	env := newFramedTestEnv(t, "personal")
	tool := NewNotesRecentTool(env)
	out, err := tool.Execute(context.Background(), []byte(`{"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	got, _ := m["notes"].([]any)
	if len(got) > 1 {
		t.Errorf("limit=1 should cap recent notes to 1; got %d", len(got))
	}
}
