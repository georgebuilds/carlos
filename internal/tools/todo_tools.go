package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/notes"
	"github.com/georgebuilds/carlos/internal/todo"
)

// todoEnv is the shared dependency the four todo_* tools hold: a single
// todo.Router built from the user's vault + frames + todos config. Constructed
// once by the registry factory, mirroring how notesEnv is shared across the
// notes_* family.
//
// When no vault is configured the router is nil and every tool returns the
// "todos not configured" envelope, so the tools still register (the model sees
// a clean error) without panicking on a path-less vault.
type todoEnv struct {
	router   *todo.Router
	buildErr error
}

// newTodoEnv translates config into a wired todo.Router for the tool family.
func newTodoEnv(vaultCfg config.VaultConfig, todosCfg config.TodosConfig, frames frame.Config, active string, cache *notes.Cache) *todoEnv {
	router, err := BuildTodoRouter(vaultCfg, todosCfg, frames, active, cache)
	if router == nil && err == nil {
		// No vault configured: todos unavailable until onboarding sets one.
		return &todoEnv{}
	}
	return &todoEnv{router: router, buildErr: err}
}

// BuildTodoRouter assembles a todo.Router from config. It is the single bridge
// from carlos config to the todo package, shared by the tool layer and the
// daemon's reminder scanner. cache (the shared notes cache) is wired as the
// write-invalidator so notes_search reflects todo edits immediately; pass nil
// to skip invalidation. External backend credentials are read from the
// environment variable each backend names (never from YAML).
//
// Returns (nil, nil) when no vault is configured AND no external backends are
// declared, so callers can distinguish "todos unavailable" from a build error.
func BuildTodoRouter(vaultCfg config.VaultConfig, todosCfg config.TodosConfig, frames frame.Config, active string, cache *notes.Cache) (*todo.Router, error) {
	resolved, err := notes.ResolveVaultPath(vaultCfg.Path, "")
	if err != nil {
		return nil, nil
	}
	specs := make([]todo.BackendSpec, 0, len(todosCfg.Backends))
	for name, b := range todosCfg.Backends {
		authValue := ""
		if b.AuthEnv != "" {
			authValue = os.Getenv(b.AuthEnv)
		}
		specs = append(specs, todo.BackendSpec{
			Name:       name,
			Type:       b.Type,
			BaseURL:    b.BaseURL,
			AuthHeader: b.AuthHeader,
			AuthValue:  authValue,
		})
	}
	var inval todo.Invalidator
	if cache != nil {
		inval = cache
	}
	return todo.BuildRouter(resolved, frames, active, todo.BuildOptions{
		DefaultBackend: todosCfg.DefaultBackend,
		Inbox:          todosCfg.Inbox,
		Invalidator:    inval,
		Backends:       specs,
	})
}

// ready returns the router or an error envelope when todos are unavailable.
func (e *todoEnv) ready() (*todo.Router, []byte, error) {
	if e == nil || (e.router == nil && e.buildErr == nil) {
		env, merr := jsonErr("todos not configured (set vault.path during onboarding, or declare todos.backends)")
		return nil, env, merr
	}
	if e.buildErr != nil {
		env, merr := jsonErr("todos: %v", e.buildErr)
		return nil, env, merr
	}
	return e.router, nil, nil
}

// parseFilter maps the optional filter arg to a todo.Filter, defaulting open.
func parseFilter(s string) todo.Filter {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "done":
		return todo.FilterDone
	case "all":
		return todo.FilterAll
	default:
		return todo.FilterOpen
	}
}

// --- todo_list ---------------------------------------------------------------

// TodoListTool registers as `todo_list`. It answers the two lenses: scope
// "frame" (default) returns one frame's todos and everything nested beneath
// its vault_subtree; scope "all" returns the master union across every frame,
// each item labelled with its source frame.
type TodoListTool struct{ env *todoEnv }

// NewTodoListTool ties the tool to the shared todo env.
func NewTodoListTool(env *todoEnv) *TodoListTool { return &TodoListTool{env: env} }

func (*TodoListTool) Name() string { return "todo_list" }

