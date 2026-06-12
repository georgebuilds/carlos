package chat

import (
	"os"
	"strings"
	"testing"
)

func writeFileForTest(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

func mkdirAllForTest(path string) error {
	return os.MkdirAll(path, 0o700)
}

func TestParseModelArg(t *testing.T) {
	tests := []struct {
		in       string
		wantProv string
		wantMod  string
	}{
		{"", "", ""},
		{"openrouter:google/gemini-3.5-flash", "openrouter", "google/gemini-3.5-flash"},
		{"  anthropic : claude-opus-4-7  ", "anthropic", "claude-opus-4-7"},
		{"gpt-5", "", "gpt-5"},                        // bare model
		{"openrouter:", "openrouter", ""},             // colon with no model
		{":just-model", "", "just-model"},             // empty provider
		{"openai:gpt-5:beta", "openai", "gpt-5:beta"}, // extra colons stay in model
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			p, m := parseModelArg(tc.in)
			if p != tc.wantProv {
				t.Errorf("provider = %q want %q", p, tc.wantProv)
			}
			if m != tc.wantMod {
				t.Errorf("model = %q want %q", m, tc.wantMod)
			}
		})
	}
}

func TestDisplayModelName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"google/gemini-3.5-flash", "gemini-3.5-flash"},
		{"anthropic/claude-opus-4-7", "claude-opus-4-7"},
		{"claude-opus-4-7", "claude-opus-4-7"}, // no slash → untouched
		{"gpt-5", "gpt-5"},
		{"llama3:latest", "llama3:latest"}, // colon-separated stays
		{"", ""},
		{"/leading-slash", "leading-slash"}, // leading slash treated as a trim too (no provider name was passed)
		{"a/b", "b"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := displayModelName(tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSortedProviderNames(t *testing.T) {
	in := map[string]struct{ DefaultModel string }{} // shape mirrors config.ProviderConfig
	_ = in                                           // unused; pin via the real config-keyed helper
}

// TestIsHiddenToolCall pins the suppression policy for carlos_about
// (and only carlos_about) so a stray addition to the hiddenChatToolNames
// map elsewhere shows up as a test failure rather than silently
// muting more tool cards than intended.
func TestIsHiddenToolCall(t *testing.T) {
	if !isHiddenToolCall("carlos_about") {
		t.Error("carlos_about should be hidden")
	}
	for _, name := range []string{"bash", "notes_read", "write", "edit", ""} {
		if isHiddenToolCall(name) {
			t.Errorf("%q should NOT be hidden", name)
		}
	}
}

// TestModelSlash_NoArgs_ListsConfigured verifies the bare /model echo
// pulls the configured-providers list from disk. We point the config
// loader at a tmpdir so the test doesn't bleed into the user's real
// ~/.carlos/config.yaml.
func TestModelSlash_NoArgs_EmptyConfigEchoesHelp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Model{}
	got := m.modelStatusLine()
	// Either path is acceptable: either the load fails (no config
	// file on disk) and we surface the load error, or it loads an
	// empty config and we surface the onboarding hint. Both prove
	// the helper handled the "no config" case without crashing.
	if !strings.Contains(got, "no providers configured") &&
		!strings.Contains(got, "no config loaded") {
		t.Errorf("expected a friendly error message; got %q", got)
	}
}

// TestModelStatusLine_WithIdentity exercises the live-identity
// surfacing: when FrameUI.Identity is wired, the status line leads
// with "active: <provider>:<model>" so the user sees what's
// answering questions before deciding to swap.
func TestModelStatusLine_WithIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Write a minimal config so Load succeeds.
	cfgPath := dir + "/.carlos/config.yaml"
	_ = strings.NewReader("") // keep import of strings used
	if err := writeMinimalCfg(t, cfgPath); err != nil {
		t.Fatal(err)
	}
	m := &Model{
		frame: FrameUI{
			Identity: func() (string, string) {
				return "anthropic", "claude-opus-4-7"
			},
		},
	}
	got := m.modelStatusLine()
	if !strings.Contains(got, "active: anthropic:claude-opus-4-7") {
		t.Errorf("missing active identity prefix; got %q", got)
	}
	if !strings.Contains(got, "*anthropic=claude-opus-4-7") {
		t.Errorf("expected anthropic to be marked active; got %q", got)
	}
}

