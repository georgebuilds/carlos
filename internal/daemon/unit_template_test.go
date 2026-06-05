package daemon

// This file is intentionally empty. The per-platform unit template
// tests live behind matching build tags:
//
//   - unit_macos_test.go   (darwin)
//   - unit_linux_test.go   (linux)
//
// Splitting them keeps the wrong-platform symbol references hidden
// from the toolchain so cross-builds + `go vet ./...` stay clean.
