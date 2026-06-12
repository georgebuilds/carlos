package clipboard

import (
	"bytes"
	"errors"
	"testing"
)

// TestLazyReader_InitFailureDegrades: a failing init marks the reader
// permanently unavailable - every ReadImage reports (nil, false) and
// init runs exactly once (no retry storms against a dead display).
func TestLazyReader_InitFailureDegrades(t *testing.T) {
	inits := 0
	r := &lazyReader{
		init: func() error { inits++; return errors.New("no display") },
		read: func() []byte { t.Fatal("read must not run after failed init"); return nil },
	}
	for i := 0; i < 3; i++ {
		if data, ok := r.ReadImage(); ok || data != nil {
			t.Fatalf("call %d: got (%v, %v), want (nil, false)", i, data, ok)
		}
	}
	if inits != 1 {
		t.Errorf("init ran %d times, want exactly 1", inits)
	}
}

// TestLazyReader_InitPanicDegrades: a panicking init (the library
// warns some platforms can blow up) is contained - no panic escapes,
// the reader degrades to unavailable.
func TestLazyReader_InitPanicDegrades(t *testing.T) {
	r := &lazyReader{
		init: func() error { panic("dlopen exploded") },
		read: func() []byte { t.Fatal("read must not run after panicked init"); return nil },
	}
	if data, ok := r.ReadImage(); ok || data != nil {
		t.Fatalf("got (%v, %v), want (nil, false)", data, ok)
	}
	// Second call must not re-run init (sync.Once) nor panic.
	if _, ok := r.ReadImage(); ok {
		t.Fatal("reader recovered after panicked init; must stay unavailable")
	}
}

// TestLazyReader_ReadImage: happy path - init succeeds once, bytes
// flow through verbatim.
func TestLazyReader_ReadImage(t *testing.T) {
	want := []byte{0x89, 'P', 'N', 'G'}
	inits := 0
	r := &lazyReader{
		init: func() error { inits++; return nil },
		read: func() []byte { return want },
	}
	for i := 0; i < 2; i++ {
		data, ok := r.ReadImage()
		if !ok || !bytes.Equal(data, want) {
			t.Fatalf("call %d: got (%v, %v), want (%v, true)", i, data, ok, want)
		}
	}
	if inits != 1 {
		t.Errorf("init ran %d times, want exactly 1 (lazy + once)", inits)
	}
}

// TestLazyReader_EmptyClipboard: a nil/empty read means "no image",
// reported as (nil, false) - callers branch on ok, never on len.
func TestLazyReader_EmptyClipboard(t *testing.T) {
	for _, ret := range [][]byte{nil, {}} {
		r := &lazyReader{
			init: func() error { return nil },
			read: func() []byte { return ret },
		}
		if data, ok := r.ReadImage(); ok || data != nil {
			t.Errorf("read=%v: got (%v, %v), want (nil, false)", ret, data, ok)
		}
	}
}

// TestLazyReader_ReadPanicIsTransient: a panic during read degrades
// THAT call but does not poison the reader - the next read works.
func TestLazyReader_ReadPanicIsTransient(t *testing.T) {
	calls := 0
	r := &lazyReader{
		init: func() error { return nil },
		read: func() []byte {
			calls++
			if calls == 1 {
				panic("pasteboard hiccup")
			}
			return []byte("img")
		},
	}
	if data, ok := r.ReadImage(); ok || data != nil {
		t.Fatalf("panicking read: got (%v, %v), want (nil, false)", data, ok)
	}
	if data, ok := r.ReadImage(); !ok || string(data) != "img" {
		t.Fatalf("read after transient panic: got (%q, %v), want (img, true)", data, ok)
	}
}

// TestSystem_NeverPanics: the real binding must satisfy the package
// contract on whatever machine runs the tests - GUI session, SSH,
// headless CI - without panicking. Either outcome of ok is legal;
// the assertions are "no panic" and "consistent with the contract".
func TestSystem_NeverPanics(t *testing.T) {
	r := System()
	if r == nil {
		t.Fatal("System() returned nil")
	}
	data, ok := r.ReadImage()
	if ok && len(data) == 0 {
		t.Error("ok=true with empty data violates the Reader contract")
	}
	if !ok && data != nil {
		t.Error("ok=false with non-nil data violates the Reader contract")
	}
	// Same singleton on every call.
	if System() != r {
		t.Error("System() must return the process-wide singleton")
	}
}

// TestFake: zero value is empty; with Image set the bytes round-trip;
// Calls counts invocations.
func TestFake(t *testing.T) {
	f := &Fake{}
	if data, ok := f.ReadImage(); ok || data != nil {
		t.Errorf("zero-value Fake: got (%v, %v), want (nil, false)", data, ok)
	}
	f.Image = []byte("png-bytes")
	if data, ok := f.ReadImage(); !ok || string(data) != "png-bytes" {
		t.Errorf("Fake with image: got (%q, %v), want (png-bytes, true)", data, ok)
	}
	if f.Calls != 2 {
		t.Errorf("Calls = %d, want 2", f.Calls)
	}
	// Compile-time + runtime check that Fake satisfies Reader.
	var _ Reader = f
}
