// Package fake is a deterministic provider used by Phase 1 preflight tests.
// It emits a scripted sequence of provider.Event values from a Go-literal
// script, with no network, no goroutine surprises, and no time-based
// behavior beyond what the caller controls.
//
// The shape mirrors providers.Provider so the same wiring (provider →
// agent loop → event log → projection) can be exercised end-to-end. The
// real Anthropic / OpenAI clients land in Phase 1d / Phase 2.
package fake

import (
	"context"
	"fmt"

	"github.com/georgebuilds/carlos/internal/providers"
)

// Script is an ordered list of events the provider will emit on Stream.
// Each call to Stream walks a fresh copy of the script — no internal state
// leaks between turns.
type Script []providers.Event

// Provider is a providers.Provider that replays a Script.
type Provider struct {
	name   string
	caps   providers.Capabilities
	script Script
	// optional: stop after this many events to simulate truncation; 0 = full
	stopAfter int
}

// New constructs a Provider that emits the given script.
func New(name string, script Script) *Provider {
	return &Provider{name: name, script: script}
}

// WithStopAfter returns a copy that halts emission after n events. Used to
// simulate provider stream truncation in the kill-and-resume test.
func (p *Provider) WithStopAfter(n int) *Provider {
	cp := *p
	cp.stopAfter = n
	return &cp
}

func (p *Provider) Name() string                       { return p.name }
func (p *Provider) Capabilities() providers.Capabilities { return p.caps }

func (p *Provider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event, len(p.script))
	go func() {
		defer close(ch)
		limit := len(p.script)
		if p.stopAfter > 0 && p.stopAfter < limit {
			limit = p.stopAfter
		}
		for i := 0; i < limit; i++ {
			select {
			case <-ctx.Done():
				return
			case ch <- p.script[i]:
			}
		}
	}()
	return ch, nil
}

// CannedScript returns a representative script: a couple of text deltas, a
// tool call, a tool result echo, and a stop event. Callers can append/prepend.
func CannedScript() Script {
	return Script{
		{Kind: providers.EventTextDelta, Text: "Hello, "},
		{Kind: providers.EventTextDelta, Text: "Boss. "},
		{Kind: providers.EventToolUseStart, ToolUse: &providers.ToolUse{ID: "tool-1", Name: "bash", Input: []byte(`{"cmd":"ls /tmp"}`)}},
		{Kind: providers.EventToolUseEnd, ToolUse: &providers.ToolUse{ID: "tool-1", Name: "bash"}},
		{Kind: providers.EventTextDelta, Text: "Found 3 entries."},
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	}
}

// Sanity: provider matches the interface at compile time.
var _ providers.Provider = (*Provider)(nil)

// Just so the package never goes dead in code analysis if unused for a build:
var _ = fmt.Sprint
