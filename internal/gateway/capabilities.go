package gateway

// OutboundCapabilities is the struct-shape of the capability matrix in
// the gateway architecture spec. The Broker reads these to:
//
//  1. Reject a Send when the chosen adapter can't render the kind
//     (e.g. routing config sends a ConversationReply to ntfy).
//  2. Truncate Actions for adapters with FixedChoice limits.
//  3. Tell the user, via the manage view, which channels degrade a
//     given approval — ntfy's "revise" without text is the canonical
//     case worth flagging.
//
// The fields mirror the columns in the spec's capability matrix one for
// one; add new dimensions here rather than introducing a parallel
// capability registry.
type OutboundCapabilities struct {
	// Push reports whether the adapter can deliver a Notification
	// envelope at all. False makes the adapter useless as anything but
	// an inbound source; the broker treats Send as an immediate failure.
	Push bool

	// FixedChoiceHITL reports whether the channel can render a small
	// set of one-tap response buttons. MaxActions bounds how many.
	FixedChoiceHITL bool

	// MaxActions is the per-envelope upper bound on len(Actions). The
	// broker truncates excess buttons before Send and records the loss
	// in the receipt. 0 means "no limit" (or "FixedChoiceHITL=false,
	// don't ask").
	MaxActions int

	// FreeFormTextInbound reports whether the adapter can carry user-
	// typed text back to the broker. False excludes the channel from
	// routing.conversations.
	FreeFormTextInbound bool

	// FileImageInbound reports whether the adapter can carry user-sent
	// attachments. Not yet consumed by any code path; the agent loop
	// will plumb attachments through when conversation-mode lands.
	FileImageInbound bool

	// DiffRichApproval reports whether the channel can render a code-
	// diff approval view legibly. ntfy/Telegram are "partial" in the
	// spec matrix; we collapse partial → false here and the manage view
	// surfaces the degradation.
	DiffRichApproval bool

	// NeedsPublicEndpoint reports whether the adapter requires an
	// inbound URL the platform can reach. ntfy's action buttons need
	// this when used against the public ntfy.sh; Telegram does not.
	// Surfaced to onboarding so the user knows when to enable Tailscale
	// Funnel.
	NeedsPublicEndpoint bool
}

// SupportsKind reports whether the adapter can render an OutboundKind
// faithfully. Used by Broker.Send to short-circuit a misrouted
// envelope.
func (c OutboundCapabilities) SupportsKind(k OutboundKind) bool {
	switch k {
	case OutboundNotification:
		return c.Push
	case OutboundApprovalRequest:
		return c.Push && c.FixedChoiceHITL
	case OutboundConversationReply:
		return c.Push && c.FreeFormTextInbound
	}
	return false
}
