package chat

import (
	"sort"
	"strings"
	"testing"
)

type fakeMCPMgr struct {
	servers    []MCPManagedServer
	allowCalls map[string][]string
	autoCalls  map[string]bool
	capModel   string
	capCap     int
	capExposed int
}

func newFakeMCPMgr(servers []MCPManagedServer) *fakeMCPMgr {
	return &fakeMCPMgr{servers: servers, allowCalls: map[string][]string{}, autoCalls: map[string]bool{}}
}

func (f *fakeMCPMgr) Servers() []MCPManagedServer { return f.servers }
func (f *fakeMCPMgr) SetAllow(server string, allow []string) error {
	f.allowCalls[server] = allow
	return nil
}
func (f *fakeMCPMgr) SetAutoApprove(server string, on bool) error {
	f.autoCalls[server] = on
	return nil
}
func (f *fakeMCPMgr) CapStatus() (string, int, int) { return f.capModel, f.capCap, f.capExposed }

func srvWithTools(name string, raws ...string) MCPManagedServer {
	t := make([]MCPManagedTool, len(raws))
	for i, r := range raws {
		t[i] = MCPManagedTool{Raw: r, Enabled: true}
	}
	return MCPManagedServer{Name: name, Connected: true, Tools: t}
}

func TestFlushMCPWorking_SubsetPersistsSortedAllowlist(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("do", "c", "a", "b")})
	m := &Model{mcpMgr: mgr, mcpServers: mgr.servers, mcpWorkingSrv: "do",
		mcpWorking: map[string]bool{"a": true, "b": false, "c": true}, mcpDirty: true}
	m.flushMCPWorking()
	got := mgr.allowCalls["do"]
	sort.Strings(got)
	if strings.Join(got, ",") != "a,c" {
		t.Errorf("SetAllow=%v want [a c]", mgr.allowCalls["do"])
	}
}

func TestFlushMCPWorking_AllEnabledPersistsNil(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("do", "a", "b")})
	m := &Model{mcpMgr: mgr, mcpServers: mgr.servers, mcpWorkingSrv: "do",
		mcpWorking: map[string]bool{"a": true, "b": true}, mcpDirty: true}
	m.flushMCPWorking()
	if got, ok := mgr.allowCalls["do"]; !ok || got != nil {
		t.Errorf("all-enabled should persist nil (expose-all), got %v ok=%v", got, ok)
	}
}

func TestFlushMCPWorking_AllDisabledPersistsHideSentinel(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("do", "a", "b")})
	m := &Model{mcpMgr: mgr, mcpServers: mgr.servers, mcpWorkingSrv: "do",
		mcpWorking: map[string]bool{"a": false, "b": false}, mcpDirty: true}
	m.flushMCPWorking()
	got, ok := mgr.allowCalls["do"]
	if !ok || len(got) != 1 || got[0] != hideAllToolsSentinel {
		t.Errorf("all-disabled should persist the hide-all sentinel [\"\"], got %v ok=%v", got, ok)
	}
}

func TestFlushMCPWorking_NotDirtyNoOp(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("do", "a")})
	m := &Model{mcpMgr: mgr, mcpServers: mgr.servers, mcpWorkingSrv: "do",
		mcpWorking: map[string]bool{"a": false}, mcpDirty: false}
	m.flushMCPWorking()
	if len(mgr.allowCalls) != 0 {
		t.Errorf("clean working set must not persist: %v", mgr.allowCalls)
	}
}

func TestFilteredMCPTools(t *testing.T) {
	m := &Model{
		mcpServers: []MCPManagedServer{{Name: "do", Tools: []MCPManagedTool{
			{Raw: "droplet_list", Description: "list droplets"},
			{Raw: "database_create", Description: "make a db"},
			{Raw: "app_deploy", Description: "deploy"},
		}}},
		mcpWorkingSrv: "do",
	}
	m.mcpFilter = "droplet"
	got := m.filteredMCPTools()
	if len(got) != 1 || got[0].Raw != "droplet_list" {
		t.Errorf("filter droplet => %v", got)
	}
	m.mcpFilter = "db" // matches description "make a db"
	if got := m.filteredMCPTools(); len(got) != 1 || got[0].Raw != "database_create" {
		t.Errorf("filter db => %v", got)
	}
	m.mcpFilter = ""
	if got := m.filteredMCPTools(); len(got) != 3 {
		t.Errorf("empty filter => all, got %d", len(got))
	}
}

