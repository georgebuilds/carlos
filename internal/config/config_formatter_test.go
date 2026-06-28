package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFormatterRoundtrip pins the formatter: block serialization: the global
// disable switch and per-formatter overrides (command, extensions, disabled)
// survive a Save+Load cycle.
func TestFormatterRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := &Config{
		UserName: "Boss",
		Formatter: FormatterConfig{
			Disabled: true,
			Formatters: map[string]FormatterSpec{
				"gofmt": {Disabled: true},
				"biome": {
					Command:    []string{"biome", "format", "--write", "$FILE"},
					Extensions: []string{".js", ".ts"},
				},
			},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Formatter.Disabled {
		t.Error("Formatter.Disabled not roundtripped")
	}
	if !got.Formatter.Formatters["gofmt"].Disabled {
		t.Error("per-formatter gofmt.Disabled not roundtripped")
	}
	biome := got.Formatter.Formatters["biome"]
	if len(biome.Command) != 4 || biome.Command[0] != "biome" {
		t.Errorf("biome.Command not roundtripped: %v", biome.Command)
	}
	if len(biome.Extensions) != 2 {
		t.Errorf("biome.Extensions: want 2 got %d", len(biome.Extensions))
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "formatter:") {
		t.Errorf("yaml should include `formatter:` block; got:\n%s", string(b))
	}
}

// TestFormatterOmittedWhenEmpty — a zero-value FormatterConfig must not emit a
// stray `formatter: {}` line so older configs without the field round-trip
// cleanly, and so an absent block keeps meaning "built-ins enabled".
func TestFormatterOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "formatter:") {
		t.Errorf("empty Formatter should be omitted; got:\n%s", string(b))
	}
}