func (*TodoListTool) Description() string {
	return "List todos from the user's task backend (Obsidian vault by default). scope \"frame\" (default) shows the current frame's todos plus everything nested beneath its vault_subtree; scope \"all\" shows the master list aggregated across every frame, each item labelled with its source frame. filter is open (default), done, or all. Items carry a stable id for todo_done / todo_update."
}

func (*TodoListTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"scope":  {"type": "string", "enum": ["frame", "all"], "description": "\"frame\" (default) = the targeted lens (one frame + its child folders); \"all\" = the master union across every frame."},
			"frame":  {"type": "string", "description": "Frame name for scope=frame. Omit to use the active frame."},
			"filter": {"type": "string", "enum": ["open", "done", "all"], "description": "Which items to return. Default open."}
		}
	}`)
}

type todoListInput struct {
	Scope  string `json:"scope"`
	Frame  string `json:"frame"`
	Filter string `json:"filter"`
}

type todoListResponse struct {
	Scope  string      `json:"scope"`
	Filter string      `json:"filter"`
	Count  int         `json:"count"`
	Items  []todo.Item `json:"items"`
}

func (t *TodoListTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	router, env, err := t.env.ready()
	if router == nil {
		return env, err
	}
	var in todoListInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("todo_list: parse input: %v", err)
	}
	filter := parseFilter(in.Filter)
	scope := strings.ToLower(strings.TrimSpace(in.Scope))
	var items []todo.Item
	var qerr error
	if scope == "all" {
		items, qerr = router.Master(ctx, filter)
	} else {
		scope = "frame"
		items, qerr = router.Frame(ctx, in.Frame, filter)
	}
	if qerr != nil {
		return jsonErr("todo_list: %v", qerr)
	}
	if items == nil {
		items = []todo.Item{}
	}
	return jsonOK(todoListResponse{Scope: scope, Filter: string(filter), Count: len(items), Items: items})
}

// --- todo_add ----------------------------------------------------------------

// TodoAddTool registers as `todo_add`. Appends a new task to the target
// frame's backend (the Obsidian inbox note for vault-backed frames).
type TodoAddTool struct{ env *todoEnv }

// NewTodoAddTool ties the tool to the shared todo env.
func NewTodoAddTool(env *todoEnv) *TodoAddTool { return &TodoAddTool{env: env} }

func (*TodoAddTool) Name() string { return "todo_add" }

func (*TodoAddTool) Description() string {
	return "Add a todo to the user's task backend (Obsidian vault by default). The item lands in the target frame's inbox (active frame unless frame is given). Optional due date (YYYY-MM-DD) and tags. Returns the created item with its stable id."
}

func (*TodoAddTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"text":  {"type": "string", "description": "The task description."},
			"frame": {"type": "string", "description": "Frame to add into. Omit to use the active frame."},
			"due":   {"type": "string", "description": "Optional due date in YYYY-MM-DD form."},
			"tags":  {"type": "array", "items": {"type": "string"}, "description": "Optional tags (with or without a leading #)."}
		},
		"required": ["text"]
	}`)
}

type todoAddInput struct {
	Text  string   `json:"text"`
	Frame string   `json:"frame"`
	Due   string   `json:"due"`
	Tags  []string `json:"tags"`
}

func (t *TodoAddTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	router, env, err := t.env.ready()
	if router == nil {
		return env, err
	}
	var in todoAddInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("todo_add: parse input: %v", err)
	}
	if strings.TrimSpace(in.Text) == "" {
		return jsonErr("todo_add: empty text")
	}
	if in.Due != "" {
		if _, ok := todo.ParseDue(in.Due); !ok {
			return jsonErr("todo_add: invalid due %q (want YYYY-MM-DD)", in.Due)
		}
	}
	item, aerr := router.Add(ctx, in.Frame, todo.Draft{Text: in.Text, Due: in.Due, Tags: in.Tags})
	if aerr != nil {
		return jsonErr("todo_add: %v", aerr)
	}
	return jsonOK(item)
}

// --- todo_done ---------------------------------------------------------------