func TestSetAllMCPWorking(t *testing.T) {
	m := &Model{
		mcpServers:    []MCPManagedServer{srvWithTools("do", "a", "b", "c")},
		mcpWorkingSrv: "do",
		mcpWorking:    map[string]bool{},
	}
	m.setAllMCPWorking(true)
	for _, k := range []string{"a", "b", "c"} {
		if !m.mcpWorking[k] {
			t.Errorf("setAll(true) missed %q", k)
		}
	}
	if !m.mcpDirty {
		t.Error("setAll should mark dirty")
	}
	m.setAllMCPWorking(false)
	for _, k := range []string{"a", "b", "c"} {
		if m.mcpWorking[k] {
			t.Errorf("setAll(false) left %q on", k)
		}
	}
}

func TestWrapCursor(t *testing.T) {
	if wrapCursor(-1, 3) != 2 {
		t.Error("wrap up")
	}
	if wrapCursor(3, 3) != 0 {
		t.Error("wrap down")
	}
	if wrapCursor(1, 3) != 1 {
		t.Error("middle")
	}
	if wrapCursor(5, 0) != 0 {
		t.Error("empty")
	}
}

func TestScrollWindow(t *testing.T) {
	rows := []string{"0", "1", "2", "3", "4", "5"}
	w, below := scrollWindow(rows, 0, 3)
	if strings.Join(w, "") != "012" || below != 3 {
		t.Errorf("top window=%v below=%d", w, below)
	}
	w, below = scrollWindow(rows, 5, 3)
	if strings.Join(w, "") != "345" || below != 0 {
		t.Errorf("bottom window=%v below=%d", w, below)
	}
	w, below = scrollWindow(rows[:2], 0, 3)
	if len(w) != 2 || below != 0 {
		t.Errorf("under capacity window=%v below=%d", w, below)
	}
}

func TestEnabledCount(t *testing.T) {
	s := MCPManagedServer{Tools: []MCPManagedTool{{Enabled: true}, {Enabled: false}, {Enabled: true}}}
	if enabledCount(s) != 2 {
		t.Errorf("enabledCount=%d want 2", enabledCount(s))
	}
}

func TestWithMCPManager(t *testing.T) {
	mgr := newFakeMCPMgr(nil)
	m := &Model{}
	WithMCPManager(mgr)(m)
	if m.mcpMgr != mgr {
		t.Error("WithMCPManager did not set mcpMgr")
	}
}

func TestOpenCloseMCPOverlay(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("do", "a")})
	m := &Model{mcpMgr: mgr}
	m.openMCPOverlay()
	if !m.showMCP || m.mcpLevel != 0 || len(m.mcpServers) != 1 {
		t.Fatalf("open: showMCP=%v level=%d servers=%d", m.showMCP, m.mcpLevel, len(m.mcpServers))
	}
	m.closeMCPOverlay()
	if m.showMCP || m.mcpServers != nil {
		t.Errorf("close did not reset state")
	}
}

