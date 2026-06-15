package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// creatableBackend is a stub that advertises + supports create, for the "+
// new" menu (meta agents) and create-routing tests.
type creatableBackend struct {
	readOnlyBackend
	name string
}

func (b creatableBackend) Name() string          { return b.name }
func (b creatableBackend) Caps() map[string]bool { return map[string]bool{"create": true} }
func (b creatableBackend) CreateThread(_ context.Context, title string) (ThreadSummary, error) {
	return ThreadSummary{ID: b.name + ":new", Title: title, Backend: b.name, State: "running"}, nil
}

func TestMeta_ListsCreatableAgents(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken}) // carlos read-only default
	s.Register(creatableBackend{name: ccBackendName})

	rec := do(t, s, "GET", "/api/meta", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("meta: %d", rec.Code)
	}
	var m Meta
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	by := map[string]AgentInfo{}
	for _, a := range m.Agents {
		by[a.Name] = a
	}
	// carlos is the read-only default (cannot create); cc can.
	if by["carlos"].Display != "carlos thread" {
		t.Errorf("carlos agent = %+v, want display 'carlos thread'", by["carlos"])
	}
	if by[ccBackendName].Display != "Claude Code" || !by[ccBackendName].CanCreate {
		t.Errorf("cc agent = %+v, want 'Claude Code'/can_create", by[ccBackendName])
	}
	// registration order: carlos first.
	if len(m.Agents) < 2 || m.Agents[0].Name != "carlos" {
		t.Errorf("agents order = %+v, want carlos first", m.Agents)
	}
}

func TestCreateThread_RoutesByBackend(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken})
	s.Register(creatableBackend{name: ccBackendName})

	// explicit backend routes to that agent's create path
	rec := do(t, s, "POST", "/api/threads", map[string]any{"backend": ccBackendName})
	if rec.Code != http.StatusOK {
		t.Fatalf("create on cc: %d (%s)", rec.Code, rec.Body.String())
	}
	var ts ThreadSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &ts)
	if ts.Backend != ccBackendName {
		t.Errorf("created backend = %q, want cc", ts.Backend)
	}

	// unknown backend is a 404, not a silent default
	rec2 := do(t, s, "POST", "/api/threads", map[string]any{"backend": "nope"})
	if rec2.Code != http.StatusNotFound {
		t.Errorf("create on unknown backend = %d, want 404", rec2.Code)
	}
}
