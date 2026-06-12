package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// frameBackend is a stub mirroring the real backend's frame contract: the
// frame resolves at attach time and reads "" while detached (spec §11.4).
type frameBackend struct {
	readOnlyBackend
	attached map[string]bool
	frame    string
}

func (b *frameBackend) Attached(id string) bool { return b.attached[id] }
func (b *frameBackend) Frame(id string) string {
	if b.attached[id] {
		return b.frame
	}
	return ""
}
func (b *frameBackend) Attach(_ context.Context, id string) error {
	b.attached[id] = true
	return nil
}
func (b *frameBackend) Detach(id string) error {
	delete(b.attached, id)
	return nil
}

func newFrameServer(t *testing.T) (*Server, *frameBackend) {
	t.Helper()
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	be := &frameBackend{attached: map[string]bool{}, frame: "work"}
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken, Backend: be})
	seedThread(t, log, "t1", "thread one", "hello")
	seedThread(t, log, "t2", "thread two", "hi there")
	return s, be
}

// The attach response is the refreshed summary; it must carry the frame the
// backend resolved at attach so the SPA can show it without waiting for the
// next roster poll.
func TestThreads_AttachResponseCarriesResolvedFrame(t *testing.T) {
	s, _ := newFrameServer(t)

	rec := do(t, s, "POST", "/api/threads/t1/attach", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got ThreadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Attached {
		t.Error("attach response should report attached=true")
	}
	if got.Frame != "work" {
		t.Errorf("attach response frame = %q, want \"work\"", got.Frame)
	}
}

// The roster reflects per-thread attachment: an attached thread carries the
// resolved frame, a detached one carries "" (its frame resolves at attach).
func TestThreads_ListFrameFollowsAttachment(t *testing.T) {
	s, be := newFrameServer(t)
	be.attached["t1"] = true

	rec := do(t, s, "GET", "/api/threads", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d", rec.Code)
	}
	var got []ThreadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]ThreadSummary{}
	for _, ts := range got {
		byID[ts.ID] = ts
	}
	if byID["t1"].Frame != "work" {
		t.Errorf("attached thread frame = %q, want \"work\"", byID["t1"].Frame)
	}
	if byID["t2"].Frame != "" {
		t.Errorf("detached thread frame = %q, want \"\"", byID["t2"].Frame)
	}
}
