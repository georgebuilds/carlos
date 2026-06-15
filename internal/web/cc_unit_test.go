package web

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Observe-only mode (no EnableDrive): every interactive op is ErrUnsupported
// or a safe zero, without touching a subprocess.
func TestCCBackend_ObserveModeInteractiveOps(t *testing.T) {
	b := newCCBackendAt(t.TempDir())
	ctx := context.Background()
	if b.Attached("cc:x") {
		t.Error("observe: nothing attached")
	}
	if b.Frame("cc:x") != "" {
		t.Error("observe: frame is empty")
	}
	if err := b.Attach(ctx, "cc:x"); err != ErrUnsupported {
		t.Errorf("Attach = %v, want ErrUnsupported", err)
	}
	if _, err := b.Send(ctx, "cc:x", "hi"); err != ErrUnsupported {
		t.Errorf("Send = %v, want ErrUnsupported", err)
	}
	if _, err := b.CreateThread(ctx, ""); err != ErrUnsupported {
		t.Errorf("CreateThread = %v, want ErrUnsupported", err)
	}
	if err := b.Resolve("cc:x", "r", "allow"); err != ErrUnsupported {
		t.Errorf("Resolve = %v, want ErrUnsupported", err)
	}
	if b.LiveText("cc:x") != "" || b.Children(ctx, "cc:x") != nil || b.PendingApprovals("cc:x") != nil {
		t.Error("observe: live/children/pending are empty")
	}
	if err := b.Detach("cc:x"); err != nil {
		t.Errorf("Detach (observe) = %v, want nil", err)
	}
	if c := b.Caps(); c["send"] || c["approve"] {
		t.Error("observe caps must not advertise send/approve")
	}
}

// Drive enabled but the thread is not attached: live ops report empty / not
// attached without execing claude.
func TestCCBackend_DriveNotAttached(t *testing.T) {
	b := driveBackend(t)
	if b.Attached("cc:x") || b.LiveText("cc:x") != "" || b.Frame("cc:x") != "" {
		t.Error("nothing attached yet")
	}
	if _, err := b.Send(context.Background(), "cc:x", "hi"); err == nil {
		t.Error("Send to an unattached thread should error")
	}
	if c := b.Caps(); !c["send"] || !c["approve"] || !c["create"] {
		t.Errorf("drive caps = %v, want send+approve+create", c)
	}
}

func TestUUIDv4Format(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		u, err := uuidV4()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(u) {
			t.Fatalf("uuidV4 = %q, not a v4 UUID", u)
		}
		if seen[u] {
			t.Fatalf("uuidV4 collision: %q", u)
		}
		seen[u] = true
	}
}

func TestCCBackend_HookCommand(t *testing.T) {
	b := driveBackend(t) // baseURL http://127.0.0.1:0, token "tok", exe /bin/carlos
	cmd := b.hookCommand("cc:abc-123")
	for _, want := range []string{
		"/bin/carlos cc-hook", "--url http://127.0.0.1:0/api/cc/hook",
		"--token tok", "--thread cc:abc-123",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("hookCommand %q missing %q", cmd, want)
		}
	}
}

func TestNewCCBackend(t *testing.T) {
	b := NewCCBackend()
	if b == nil || b.Name() != "cc" {
		t.Fatalf("NewCCBackend name = %v, want cc", b)
	}
	// rooted under the user home's .claude/projects (or "" if home is unset).
	if b.root != "" && !strings.HasSuffix(b.root, "/.claude/projects") {
		t.Errorf("root = %q, want .../.claude/projects", b.root)
	}
}

func TestAgentDisplay(t *testing.T) {
	cases := map[string]string{"carlos": "carlos thread", "cc": "Claude Code", "opencode": "opencode"}
	for in, want := range cases {
		if got := agentDisplay(in); got != want {
			t.Errorf("agentDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandleCCHook_Errors(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	cc := driveBackend(t)
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken})
	s.Register(cc)

	// bad body -> 400
	if rec := do(t, s, "POST", "/api/cc/hook", map[string]any{}); rec.Code != 400 {
		t.Errorf("empty thread body = %d, want 400", rec.Code)
	}
	// a thread that routes to carlos (not cc) -> 404 unknown_backend
	rec := do(t, s, "POST", "/api/cc/hook", map[string]any{"thread": "01CARLOS", "tool_name": "Bash"})
	if rec.Code != 404 {
		t.Errorf("non-cc thread = %d, want 404", rec.Code)
	}
}

// handleHideThread degrades gracefully when no group store is wired.
func TestHandleHide_NoStore(t *testing.T) {
	log, _ := newTestLog(t)
	s := NewServer(Options{Log: log, Token: testToken}) // no Groups
	req := httptest.NewRequest("POST", "/api/threads/cc:x/hide", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Errorf("hide with no store = %d, want 503", rec.Code)
	}
}
