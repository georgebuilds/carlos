package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
)

// withTempConfig points CARLOS_CONFIG at a tmpdir for the duration of
// the test so handleScheduleSlash edits an isolated config instead of
// the user's real ~/.carlos/config.yaml.
func withTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{
		UserName: "Tester",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-test"},
		},
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Setenv("CARLOS_CONFIG", path)
	return path
}

func TestHandleScheduleSlash_ListEmpty(t *testing.T) {
	_ = withTempConfig(t)
	got := handleScheduleSlash("list")
	if !strings.Contains(got, "no schedules") {
		t.Fatalf("expected 'no schedules' message, got %q", got)
	}
}

func TestHandleScheduleSlash_AddAndList(t *testing.T) {
	path := withTempConfig(t)
	added := handleScheduleSlash(`add "every weekday at 9am" summarize my unread DMs`)
	if !strings.Contains(added, "added") {
		t.Fatalf("add response: %q", added)
	}
	// Persisted to disk?
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Schedules) != 1 {
		t.Fatalf("expected 1 schedule on disk, got %d", len(cfg.Schedules))
	}
	if cfg.Schedules[0].Spec != "0 9 * * 1-5" {
		t.Errorf("spec wrong: %q", cfg.Schedules[0].Spec)
	}
	if cfg.Schedules[0].Prompt != "summarize my unread DMs" {
		t.Errorf("prompt wrong: %q", cfg.Schedules[0].Prompt)
	}
	listed := handleScheduleSlash("list")
	if !strings.Contains(listed, "0 9 * * 1-5") {
		t.Errorf("list missing spec: %q", listed)
	}
}

func TestHandleScheduleSlash_Rm(t *testing.T) {
	path := withTempConfig(t)
	_ = handleScheduleSlash(`add "daily at 8am" backup the photos`)
	cfg, _ := config.Load(path)
	if len(cfg.Schedules) != 1 {
		t.Fatalf("setup: %d schedules", len(cfg.Schedules))
	}
	name := cfg.Schedules[0].Name
	resp := handleScheduleSlash("rm " + name)
	if !strings.Contains(resp, "removed") {
		t.Fatalf("rm response: %q", resp)
	}
	cfg2, _ := config.Load(path)
	if len(cfg2.Schedules) != 0 {
		t.Fatalf("expected 0 schedules after rm, got %d", len(cfg2.Schedules))
	}
}

func TestHandleScheduleSlash_BadInputs(t *testing.T) {
	_ = withTempConfig(t)
	cases := []struct {
		in        string
		wantMatch string
	}{
		{"", "usage"},
		{"add", "usage"},
		{`add "blursday at 9am" do stuff`, "schedule add:"},
		{"rm", "name required"},
		{"rm nonexistent", "no schedule named"},
		{"florp", "usage"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := handleScheduleSlash(c.in)
			if !strings.Contains(got, c.wantMatch) {
				t.Errorf("input %q: expected to contain %q, got %q", c.in, c.wantMatch, got)
			}
		})
	}
}

// TestHandleScheduleSlash_PersistsAcrossLoad — the round-trip through
// config.Save + config.Load preserves every field a /schedule add
// produces, including Once and Spec.
func TestHandleScheduleSlash_PersistsAcrossLoad(t *testing.T) {
	path := withTempConfig(t)
	_ = handleScheduleSlash(`add "tomorrow at 3pm" call mom`)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "once: true") {
		t.Fatalf("expected once:true in YAML, got:\n%s", raw)
	}
}
