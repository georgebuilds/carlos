package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

// makeAltEnv wires a notesEnv pointing at the alt vault fixture so the
// obsidian_* tools have a target to query.
func makeAltEnv(t *testing.T) *notesEnv {
	t.Helper()
	return newNotesEnv(config.VaultConfig{
		Path: testAltVaultPath(t),
	})
}

// TestObsidianBacklinks_HappyPath drives the obsidian_backlinks tool
// against the alt vault fixture and confirms a non-error envelope is
// produced (the fixture has no incoming links so the list may be
// empty; we just need the path + vault to round-trip).
func TestObsidianBacklinks_HappyPath(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianBacklinksTool(env)
	in := []byte(`{"note":"carlos","vault":"` + testAltVaultPath(t) + `"}`)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := asMap(t, out)
	if m["target"] == nil {
		t.Errorf("expected target field: %+v", m)
	}
}

// TestObsidianBacklinks_MissingVaultField surfaces the documented
// missing-required-field envelope rather than panicking.
func TestObsidianBacklinks_MissingVaultField(t *testing.T) {
	env := newNotesEnv(config.VaultConfig{})
	tool := NewObsidianBacklinksTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`{"note":"x"}`))
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "vault") {
		t.Errorf("expected vault-required envelope; got %+v", m)
	}
}

// TestObsidianBacklinks_MissingNoteField same as above but for note.
func TestObsidianBacklinks_MissingNoteField(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianBacklinksTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "note") {
		t.Errorf("expected note-required envelope; got %+v", m)
	}
}

// TestObsidianBacklinks_BadJSON returns a parse-error envelope.
func TestObsidianBacklinks_BadJSON(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianBacklinksTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`not json`))
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "parse input") {
		t.Errorf("expected parse-input envelope; got %+v", m)
	}
}

// TestObsidianTagged_HappyPath drives obsidian_tagged.
func TestObsidianTagged_HappyPath(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianTaggedTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","tag":"other"}`)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := asMap(t, out)
	if m["tag"] != "other" {
		t.Errorf("tag should round-trip: %+v", m)
	}
}

// TestObsidianTagged_MissingTagField surfaces the standard envelope.
func TestObsidianTagged_MissingTagField(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianTaggedTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "tag") {
		t.Errorf("expected tag-required envelope; got %+v", m)
	}
}

// TestObsidianTagged_BadJSON exercises the parse-input branch.
func TestObsidianTagged_BadJSON(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianTaggedTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`nope`))
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "parse") {
		t.Errorf("expected parse error; got %+v", m)
	}
}

// TestObsidianNeighbors_HappyPath drives obsidian_neighbors.
func TestObsidianNeighbors_HappyPath(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianNeighborsTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","note":"carlos"}`)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := asMap(t, out)
	if m["note"] == nil {
		t.Errorf("note field should be populated: %+v", m)
	}
}

// TestObsidianNeighbors_MissingNoteField surfaces the standard envelope.
func TestObsidianNeighbors_MissingNoteField(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianNeighborsTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "note") {
		t.Errorf("expected note envelope; got %+v", m)
	}
}

// TestObsidianRecent_HappyPath drives obsidian_recent.
func TestObsidianRecent_HappyPath(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianRecentTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","limit":3}`)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := asMap(t, out)
	if m["vault"] == nil {
		t.Errorf("vault field should be populated: %+v", m)
	}
}

// TestObsidianRecent_BadSinceFormat surfaces the documented invalid-since
// envelope.
func TestObsidianRecent_BadSinceFormat(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianRecentTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","since":"wat"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "since") {
		t.Errorf("expected invalid-since envelope; got %+v", m)
	}
}

// TestObsidianRecent_EmptyInput works because obsidian_recent allows
// "no args" calls (just the vault is required, which we provide via
// frame fallback when there's a frame configured). Without frame and
// without vault, returns the missing-vault envelope.
func TestObsidianRecent_NoVaultIsRequired(t *testing.T) {
	env := newNotesEnv(config.VaultConfig{})
	tool := NewObsidianRecentTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`{}`))
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "vault") {
		t.Errorf("expected vault-required envelope; got %+v", m)
	}
}

