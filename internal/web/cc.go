package web

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// cc.go: the observe-only Claude Code backend (multi-backend B-2). It
// projects on-disk CC sessions (~/.claude/projects/<enc-cwd>/<uuid>.jsonl)
// into the wire vocabulary read-only: list, read, live-tail. It owns no
// event log and drives no process; the file IS the source. Interactive
// operations report ErrUnsupported until the drive (B-3) and approval-bridge
// (B-4) slices land. The pure record->wire mapping lives in cc_map.go and is
// golden-file tested; this file is the filesystem + lifecycle shell.

const (
	ccBackendName = "cc"
	// ccRecencyWindow bounds the roster: only sessions touched within it are
	// listed, so the console is not flooded by years of historical sessions.
	// A session outside the window stays openable by id (ReadEvents/Subscribe
	// fall back to a direct uuid lookup).
	ccRecencyWindow = 14 * 24 * time.Hour
	// ccPollInterval is the live-tail file poll cadence.
	ccPollInterval = time.Second
	// ccLiveWindow: a session whose file changed within it reads as
	// "running" (some claude is likely driving it); older reads as "done".
	ccLiveWindow = 30 * time.Second
	ccTitleRunes = 80
	ccPrevRunes  = 140
)

var ccCaps = map[string]bool{
	"create": false, "send": false, "approve": false,
	"observe": true, "children": false,
}

// CCBackend implements web.Backend over the Claude Code session store. With
// drive disabled it is observe-only (list/read/tail); EnableDrive (cc_drive.go)
// turns on the interactive path (attach a `claude` subprocess, send turns,
// bridge tool approval to the browser).
type CCBackend struct {
	root string           // ~/.claude/projects
	now  func() time.Time // injectable clock (tests)

	mu    sync.Mutex
	index map[string]string       // "cc:<uuid>" -> jsonl path (refreshed on scan)
	cache map[string]ccCacheEntry // path -> summary keyed by mtime+size

	// drive config, set by EnableDrive; a nil hub means observe-only.
	lifeCtx context.Context
	hub     *ephemeralHub
	baseURL string // e.g. http://127.0.0.1:7777 (for the hook callback)
	token   string // bearer the hook authenticates with
	exePath string // the carlos binary, for the hook command

	driveMu sync.Mutex
	drivers map[string]*ccDriver  // attached thread -> live process
	pending map[string]*ccPending // approval request_id -> blocked hook
}

type ccCacheEntry struct {
	modNano int64
	size    int64
	summary ThreadSummary
}

// NewCCBackend builds the adapter rooted at ~/.claude/projects. A missing
// root is fine: the backend simply lists nothing.
func NewCCBackend() *CCBackend {
	root := ""
	if home, err := os.UserHomeDir(); err == nil {
		root = filepath.Join(home, ".claude", "projects")
	}
	return newCCBackendAt(root)
}

// newCCBackendAt is the test seam (root + default clock).
func newCCBackendAt(root string) *CCBackend {
	return &CCBackend{
		root:  root,
		now:   time.Now,
		index: map[string]string{},
		cache: map[string]ccCacheEntry{},
	}
}

func (b *CCBackend) Name() string { return ccBackendName }

// ListThreads scans the project store for recent sessions, newest first.
// Stat + a single cheap scan per file, cached by mtime+size so the 3s
// roster poll is free after warm-up. A missing root yields an empty roster,
// never an error (the carlos roster must not blank if CC is absent).
func (b *CCBackend) ListThreads(ctx context.Context) ([]ThreadSummary, error) {
	if b.root == "" {
		return []ThreadSummary{}, nil
	}
	projects, err := os.ReadDir(b.root)
	if err != nil {
		if os.IsNotExist(err) {
			return []ThreadSummary{}, nil
		}
		return nil, err
	}
	cutoff := b.now().Add(-ccRecencyWindow)
	out := []ThreadSummary{}
	idx := map[string]string{}
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		dir := filepath.Join(b.root, proj.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			id := ccBackendName + ":" + strings.TrimSuffix(f.Name(), ".jsonl")
			path := filepath.Join(dir, f.Name())
			idx[id] = path
			out = append(out, b.summaryFor(id, path, info))
		}
	}
	// Newest first. UpdatedAt is millisecond RFC3339, so lexicographic
	// order is chronological.
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })

	b.mu.Lock()
	b.index = idx
	b.mu.Unlock()
	return out, nil
}

// GetThread returns one CC session's summary, resolving the file even when
// it sits outside the recency window (direct open by id).
func (b *CCBackend) GetThread(ctx context.Context, id string) (ThreadSummary, bool, error) {
	path, ok := b.pathFor(id)
	if !ok {
		return ThreadSummary{}, false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return ThreadSummary{}, false, nil
	}
	return b.summaryFor(id, path, info), true, nil
}

