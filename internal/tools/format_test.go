package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
)

// testFormatter builds a Formatter from cfg, then swaps in fakes for the
// PATH lookup and the runner so selection/substitution can be asserted
// without any real formatter binary. The returned pointer's lastArgv is
// updated on each run; found controls whether the binary is "installed".
func testFormatter(cfg config.FormatterConfig, found bool, runErr error) (*Formatter, *[]string) {
	f := NewFormatter(cfg)
	captured := new([]string)
	f.lookPath = func(bin string) (string, error) {
		if found {
			return "/fake/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}
	f.run = func(_ context.Context, _ string, argv []string) error {
		*captured = argv
		return runErr
	}
	return f, captured
}

func TestNewFormatterBuiltinExtensionIndex(t *testing.T) {
	f := NewFormatter(config.FormatterConfig{})
	cases := map[string]string{
		".go":    "gofmt",
		".ts":    "prettier",
		".tsx":   "prettier",
		".json":  "prettier",
		".md":    "prettier",
		".py":    "ruff",
		".rs":    "rustfmt",
		".sh":    "shfmt",
		".c":     "clang-format",
		".cpp":   "clang-format",
		".zig":   "zig",
		".php":   "pint",
		".kt":    "ktlint",
		".kts":   "ktlint",
		".java":  "google-java-format",
		".scala": "scalafmt",
		".sc":    "scalafmt",
		".toml":  "taplo",
	}
	for ext, want := range cases {
		if got := f.byExt[ext]; got != want {
			t.Errorf("ext %s: want formatter %q got %q", ext, want, got)
		}
	}
	if _, ok := f.byExt[".unknownext"]; ok {
		t.Error("unexpected formatter mapped for .unknownext")
	}
}

func TestFormatSelectsByExtensionAndSubstitutesFile(t *testing.T) {
	f, argv := testFormatter(config.FormatterConfig{}, true, nil)
	res := f.Format(context.Background(), "/work/src/main.go")
	if !res.Ran || res.Name != "gofmt" {
		t.Fatalf("want gofmt ran; got %+v", res)
	}
	want := []string{"gofmt", "-w", "/work/src/main.go"}
	if strings.Join(*argv, " ") != strings.Join(want, " ") {
		t.Errorf("argv: want %v got %v", want, *argv)
	}
	if res.Note() != "formatted with gofmt" {
		t.Errorf("note: got %q", res.Note())
	}
}

func TestFormatExtensionIsCaseInsensitive(t *testing.T) {
	f, _ := testFormatter(config.FormatterConfig{}, true, nil)
	if res := f.Format(context.Background(), "/x/MAIN.GO"); res.Name != "gofmt" {
		t.Errorf("uppercase extension not matched: %+v", res)
	}
}

func TestFormatNoMatchingFormatter(t *testing.T) {
	f, argv := testFormatter(config.FormatterConfig{}, true, nil)
	res := f.Format(context.Background(), "/x/data.bin")
	if res.Ran || res.Name != "" || res.Err != nil {
		t.Errorf("unmatched ext should be silent zero; got %+v", res)
	}
	if res.Note() != "" {
		t.Errorf("note for no match should be empty; got %q", res.Note())
	}
	if len(*argv) != 0 {
		t.Error("runner should not fire when no formatter matches")
	}
}

func TestFormatBinaryAbsentIsSilent(t *testing.T) {
	f, argv := testFormatter(config.FormatterConfig{}, false, nil)
	res := f.Format(context.Background(), "/x/main.go")
	if res.Ran || res.Err != nil {
		t.Errorf("absent binary should not run or error; got %+v", res)
	}
	if res.Note() != "" {
		t.Errorf("absent binary should be silent; got note %q", res.Note())
	}
	if len(*argv) != 0 {
		t.Error("runner should not fire when binary is absent")
	}
}

func TestFormatRunFailureIsBestEffort(t *testing.T) {
	f, _ := testFormatter(config.FormatterConfig{}, true, errors.New("boom"))
	res := f.Format(context.Background(), "/x/main.go")
	if res.Ran {
		t.Error("failed run must not report Ran=true")
	}
	if res.Err == nil {
		t.Fatal("failed run should set Err")
	}
	if !strings.Contains(res.Note(), "formatter gofmt failed") {
		t.Errorf("note should describe failure; got %q", res.Note())
	}
}

func TestFormatGloballyDisabled(t *testing.T) {
	f, argv := testFormatter(config.FormatterConfig{Disabled: true}, true, nil)
	if res := f.Format(context.Background(), "/x/main.go"); res.Ran || res.Name != "" {
		t.Errorf("disabled formatter must be a no-op; got %+v", res)
	}
	if len(*argv) != 0 {
		t.Error("runner should not fire when globally disabled")
	}
}

func TestNilFormatterIsNoOp(t *testing.T) {
	var f *Formatter
	if res := f.Format(context.Background(), "/x/main.go"); res.Ran || res.Name != "" {
		t.Errorf("nil formatter must be a no-op; got %+v", res)
	}
}

func TestFormatConfigDisablesOneBuiltin(t *testing.T) {
	cfg := config.FormatterConfig{Formatters: map[string]config.FormatterSpec{
		"gofmt": {Disabled: true},
	}}
	f, _ := testFormatter(cfg, true, nil)
	if res := f.Format(context.Background(), "/x/main.go"); res.Name != "" {
		t.Errorf("disabled gofmt should not match .go; got %+v", res)
	}
	// A sibling built-in is untouched.
	if res := f.Format(context.Background(), "/x/app.py"); res.Name != "ruff" {
		t.Errorf("ruff should still match .py; got %+v", res)
	}
}

func TestFormatConfigOverridesBuiltinCommand(t *testing.T) {
	cfg := config.FormatterConfig{Formatters: map[string]config.FormatterSpec{
		"gofmt": {Command: []string{"gofumpt", "-w", "$FILE"}},
	}}
	f, argv := testFormatter(cfg, true, nil)
	res := f.Format(context.Background(), "/x/main.go")
	if !res.Ran || (*argv)[0] != "gofumpt" {
		t.Errorf("override command not used; argv=%v res=%+v", *argv, res)
	}
}

func TestFormatConfigAddsNewFormatter(t *testing.T) {
	cfg := config.FormatterConfig{Formatters: map[string]config.FormatterSpec{
		"myfmt": {Command: []string{"myfmt", "--write", "$FILE"}, Extensions: []string{".xyz"}},
	}}
	f, argv := testFormatter(cfg, true, nil)
	res := f.Format(context.Background(), "/x/thing.xyz")
	if !res.Ran || res.Name != "myfmt" {
		t.Fatalf("custom formatter not registered; got %+v", res)
	}
	if (*argv)[0] != "myfmt" || (*argv)[2] != "/x/thing.xyz" {
		t.Errorf("custom argv wrong: %v", *argv)
	}
}

func TestFormatConfigWinsExtensionCollision(t *testing.T) {
	// A custom formatter that claims .go must override the built-in gofmt.
	cfg := config.FormatterConfig{Formatters: map[string]config.FormatterSpec{
		"mygo": {Command: []string{"mygo", "$FILE"}, Extensions: []string{".go"}},
	}}
	f, _ := testFormatter(cfg, true, nil)
	if res := f.Format(context.Background(), "/x/main.go"); res.Name != "mygo" {
		t.Errorf("config formatter should win .go collision; got %+v", res)
	}
}

func TestFormatConfigExtensionWithoutLeadingDot(t *testing.T) {
	// A user who writes the extension without a dot still gets a match.
	cfg := config.FormatterConfig{Formatters: map[string]config.FormatterSpec{
		"myfmt": {Command: []string{"myfmt", "$FILE"}, Extensions: []string{"xyz"}},
	}}
	f, _ := testFormatter(cfg, true, nil)
	if res := f.Format(context.Background(), "/x/thing.xyz"); res.Name != "myfmt" {
		t.Errorf("dotless extension should normalize and match; got %+v", res)
	}
}

func TestFormatResolvesRelativeAndLeadingDashPath(t *testing.T) {
	// A relative path whose name begins with "-" must reach the formatter as
	// an absolute path so it can never be parsed as a flag.
	f, argv := testFormatter(config.FormatterConfig{}, true, nil)
	res := f.Format(context.Background(), "-weird.go")
	if !res.Ran {
		t.Fatalf("expected run; got %+v", res)
	}
	last := (*argv)[len(*argv)-1]
	if !filepath.IsAbs(last) {
		t.Errorf("path arg should be absolute, not a dash-led flag; got %q", last)
	}
	if strings.HasPrefix(last, "-") {
		t.Errorf("path arg must not start with '-'; got %q", last)
	}
}

func TestFormatTimeoutDoesNotHang(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	// A formatter that sleeps far past the deadline must be killed and
	// reported as a failure, not block the call.
	cfg := config.FormatterConfig{Formatters: map[string]config.FormatterSpec{
		"sleeper": {Command: []string{"sh", "-c", "sleep 30"}, Extensions: []string{".slow"}},
	}}
	f := NewFormatter(cfg)
	f.timeout = 250 * time.Millisecond
	done := make(chan FormatResult, 1)
	go func() { done <- f.Format(context.Background(), "/tmp/x.slow") }()
	select {
	case res := <-done:
		if res.Ran {
			t.Errorf("a killed formatter must not report success: %+v", res)
		}
		if res.Err == nil {
			t.Error("a killed formatter should report an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Format hung past the deadline")
	}
}

func TestSubstituteFile(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{"token replaced", []string{"fmt", "-w", "$FILE"}, []string{"fmt", "-w", "/p/f.go"}},
		{"appended when absent", []string{"fmt", "-w"}, []string{"fmt", "-w", "/p/f.go"}},
		{"multiple tokens", []string{"fmt", "$FILE", "$FILE"}, []string{"fmt", "/p/f.go", "/p/f.go"}},
		{"embedded in arg", []string{"fmt", "--path=$FILE"}, []string{"fmt", "--path=/p/f.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := substituteFile(tc.argv, "/p/f.go")
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("want %v got %v", tc.want, got)
			}
		})
	}
}

func TestFormatResultNote(t *testing.T) {
	cases := []struct {
		res  FormatResult
		want string
	}{
		{FormatResult{Name: "gofmt", Ran: true}, "formatted with gofmt"},
		{FormatResult{Name: "gofmt", Err: errors.New("x")}, "formatter gofmt failed: x"},
		{FormatResult{Name: "gofmt"}, ""},
		{FormatResult{}, ""},
	}
	for _, tc := range cases {
		if got := tc.res.Note(); got != tc.want {
			t.Errorf("Note(%+v): want %q got %q", tc.res, tc.want, got)
		}
	}
}

func TestTruncateFormatOutput(t *testing.T) {
	if got := truncateFormatOutput("a\nb"); got != "a b" {
		t.Errorf("newlines should fold to spaces; got %q", got)
	}
	long := strings.Repeat("x", 300)
	got := truncateFormatOutput(long)
	if len(got) != 203 || !strings.HasSuffix(got, "...") {
		t.Errorf("long output should truncate to 200+ellipsis; got len %d", len(got))
	}
}

func TestDefaultFormatRunErrorPaths(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	dir := t.TempDir()
	// Failure with diagnostic output: the message is folded into the error.
	err := defaultFormatRun(context.Background(), dir, []string{"sh", "-c", "echo bad syntax 1>&2; exit 1"})
	if err == nil || !strings.Contains(err.Error(), "bad syntax") {
		t.Errorf("want error carrying stderr; got %v", err)
	}
	// Failure with no output: bare exit error, no panic.
	if err := defaultFormatRun(context.Background(), dir, []string{"sh", "-c", "exit 3"}); err == nil {
		t.Error("want error on non-zero exit with no output")
	}
	// Success path returns nil.
	if err := defaultFormatRun(context.Background(), dir, []string{"sh", "-c", "exit 0"}); err != nil {
		t.Errorf("clean exit should be nil; got %v", err)
	}
}

// TestFormatRealGofmt exercises the real default runner end to end against
// gofmt, which is guaranteed present in the Go toolchain CI uses. It proves
// the file is actually rewritten in place, not just that the right argv was
// assembled.
func TestFormatRealGofmt(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "main.go")
	unformatted := "package main\nfunc  main(){x:=1;_=x}\n"
	if err := os.WriteFile(p, []byte(unformatted), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFormatter(config.FormatterConfig{})
	res := f.Format(context.Background(), p)
	if !res.Ran || res.Err != nil {
		t.Fatalf("gofmt should have run cleanly; got %+v", res)
	}
	out, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) == unformatted {
		t.Errorf("gofmt did not reformat the file:\n%s", out)
	}
	if !strings.Contains(string(out), "func main()") {
		t.Errorf("expected gofmt-normalized output; got:\n%s", out)
	}
}
