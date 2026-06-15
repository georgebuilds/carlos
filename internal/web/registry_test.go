package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// fakeBackend is a minimal Backend for registry unit tests: it embeds
// readOnlyBackend (nil reader) for the unused interactive/read methods and
// overrides only Name + ListThreads, which is all the registry exercises.
type fakeBackend struct {
	readOnlyBackend
	name string
	list []ThreadSummary
	err  error
}

func (b fakeBackend) Name() string { return b.name }
func (b fakeBackend) ListThreads(context.Context) ([]ThreadSummary, error) {
	return b.list, b.err
}

func TestSplitThreadID(t *testing.T) {
	cases := []struct {
		id       string
		backend  string
		native   string
		prefixed bool
	}{
		{"01KTT4520M8STEK8TM3WF9RZH6", "", "01KTT4520M8STEK8TM3WF9RZH6", false}, // carlos ULID, no prefix
		{"cc:d495f634-e0c8", "cc", "d495f634-e0c8", true},                       // claude code
		{"cc:", "cc", "", true}, // prefix, empty native
		{":x", "", ":x", false}, // leading colon is not a prefix
		{"", "", "", false},     // empty
	}
	for _, c := range cases {
		b, n, p := splitThreadID(c.id)
		if b != c.backend || n != c.native || p != c.prefixed {
			t.Errorf("splitThreadID(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.id, b, n, p, c.backend, c.native, c.prefixed)
		}
	}
}

func TestRegistry_RoutingAndFanout(t *testing.T) {
	r := NewRegistry()
	carlos := fakeBackend{name: "carlos", list: []ThreadSummary{{ID: "01ABC"}}}
	cc := fakeBackend{name: "cc", list: []ThreadSummary{{ID: "cc:u1"}}}
	r.Register(carlos)
	r.Register(cc)

	if got := r.Default().Name(); got != "carlos" {
		t.Errorf("Default = %q, want carlos (first registered)", got)
	}
	if names := r.Names(); len(names) != 2 || names[0] != "carlos" || names[1] != "cc" {
		t.Errorf("Names = %v, want [carlos cc]", names)
	}

	// Unprefixed id -> default; prefixed -> named; unknown prefix -> !ok.
	if b, ok := r.For("01ABC"); !ok || b.Name() != "carlos" {
		t.Errorf("For(unprefixed) routed to %v/%v, want carlos/true", b, ok)
	}
	if b, ok := r.For("cc:u1"); !ok || b.Name() != "cc" {
		t.Errorf("For(cc:u1) routed to %v/%v, want cc/true", b, ok)
	}
	if _, ok := r.For("zz:nope"); ok {
		t.Error("For(unknown prefix) should not resolve")
	}

	// ListThreads concatenates in registration order.
	all, err := r.ListThreads(context.Background())
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(all) != 2 || all[0].ID != "01ABC" || all[1].ID != "cc:u1" {
		t.Errorf("ListThreads = %+v, want [01ABC cc:u1] in order", all)
	}
}

// Re-registering the same name replaces in place (the SetBackend swap of
// the read-only placeholder for the interactive carlos backend), keeping
// the default slot.
func TestRegistry_RegisterReplacesInPlace(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeBackend{name: "carlos", list: []ThreadSummary{{ID: "old"}}})
	r.Register(fakeBackend{name: "carlos", list: []ThreadSummary{{ID: "new"}}})
	if names := r.Names(); len(names) != 1 {
		t.Fatalf("Names = %v, want one entry after replace", names)
	}
	all, _ := r.ListThreads(context.Background())
	if len(all) != 1 || all[0].ID != "new" {
		t.Errorf("after replace, list = %+v, want [new]", all)
	}
}

// A backend that errors is skipped (its threads absent) as long as another
// succeeds; only an all-backends failure surfaces an error (so a single
// carlos list failure still 500s, preserving the pre-router behavior).
func TestRegistry_ListThreadsDegradesUnlessAllFail(t *testing.T) {
	ctx := context.Background()

	partial := NewRegistry()
	partial.Register(fakeBackend{name: "carlos", err: errors.New("boom")})
	partial.Register(fakeBackend{name: "cc", list: []ThreadSummary{{ID: "cc:1"}}})
	all, err := partial.ListThreads(ctx)
	if err != nil {
		t.Errorf("partial failure should not error, got %v", err)
	}
	if len(all) != 1 || all[0].ID != "cc:1" {
		t.Errorf("partial failure list = %+v, want only [cc:1]", all)
	}

	total := NewRegistry()
	total.Register(fakeBackend{name: "carlos", err: errors.New("boom")})
	if _, err := total.ListThreads(ctx); err == nil {
		t.Error("all-backends failure should surface an error (handler 500)")
	}
}

// At the handler layer: a prefixed id whose backend is not registered gets
// 404 unknown_backend, while an unprefixed unknown id still routes to
// carlos and gets the carlos not_found (pre-router behavior preserved).
func TestThreads_UnknownBackendVsNotFound(t *testing.T) {
	s, _, _ := newTestServer(t, "") // only the carlos read-only backend

	rec := do(t, s, "GET", "/api/threads/cc:does-not-exist", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "unknown_backend") {
		t.Errorf("prefixed unknown backend: got %d %s, want 404 unknown_backend", rec.Code, rec.Body.String())
	}

	rec2 := do(t, s, "GET", "/api/threads/01NOSUCHTHREAD", nil)
	if rec2.Code != http.StatusNotFound || !strings.Contains(rec2.Body.String(), "not_found") {
		t.Errorf("unprefixed unknown id: got %d %s, want 404 not_found", rec2.Code, rec2.Body.String())
	}
}
