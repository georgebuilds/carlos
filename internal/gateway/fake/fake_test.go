package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/gateway/fake"
)

func TestFake_BasicSend(t *testing.T) {
	f := fake.New(gateway.SourceFake)
	env := gateway.OutboundEnvelope{
		ID:    "env-1",
		Kind:  gateway.OutboundNotification,
		Title: "hi",
	}
	r, err := f.Send(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != gateway.StatusDelivered {
		t.Errorf("status: want delivered got %q", r.Status)
	}
	if got := f.Sent(); len(got) != 1 || got[0].ID != "env-1" {
		t.Errorf("sent: %+v", got)
	}
}

func TestFake_SetFailure(t *testing.T) {
	f := fake.New(gateway.SourceFake)
	f.SetFailure("env-bad", errors.New("nope"))
	_, err := f.Send(context.Background(), gateway.OutboundEnvelope{ID: "env-bad", Kind: gateway.OutboundNotification, Title: "x"})
	if err == nil {
		t.Error("expected send error")
	}
}

func TestFake_PushBeforeStart_Errors(t *testing.T) {
	f := fake.New(gateway.SourceFake)
	if err := f.Push(context.Background(), gateway.InboundEnvelope{}); err == nil {
		t.Error("expected push-before-start error")
	}
}

func TestFake_StartStop(t *testing.T) {
	f := fake.New(gateway.SourceFake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := make(chan gateway.InboundEnvelope, 1)
	ingest := func(_ context.Context, env gateway.InboundEnvelope) error {
		called <- env
		return nil
	}
	go func() { _ = f.Start(ctx, ingest) }()
	select {
	case <-f.Started():
	case <-time.After(time.Second):
		t.Fatal("started signal missing")
	}

	env := gateway.InboundEnvelope{
		Source: gateway.SourceFake, GatewayEventID: "x",
		Kind: gateway.InboundMessage, Body: "hi",
	}
	if err := f.Push(ctx, env); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-called:
		if got.Body != "hi" {
			t.Errorf("ingest got %q", got.Body)
		}
	case <-time.After(time.Second):
		t.Error("ingest never called")
	}
	_ = f.Stop(ctx)
	// stop is idempotent
	_ = f.Stop(ctx)
}

func TestFake_Reset(t *testing.T) {
	f := fake.New(gateway.SourceFake)
	_, _ = f.Send(context.Background(), gateway.OutboundEnvelope{ID: "e", Kind: gateway.OutboundNotification, Title: "x"})
	if len(f.Sent()) != 1 || len(f.Receipts()) != 1 {
		t.Errorf("pre-reset state: sent=%d receipts=%d", len(f.Sent()), len(f.Receipts()))
	}
	f.Reset()
	if len(f.Sent()) != 0 || len(f.Receipts()) != 0 {
		t.Errorf("post-reset state not empty")
	}
}

func TestFake_WithCapabilities(t *testing.T) {
	c := gateway.OutboundCapabilities{Push: true, MaxActions: 2}
	f := fake.New(gateway.SourceFake, fake.WithCapabilities(c))
	if got := f.OutboundCapabilities(); got != c {
		t.Errorf("caps: want %+v got %+v", c, got)
	}
}

func TestFake_DoubleStartErrors(t *testing.T) {
	f := fake.New(gateway.SourceFake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil }) }()
	<-f.Started()
	if err := f.Start(ctx, func(context.Context, gateway.InboundEnvelope) error { return nil }); err == nil {
		t.Error("expected double-start error")
	}
}

func TestFake_InvalidNameFallsBackToFake(t *testing.T) {
	f := fake.New(gateway.Source(""))
	if f.Name() != gateway.SourceFake {
		t.Errorf("invalid name: want SourceFake got %q", f.Name())
	}
}
