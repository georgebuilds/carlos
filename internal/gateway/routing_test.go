package gateway_test

import (
	"reflect"
	"testing"

	"github.com/georgebuilds/carlos/internal/gateway"
)

func TestDefaultRoutingConfig_Matches_Spec(t *testing.T) {
	got := gateway.DefaultRoutingConfig()
	wantNotifications := []gateway.Source{gateway.SourceNtfy, gateway.SourceTelegram}
	wantApprovals := []gateway.Source{gateway.SourceTelegram, gateway.SourceNtfy}
	wantConversations := []gateway.Source{gateway.SourceTelegram}
	if !reflect.DeepEqual(got.Notifications, wantNotifications) {
		t.Errorf("notifications: want %v got %v", wantNotifications, got.Notifications)
	}
	if !reflect.DeepEqual(got.Approvals, wantApprovals) {
		t.Errorf("approvals: want %v got %v", wantApprovals, got.Approvals)
	}
	if !reflect.DeepEqual(got.Conversations, wantConversations) {
		t.Errorf("conversations: want %v got %v", wantConversations, got.Conversations)
	}
}

func TestRoutingConfig_ChannelsFor(t *testing.T) {
	r := gateway.RoutingConfig{
		Notifications: []gateway.Source{gateway.SourceNtfy},
		Approvals:     []gateway.Source{gateway.SourceTelegram},
		Conversations: []gateway.Source{gateway.SourceCustom},
	}
	cases := []struct {
		k    gateway.OutboundKind
		want []gateway.Source
	}{
		{gateway.OutboundNotification, []gateway.Source{gateway.SourceNtfy}},
		{gateway.OutboundApprovalRequest, []gateway.Source{gateway.SourceTelegram}},
		{gateway.OutboundConversationReply, []gateway.Source{gateway.SourceCustom}},
		{gateway.OutboundKind("unknown"), nil},
	}
	for _, tc := range cases {
		got := r.ChannelsFor(tc.k)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ChannelsFor(%q) = %v want %v", tc.k, got, tc.want)
		}
	}
}