// TestObsidianResolve_HappyPath drives obsidian_resolve against a real
// note in the alt vault.
func TestObsidianResolve_HappyPath(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianResolveTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","link":"carlos"}`)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := asMap(t, out)
	if m["link"] != "carlos" {
		t.Errorf("link round-trip: %+v", m)
	}
}

// TestObsidianResolve_NotFound surfaces the documented envelope.
func TestObsidianResolve_NotFound(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianResolveTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","link":"does-not-exist-xyz"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "not found") {
		t.Errorf("expected not-found envelope; got %+v", m)
	}
}

// TestObsidianResolve_MissingLinkField surfaces the standard envelope.
func TestObsidianResolve_MissingLinkField(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianResolveTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "link") {
		t.Errorf("expected link-required envelope; got %+v", m)
	}
}

// TestObsidianGet_BadJSON exercises the parse-input branch on
// ObsidianGet specifically.
func TestObsidianGet_BadJSON(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianGetTool(env)
	out, _ := tool.Execute(context.Background(), []byte(`not json`))
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "parse") {
		t.Errorf("expected parse error; got %+v", m)
	}
}

// TestObsidianGet_NoteMissingField surfaces the envelope.
func TestObsidianGet_NoteMissingField(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianGetTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "note") {
		t.Errorf("expected note-required envelope; got %+v", m)
	}
}

// TestObsidianGet_SectionNotFound - the alt fixture has no section
// matching the requested one, so we get the "section not found" envelope.
func TestObsidianGet_SectionNotFound(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianGetTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","note":"carlos","section":"NopeSection"}`)
	out, _ := tool.Execute(context.Background(), in)
	m := asMap(t, out)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "section not found") {
		t.Errorf("expected section-not-found envelope; got %+v", m)
	}
}

// TestObsidianGet_BodyOptIn exercises the in.Body == true branch.
func TestObsidianGet_BodyOptIn(t *testing.T) {
	env := makeAltEnv(t)
	tool := NewObsidianGetTool(env)
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","note":"carlos","body":true}`)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	body, _ := m["body"].(string)
	if body == "" {
		t.Errorf("body should be populated when body=true")
	}
}

// TestObsidianGet_FrameWithExplicitVault verifies that an explicit
// vault: overrides any frame: shorthand (the latter only labels).
func TestObsidianGet_FrameAndVaultBothSet(t *testing.T) {
	env := newNotesEnvWithFrames(
		config.VaultConfig{Path: testVaultPath(t)},
		frame.Config{
			Default: "personal",
			Active:  "personal",
			List: []frame.Frame{
				{Name: "personal", VaultSubtree: ""},
			},
		},
		"personal",
	)
	tool := NewObsidianGetTool(env)
	// Pass an explicit vault that points to alt, with frame=personal.
	in := []byte(`{"vault":"` + testAltVaultPath(t) + `","note":"carlos","frame":"personal"}`)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(t, out)
	title, _ := m["title"].(string)
	if !strings.Contains(title, "alt") {
		t.Errorf("explicit vault should win over frame; title=%q", title)
	}
}

// TestAsString_HappyPath proves the helper returns the string value.
func TestAsString_HappyPath(t *testing.T) {
	got, err := asString([]byte(`{"name":"  alice  "}`), "name")
	if err != nil {
		t.Fatalf("asString: %v", err)
	}
	if got != "alice" {
		t.Errorf("trimmed string: %q", got)
	}
}

// TestAsString_MissingField surfaces the documented error.
func TestAsString_MissingField(t *testing.T) {
	_, err := asString([]byte(`{}`), "name")
	if err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("missing field err: %v", err)
	}
}

// TestAsString_WrongType surfaces a clean error when the JSON value
// is not a string.
func TestAsString_WrongType(t *testing.T) {
	_, err := asString([]byte(`{"name":42}`), "name")
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Errorf("wrong-type err: %v", err)
	}
}

// TestAsString_BadJSON wraps the parse failure.
func TestAsString_BadJSON(t *testing.T) {
	_, err := asString([]byte(`not json`), "name")
	if err == nil || !strings.Contains(err.Error(), "parse input") {
		t.Errorf("parse err: %v", err)
	}
}

// TestRegistry_ExecuteHappyPath drives the dispatcher with a tool that
// returns a known marshaled payload.
func TestRegistry_ExecuteHappyPath(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "echo", out: []byte("hi")})
	got, err := r.Execute(context.Background(), "echo", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Errorf("Execute output: %q", got)
	}
}

