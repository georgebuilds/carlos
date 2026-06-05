package gateway_test

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/gateway"
)

func TestCapabilities_SupportsKind(t *testing.T) {
	full := gateway.Capabilities{
		Push:                true,
		FixedChoiceHITL:     true,
		FreeFormTextInbound: true,
		MaxActions:          3,
	}
	notifyOnly := gateway.Capabilities{Push: true}
	hitlOnly := gateway.Capabilities{Push: true, FixedChoiceHITL: true, MaxActions: 3}
	convo := gateway.Capabilities{Push: true, FreeFormTextInbound: true}

	cases := []struct {
		name string
		caps gateway.Capabilities
		kind gateway.OutboundKind
		want bool
	}{
		{"full-notification", full, gateway.OutboundNotification, true},
		{"full-approval", full, gateway.OutboundApprovalRequest, true},
		{"full-conversation", full, gateway.OutboundConversationReply, true},
		{"notify-only-approval", notifyOnly, gateway.OutboundApprovalRequest, false},
		{"notify-only-conversation", notifyOnly, gateway.OutboundConversationReply, false},
		{"hitl-conversation", hitlOnly, gateway.OutboundConversationReply, false},
		{"convo-approval", convo, gateway.OutboundApprovalRequest, false},
		{"no-push-anything", gateway.Capabilities{}, gateway.OutboundNotification, false},
		{"unknown-kind", full, gateway.OutboundKind("garbage"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.caps.SupportsKind(tc.kind); got != tc.want {
				t.Errorf("SupportsKind(%q) = %v want %v", tc.kind, got, tc.want)
			}
		})
	}
}
