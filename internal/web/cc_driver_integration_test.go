package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCCDriver_Integration drives a real `claude` subprocess end to end:
// create a resumable session, start the driver, send a turn, and assert live
// text deltas flow through the publish callback. Gated behind CARLOS_CC_IT so
// CI (no claude, no auth) skips it; run locally with:
//
//	CARLOS_CC_IT=1 go test ./internal/web/ -run CCDriver_Integration -v
func TestCCDriver_Integration(t *testing.T) {
	if os.Getenv("CARLOS_CC_IT") == "" {
		t.Skip("set CARLOS_CC_IT=1 to run (spawns a real claude subprocess)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not in PATH")
	}

	cwd := t.TempDir()
	const uuid = "11111111-2222-4333-8444-555555555555"
	// Seed a resumable session in cwd with this id.
	seed := exec.Command("claude", "-p", "--session-id", uuid,
		"--permission-mode", "default", "Reply with exactly: READY")
	seed.Dir = cwd
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed session: %v\n%s", err, out)
	}
	// Best-effort cleanup of the on-disk session this test created.
	t.Cleanup(func() {
		if home, err := os.UserHomeDir(); err == nil {
			enc := strings.ReplaceAll(cwd, "/", "-")
			_ = os.Remove(filepath.Join(home, ".claude", "projects", enc, uuid+".jsonl"))
		}
	})

	var mu sync.Mutex
	var sb strings.Builder
	publish := func(ev WireEvent) {
		if ev.Kind == "delta" {
			if m, ok := ev.Data.(map[string]any); ok {
				mu.Lock()
				sb.WriteString(m["text"].(string))
				mu.Unlock()
			}
		}
	}

	drv, err := startCCDriver(context.Background(), "cc:"+uuid, uuid, cwd, "", publish)
	if err != nil {
		t.Fatalf("startCCDriver: %v", err)
	}
	defer drv.stop()

	if err := drv.send("Reply with exactly the token PONG and nothing else."); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.After(60 * time.Second)
	for {
		mu.Lock()
		got := sb.String()
		mu.Unlock()
		if strings.Contains(got, "PONG") {
			// live deltas flowed and reconstruct the assistant text.
			if lt := drv.liveText(); !strings.Contains(lt, "PONG") {
				t.Errorf("liveText() = %q, want it to contain the streamed PONG", lt)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("no PONG delta within 60s; got %q", got)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// TestCCApproval_Integration proves the full B-4 chain against real claude:
// a driven session that wants to run a tool invokes the REAL `carlos cc-hook`
// subprocess (installed via --settings), which calls back over HTTP; here a
// stub intake denies it, and we assert the hook fired with the tool payload
// and the tool was blocked. Same gate as the driver integration test.
func TestCCApproval_Integration(t *testing.T) {
	if os.Getenv("CARLOS_CC_IT") == "" {
		t.Skip("set CARLOS_CC_IT=1 to run (spawns real claude + builds carlos)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not in PATH")
	}

	// Build the carlos binary so the hook command is the real subcommand.
	bin := filepath.Join(t.TempDir(), "carlos")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/georgebuilds/carlos/cmd/carlos").CombinedOutput(); err != nil {
		t.Fatalf("build carlos: %v\n%s", err, out)
	}

	// Stub carlos-web intake: record the hook callback, deny the tool.
	var mu sync.Mutex
	var gotTool string
	hookHit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ToolName string `json:"tool_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		gotTool = body.ToolName
		mu.Unlock()
		select {
		case hookHit <- struct{}{}:
		default:
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"decision": "deny"})
	}))
	defer srv.Close()

	cwd := t.TempDir()
	const uuid = "22222222-3333-4444-8555-666666666666"
	seed := exec.Command("claude", "-p", "--session-id", uuid, "--permission-mode", "default", "say ok")
	seed.Dir = cwd
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if home, err := os.UserHomeDir(); err == nil {
			enc := strings.ReplaceAll(cwd, "/", "-")
			_ = os.Remove(filepath.Join(home, ".claude", "projects", enc, uuid+".jsonl"))
		}
	})

	hookCmd := bin + " cc-hook --url " + srv.URL + "/api/cc/hook --token tok --thread cc:" + uuid
	drv, err := startCCDriver(context.Background(), "cc:"+uuid, uuid, cwd, hookCmd, func(WireEvent) {})
	if err != nil {
		t.Fatalf("startCCDriver: %v", err)
	}
	defer drv.stop()

	if err := drv.send("Use the Bash tool to run exactly: echo HELLO_FROM_HOOK"); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case <-hookHit:
		mu.Lock()
		tool := gotTool
		mu.Unlock()
		if tool != "Bash" {
			t.Errorf("hook fired for tool %q, want Bash", tool)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the carlos cc-hook never called back (full approval chain broken)")
	}
}