// TestRegistry_ExecuteUnknownTool surfaces an error rather than
// returning nil silently.
func TestRegistry_ExecuteUnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := r.Execute(context.Background(), "nope", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected unknown-tool err; got %v", err)
	}
}

// fakeTool is a minimal Tool implementation used by the dispatcher
// tests.
type fakeTool struct {
	name string
	out  []byte
	err  error
}

func (f *fakeTool) Name() string                                    { return f.name }
func (f *fakeTool) Description() string                             { return "fake" }
func (f *fakeTool) Schema() []byte                                  { return []byte(`{"type":"object"}`) }
func (f *fakeTool) Execute(context.Context, []byte) ([]byte, error) { return f.out, f.err }

// TestCappedWriter_OverflowDiscards confirms the cappedWriter caps the
// payload at .max but still reports len(p) so the io.Copy loop never
// stalls. The writer does NOT emit a marker — Execute owns that — but
// it does track the discarded count so Execute can render an honest
// "[truncated, N more bytes]" tail.
func TestCappedWriter_OverflowDiscards(t *testing.T) {
	buf := &bytes.Buffer{}
	cw := &cappedWriter{buf: buf, max: 10}
	n, err := cw.Write([]byte("0123456789ABCDEF"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 16 {
		t.Errorf("Write should report full len; got %d", n)
	}
	if buf.Len() != 10 {
		t.Errorf("buf len = %d, want 10 (cap, no marker)", buf.Len())
	}
	if string(buf.Bytes()) != "0123456789" {
		t.Errorf("buf payload mismatch: %q", buf.Bytes())
	}
	if bytes.Contains(buf.Bytes(), []byte("truncated")) {
		t.Errorf("cappedWriter must not write its own marker: %q", buf.Bytes())
	}
	if got := cw.Discarded(); got != 6 {
		t.Errorf("Discarded = %d, want 6", got)
	}
	// Subsequent write should report success and grow the discard count
	// while leaving the in-buf payload untouched.
	n, err = cw.Write([]byte("more"))
	if err != nil || n != 4 || buf.Len() != 10 {
		t.Errorf("post-cap write: n=%d err=%v buflen=%d want=10", n, err, buf.Len())
	}
	if got := cw.Discarded(); got != 10 {
		t.Errorf("Discarded after second write = %d, want 10", got)
	}
}

// TestCappedWriter_NoOverflow takes the easy path: input smaller than
// max gets written verbatim.
func TestCappedWriter_NoOverflow(t *testing.T) {
	buf := &bytes.Buffer{}
	cw := &cappedWriter{buf: buf, max: 100}
	n, err := cw.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	if buf.String() != "hello" {
		t.Errorf("buf: %q", buf.String())
	}
}

// TestGitignore_RootReturnsAbsolute confirms the Root accessor returns
// an absolute path matching the load argument (canonicalised).
func TestGitignore_RootReturnsAbsolute(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, ".gitignore"), "*.log\n")
	ig, err := LoadIgnorer(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(ig.Root()) {
		t.Errorf("Root should be absolute; got %q", ig.Root())
	}
	if ig.Root() != tmp {
		// We expect the same path since tmp from t.TempDir is already abs.
		t.Errorf("Root: got %q, want %q", ig.Root(), tmp)
	}
}

// TestIsPrivateIP_AllBranches exercises every code path: nil, loopback,
// unspecified, link-local, multicast, private, and a public address.
func TestIsPrivateIP_AllBranches(t *testing.T) {
	cases := []struct {
		ip    string
		want  bool
		nilIP bool
	}{
		{"", false, true}, // nil
		{"127.0.0.1", true, false},
		{"0.0.0.0", true, false},
		{"169.254.0.1", true, false},
		{"224.0.0.1", true, false},
		{"10.0.0.5", true, false},
		{"192.168.1.1", true, false},
		{"172.16.0.1", true, false},
		{"8.8.8.8", false, false},
		{"2606:4700:4700::1111", false, false},
	}
	for _, c := range cases {
		var ip net.IP
		if !c.nilIP {
			ip = net.ParseIP(c.ip)
			if ip == nil {
				t.Errorf("bad fixture: %q", c.ip)
				continue
			}
		}
		got, why := isPrivateIP(ip)
		if got != c.want {
			t.Errorf("isPrivateIP(%q) = %v (%q), want %v", c.ip, got, why, c.want)
		}
	}
}

// TestIsPrivateHost_LocalhostHostname covers the literal "localhost"
// path and the ".localhost" suffix path.
func TestIsPrivateHost_LocalhostHostname(t *testing.T) {
	cases := []string{"localhost", "LocalHost", "myhost.localhost", "localhost.", "127.0.0.1"}
	for _, h := range cases {
		got, _ := isPrivateHost(h)
		if !got {
			t.Errorf("isPrivateHost(%q) = false, want true", h)
		}
	}
}

// TestIsPrivateHost_PublicLiteralIP - a public IP literal passes.
func TestIsPrivateHost_PublicLiteralIP(t *testing.T) {
	got, _ := isPrivateHost("8.8.8.8:80")
	if got {
		t.Errorf("8.8.8.8 should not be private")
	}
}

// TestRobotsAllows covers the disallow-prefix branch + the "always
// allow" empty-disallows branch.
func TestRobotsAllows(t *testing.T) {
	cases := []struct {
		disallows []string
		path      string
		want      bool
	}{
		{nil, "/page", true},
		{[]string{"/"}, "/x", false},
		{[]string{"/private/"}, "/private/page", false},
		{[]string{"/private/"}, "/public/page", true},
		{[]string{""}, "/x", true}, // empty strings skipped
		{[]string{"/a", "/b"}, "/c", true},
	}
	for _, c := range cases {
		got := robotsAllows(c.disallows, c.path)
		if got != c.want {
			t.Errorf("robotsAllows(%v,%q) = %v, want %v", c.disallows, c.path, got, c.want)
		}
	}
}

// TestParseRobots covers the common shapes: User-agent: *, ignored
// non-star group, Disallow path, comment lines.
func TestParseRobots(t *testing.T) {
	body := `# comment
User-agent: googlebot
Disallow: /not-us/

User-agent: *
Disallow: /private/
Disallow: /admin/  # trailing comment
# orphan comment
Random-key: ignored
`
	got := parseRobots(body)
	if len(got) != 2 {
		t.Fatalf("expected 2 disallows for *, got %v", got)
	}
	if got[0] != "/private/" || got[1] != "/admin/" {
		t.Errorf("disallows: %v", got)
	}
}

// TestHumanBytes covers the three formatting branches.
func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:                    "512 B",
		2 * 1024:               "2 KiB",
		3 * 1024 * 1024:        "3 MiB",
		5 * 1024 * 1024 * 1024: "5120 MiB",
	}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestRequireTextContentType - text/* allowed, xhtml allowed, anything
// else rejected.
func TestRequireTextContentType(t *testing.T) {
	good := []string{"text/html", "text/plain", "text/markdown", "TEXT/HTML",
		"text/html; charset=utf-8", "application/xhtml+xml"}
	bad := []string{"application/json", "image/png", "video/mp4", "application/pdf"}
	for _, g := range good {
		if err := requireTextContentType(g); err != nil {
			t.Errorf("text content %q rejected: %v", g, err)
		}
	}
	for _, b := range bad {
		if err := requireTextContentType(b); err == nil {
			t.Errorf("non-text content %q should be rejected", b)
		}
	}
}

// TestNormalizeWhitespace_EdgeCases covers an empty input, a single
// paragraph, tabs vs newlines.
func TestNormalizeWhitespace_EdgeCases(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"   ":               "",
		"hello":             "hello",
		"a\nb":              "a b",
		"line1\n\nline2":    "line1\n\nline2",
		"x\t y\n z\n\n\n a": "x y z\n\na",
	}
	for in, want := range cases {
		if got := normalizeWhitespace(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCarlosAbout_AllSectionEnvelope drives the full-introspection
// path and confirms every section field is present.
func TestCarlosAbout_AllSectionEnvelope(t *testing.T) {
	cfg := config.VaultConfig{Path: "/home/user/vault", Exclude: []string{"templates/**"}}
	frames := frame.Config{
		Default: "personal", Active: "personal",
		List: []frame.Frame{
			{Name: "personal", Provider: "anthropic", Model: "m"},
			{Name: "work", Provider: "openrouter", Model: "n"},
		},
	}
	tool := NewCarlosAboutTool(cfg, frames, "personal", map[string]ProviderSummary{
		"anthropic":  {HasKey: true, DefaultModel: "m"},
		"openrouter": {HasKey: true, DefaultModel: "n"},
	}, "Tester")
	out, err := tool.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["user"] != "Tester" {
		t.Errorf("user: %v", m["user"])
	}
	if _, has := m["vault"]; !has {
		t.Errorf("vault field missing: %+v", m)
	}
	if frames, _ := m["frames"].([]any); len(frames) != 2 {
		t.Errorf("frames count: %d", len(frames))
	}
	if providers, _ := m["providers"].(map[string]any); len(providers) != 2 {
		t.Errorf("providers: %+v", m["providers"])
	}
}

// TestCarlosAbout_SectionFilter - passing section=vault returns only
// the vault block.
func TestCarlosAbout_SectionFilter(t *testing.T) {
	tool := NewCarlosAboutTool(
		config.VaultConfig{Path: "/v"},
		frame.Config{},
		"",
		nil,
		"Tester",
	)
	out, err := tool.Execute(context.Background(), []byte(`{"section":"vault"}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["vault"]; !has {
		t.Errorf("vault should be present")
	}
	if _, has := m["frames"]; has {
		t.Errorf("frames should be omitted when section=vault")
	}
	if _, has := m["user"]; has {
		t.Errorf("user should be omitted when section=vault")
	}
}

// TestCarlosAbout_UnknownSectionRejected - guards the enum validation
// so a future caller can't sneak a typo through.
func TestCarlosAbout_UnknownSectionRejected(t *testing.T) {
	tool := NewCarlosAboutTool(config.VaultConfig{}, frame.Config{}, "", nil, "")
	_, err := tool.Execute(context.Background(), []byte(`{"section":"bogus"}`))
	if err == nil {
		t.Fatal("expected unknown-section error")
	}
	if !strings.Contains(err.Error(), "unknown section") {
		t.Errorf("error should mention unknown section; got %v", err)
	}
}

// TestCarlosAbout_BadJSONInput surfaces a parse error.
func TestCarlosAbout_BadJSONInput(t *testing.T) {
	tool := NewCarlosAboutTool(config.VaultConfig{}, frame.Config{}, "", nil, "")
	_, err := tool.Execute(context.Background(), []byte(`{`))
	if err == nil || !strings.Contains(err.Error(), "parse input") {
		t.Errorf("expected parse-input err; got %v", err)
	}
}

// TestCarlosAbout_EmptyInputAccepted matches the "no args allowed"
// surface: `carlos_about {}` returns everything.
func TestCarlosAbout_EmptyInputAccepted(t *testing.T) {
	tool := NewCarlosAboutTool(config.VaultConfig{Path: "/v"}, frame.Config{}, "", nil, "Tester")
	out, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("Tester")) {
		t.Errorf("user not in output: %s", out)
	}
}

// TestProviderSummariesFromConfig - empty in, empty out; non-empty in
// surfaces HasKey/HasBaseURL flags + the default model.
func TestProviderSummariesFromConfig(t *testing.T) {
	if got := ProviderSummariesFromConfig(nil); got != nil {
		t.Errorf("nil in should produce nil out; got %v", got)
	}
	got := ProviderSummariesFromConfig(map[string]config.ProviderConfig{
		"anthropic": {APIKey: "sk-x", DefaultModel: "m"},
		"ollama":    {BaseURL: "http://localhost:11434"},
	})
	if !got["anthropic"].HasKey {
		t.Errorf("anthropic HasKey false")
	}
	if got["anthropic"].DefaultModel != "m" {
		t.Errorf("default model: %q", got["anthropic"].DefaultModel)
	}
	if !got["ollama"].HasBaseURL {
		t.Errorf("ollama HasBaseURL false")
	}
	if got["ollama"].HasKey {
		t.Errorf("ollama HasKey should be false")
	}
}

// TestFlattenCapabilities - empty map, map with non-string backend,
// and a happy path.
func TestFlattenCapabilities(t *testing.T) {
	if got := flattenCapabilities(frame.Frame{}); got != nil {
		t.Errorf("empty -> nil; got %v", got)
	}
	if got := flattenCapabilities(frame.Frame{
		Capabilities: map[string]map[string]any{
			"web":    {"backend": 42}, // non-string backend dropped
			"search": {"backend": ""}, // empty backend dropped
		},
	}); got != nil {
		t.Errorf("invalid backends should drop -> nil; got %v", got)
	}
	got := flattenCapabilities(frame.Frame{
		Capabilities: map[string]map[string]any{
			"web":    {"backend": "ddg"},
			"search": {"backend": "brave"},
		},
	})
	if got["web"] != "ddg" || got["search"] != "brave" {
		t.Errorf("flatten: %v", got)
	}
}

// TestSortedProviders - empty in -> nil; populated in -> stable key
// order.
func TestSortedProviders(t *testing.T) {
	if got := sortedProviders(nil); got != nil {
		t.Errorf("nil in -> nil out; got %v", got)
	}
	out := sortedProviders(map[string]ProviderSummary{
		"z": {HasKey: true},
		"a": {HasKey: true},
		"m": {HasKey: true},
	})
	if len(out) != 3 {
		t.Errorf("count: %d", len(out))
	}
	for k := range out {
		if k == "" {
			t.Errorf("empty key sneaked in")
		}
	}
}

// TestResolveBaseDir handles absolute paths verbatim and relative paths
// joined against the base.
func TestResolveBaseDir(t *testing.T) {
	cases := []struct {
		base, in, want string
	}{
		{"", "x.txt", "x.txt"},
		{"/base", "x.txt", filepath.Join("/base", "x.txt")},
		{"/base", "/abs/path.txt", "/abs/path.txt"},
	}
	for _, c := range cases {
		got, err := resolveBaseDir(c.base, c.in)
		if err != nil {
			t.Errorf("resolveBaseDir(%q,%q) unexpected error: %v", c.base, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("resolveBaseDir(%q,%q) = %q, want %q", c.base, c.in, got, c.want)
		}
	}
}

// TestObsidianTools_FrameShorthand drives every obsidian_* tool with
// only `frame:` (no `vault:`), which routes through the configured
// vault's frame subtree. Hits the otherwise-untested frame-only branch
// of resolveObsidianVault.
func TestObsidianTools_FrameShorthand(t *testing.T) {
	env := newNotesEnvWithFrames(
		config.VaultConfig{Path: testVaultPath(t)},
		frame.Config{
			Default: "personal", Active: "personal",
			List: []frame.Frame{
				{Name: "personal", VaultSubtree: ""},
			},
		},
		"personal",
	)
	// obsidian_get
	out, err := NewObsidianGetTool(env).Execute(context.Background(),
		[]byte(`{"note":"carlos","frame":"personal"}`))
	if err != nil {
		t.Fatalf("obsidian_get with frame: %v", err)
	}
	m := asMap(t, out)
	if m["path"] != "carlos.md" {
		t.Errorf("obsidian_get frame: %+v", m)
	}

	// obsidian_search
	out, err = NewObsidianSearchTool(env).Execute(context.Background(),
		[]byte(`{"query":"carlos","frame":"personal"}`))
	if err != nil {
		t.Fatal(err)
	}
	asMap(t, out)

	// obsidian_tagged
	out, err = NewObsidianTaggedTool(env).Execute(context.Background(),
		[]byte(`{"tag":"core","frame":"personal"}`))
	if err != nil {
		t.Fatal(err)
	}
	asMap(t, out)

	// obsidian_neighbors
	out, err = NewObsidianNeighborsTool(env).Execute(context.Background(),
		[]byte(`{"note":"carlos","frame":"personal"}`))
	if err != nil {
		t.Fatal(err)
	}
	asMap(t, out)

	// obsidian_recent
	out, err = NewObsidianRecentTool(env).Execute(context.Background(),
		[]byte(`{"frame":"personal","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	asMap(t, out)

	// obsidian_resolve
	out, err = NewObsidianResolveTool(env).Execute(context.Background(),
		[]byte(`{"link":"carlos","frame":"personal"}`))
	if err != nil {
		t.Fatal(err)
	}
	asMap(t, out)

	// obsidian_backlinks
	out, err = NewObsidianBacklinksTool(env).Execute(context.Background(),
		[]byte(`{"note":"carlos","frame":"personal"}`))
	if err != nil {
		t.Fatal(err)
	}
	asMap(t, out)
}

// TestObsidianTools_UnknownFrameRejected - passing an explicit frame
// that doesn't exist surfaces the unknown-frame envelope for each tool
// in the family.
func TestObsidianTools_UnknownFrameRejected(t *testing.T) {
	env := newNotesEnvWithFrames(
		config.VaultConfig{Path: testVaultPath(t)},
		frame.Config{Default: "personal", Active: "personal", List: []frame.Frame{{Name: "personal"}}},
		"personal",
	)
	bad := []byte(`{"note":"carlos","frame":"ghost","vault":"` + testVaultPath(t) + `"}`)
	cases := []struct {
		name string
		tool Tool
		in   []byte
	}{
		{"get", NewObsidianGetTool(env), bad},
		{"backlinks", NewObsidianBacklinksTool(env), bad},
		{"neighbors", NewObsidianNeighborsTool(env), bad},
		{"resolve", NewObsidianResolveTool(env), []byte(`{"link":"carlos","frame":"ghost","vault":"` + testVaultPath(t) + `"}`)},
		{"tagged", NewObsidianTaggedTool(env), []byte(`{"tag":"core","frame":"ghost","vault":"` + testVaultPath(t) + `"}`)},
		{"search", NewObsidianSearchTool(env), []byte(`{"query":"carlos","frame":"ghost","vault":"` + testVaultPath(t) + `"}`)},
		{"recent", NewObsidianRecentTool(env), []byte(`{"frame":"ghost","vault":"` + testVaultPath(t) + `"}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _ := c.tool.Execute(context.Background(), c.in)
			m := asMap(t, out)
			if msg, _ := m["error"].(string); !strings.Contains(msg, "unknown frame") {
				t.Errorf("expected unknown-frame envelope; got %+v", m)
			}
		})
	}
}

// TestActiveFrame_NoFramesWired returns nil so callers downgrade to
// legacy single-shelf behaviour without errors.
func TestActiveFrame_NoFramesWired(t *testing.T) {
	env := newNotesEnv(config.VaultConfig{Path: "/v"})
	if env.activeFrame() != nil {
		t.Errorf("no frames -> activeFrame should be nil")
	}
}

// TestActiveFrame_DefaultsToFrameConfigDefault - when env.active is
// empty and frames.Active is empty, fall back to frames.Default.
func TestActiveFrame_DefaultsToFrameConfigDefault(t *testing.T) {
	env := newNotesEnvWithFrames(
		config.VaultConfig{Path: "/v"},
		frame.Config{
			Default: "personal", Active: "",
			List: []frame.Frame{{Name: "personal"}, {Name: "work"}},
		},
		"",
	)
	af := env.activeFrame()
	if af == nil || af.Name != "personal" {
		t.Errorf("expected personal default; got %+v", af)
	}
}

// TestCleanSubtree exercises the normalisation rules.
func TestCleanSubtree(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"  ":          "",
		"/work":       "work",
		"work/":       "work",
		"work/notes/": "work/notes",
		".":           "",
		"work/./x":    "work/x",
	}
	for in, want := range cases {
		if got := cleanSubtree(in); got != want {
			t.Errorf("cleanSubtree(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInSubtree edges.
func TestInSubtree(t *testing.T) {
	cases := []struct {
		path, subtree string
		want          bool
	}{
		{"any/path.md", "", true},
		{"work/notes.md", "work", true},
		{"work", "work", true},
		{"workshop/notes.md", "work", false},
		{"work-extra/notes.md", "work", false},
		{"work/deep/n.md", "work", true},
	}
	for _, c := range cases {
		if got := inSubtree(c.path, c.subtree); got != c.want {
			t.Errorf("inSubtree(%q,%q) = %v, want %v", c.path, c.subtree, got, c.want)
		}
	}
}

// TestJSONOK_HappyPath - round-trips a struct through the marshal
// helper.
func TestJSONOK_HappyPath(t *testing.T) {
	type x struct {
		A int `json:"a"`
	}
	got, err := jsonOK(x{A: 7})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":7}` {
		t.Errorf("jsonOK: %s", got)
	}
}

// TestJSONOK_MarshalErrorSurfaces - a value json/encoding can't
// marshal (a channel) surfaces a wrapped error.
func TestJSONOK_MarshalErrorSurfaces(t *testing.T) {
	_, err := jsonOK(make(chan int))
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("wrap should mention marshal; got %v", err)
	}
}

// TestJSONErr formats the envelope properly.
func TestJSONErr(t *testing.T) {
	got, err := jsonErr("hello %s", "world")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"error":"hello world"}` {
		t.Errorf("jsonErr: %s", got)
	}
}

// TestWebFetchClient_BuildsDefaultClient covers the no-Client-injected
// branch + the CheckRedirect enforcement.
func TestWebFetchClient_BuildsDefaultClient(t *testing.T) {
	tool := NewWebFetchTool() // zero-value
	c := tool.client()
	if c == nil {
		t.Fatal("client should never be nil")
	}
	if c.Timeout != defaultWebFetchTimeout {
		t.Errorf("default timeout: %v", c.Timeout)
	}
	// Build a chain of fake requests to trip CheckRedirect.
	req, _ := http.NewRequest("GET", "http://x/y", nil)
	var via []*http.Request
	for i := 0; i < defaultWebFetchMaxRedirects+1; i++ {
		via = append(via, req)
	}
	err := c.CheckRedirect(req, via)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("CheckRedirect should refuse beyond cap; got %v", err)
	}
	err = c.CheckRedirect(req, via[:1])
	if err != nil {
		t.Errorf("CheckRedirect within cap should be nil; got %v", err)
	}
}

// TestWebFetchClient_HonorsExplicitTimeout - custom Timeout flows
// into the constructed *http.Client.
func TestWebFetchClient_HonorsExplicitTimeout(t *testing.T) {
	tool := &WebFetchTool{Timeout: 2 * time.Hour, MaxRedirects: 7}
	c := tool.client()
	if c.Timeout != 2*time.Hour {
		t.Errorf("custom timeout: %v", c.Timeout)
	}
	// Custom MaxRedirects is reflected in CheckRedirect cap.
	req, _ := http.NewRequest("GET", "http://x/y", nil)
	via := make([]*http.Request, 7)
	for i := range via {
		via[i] = req
	}
	if err := c.CheckRedirect(req, via); err == nil {
		t.Errorf("custom cap should still refuse beyond %d", 7)
	}
}

// TestWebFetchClient_InjectedClientReused - when a Client is provided,
// it's returned verbatim without rebuilding.
func TestWebFetchClient_InjectedClientReused(t *testing.T) {
	custom := &http.Client{Timeout: 99 * time.Millisecond}
	tool := &WebFetchTool{Client: custom}
	if tool.client() != custom {
		t.Errorf("injected client should be returned verbatim")
	}
}

// TestCloseBody_NilSafe - closeBody on a nil response should not panic.
func TestCloseBody_NilSafe(t *testing.T) {
	closeBody(nil)
	// Also handle a response with a nil Body.
	closeBody(&http.Response{})
}

// TestGlob_AnchoredPattern - leading "/" pattern matches only at the
// root, not recursively.
func TestGlob_AnchoredPattern(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.go"), "")
	writeFile(t, filepath.Join(root, "sub", "x.go"), "")
	in, _ := json.Marshal(map[string]any{"pattern": "/x.go", "root": root})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("x.go")) {
		t.Errorf("root x.go missing: %q", out)
	}
	if bytes.Contains(out, []byte("sub/x.go")) {
		t.Errorf("anchored should not match sub: %q", out)
	}
}

// TestGlob_BaseDirFallback - when no root is provided but the tool
// has a BaseDir, that's used as the walk root.
func TestGlob_BaseDirFallback(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.txt"), "")
	tool := NewGlobTool()
	tool.BaseDir = root
	in, _ := json.Marshal(map[string]any{"pattern": "*.txt"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("x.txt")) {
		t.Errorf("BaseDir fallback failed: %q", out)
	}
}

// TestGlob_RespectGitignoreFalseShowsIgnored - opting out of gitignore
// respect surfaces normally-hidden files.
func TestGlob_RespectGitignoreFalseShowsIgnored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "secrets.txt\n")
	writeFile(t, filepath.Join(root, "secrets.txt"), "leak")
	respect := false
	in, _ := json.Marshal(map[string]any{
		"pattern":           "*.txt",
		"root":              root,
		"respect_gitignore": respect,
	})
	out, err := NewGlobTool().Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("secrets.txt")) {
		t.Errorf("opt-out should surface ignored files: %q", out)
	}
}

// TestErrorImportSatisfied silences the carlos_about unused-import
// pin without depending on test ordering.
func TestErrorImportSatisfied(_ *testing.T) {
	_ = errors.New("x")
}
