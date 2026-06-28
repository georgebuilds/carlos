package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/config"
)

// defaultFormatTimeout bounds a single formatter invocation. Formatters are
// fast (gofmt on a large file is milliseconds), so a generous ceiling here
// only matters when a formatter hangs; we kill it and treat the run as a
// best-effort miss rather than blocking the agent loop.
const defaultFormatTimeout = 10 * time.Second

// formatWaitGrace bounds how long defaultFormatRun waits for a formatter's
// output pipe to close after the context deadline fires. exec.CommandContext
// SIGKILLs the direct child on timeout, but a formatter that spawned a
// grandchild holding the pipe could otherwise block CombinedOutput
// indefinitely; WaitDelay caps that so a hung formatter can never stall the
// agent loop (mirrors bash.go's process-group kill intent).
const formatWaitGrace = 3 * time.Second

// fileToken is the placeholder a FormatterSpec.Command may use to mark where
// the target file path goes. Mirrors opencode's $FILE convention.
const fileToken = "$FILE"

// builtinFormatters is the curated "most useful" subset: one formatter per
// extension (no ambiguity), each a single-file in-place formatter that
// respects project configuration (prettier/ruff/rustfmt/clang-format read the
// repo's config when run from the file's directory). gofmt, prettier, ruff,
// rustfmt, shfmt, clang-format, zig, pint, and ktlint mirror formatters
// opencode ships; google-java-format (.java), scalafmt (.scala/.sc), and
// taplo (.toml) go beyond opencode's set, which has no built-in
// Java/Scala/TOML formatter. prettier here also covers the common data and
// markup formats (.json, .jsonc, .yaml, .yml, .css, .html, .md, ...).
// Deliberately omitted because they overlap a built-in on the same extension
// or have version-dependent single-file semantics: biome, black, gofumpt,
// terraform. Users add or override any of these via config.formatter.
func builtinFormatters() map[string]config.FormatterSpec {
	return map[string]config.FormatterSpec{
		"gofmt": {
			Command:    []string{"gofmt", "-w", fileToken},
			Extensions: []string{".go"},
		},
		"prettier": {
			Command: []string{"prettier", "--write", fileToken},
			Extensions: []string{
				".js", ".jsx", ".mjs", ".cjs",
				".ts", ".tsx",
				".json", ".jsonc",
				".css", ".scss", ".less",
				".html", ".vue", ".svelte",
				".yaml", ".yml",
				".md", ".mdx",
				".graphql",
			},
		},
		"ruff": {
			Command:    []string{"ruff", "format", fileToken},
			Extensions: []string{".py", ".pyi"},
		},
		"rustfmt": {
			Command:    []string{"rustfmt", fileToken},
			Extensions: []string{".rs"},
		},
		"shfmt": {
			Command:    []string{"shfmt", "-w", fileToken},
			Extensions: []string{".sh", ".bash"},
		},
		"clang-format": {
			Command:    []string{"clang-format", "-i", fileToken},
			Extensions: []string{".c", ".h", ".cpp", ".hpp", ".cc", ".cxx", ".hh", ".hxx"},
		},
		"zig": {
			Command:    []string{"zig", "fmt", fileToken},
			Extensions: []string{".zig"},
		},
		"pint": {
			Command:    []string{"pint", fileToken},
			Extensions: []string{".php"},
		},
		"ktlint": {
			Command:    []string{"ktlint", "-F", fileToken},
			Extensions: []string{".kt", ".kts"},
		},
		"google-java-format": {
			Command:    []string{"google-java-format", "-i", fileToken},
			Extensions: []string{".java"},
		},
		"scalafmt": {
			Command:    []string{"scalafmt", fileToken},
			Extensions: []string{".scala", ".sc"},
		},
		"taplo": {
			Command:    []string{"taplo", "fmt", fileToken},
			Extensions: []string{".toml"},
		},
	}
}

// Formatter runs the right code formatter on a file after the write/edit
// tools touch it. It is constructed once per session from config and shared
// (read-only) across tool calls. A nil *Formatter is a valid no-op, so call
// sites that never wired one (sub-agents, legacy tests) behave exactly as
// before this feature existed.
type Formatter struct {
	disabled bool
	// specs is the merged formatter set (built-ins overlaid with config).
	specs map[string]config.FormatterSpec
	// byExt maps a lowercased file extension to the formatter name that
	// owns it. Config-defined formatters win over built-ins on collision.
	byExt   map[string]string
	timeout time.Duration

	// lookPath and run are injection seams for tests; both default to the
	// real os/exec implementations.
	lookPath func(string) (string, error)
	run      func(ctx context.Context, dir string, argv []string) error
}

// FormatResult reports the outcome of a Format call. Ran is true only when a
// formatter actually executed and succeeded; Err is set only when a matched,
// present formatter ran and exited non-zero. A file with no matching
// formatter, or one whose formatter binary is absent from PATH, yields the
// zero value (Name possibly set, Ran false, Err nil) and is silent.
type FormatResult struct {
	Name string
	Ran  bool
	Err  error
}

// Note renders a one-line, human-readable receipt fragment for a result, or
// "" when there is nothing worth telling the model (no match, or binary
// absent). The write/edit tools append this to their receipt.
func (r FormatResult) Note() string {
	switch {
	case r.Ran:
		return "formatted with " + r.Name
	case r.Err != nil:
		return fmt.Sprintf("formatter %s failed: %v", r.Name, r.Err)
	default:
		return ""
	}
}

