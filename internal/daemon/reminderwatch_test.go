package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/todo"
)

// reminderRecorder records delivered reminder batches.
type reminderRecorder struct {
	batches [][]todo.Item
}

func (r *reminderRecorder) deliver(_ context.Context, items []todo.Item) {
	cp := make([]todo.Item, len(items))
	copy(cp, items)
	r.batches = append(r.batches, cp)
}

// reminderVault seeds a temp vault and returns an Obsidian-backed router over
// it with a single anonymous frame.
func reminderRouter(t *testing.T, files map[string]string) *todo.Router {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	obs := todo.NewObsidianStore(root)
	return todo.NewRouter(frame.Config{}, "obsidian", map[string]todo.Store{"obsidian": obs})
}

// fixedClock returns a clock pinned at the given date.
func fixedClock(date string) func() time.Time {
	return func() time.Time {
		d, _ := time.ParseInLocation("2006-01-02", date, time.UTC)
		return d
	}
}

func TestReminderWatcher_FiresWhenAwayAndDue(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, true)
	router := reminderRouter(t, map[string]string{
		"todos.md": "- [ ] overdue thing 📅 2026-06-01 ^todo-1\n- [ ] future thing 📅 2026-12-31 ^todo-2\n",
	})
	rec := &reminderRecorder{}
	w := newReminderWatcher(log, router, rec.deliver, fixedClock("2026-06-23"), time.Second)
	w.drainPresence(context.Background())
	w.scan(context.Background())

	if len(rec.batches) != 1 {
		t.Fatalf("want 1 batch, got %d", len(rec.batches))
	}
	if len(rec.batches[0]) != 1 || rec.batches[0][0].ID != "todo-1" {
		t.Errorf("only the overdue item should fire: %+v", rec.batches[0])
	}
}

func TestReminderWatcher_SilentWhenPresent(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, false)
	router := reminderRouter(t, map[string]string{"todos.md": "- [ ] due 📅 2026-06-01 ^todo-1\n"})
	rec := &reminderRecorder{}
	w := newReminderWatcher(log, router, rec.deliver, fixedClock("2026-06-23"), time.Second)
	w.drainPresence(context.Background())
	w.scan(context.Background())
	if len(rec.batches) != 0 {
		t.Errorf("present user should get no reminders, got %d", len(rec.batches))
	}
}

func TestReminderWatcher_DedupsAcrossScans(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, true)
	router := reminderRouter(t, map[string]string{"todos.md": "- [ ] due 📅 2026-06-01 ^todo-1\n"})
	rec := &reminderRecorder{}
	w := newReminderWatcher(log, router, rec.deliver, fixedClock("2026-06-23"), time.Second)
	w.drainPresence(context.Background())
	w.scan(context.Background())
	w.scan(context.Background()) // second scan must not re-fire
	if len(rec.batches) != 1 {
		t.Errorf("item should fire once across scans, got %d batches", len(rec.batches))
	}
}

func TestReminderWatcher_NoDueNoFire(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, true)
	router := reminderRouter(t, map[string]string{"todos.md": "- [ ] later 📅 2026-12-31 ^todo-1\n- [ ] someday ^todo-2\n"})
	rec := &reminderRecorder{}
	w := newReminderWatcher(log, router, rec.deliver, fixedClock("2026-06-23"), time.Second)
	w.drainPresence(context.Background())
	w.scan(context.Background())
	if len(rec.batches) != 0 {
		t.Errorf("nothing due today should fire nothing, got %d", len(rec.batches))
	}
}

func TestReminderWatcher_NilRouterSafe(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, true)
	rec := &reminderRecorder{}
	w := newReminderWatcher(log, nil, rec.deliver, fixedClock("2026-06-23"), time.Second)
	w.drainPresence(context.Background())
	w.scan(context.Background()) // must not panic
	if len(rec.batches) != 0 {
		t.Errorf("nil router should deliver nothing")
	}
}

func TestReminderWatcher_RunCancels(t *testing.T) {
	log := awayTestLog(t)
	router := reminderRouter(t, nil)
	w := newReminderWatcher(log, router, func(context.Context, []todo.Item) {}, fixedClock("2026-06-23"), time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestReminderNotification_Rendering(t *testing.T) {
	title, body := reminderNotification([]todo.Item{
		{Text: "ship it", Frame: "work", Due: "2026-06-01"},
	})
	if title != "carlos: 1 todo due" {
		t.Errorf("title = %q", title)
	}
	if body != "- ship it (work) 📅 2026-06-01" {
		t.Errorf("body = %q", body)
	}

	// Plural + truncation.
	many := make([]todo.Item, maxReminderList+3)
	for i := range many {
		many[i] = todo.Item{Text: "t"}
	}
	title, body = reminderNotification(many)
	if title != "carlos: 8 todos due" {
		t.Errorf("plural title = %q", title)
	}
	if !contains(body, "+3 more") {
		t.Errorf("body should summarise overflow: %q", body)
	}
}

// TestNotifyTodoDue_NilGatewaySafe: the production deliverer degrades to
// silence (no panic) when no gateway is wired.
func TestNotifyTodoDue_NilGatewaySafe(t *testing.T) {
	d := &Daemon{}
	d.notifyTodoDue(context.Background(), []todo.Item{{Text: "x", Due: "2026-06-01"}})
	// Reaching here without panicking is the assertion.
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
