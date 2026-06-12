package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// sseFrame is one parsed SSE message: its id (from "id:") and the decoded
// WireEvent (from "data:").
type sseFrame struct {
	id    string
	event WireEvent
}

// readSSE consumes the recorder body and returns parsed frames, ignoring
// heartbeat comment lines.
func readSSE(t *testing.T, body string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	var curID string
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ": "):
			// heartbeat, ignore
		case strings.HasPrefix(line, "id: "):
			curID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			var we WireEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &we); err != nil {
				t.Fatalf("bad SSE data line %q: %v", line, err)
			}
			frames = append(frames, sseFrame{id: curID, event: we})
			curID = ""
		}
	}
	return frames
}

func TestSSE_BackfillThenLiveNoDuplicates(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "one", "first message") // seq 1
	appendEvent(t, log, "t1", agent.EvtAssistantMessage, agent.MessagePayload{Text: "first reply"})

	// Drive the handler on a goroutine; append a live event mid-stream;
	// then cancel so the handler returns and we can inspect the body.
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/threads/t1/stream?from=0&token="+testToken, nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.Handler().ServeHTTP(rec, req)
		close(done)
	}()

	// Give the handler time to subscribe + backfill, then append a live
	// event. The splice must not re-deliver the backfilled seqs.
	time.Sleep(60 * time.Millisecond)
	liveSeq := appendEvent(t, log, "t1", agent.EvtUserMessage, agent.MessagePayload{Text: "live message"})
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	frames := readSSE(t, rec.Body.String())
	if len(frames) < 3 {
		t.Fatalf("expected >=3 frames (2 backfill + 1 live), got %d: %s", len(frames), rec.Body.String())
	}

	// No duplicate seqs (splice dedupe).
	seen := map[int64]int{}
	var sawLive bool
	for _, f := range frames {
		if f.event.Seq > 0 {
			seen[f.event.Seq]++
		}
		if f.event.Seq == liveSeq {
			sawLive = true
			if f.id != strconv.FormatInt(liveSeq, 10) {
				t.Errorf("live frame id = %q, want %d", f.id, liveSeq)
			}
		}
	}
	for seq, n := range seen {
		if n > 1 {
			t.Errorf("seq %d delivered %d times (splice should dedupe)", seq, n)
		}
	}
	if !sawLive {
		t.Errorf("live event seq %d never delivered", liveSeq)
	}
}

func TestSSE_EphemeralSnapshotOnConnect(t *testing.T) {
	// A backend with in-flight delta + a pending approval should be
	// snapshotted right after connect (spec §9.3 step 4).
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	seedThread(t, log, "t1", "one", "hi")
	be := &snapshotBackend{
		liveText: "carlos is typing...",
		pending: []WireEvent{{
			Thread: "t1", TS: rfc3339(time.Now().UTC()), Kind: "approval_request",
			Data: map[string]any{"request_id": "req_1", "name": "Bash"},
		}},
	}
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken, Backend: be})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/threads/t1/stream?from=0&token="+testToken, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(rec, req); close(done) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	frames := readSSE(t, rec.Body.String())
	var sawDelta, sawApproval bool
	for _, f := range frames {
		if f.event.Kind == "delta" {
			sawDelta = true
			if f.id != "" {
				t.Error("ephemeral delta must not carry an id (would corrupt the cursor)")
			}
		}
		if f.event.Kind == "approval_request" {
			sawApproval = true
		}
	}
	if !sawDelta {
		t.Error("expected a delta snapshot on connect")
	}
	if !sawApproval {
		t.Error("expected the pending approval to be re-emitted on connect")
	}
}

func TestSSE_HubFanout(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "one", "hi")

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/threads/t1/stream?from=0&token="+testToken, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(rec, req); close(done) }()

	time.Sleep(60 * time.Millisecond)
	// Publish an ephemeral delta through the hub (what WebTextSource does).
	s.Hub().publish(WireEvent{Thread: "t1", TS: rfc3339(time.Now().UTC()), Kind: "delta", Data: map[string]any{"text": "hi"}})
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	frames := readSSE(t, rec.Body.String())
	var sawHubDelta bool
	for _, f := range frames {
		if f.event.Kind == "delta" && f.event.Data.(map[string]any)["text"] == "hi" {
			sawHubDelta = true
		}
	}
	if !sawHubDelta {
		t.Error("hub-published delta never reached the SSE client")
	}
}

// snapshotBackend is a read-only backend with canned ephemeral state for
// the reconnect-snapshot test.
type snapshotBackend struct {
	readOnlyBackend
	liveText string
	pending  []WireEvent
}

func (b *snapshotBackend) LiveText(string) string              { return b.liveText }
func (b *snapshotBackend) PendingApprovals(string) []WireEvent { return b.pending }
