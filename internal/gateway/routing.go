package gateway

// RoutingConfig is the per-kind channel preference. The same envelope
// kind may fan out to multiple channels (a notification pinged to both
// ntfy and Telegram). For approvals the order is preference: first
// channel to land a Decision wins; later decisions are still logged but
// no-op.
//
// Empty lists mean "do not route this kind anywhere" - the broker
// returns no receipts and never fails the send. This is the right shape
// for the spec's default-quiet posture: a user that hasn't opted in
// sees no traffic.
type RoutingConfig struct {
	// Notifications: daemon results, scheduled-task completions, cost
	// alerts. Spec default ntfy + telegram.
	Notifications []Source `json:"notifications,omitempty"`
	// Approvals: HITL pending-artifact pings. Spec default
	// telegram + ntfy, with telegram first (the channel that supports
	// rich revise text wins races by default).
	Approvals []Source `json:"approvals,omitempty"`
	// Conversations: free-form chat with the user. ntfy excluded by
	// construction; only channels with FreeFormTextInbound capability
	// should appear here.
	Conversations []Source `json:"conversations,omitempty"`
}

// DefaultRoutingConfig matches the spec § Config shape § routing block.
// Useful as a starting point for tests + the onboarding wizard.
func DefaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		Notifications: []Source{SourceNtfy, SourceTelegram},
		Approvals:     []Source{SourceTelegram, SourceNtfy},
		Conversations: []Source{SourceTelegram},
	}
}

// ChannelsFor returns the routing list for k. Unknown kinds get an
// empty slice rather than a panic - a future kind is a forward-compat
// concern, not a runtime fault.
func (r RoutingConfig) ChannelsFor(k OutboundKind) []Source {
	switch k {
	case OutboundNotification:
		return r.Notifications
	case OutboundApprovalRequest:
		return r.Approvals
	case OutboundConversationReply:
		return r.Conversations
	}
	return nil
}
