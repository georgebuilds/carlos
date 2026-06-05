// Test helpers shared across the agent package's _test.go files.
// Lives in the agent_test external test package so it can be reused
// from spawn_test, heartbeat_test, depth_test, restart_test without
// collision with production code.
package agent_test

import (
	"context"

	"github.com/georgebuilds/carlos/internal/providers"
)

// hangingProvider is a providers.Provider whose Stream blocks forever
// (until ctx is cancelled). Used by tests that want to spawn a child
// agent without letting it actually progress — e.g. concurrency-cap
// and orphan-sweep tests where we need the child to stay in `running`
// until the test triggers its own termination signal.
type hangingProvider struct{}

func newHangingProvider() *hangingProvider { return &hangingProvider{} }

func (hangingProvider) Name() string                         { return "hanging" }
func (hangingProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }

func (hangingProvider) Stream(ctx context.Context, _ providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

var _ providers.Provider = (*hangingProvider)(nil)