// summaryFor builds (or returns cached) the roster summary for one session
// file. The cache key is mtime+size, so an unchanged file is never re-read.
func (b *CCBackend) summaryFor(id, path string, info os.FileInfo) ThreadSummary {
	mod := info.ModTime()
	b.mu.Lock()
	if e, ok := b.cache[path]; ok && e.modNano == mod.UnixNano() && e.size == info.Size() {
		s := e.summary
		b.mu.Unlock()
		return s
	}
	b.mu.Unlock()

	data, _ := os.ReadFile(path)
	sc := ccScanSession(data)

	title := truncateRunes(sc.FirstUser, ccTitleRunes)
	if title == "" {
		title = "claude code session"
	}
	created := rfc3339(mod)
	if sc.FirstTS != "" {
		created = ccTimestamp(sc.FirstTS)
	}
	state := "done"
	if b.now().Sub(mod) < ccLiveWindow {
		state = "running"
	}
	frame := ""
	if sc.Cwd != "" {
		frame = filepath.Base(sc.Cwd) // the project name, as a lightweight "where"
	}

	s := ThreadSummary{
		ID:           id,
		Title:        title,
		Model:        sc.Model,
		State:        state,
		Attached:     false,
		CreatedAt:    created,
		UpdatedAt:    rfc3339(mod),
		Preview:      truncateRunes(sc.FirstUser, ccPrevRunes),
		UserMsgs:     sc.UserMsgs,
		Frame:        frame,
		Backend:      ccBackendName,
		Capabilities: b.caps(),
	}
	b.mu.Lock()
	b.cache[path] = ccCacheEntry{modNano: mod.UnixNano(), size: info.Size(), summary: s}
	b.mu.Unlock()
	return s
}

// ReadEvents projects the session transcript into wire events with seq in
// (from, upTo). A missing/unreadable file yields an empty transcript, not an
// error (the thread simply renders empty).
func (b *CCBackend) ReadEvents(ctx context.Context, id string, from, upTo int64) ([]WireEvent, error) {
	path, ok := b.pathFor(id)
	if !ok {
		return []WireEvent{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []WireEvent{}, nil
	}
	out := []WireEvent{}
	for _, we := range ccRecordsToWire(id, data) {
		if we.Seq <= from {
			continue
		}
		if upTo > 0 && we.Seq >= upTo {
			break
		}
		out = append(out, we)
	}
	return out, nil
}

// Subscribe live-tails the session file by polling: every tick it re-projects
// the file and forwards any wire event past the last delivered seq. The
// initial cursor is the file's current max seq, so the tail delivers only
// future appends while the SSE backfill replays history (the seq dedupe in
// the handler reconciles any overlap, exactly as for carlos). The returned
// unsubscribe is idempotent and stops the goroutine.
func (b *CCBackend) Subscribe(id string) (<-chan WireEvent, func(), error) {
	out := make(chan WireEvent, 64)
	done := make(chan struct{})
	var once sync.Once
	unsub := func() { once.Do(func() { close(done) }) }

	path, ok := b.pathFor(id)
	if !ok {
		// No file: a stream that never emits until the client disconnects.
		// (The SSE handler closes via unsub; ReadEvents already returned the
		// empty transcript.)
		go func() {
			<-done
			close(out)
		}()
		return out, unsub, nil
	}

	last := int64(0)
	if data, err := os.ReadFile(path); err == nil {
		if ev := ccRecordsToWire(id, data); len(ev) > 0 {
			last = ev[len(ev)-1].Seq
		}
	}

	go func() {
		defer close(out)
		t := time.NewTicker(ccPollInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				for _, we := range ccRecordsToWire(id, data) {
					if we.Seq <= last {
						continue
					}
					select {
					case out <- we:
						last = we.Seq
					case <-done:
						return
					}
				}
			}
		}
	}()
	return out, unsub, nil
}

// pathFor resolves a "cc:<uuid>" id to a session file path, refreshing the
// scan index on a miss and finally searching by uuid across project dirs (so
// a session outside the recency window is still openable).
func (b *CCBackend) pathFor(id string) (string, bool) {
	b.mu.Lock()
	p, ok := b.index[id]
	b.mu.Unlock()
	if ok {
		return p, true
	}
	_, _ = b.ListThreads(context.Background())
	b.mu.Lock()
	p, ok = b.index[id]
	b.mu.Unlock()
	if ok {
		return p, true
	}
	return b.findByUUID(id)
}

func (b *CCBackend) findByUUID(id string) (string, bool) {
	uuid := strings.TrimPrefix(id, ccBackendName+":")
	if uuid == "" || uuid == id || b.root == "" {
		return "", false
	}
	projects, err := os.ReadDir(b.root)
	if err != nil {
		return "", false
	}
	target := uuid + ".jsonl"
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		p := filepath.Join(b.root, proj.Name(), target)
		if _, err := os.Stat(p); err == nil {
			b.mu.Lock()
			b.index[id] = p
			b.mu.Unlock()
			return p, true
		}
	}
	return "", false
}
