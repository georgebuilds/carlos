package tools

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestRegistry_DefaultPopulated(t *testing.T) {
	r := NewDefaultRegistry()
	want := []string{
		"bash", "read", "write", "edit", "grep", "glob",
		"git_status", "git_diff", "git_log", "git_blame", "git_show",
	}
	for _, name := range want {
		if _, ok := r.Get(name); !ok {
			t.Errorf("default registry missing tool %q", name)
		}
	}
}

func TestRegistry_AllSortedByName(t *testing.T) {
	r := NewDefaultRegistry()
	all := r.All()
	names := make([]string, len(all))
	for i, tl := range all {
		names[i] = tl.Name()
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("All() must be sorted by name; got %v", names)
	}
}

func TestRegistry_AllSchemasValidJSON(t *testing.T) {
	r := NewDefaultRegistry()
	for _, tl := range r.All() {
		var v any
		if err := json.Unmarshal(tl.Schema(), &v); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", tl.Name(), err)
		}
		if tl.Description() == "" {
			t.Errorf("%s: empty description", tl.Name())
		}
	}
}
