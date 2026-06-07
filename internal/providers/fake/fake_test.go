package fake

import (
	"context"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

func TestNew_StoresNameAndScript(t *testing.T) {
	s := Script{{Kind: providers.EventTextDelta, Text: "x"}}
	p := New("test-provider", s)
	if p.Name() != "test-provider" {
		t.Errorf("Name=%q want test-provider", p.Name())
	}
	if len(p.script) != 1 {
		t.Errorf("script len=%d want 1", len(p.script))
	}
}

func TestCapabilities_ZeroByDefault(t *testing.T) {
	p := New("x", nil)
	c := p.Capabilities()
	zero := providers.Capabilities{}
	if c != zero {
		t.Errorf("Capabilities=%+v want zero value %+v", c, zero)
	}
}

func TestLastRequest_ZeroBeforeStream(t *testing.T) {
	p := New("x", nil)
	if p.LastRequest().System != "" {
		t.Errorf("LastRequest.System=%q want empty before Stream", p.LastRequest().System)
	}
}

func TestStream_EmitsFullScriptThenCloses(t *testing.T) {
	s := CannedScript()
	p := New("c", s)
	ch, err := p.Stream(context.Background(), providers.Request{System: "you are carlos"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := 0
	for range ch {
		got++
	}
	if got != len(s) {
		t.Errorf("got %d events, want %d", got, len(s))
	}
	if last := p.LastRequest(); last.System != "you are carlos" {
		t.Errorf("LastRequest.System=%q, want pinned through", last.System)
	}
}

func TestStream_RespectsStopAfter(t *testing.T) {
	s := CannedScript()
	p := New("c", s).WithStopAfter(2)
	ch, _ := p.Stream(context.Background(), providers.Request{})
	got := 0
	for range ch {
		got++
	}
	if got != 2 {
		t.Errorf("stopAfter=2 emitted %d events", got)
	}
}

func TestStream_StopAfterLargerThanScriptEmitsAll(t *testing.T) {
	s := CannedScript()
	p := New("c", s).WithStopAfter(100)
	ch, _ := p.Stream(context.Background(), providers.Request{})
	got := 0
	for range ch {
		got++
	}
	if got != len(s) {
		t.Errorf("stopAfter > script emitted %d, want %d", got, len(s))
	}
}

func TestStream_CtxCancelHaltsEmission(t *testing.T) {
	s := CannedScript()
	p := New("c", s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, _ := p.Stream(ctx, providers.Request{})
	got := 0
	for range ch {
		got++
	}
	if got > len(s) {
		t.Errorf("cancelled ctx still emitted %d", got)
	}
}

func TestWithStopAfter_DoesNotShareMutexWithSource(t *testing.T) {
	p1 := New("a", CannedScript())
	p2 := p1.WithStopAfter(1)
	if p1 == p2 {
		t.Error("WithStopAfter returned same pointer")
	}
	if _, err := p1.Stream(context.Background(), providers.Request{System: "p1"}); err != nil {
		t.Fatalf("p1.Stream: %v", err)
	}
	if _, err := p2.Stream(context.Background(), providers.Request{System: "p2"}); err != nil {
		t.Fatalf("p2.Stream: %v", err)
	}
	if p1.LastRequest().System != "p1" || p2.LastRequest().System != "p2" {
		t.Errorf("LastRequest not isolated: p1=%q p2=%q", p1.LastRequest().System, p2.LastRequest().System)
	}
}

func TestCannedScript_ShapeIsStable(t *testing.T) {
	s := CannedScript()
	if len(s) == 0 {
		t.Fatal("CannedScript empty")
	}
	hasToolStart, hasStop := false, false
	for _, ev := range s {
		if ev.Kind == providers.EventToolUseStart {
			hasToolStart = true
		}
		if ev.Kind == providers.EventStopReason {
			hasStop = true
		}
	}
	if !hasToolStart {
		t.Error("CannedScript missing EventToolUseStart")
	}
	if !hasStop {
		t.Error("CannedScript missing EventStopReason")
	}
}
