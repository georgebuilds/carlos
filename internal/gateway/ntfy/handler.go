// Action-button callback handler for the ntfy adapter.
//
// The handler is the inbound half of the ntfy contract. When the user
// taps an action button on their phone, the ntfy server fires the
// configured http action - a POST against the public action endpoint
// we baked into the publish. That request hits the handler below.
//
// The daemon owns the public listener (Tailscale Funnel); the adapter
// only exposes a stdlib http.Handler that the daemon mounts at the
// configured path (e.g. /gateway/ntfy/action). Keeping the adapter
// out of the listener business simplifies the contract - Start does
// not bind a port, it only wires the IngestFunc.
//
// # Status codes (audited by handler_test.go)
//
//   - 405 - method other than POST. ntfy fires POST per our publish
//     spec; anything else is a misconfigured Action button or a poker.
//   - 400 - token missing or malformed. ntfy never produces a missing
//     token because the URL we publish always carries one; 400 means
//     someone is hitting the endpoint directly with garbage.
//   - 401 - token signature invalid OR expired. Both forms get the
//     same code so a brute-force probe can't distinguish "wrong key"
//     from "expired key" via the response.
//   - 500 - broker IngestFunc failed (event log down). The user's tap
//     will appear unhandled; ntfy's action UI surfaces the failure.
//   - 204 - success. No body - there is no subscriber to read it.
//
// # Replay
//
// The handler does NOT dedupe replayed tokens. Per the spec, ntfy's
// design lets the user tap a button multiple times; the broker's
// inbound dedupe (keyed on Source + GatewayEventID) is the right
// chokepoint. GatewayEventID is envelope_id+":"+action_id which is
// stable across replays of the same token, so the broker collapses
// duplicates without the adapter having to keep state.

package ntfy

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/georgebuilds/carlos/internal/gateway"
)

// actionTokenQueryParam is the query-string key the published action
// URL uses to carry the signed token. Mirrored by signAndAttach in
// ntfy.go which builds the URL.
const actionTokenQueryParam = "t"

// actionHandler is the http.Handler returned by Adapter.Handler. It
// closes over the adapter's signing key, ingest callback, and clock
// so the request hot-path stays allocation-light.
type actionHandler struct {
	key    []byte
	ingest func() gateway.IngestFunc // late-bound after Start
	now    func() time.Time
}

// ServeHTTP implements http.Handler. See the file-level comment for
// the status-code contract.
func (h *actionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.URL.Query().Get(actionTokenQueryParam)
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	payload, err := verifyToken(h.key, token, h.now())
	if err != nil {
		switch {
		case errors.Is(err, ErrTokenBadSignature), errors.Is(err, ErrTokenExpired):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case errors.Is(err, ErrTokenMalformed):
			http.Error(w, "bad token", http.StatusBadRequest)
		default:
			// ErrSigningKeyTooShort or any unforeseen condition;
			// surface as 500 because it's a server-side misconfig
			// and the user's tap should be retried after a fix.
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	// Translate the action_id back into the typed Decision shape.
	// Unknown action IDs are rejected here rather than at the broker:
	// the adapter is the only thing that knows the publish encoded a
	// canonical action_id, so it owns the round-trip integrity check.
	decisionKind := gateway.DecisionKind(payload.ActionID)
	if !decisionKind.Valid() {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	ingestFn := h.ingest()
	if ingestFn == nil {
		// Start has not been called yet - the adapter is technically
		// unwired. Surface as 503 so ntfy's action retry semantics
		// give the daemon a chance to come up.
		http.Error(w, "adapter not started", http.StatusServiceUnavailable)
		return
	}
	env := gateway.InboundEnvelope{
		// GatewayEventID combines envelope_id and action_id so a
		// replay of the same button taps is deduped by the broker,
		// but two different actions on the same envelope (the user
		// taps Approve then Reject) flow through as distinct rows.
		GatewayEventID: payload.EnvelopeID + ":" + payload.ActionID,
		Source:         gateway.SourceNtfy,
		From:           "ntfy:" + payload.EnvelopeID,
		Kind:           gateway.InboundDecision,
		ArtifactID:     payload.ArtifactID,
		Decision: &gateway.Decision{
			Kind: decisionKind,
		},
	}
	// ctx wraps the request context with a short cap so a misbehaving
	// ingest path can't pin the goroutine for the entire ntfy action
	// retry window. The broker's own retries are layered above us.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := ingestFn(ctx, env); err != nil {
		http.Error(w, "ingest failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