// NewFormatter builds a Formatter from config. The built-in set is the
// baseline; cfg.Formatters overlays it (replace command/extensions for a
// known name, disable a name, or register a new one). When cfg.Disabled is
// true the returned Formatter is a no-op. The result is safe for concurrent
// use: it is never mutated after construction.
func NewFormatter(cfg config.FormatterConfig) *Formatter {
	f := &Formatter{
		disabled: cfg.Disabled,
		specs:    builtinFormatters(),
		timeout:  defaultFormatTimeout,
		lookPath: exec.LookPath,
	}
	f.run = defaultFormatRun

	// fromConfig tracks names touched by config so they win extension
	// collisions against pure built-ins.
	fromConfig := make(map[string]bool, len(cfg.Formatters))
	for name, spec := range cfg.Formatters {
		if spec.Disabled {
			delete(f.specs, name)
			continue
		}
		base := f.specs[name] // zero value if a brand-new formatter
		if len(spec.Command) > 0 {
			base.Command = spec.Command
		}
		if len(spec.Extensions) > 0 {
			base.Extensions = spec.Extensions
		}
		base.Disabled = false
		f.specs[name] = base
		fromConfig[name] = true
	}

	f.byExt = buildExtIndex(f.specs, fromConfig)
	return f
}

// buildExtIndex maps each extension to the formatter that owns it. Built-in
// names are laid down first (sorted for determinism), then config-defined
// names overwrite, so a user-supplied formatter for an extension a built-in
// already claims takes precedence. Specs missing a command or extensions are
// skipped (nothing to run / nothing to match).
func buildExtIndex(specs map[string]config.FormatterSpec, fromConfig map[string]bool) map[string]string {
	var builtinNames, configNames []string
	for name := range specs {
		if fromConfig[name] {
			configNames = append(configNames, name)
		} else {
			builtinNames = append(builtinNames, name)
		}
	}
	sort.Strings(builtinNames)
	sort.Strings(configNames)

	byExt := make(map[string]string)
	lay := func(names []string) {
		for _, name := range names {
			spec := specs[name]
			if len(spec.Command) == 0 || len(spec.Extensions) == 0 {
				continue
			}
			for _, ext := range spec.Extensions {
				byExt[normalizeExt(ext)] = name
			}
		}
	}
	lay(builtinNames)
	lay(configNames)
	return byExt
}

// Format runs the formatter that owns path's extension, if any. It is
// best-effort: a missing binary is a silent miss, and a formatter that exits
// non-zero is reported but never surfaced as a hard error, because the file
// has already been written correctly and well-behaved formatters leave it
// untouched on parse failure. A nil receiver is a no-op.
func (f *Formatter) Format(ctx context.Context, path string) FormatResult {
	if f == nil || f.disabled {
		return FormatResult{}
	}
	name, ok := f.byExt[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return FormatResult{}
	}
	spec := f.specs[name]
	if len(spec.Command) == 0 {
		return FormatResult{}
	}
	if _, err := f.lookPath(spec.Command[0]); err != nil {
		// Binary not installed: stay quiet so default-on does not nag on
		// machines that lack the tool.
		return FormatResult{Name: name}
	}

	// Normalize to an absolute path so a relative name beginning with "-"
	// (e.g. "-x.go") can never be parsed as a flag by the formatter, and so
	// cmd.Dir points at the file's real directory for project-config lookup.
	abs := path
	if !filepath.IsAbs(abs) {
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
	}

	argv := substituteFile(spec.Command, abs)
	runCtx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	if err := f.run(runCtx, filepath.Dir(abs), argv); err != nil {
		return FormatResult{Name: name, Err: err}
	}
	return FormatResult{Name: name, Ran: true}
}

// substituteFile replaces every $FILE token in argv with path. If no token is
// present, path is appended as the final argument, so a command authored as
// just ["myfmt"] still receives the file.
func substituteFile(argv []string, path string) []string {
	out := make([]string, 0, len(argv)+1)
	replaced := false
	for _, a := range argv {
		if strings.Contains(a, fileToken) {
			out = append(out, strings.ReplaceAll(a, fileToken, path))
			replaced = true
			continue
		}
		out = append(out, a)
	}
	if !replaced {
		out = append(out, path)
	}
	return out
}

// normalizeExt lowercases an extension and guarantees a leading dot so a
// user-authored config entry like "go" matches filepath.Ext's ".go".
func normalizeExt(ext string) string {
	ext = strings.ToLower(ext)
	if ext != "" && !strings.HasPrefix(ext, ".") {
		return "." + ext
	}
	return ext
}

// defaultFormatRun executes argv in dir, folding stdout+stderr into the error
// message on failure so the receipt note is actionable. argv[0] is the
// binary; callers have already confirmed it is on PATH. WaitDelay bounds the
// wait on the output pipe so a formatter that spawns a lingering grandchild
// cannot block past the deadline.
func defaultFormatRun(ctx context.Context, dir string, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = formatWaitGrace
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%w: %s", err, truncateFormatOutput(msg))
		}
		return err
	}
	return nil
}

// truncateFormatOutput keeps a failing formatter's diagnostic short enough to
// sit on one receipt line without flooding the model's context.
func truncateFormatOutput(s string) string {
	const max = 200
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
