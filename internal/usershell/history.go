package usershell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// History is a tiny file-backed shell-command history. Used by the
// chat composer to cycle previous "!cmd" entries via ↑/↓ when shell
// mode is active (Phase U S7).
//
// Separate from the chat history because the vocabularies differ —
// the user pressing ↑ on `!cargo test` should get their last shell
// command, not their last chat turn.
//
// File format: one command per line. Append-only writes. Reads load
// the whole file into memory (bounded by HistoryMaxLines on save).
// File mode 0o600 because shell history may contain echoed secrets.
type History struct {
	path     string
	maxLines int

	mu      sync.Mutex
	entries []string
	cursor  int // current position when scrolling; -1 means "not browsing"
}

// HistoryMaxLines is the default cap on retained entries. Reached
// only by very heavy users; rotation drops oldest entries first.
const HistoryMaxLines = 5000

// DefaultHistoryPath returns ~/.carlos/shell-history.
func DefaultHistoryPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".carlos", "shell-history")
	}
	return filepath.Join(home, ".carlos", "shell-history")
}

// NewHistory loads the history file (or starts empty if absent).
// path == "" falls back to DefaultHistoryPath. Errors reading an
// existing-but-broken file are non-fatal — we start with an empty
// in-memory log and the user just loses prior entries.
func NewHistory(path string) *History {
	if path == "" {
		path = DefaultHistoryPath()
	}
	h := &History{path: path, maxLines: HistoryMaxLines, cursor: -1}
	h.load()
	return h
}

// load reads the on-disk file into h.entries. Best-effort: a missing
// file is normal (first run); a corrupted file yields an empty
// history.
func (h *History) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	var lines []string
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	h.mu.Lock()
	h.entries = lines
	h.cursor = len(lines) // "past the end" = fresh-start cursor
	h.mu.Unlock()
}

// Add records cmd and appends to the file. Empty / duplicate-of-last
// entries are skipped — same recipe bash, zsh, and atuin all use.
//
// Returns nil on disk-write failure too; the in-memory entry still
// lands so ↑/↓ works within the session even when persistence is
// degraded (e.g., the directory got renamed mid-session).
func (h *History) Add(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	h.mu.Lock()
	if n := len(h.entries); n > 0 && h.entries[n-1] == cmd {
		h.cursor = n
		h.mu.Unlock()
		return nil
	}
	h.entries = append(h.entries, cmd)
	if len(h.entries) > h.maxLines {
		h.entries = h.entries[len(h.entries)-h.maxLines:]
	}
	h.cursor = len(h.entries)
	entries := append([]string(nil), h.entries...)
	h.mu.Unlock()
	return h.persist(entries)
}

// persist writes the entries slice to disk via temp+rename. After
// rotation (when entries exceeds maxLines) the full slice is
// rewritten so the on-disk file matches; for the common no-rotation
// case the temp+rename still happens — append-only with truncate
// after every write is the simplest correct shape, and shell-history
// files are tiny.
func (h *History) persist(entries []string) error {
	dir := filepath.Dir(h.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("history: mkdir %s: %w", dir, err)
	}
	tmp := h.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("history: open tmp: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		if _, err := io.WriteString(w, e); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("history: write: %w", err)
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("history: write: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("history: flush: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("history: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("history: close: %w", err)
	}
	if err := os.Rename(tmp, h.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("history: rename: %w", err)
	}
	return nil
}

// Prev returns the previous entry while browsing back (↑ in the
// composer). Returns "" when already at the oldest entry. Resets the
// cursor on first call from "not browsing" state.
func (h *History) Prev() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.entries) == 0 {
		return ""
	}
	if h.cursor > 0 {
		h.cursor--
	}
	return h.entries[h.cursor]
}

// Next returns the next entry while browsing forward (↓). Returns ""
// when we've walked past the newest entry — the caller (composer)
// clears the input on that signal.
func (h *History) Next() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.entries) == 0 || h.cursor >= len(h.entries) {
		return ""
	}
	h.cursor++
	if h.cursor >= len(h.entries) {
		return ""
	}
	return h.entries[h.cursor]
}

// Reset moves the browsing cursor past the newest entry. The
// composer calls this on submit so the next ↑ starts at the most
// recent.
func (h *History) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cursor = len(h.entries)
}

// Len returns the number of in-memory entries. Exposed so tests can
// pin rotation behavior; chat code doesn't read it.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// Exists reports whether the backing file is present on disk. Used
// by tests; never load-bearing in production code paths.
func (h *History) Exists() bool {
	_, err := os.Stat(h.path)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}
