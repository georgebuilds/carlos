// Package clipboard is carlos's thin seam over the system clipboard,
// scoped to the one operation the chat composer needs: "is there an
// image on the clipboard right now, and if so give me its bytes".
//
// The heavy lifting is golang.design/x/clipboard (pinned at v0.8.0,
// the first cgo-free desktop release: purego Dlopen(AppKit) on darwin,
// raw X11/Wayland wire protocols on linux). The wrapper exists so:
//
//   - the rest of carlos depends on a one-method interface ([Reader])
//     that a [Fake] satisfies in tests - no test ever touches the real
//     pasteboard;
//   - the library's Init() requirement is handled lazily and exactly
//     once, on the FIRST ReadImage call, never at boot - a headless
//     session (no $DISPLAY, no X server) degrades to "no image
//     available" instead of failing startup;
//   - any panic out of the backing library (its docs warn that
//     Read after a failed Init may panic on some platforms) is
//     contained here and degrades to (nil, false).
//
// Platform note: the darwin backend dlopens AppKit at package-import
// time (a process-wide, sub-millisecond operation against a system
// framework that exists on every macOS install). Linux defers all
// display-server contact to the lazy Init. CGO_ENABLED=0 builds work
// on both by construction - that is the entire point of v0.8.0.
//
// Image encoding: the library normalizes clipboard images to PNG, so
// ReadImage bytes are always PNG-encoded ("image/png").
package clipboard

import (
	"sync"

	xclipboard "golang.design/x/clipboard"
)

// Reader reads the current image content of a clipboard.
//
// Implementations: [System] (the real OS clipboard) and [Fake]
// (tests / headless stubs).
type Reader interface {
	// ReadImage returns the clipboard's current image as PNG-encoded
	// bytes and true, or (nil, false) when the clipboard holds no
	// image or the clipboard is unavailable (headless session,
	// unsupported platform, backend failure). It never panics and
	// never blocks beyond the underlying pasteboard round-trip.
	ReadImage() ([]byte, bool)
}

// System returns the process-wide [Reader] backed by the real OS
// clipboard. The underlying library is initialized lazily on the
// first ReadImage call; if that initialization fails (e.g. no X
// display) every subsequent ReadImage cheaply reports (nil, false).
func System() Reader { return systemReader }

// systemReader is the singleton real-clipboard reader. A single
// instance is deliberate: golang.design/x/clipboard's Init is
// process-global (sync.Once inside the library), so per-instance
// state would be a lie.
var systemReader = &lazyReader{
	init: xclipboard.Init,
	read: func() []byte { return xclipboard.Read(xclipboard.FmtImage) },
}

// lazyReader wraps an init-then-read pair with exactly-once lazy
// initialization and panic containment. The init/read funcs are
// injected so tests can exercise every failure mode without a real
// clipboard; production wiring binds them to golang.design/x/clipboard
// in [systemReader].
type lazyReader struct {
	once  sync.Once
	ready bool
	init  func() error
	read  func() []byte
}

// ReadImage implements [Reader]. First call runs init (once, ever);
// an init error or panic marks the reader permanently unavailable.
// A read panic degrades that call to (nil, false) without poisoning
// future calls - a transient backend hiccup shouldn't disable paste
// for the rest of the session.
func (r *lazyReader) ReadImage() (data []byte, ok bool) {
	r.once.Do(func() {
		defer func() {
			if recover() != nil {
				r.ready = false
			}
		}()
		r.ready = r.init() == nil
	})
	if !r.ready {
		return nil, false
	}
	defer func() {
		if recover() != nil {
			data, ok = nil, false
		}
	}()
	b := r.read()
	if len(b) == 0 {
		return nil, false
	}
	return b, true
}

// Fake is a canned [Reader] for tests and previews. Zero value reads
// as "no image"; set Image to serve those bytes on every ReadImage.
type Fake struct {
	// Image is returned (with ok=true) by ReadImage when non-empty.
	Image []byte
	// Calls counts ReadImage invocations, for assertions on lazy /
	// gated call sites ("the composer must not poll the clipboard").
	Calls int
}

// ReadImage implements [Reader].
func (f *Fake) ReadImage() ([]byte, bool) {
	f.Calls++
	if len(f.Image) == 0 {
		return nil, false
	}
	return f.Image, true
}
