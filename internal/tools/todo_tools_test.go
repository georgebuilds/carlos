package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/notes"
)

// todoVault seeds a temp vault and returns its path.
func todoVault(t *testing.T, files map[string]string) string {
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
	return root
}

func todoFrames() frame.Config {
	return frame.Config{
		Active:  "personal",
		Default: "personal",
		List: []frame.Frame{
			{Name: "personal", VaultSubtree: "personal"},
			{Name: "work", VaultSubtree: "work"},
		},
	}
}

func newTodoTestEnv(t *testing.T, vault string) *todoEnv {
	t.Helper()
	return newTodoEnv(
		config.VaultConfig{Path: vault},
		config.TodosConfig{},
		todoFrames(),
		"personal",
		notes.NewCache(nil),
	)
}

func TestTodoList_FrameLens(t *testing.T) {
	vault := todoVault(t, map[string]string{
		"personal/todos.md": "- [ ] personal item ^todo-p1\n",
		"work/todos.md":     "- [ ] work item ^todo-w1\n",
	})
	tool := NewTodoListTool(newTodoTestEnv(t, vault))
	out, err := tool.Execute(context.Background(), []byte(`{"scope":"frame"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["count"].(float64) != 1 {
		t.Fatalf("active frame lens should return 1 item: %+v", m)
	}
	items := m["items"].([]any)
	first := items[0].(map[string]any)
	if first["frame"] != "personal" {
		t.Errorf("frame label = %v", first["frame"])
	}
}

func TestTodoList_MasterLens(t *testing.T) {
	vault := todoVault(t, map[string]string{
		"personal/todos.md": "- [ ] personal item ^todo-p1\n",
		"work/todos.md":     "- [ ] work item ^todo-w1\n",
	})
	tool := NewTodoListTool(newTodoTestEnv(t, vault))
	out, err := tool.Execute(context.Background(), []byte(`{"scope":"all"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["count"].(float64) != 2 {
		t.Errorf("master lens should union both frames: %+v", m)
	}
}

func TestTodoAdd_WritesToActiveFrame(t *testing.T) {
	vault := todoVault(t, nil)
	tool := NewTodoAddTool(newTodoTestEnv(t, vault))
	out, err := tool.Execute(context.Background(), []byte(`{"text":"buy milk","due":"2026-07-01","tags":["errand"]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["frame"] != "personal" {
		t.Errorf("should add to active frame: %+v", m)
	}
	body, _ := os.ReadFile(filepath.Join(vault, "personal", "todos.md"))
	if string(body) == "" {
		t.Fatal("inbox not written")
	}
	if indexOfBytes(body, "buy milk") < 0 {
		t.Errorf("body missing task: %s", body)
	}
}

func TestTodoAdd_InvalidDue(t *testing.T) {
	vault := todoVault(t, nil)
	tool := NewTodoAddTool(newTodoTestEnv(t, vault))
	out, _ := tool.Execute(context.Background(), []byte(`{"text":"x","due":"July 1st"}`))
	m := asMap(t, out)
	if _, has := m["error"]; !has {
		t.Errorf("invalid due should error: %+v", m)
	}
}

func TestTodoDone_RoundTrip(t *testing.T) {
	vault := todoVault(t, map[string]string{
		"personal/todos.md": "- [ ] finish me ^todo-x1\n",
	})
	env := newTodoTestEnv(t, vault)
	done := NewTodoDoneTool(env)
	out, err := done.Execute(context.Background(), []byte(`{"id":"todo-x1"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["done"] != true {
		t.Errorf("not marked done: %+v", m)
	}
	// And it disappears from the open list.
	list := NewTodoListTool(env)
	lout, _ := list.Execute(context.Background(), []byte(`{"scope":"frame","filter":"open"}`))
	lm := asMap(t, lout)
	if lm["count"].(float64) != 0 {
		t.Errorf("completed item should not be in the open list: %+v", lm)
	}
}

func TestTodoUpdate_Partial(t *testing.T) {
	vault := todoVault(t, map[string]string{
		"personal/todos.md": "- [ ] old ^todo-u1\n",
	})
	tool := NewTodoUpdateTool(newTodoTestEnv(t, vault))
	out, err := tool.Execute(context.Background(), []byte(`{"id":"todo-u1","text":"new text","due":"2026-09-09"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	if m["text"] != "new text" || m["due"] != "2026-09-09" {
		t.Errorf("update result wrong: %+v", m)
	}
}

func TestTodoUpdate_NoFields(t *testing.T) {
	vault := todoVault(t, map[string]string{"personal/todos.md": "- [ ] x ^todo-u1\n"})
	tool := NewTodoUpdateTool(newTodoTestEnv(t, vault))
	out, _ := tool.Execute(context.Background(), []byte(`{"id":"todo-u1"}`))
	m := asMap(t, out)
	if _, has := m["error"]; !has {
		t.Errorf("empty patch should error: %+v", m)
	}
}

func TestTodo_NotConfigured(t *testing.T) {
	env := newTodoEnv(config.VaultConfig{}, config.TodosConfig{}, frame.Config{}, "", nil)
	out, _ := NewTodoListTool(env).Execute(context.Background(), []byte(`{}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); errMsg == "" {
		t.Errorf("expected not-configured envelope: %+v", m)
	}
}

func TestTodo_BadBackendConfig(t *testing.T) {
	vault := todoVault(t, nil)
	env := newTodoEnv(
		config.VaultConfig{Path: vault},
		config.TodosConfig{Backends: map[string]config.TodoBackendConfig{
			"weird": {Type: "carrier-pigeon"},
		}},
		todoFrames(), "personal", notes.NewCache(nil),
	)
	out, _ := NewTodoListTool(env).Execute(context.Background(), []byte(`{}`))
	m := asMap(t, out)
	if errMsg, _ := m["error"].(string); errMsg == "" {
		t.Errorf("unsupported backend type should surface an error: %+v", m)
	}
}

func TestTodoTools_RegisteredAndAllowlisted(t *testing.T) {
	reg := NewDefaultRegistryWithIdentity("", config.VaultConfig{Path: t.TempDir()}, todoFrames(), "personal", nil, "", config.TodosConfig{})
	for _, name := range []string{"todo_list", "todo_add", "todo_done", "todo_update"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("%s not registered", name)
		}
	}
}

// indexOfBytes is a tiny substring search over raw bytes (the vault file body).
func indexOfBytes(b []byte, sub string) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == sub {
			return i
		}
	}
	return -1
}
