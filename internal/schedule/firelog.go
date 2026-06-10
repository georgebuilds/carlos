package schedule

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FireLog is the on-disk append-only log of (schedule_name, slot) pairs
// the daemon has already initiated. It exists to suppress double-fires
// across a crash window: the daemon writes a slot to the log BEFORE
// invoking the action, so a process restart that replays the log skips
// the recorded slot rather than re-running it.
//
// Wire format: one line per entry, "<name>\t<slot_unix_nanos>\n". The
// name is restricted to schedule.Schedule.Name which the Validate gate
// already strips of whitespace and tabs. UTC nanos make the timestamp
// portable across machines that may share a config via dotfile sync.
//
// Concurrency: a single FireLog is safe for use by one goroutine
// (the daemon's tick loop). The mutex guards the in-memory set + file
// handle so an out-of-band test or future caller from a second
// goroutine cannot tear a write.
//
// Compaction: v1 keeps the log unbounded. The daemon rolls config
// state forward via LastRunAt; older entries become redundant once
// LastRunAt has advanced past them but pruning is deferred to a
// follow-up (see roadmap).
type FireLog struct {
	mu   sync.Mutex
	f    *os.File
	seen map[string]struct{}
	// path is retained so tests + close-then-reopen flows can report
	// the backing file when surfacing errors.
	path string
}

// OpenFireLog opens or creates the log at path, replays it into an
// in-memory set, and returns the handle. The parent directory is
// created with 0700 if missing (mirrors config.Save's directory
// semantics). File mode is 0600 since the log records the user's
// scheduling activity.
func OpenFireLog(path string) (*FireLog, error) {
	if path == "" {
		return nil, fmt.Errorf("schedule: OpenFireLog called with empty path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("schedule: firelog mkdir %s: %w", dir, err)
	}
	// O_APPEND + O_CREATE: append-only writes; O_RDWR so we can
	// position the read scanner at the start before appending.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("schedule: firelog open %s: %w", path, err)
	}
	log := &FireLog{
		f:    f,
		seen: make(map[string]struct{}),
		path: path,
	}
	if err := log.replay(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return log, nil
}

// replay scans the file from the start and populates seen. Malformed
// lines are skipped (silent tolerance for partial writes from a crash
// mid-Append - the next sound entry resumes the log).
func (l *FireLog) replay() error {
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("schedule: firelog seek: %w", err)
	}
	sc := bufio.NewScanner(l.f)
	// Allow long lines (defensive; entries are short but a future
	// schema bump could carry richer payloads).
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		name := line[:tab]
		nanosStr := line[tab+1:]
		nanos, err := strconv.ParseInt(nanosStr, 10, 64)
		if err != nil {
			continue
		}
		l.seen[firelogKey(name, time.Unix(0, nanos).UTC())] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("schedule: firelog scan: %w", err)
	}
	// Reposition the file for subsequent O_APPEND writes (seek is a
	// no-op for O_APPEND on POSIX but keeps the cursor sane on any
	// future platform that does not auto-append).
	if _, err := l.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("schedule: firelog seek end: %w", err)
	}
	return nil
}

// firelogKey is the in-memory set key. Slot is normalised to UTC and
// truncated to nanoseconds to match what the wire format records;
// callers can pass a local-time slot and still hit the right entry.
func firelogKey(name string, slot time.Time) string {
	return name + "\x00" + strconv.FormatInt(slot.UTC().UnixNano(), 10)
}

// Has reports whether (name, slot) has been recorded.
func (l *FireLog) Has(name string, slot time.Time) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.seen[firelogKey(name, slot)]
	return ok
}

// Append records (name, slot) and fsyncs the file so a crash before
// the action callback finishes still leaves a durable trace. The
// in-memory set is updated only after the write succeeds so a write
// failure does not silently mask the slot from the next Due check.
//
// Append is idempotent: a duplicate (name, slot) returns nil without
// appending a redundant line (and without touching the file).
func (l *FireLog) Append(name string, slot time.Time) error {
	if l == nil {
		return fmt.Errorf("schedule: firelog Append called on nil log")
	}
	if name == "" {
		return fmt.Errorf("schedule: firelog Append with empty name")
	}
	if strings.ContainsAny(name, "\t\n") {
		return fmt.Errorf("schedule: firelog Append: name %q contains tab or newline", name)
	}
	if slot.IsZero() {
		return fmt.Errorf("schedule: firelog Append: zero slot for %q", name)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := firelogKey(name, slot)
	if _, ok := l.seen[key]; ok {
		return nil
	}
	line := fmt.Sprintf("%s\t%d\n", name, slot.UTC().UnixNano())
	if _, err := l.f.WriteString(line); err != nil {
		return fmt.Errorf("schedule: firelog write: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("schedule: firelog sync: %w", err)
	}
	l.seen[key] = struct{}{}
	return nil
}

// Close releases the file handle. Idempotent.
func (l *FireLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Path returns the backing file path. Useful for tests + diagnostics.
func (l *FireLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
