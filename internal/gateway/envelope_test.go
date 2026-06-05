package gateway_test

import (
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

func TestOutboundEnvelope_Validate(t *testing.T) {
	good := gateway.OutboundEnvelope{
		Kind:  gateway.OutboundNotification,
		Title: "Hello",
	}
	if err := good.Validate(); err != nil {
		t.Errorf("good envelope: %v", err)
	}

	approval := gateway.OutboundEnvelope{
		Kind:       gateway.OutboundApprovalRequest,
		Title:      "Review me",
		Body:       "body",
		ArtifactID: "art-1",
		Actions:    gateway.CanonicalActions(),
	}
	if err := approval.Validate(); err != nil {
		t.Errorf("approval envelope: %v", err)
	}

	cases := map[string]gateway.OutboundEnvelope{
		"unknown kind": {Kind: gateway.OutboundKind("garbage"), Title: "x"},
		"empty body+title": {Kind: gateway.OutboundNotification},
		"approval no artifact": {
			Kind:    gateway.OutboundApprovalRequest,
			Title:   "x",
			Actions: gateway.CanonicalActions(),
		},
		"approval no actions": {
			Kind:       gateway.OutboundApprovalRequest,
			Title:      "x",
			ArtifactID: "art-2",
		},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if err := env.Validate(); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestInboundEnvelope_Validate(t *testing.T) {
	now := time.Now().UTC()
	msg := gateway.InboundEnvelope{
		Source:         gateway.SourceTelegram,
		GatewayEventID: "tg-1",
		Kind:           gateway.InboundMessage,
		Body:           "hi",
		ReceivedAt:     now,
	}
	if err := msg.Validate(); err != nil {
		t.Errorf("good message envelope: %v", err)
	}

	dec := gateway.InboundEnvelope{
		Source:         gateway.SourceNtfy,
		GatewayEventID: "ntfy-click-1",
		Kind:           gateway.InboundDecision,
		ArtifactID:     "art-3",
		Decision:       &gateway.Decision{Kind: gateway.DecisionApprove},
		ReceivedAt:     now,
	}
	if err := dec.Validate(); err != nil {
		t.Errorf("good decision envelope: %v", err)
	}

	cases := map[string]gateway.InboundEnvelope{
		"unknown source": {
			Source: gateway.Source("garbage"), GatewayEventID: "x",
			Kind: gateway.InboundMessage, Body: "x",
		},
		"missing gateway_event_id": {
			Source: gateway.SourceTelegram, Kind: gateway.InboundMessage, Body: "x",
		},
		"message empty body": {
			Source: gateway.SourceTelegram, GatewayEventID: "x",
			Kind: gateway.InboundMessage,
		},
		"decision missing artifact": {
			Source: gateway.SourceTelegram, GatewayEventID: "x",
			Kind: gateway.InboundDecision, Decision: &gateway.Decision{Kind: gateway.DecisionApprove},
		},
		"decision missing payload": {
			Source: gateway.SourceTelegram, GatewayEventID: "x",
			Kind: gateway.InboundDecision, ArtifactID: "art",
		},
		"decision bad kind": {
			Source: gateway.SourceTelegram, GatewayEventID: "x",
			Kind: gateway.InboundDecision, ArtifactID: "art",
			Decision: &gateway.Decision{Kind: gateway.DecisionKind("nope")},
		},
		"command empty body": {
			Source: gateway.SourceTelegram, GatewayEventID: "x",
			Kind: gateway.InboundCommand,
		},
		"unknown kind": {
			Source: gateway.SourceTelegram, GatewayEventID: "x",
			Kind: gateway.InboundKind("garbage"),
		},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if err := env.Validate(); err == nil {
				t.Errorf("expected error for %s", name)
			} else if strings.TrimSpace(err.Error()) == "" {
				t.Errorf("error message empty for %s", name)
			}
		})
	}
}

func TestCanonicalActions(t *testing.T) {
	got := gateway.CanonicalActions()
	if len(got) != 3 {
		t.Fatalf("CanonicalActions: want 3 got %d", len(got))
	}
	wantOrder := []string{"approve", "revise", "reject"}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("CanonicalActions[%d].ID = %q want %q", i, got[i].ID, w)
		}
		if got[i].Label == "" {
			t.Errorf("CanonicalActions[%d].Label empty", i)
		}
	}
}