func TestHandleMCPServersKey_NavEnterEscAuto(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("a", "x"), srvWithTools("b", "y")})
	m := &Model{mcpMgr: mgr, mcpServers: mgr.servers, showMCP: true}

	m.handleMCPServersKey(key("down"))
	if m.mcpSrvCursor != 1 {
		t.Errorf("down: cursor=%d", m.mcpSrvCursor)
	}
	m.handleMCPServersKey(key("up"))
	if m.mcpSrvCursor != 0 {
		t.Errorf("up: cursor=%d", m.mcpSrvCursor)
	}
	// `a` toggles the focused server's auto-approve (was false -> true).
	m.handleMCPServersKey(key("a"))
	if v, ok := mgr.autoCalls["a"]; !ok || !v {
		t.Errorf("auto toggle: %v ok=%v", v, ok)
	}
	// enter drills into the tool picker.
	m.handleMCPServersKey(key("enter"))
	if m.mcpLevel != 1 || m.mcpWorkingSrv != "a" {
		t.Errorf("enter: level=%d srv=%q", m.mcpLevel, m.mcpWorkingSrv)
	}
	// esc from the servers level closes the overlay.
	m.mcpLevel = 0
	m.handleMCPServersKey(key("esc"))
	if m.showMCP {
		t.Error("esc should close")
	}
}

func TestHandleMCPToolsKey_ToggleAllNoneFilterBack(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("do", "a", "b")})
	m := &Model{mcpMgr: mgr, mcpServers: mgr.servers}
	m.enterMCPTools() // loads working set from server 0 ("do")
	if m.mcpLevel != 1 {
		t.Fatalf("enterMCPTools level=%d", m.mcpLevel)
	}
	// space toggles the cursor tool off.
	m.handleMCPToolsKey(key(" "))
	tools := m.filteredMCPTools()
	if m.mcpWorking[tools[0].Raw] {
		t.Error("space should toggle off")
	}
	// n disables all, a enables all.
	m.handleMCPToolsKey(key("n"))
	if m.mcpWorking["a"] || m.mcpWorking["b"] {
		t.Error("n should disable all")
	}
	m.handleMCPToolsKey(key("a"))
	if !m.mcpWorking["a"] || !m.mcpWorking["b"] {
		t.Error("a should enable all")
	}
	// / enters filter mode; a rune types into the filter; esc exits filter.
	m.handleMCPToolsKey(key("/"))
	if !m.mcpFilterMode {
		t.Error("/ should enter filter mode")
	}
	m.handleMCPToolsKey(key("a"))
	if m.mcpFilter != "a" {
		t.Errorf("filter=%q want a", m.mcpFilter)
	}
	m.handleMCPToolsKey(key("backspace"))
	if m.mcpFilter != "" {
		t.Errorf("backspace: filter=%q", m.mcpFilter)
	}
	m.handleMCPToolsKey(key("esc"))
	if m.mcpFilterMode {
		t.Error("esc should exit filter mode")
	}
	// esc again flushes and returns to the servers level.
	m.handleMCPToolsKey(key("esc"))
	if m.mcpLevel != 0 {
		t.Errorf("esc should go back to servers, level=%d", m.mcpLevel)
	}
}

func TestHandleMCPOverlayKey_CtrlCFallsThrough(t *testing.T) {
	m := &Model{mcpServers: []MCPManagedServer{srvWithTools("do", "a")}}
	_, _, handled := m.handleMCPOverlayKey(key("ctrl+c"))
	if handled {
		t.Error("ctrl+c must fall through (handled=false) so the user can quit")
	}
}

func TestRenderMCPOverlay_BothLevelsAndBanner(t *testing.T) {
	mgr := newFakeMCPMgr([]MCPManagedServer{srvWithTools("digitalocean", "droplet_list", "db_create")})
	mgr.capModel, mgr.capCap, mgr.capExposed = "x-ai/grok-4", 200, 240
	m := &Model{mcpMgr: mgr, mcpServers: mgr.servers}

	out := renderMCPOverlay(m, 80, 20) // servers level
	if !strings.Contains(out, "digitalocean") {
		t.Error("servers render missing server name")
	}
	if !strings.Contains(out, "grok") {
		t.Error("cap banner missing on overflow")
	}

	m.enterMCPTools()
	out = renderMCPOverlay(m, 80, 20) // tools level
	if !strings.Contains(out, "droplet_list") || !strings.Contains(out, "db_create") {
		t.Error("tools render missing tool names")
	}
}
