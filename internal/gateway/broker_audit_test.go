package gateway_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/fake"
)

// When an outbound audit row fails to persist (here the event log is closed
// underneath the broker), the failure is surfaced via AuditErr rather than
// being silently dropped - the outbound log is a delivery contract.
func TestBroker_Send_SurfacesAuditWriteFailure(t *testing.T) {
	log := newLog(t)
	var audits int64
	b, err := gateway.New(gateway.Options{
		Log:      log,
		Routing:  gateway.RoutingConfig{Notifications: []gateway.Source{gateway.SourceFake}},
		Retry:    gateway.RetryConfig{MaxAttempts: 1, BackoffInitial: time.Millisecond, BackoffMax: 2 * time.Millisecond},
		Sleep:    func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		AuditErr: func(error) { atomic.AddInt64(&audits, 1) },
	})
	if err != nil {
		t.Fatalf("new broker: %v", err)
	}
	if err := b.Register(fake.New(gateway.SourceFake)); err != nil {
		t.Fatal(err)
	}

	// Close the log so the next audit write fails.
	_ = log.Close()

	_, _ = b.Send(context.Background(), gateway.OutboundEnvelope{
		Kind:  gateway.OutboundNotification,
		Title: "hi",
	})
	if atomic.LoadInt64(&audits) == 0 {
		t.Fatal("AuditErr was not invoked when the audit row failed to persist")
	}
}