// TodoDoneTool registers as `todo_done`. Marks a task complete by id.
type TodoDoneTool struct{ env *todoEnv }

// NewTodoDoneTool ties the tool to the shared todo env.
func NewTodoDoneTool(env *todoEnv) *TodoDoneTool { return &TodoDoneTool{env: env} }

func (*TodoDoneTool) Name() string { return "todo_done" }

func (*TodoDoneTool) Description() string {
	return "Mark a todo complete by its id (from todo_list). Pass the frame if the item lives in a non-active frame. Returns the updated item."
}

func (*TodoDoneTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"id":    {"type": "string", "description": "The item id from todo_list (e.g. \"todo-ab12cd\")."},
			"frame": {"type": "string", "description": "Frame the item belongs to. Omit to use the active frame."}
		},
		"required": ["id"]
	}`)
}

type todoIDInput struct {
	ID    string `json:"id"`
	Frame string `json:"frame"`
}

func (t *TodoDoneTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	router, env, err := t.env.ready()
	if router == nil {
		return env, err
	}
	var in todoIDInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("todo_done: parse input: %v", err)
	}
	if strings.TrimSpace(in.ID) == "" {
		return jsonErr("todo_done: empty id")
	}
	item, derr := router.Complete(ctx, in.Frame, in.ID)
	if derr != nil {
		return jsonErr("todo_done: %v", derr)
	}
	return jsonOK(item)
}

// --- todo_update -------------------------------------------------------------

// TodoUpdateTool registers as `todo_update`. Applies a partial edit to a task.
type TodoUpdateTool struct{ env *todoEnv }

// NewTodoUpdateTool ties the tool to the shared todo env.
func NewTodoUpdateTool(env *todoEnv) *TodoUpdateTool { return &TodoUpdateTool{env: env} }

func (*TodoUpdateTool) Name() string { return "todo_update" }

func (*TodoUpdateTool) Description() string {
	return "Edit a todo by id: change its text, due date, tags, or done state. Only the fields you supply change. Pass the frame for items in a non-active frame. Returns the updated item."
}

func (*TodoUpdateTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"id":    {"type": "string", "description": "The item id from todo_list."},
			"frame": {"type": "string", "description": "Frame the item belongs to. Omit to use the active frame."},
			"text":  {"type": "string", "description": "New description (omit to leave unchanged)."},
			"done":  {"type": "boolean", "description": "New done state (omit to leave unchanged)."},
			"due":   {"type": "string", "description": "New due date YYYY-MM-DD, or empty string to clear (omit to leave unchanged)."},
			"tags":  {"type": "array", "items": {"type": "string"}, "description": "Replacement tag set (omit to leave unchanged)."}
		},
		"required": ["id"]
	}`)
}

type todoUpdateInput struct {
	ID    string    `json:"id"`
	Frame string    `json:"frame"`
	Text  *string   `json:"text"`
	Done  *bool     `json:"done"`
	Due   *string   `json:"due"`
	Tags  *[]string `json:"tags"`
}

func (t *TodoUpdateTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	router, env, err := t.env.ready()
	if router == nil {
		return env, err
	}
	var in todoUpdateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return jsonErr("todo_update: parse input: %v", err)
	}
	if strings.TrimSpace(in.ID) == "" {
		return jsonErr("todo_update: empty id")
	}
	if in.Text == nil && in.Done == nil && in.Due == nil && in.Tags == nil {
		return jsonErr("todo_update: no fields to change")
	}
	if in.Due != nil && *in.Due != "" {
		if _, ok := todo.ParseDue(*in.Due); !ok {
			return jsonErr("todo_update: invalid due %q (want YYYY-MM-DD or empty to clear)", *in.Due)
		}
	}
	item, uerr := router.Update(ctx, in.Frame, in.ID, todo.Patch{
		Text: in.Text,
		Done: in.Done,
		Due:  in.Due,
		Tags: in.Tags,
	})
	if uerr != nil {
		return jsonErr("todo_update: %v", uerr)
	}
	return jsonOK(item)
}