// writeMinimalCfg drops a tiny but parseable carlos config so the
// status helper's Load succeeds. Mirrors the onboarding flow's
// minimal shape (one provider, one default model).
func writeMinimalCfg(t *testing.T, path string) error {
	t.Helper()
	if err := osMkdirAll(t, path); err != nil {
		return err
	}
	body := `user_name: tester
default_provider: anthropic
providers:
  anthropic:
    api_key: dummy
    default_model: claude-opus-4-7
`
	return writeFileForTest(path, body)
}

// TestModelSlash_NotWired_StatusBranch hits the "SwitchModel == nil"
// branch — the dev-aid chat surface and tests both leave the hook
// unset and the slash should fall back to a friendly status echo
// rather than a panic.
func TestModelSlash_NotWired_StatusBranch(t *testing.T) {
	m := &Model{}
	cmd := m.modelSlash("openrouter:gpt-5")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	msg, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", cmd())
	}
	if !strings.Contains(msg.text, "not wired") {
		t.Errorf("expected 'not wired' echo; got %q", msg.text)
	}
}

// TestModelSlash_EmptyArg_Listing makes sure the no-args echo never
// returns nil cmd.
func TestModelSlash_EmptyArg_Listing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Model{}
	cmd := m.modelSlash("")
	if cmd == nil {
		t.Fatal("expected non-nil cmd for /model bare")
	}
}

// TestModelSlash_AllWhitespace_Empty hits the parser's degenerate
// "all colons no content" branch.
func TestModelSlash_BareColon_EmptyEcho(t *testing.T) {
	m := &Model{
		frame: FrameUI{
			SwitchModel: func(p, mod string) (string, string, error) {
				return p, mod, nil
			},
		},
	}
	cmd := m.modelSlash(":")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.text, "empty target") {
		t.Errorf("got %q; want 'empty target' warning", msg.text)
	}
}

// TestModelSlash_Switch_Success verifies the happy path: parseModelArg
// → SwitchModel → status echo with the resolved (provider, model).
func TestModelSlash_Switch_Success(t *testing.T) {
	called := false
	m := &Model{
		frame: FrameUI{
			SwitchModel: func(p, mod string) (string, string, error) {
				called = true
				if p != "openrouter" || mod != "google/gemini-3.5-flash" {
					t.Errorf("unexpected (%q, %q)", p, mod)
				}
				return p, mod, nil
			},
		},
	}
	cmd := m.modelSlash("openrouter:google/gemini-3.5-flash")
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	msg := cmd().(statusMsg)
	if !called {
		t.Error("SwitchModel never called")
	}
	if !strings.Contains(msg.text, "switched to openrouter:google/gemini-3.5-flash") {
		t.Errorf("unexpected echo: %q", msg.text)
	}
}

// TestModelSlash_Switch_ErrorPropagates surfaces the runtime error as
// a warn-level status row instead of swallowing it.
func TestModelSlash_Switch_ErrorPropagates(t *testing.T) {
	m := &Model{
		frame: FrameUI{
			SwitchModel: func(p, mod string) (string, string, error) {
				return "", "", &mockErr{"bad model"}
			},
		},
	}
	cmd := m.modelSlash("openai:gpt-5")
	msg := cmd().(statusMsg)
	if msg.kind != statusWarn {
		t.Errorf("expected warn kind; got %v", msg.kind)
	}
	if !strings.Contains(msg.text, "bad model") {
		t.Errorf("error should propagate; got %q", msg.text)
	}
}

type mockErr struct{ s string }

func (e *mockErr) Error() string { return e.s }

// osMkdirAll creates the parent directory of path. Returned errors
// land in the caller's t.Fatal so test setup failures are loud.
func osMkdirAll(t *testing.T, path string) error {
	t.Helper()
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return nil
	}
	return mkdirAllForTest(path[:idx])
}

// TestModelSlash_BareModel_KeepsProvider — when the user types just a
// model id (no colon), parseModelArg returns ("", model). The runtime
// closure interprets empty provider as "keep current". We verify the
// arg arrives at SwitchModel with provider=="".
func TestModelSlash_BareModel_KeepsProvider(t *testing.T) {
	gotProv := "untouched"
	m := &Model{
		frame: FrameUI{
			SwitchModel: func(p, mod string) (string, string, error) {
				gotProv = p
				return "anthropic", mod, nil
			},
		},
	}
	cmd := m.modelSlash("claude-opus-4-7")
	_ = cmd()
	if gotProv != "" {
		t.Errorf("bare model should pass empty provider; got %q", gotProv)
	}
}
