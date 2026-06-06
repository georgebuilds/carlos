// Package ntfy implements the gateway.Adapter for ntfy.sh.
//
// ntfy is the spec's "fire-and-forget HITL" channel: HTTP POST publishes
// to a topic, push subscribers receive the message, and up to three
// "http" action buttons let the user respond with one tap. The publish
// API has two flavors — the plain-body form (priority/title carried in
// X-* headers) and the structured JSON form. We use the JSON form
// uniformly for two reasons:
//
//  1. Action buttons are first-class JSON: a [{"action":"http", ...}]
//     array attached to the publish, no fragile X-Actions header parser.
//  2. The server's response is a JSON publish receipt with an `id` field
//     when we accept JSON in return — exactly what we need to populate
//     DeliveryReceipt.ProviderRef without speculative parsing.
//
// The shapes in this file mirror the publish + receipt subset we care
// about. They are intentionally minimal: ntfy supports a much wider
// publish surface (attachments, icons, email forwarding) that carlos
// does not exercise in this slice.
//
// Reference: https://docs.ntfy.sh/publish/#publish-as-json
package ntfy

// publishRequest is the structured ntfy publish body. We POST it to the
// server *root* with Content-Type: application/json and the topic
// embedded in the payload — the alternative (POST /<topic>) cannot
// carry actions cleanly in plain-body mode.
type publishRequest struct {
	// Topic is the destination ntfy topic. Required.
	Topic string `json:"topic"`

	// Message is the body text. Markdown is supported by recent ntfy
	// clients but is rendered as plaintext on older ones — adapters
	// should not rely on formatting fidelity.
	Message string `json:"message,omitempty"`

	// Title is the short headline displayed above the body.
	Title string `json:"title,omitempty"`

	// Priority is 1..5; ntfy treats 3 as default, 5 as max ("urgent"),
	// 1 as min ("silent push"). The mapping from gateway.Urgency lives
	// in Config.PriorityMap.
	Priority int `json:"priority,omitempty"`

	// Tags are emoji shortcodes / freeform strings rendered as a chip
	// row under the title. We do not surface them today but the field
	// exists so config-supplied Headers["X-Tags"] could later migrate
	// into the typed JSON form.
	Tags []string `json:"tags,omitempty"`

	// Actions carries the action-button row. ntfy caps this at 3
	// per message; the adapter truncates before send.
	Actions []publishAction `json:"actions,omitempty"`
}

// publishAction is one "http" action button. ntfy supports "view" and
// "broadcast" actions too, but we only emit "http" — it's the only one
// that round-trips back into our handler without an Android intent.
//
// The button fires Method against URL, attaching Headers. Body is
// ignored for GET; we send POST with an empty body and rely on the URL
// query string to carry the signed token (so a curious user tapping
// the button from a web subscriber doesn't have to deal with form
// encoding quirks).
type publishAction struct {
	Action  string            `json:"action"`            // always "http"
	Label   string            `json:"label"`             // user-visible button text
	URL     string            `json:"url"`               // includes ?t=<token>
	Method  string            `json:"method,omitempty"`  // "POST"
	Headers map[string]string `json:"headers,omitempty"` // bearer auth etc.
	Clear   bool              `json:"clear,omitempty"`   // dismiss notification on tap
}

// publishResponse is the JSON receipt ntfy returns when we publish with
// Content-Type: application/json. The shape is documented but ntfy.sh
// has historically reserved the right to add fields — we only decode
// the ones we need and let unknown fields drift through.
type publishResponse struct {
	ID    string `json:"id"`    // ntfy-assigned message id; used as ProviderRef
	Time  int64  `json:"time"`  // unix seconds the server received the publish
	Event string `json:"event"` // typically "message"
	Topic string `json:"topic"`
}
