// Action-token signing for the ntfy adapter.
//
// # Threat model
//
// ntfy topics are *public* unless self-hosted with auth - anyone who
// learns the topic name can subscribe to outbound messages AND see
// every action-button URL we publish. If those URLs were
// unauthenticated, any subscriber could forge a Decision inbound by
// replaying the URL from their own browser.
//
// The mitigation per the spec's "Risks tracked" entry is that we sign
// each action URL with a short-lived HMAC token. The token binds the
// envelope, artifact, action, and an expiry; the daemon-side handler
// (handler.go) verifies the HMAC and exp before turning the request
// into an InboundEnvelope.
//
// # Token format
//
// The token rides in the URL query string as ?t=<token>. On the wire:
//
//	token := base64url(payload_json) + "." + base64url(hmac_sha256)
//
// Where payload_json is the JSON encoding of tokenPayload (envelope_id,
// artifact_id, action_id, exp_unix_ms). HMAC is computed over the raw
// payload_json bytes - NOT over the base64-encoded form - so any
// canonicalization differences in base64 padding cannot smuggle past
// verification.
//
// We use base64url (RFC 4648 §5) with padding STRIPPED so the URL
// stays clean; encode + decode helpers handle the missing padding.
//
// We chose a dotted two-segment form rather than the more common
// JWT-style three-segment form because we don't need a separate header
// (algorithm is fixed; rotating to a new HMAC key is a deploy
// concern, not an in-token concern).
package ntfy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// minSigningKeyLen is the minimum acceptable HMAC key length. 32 bytes
// matches the SHA-256 block-output size; shorter keys reduce HMAC
// strength below the hash's collision resistance and are almost
// certainly a misconfiguration.
const minSigningKeyLen = 32

// tokenSeparator splits the encoded payload from the encoded HMAC.
// Picked because base64url's alphabet does not contain '.', so the
// split is unambiguous.
const tokenSeparator = "."

// Token verification errors. Callers (handler.go) compare via
// errors.Is so the HTTP layer can map each to a precise 4xx code.
var (
	// ErrTokenMalformed covers shape errors: missing separator, bad
	// base64, payload not JSON. Maps to 400 Bad Request.
	ErrTokenMalformed = errors.New("ntfy: token malformed")
	// ErrTokenBadSignature is returned when the HMAC does not match -
	// either a wrong key or a tampered payload. Maps to 401.
	ErrTokenBadSignature = errors.New("ntfy: token signature invalid")
	// ErrTokenExpired is returned when the embedded exp is in the
	// past. Maps to 401 (not 403) because the user can retry by
	// having the daemon re-publish.
	ErrTokenExpired = errors.New("ntfy: token expired")
	// ErrSigningKeyTooShort is returned by signToken when the
	// configured key is shorter than minSigningKeyLen.
	ErrSigningKeyTooShort = fmt.Errorf("ntfy: signing key must be >= %d bytes", minSigningKeyLen)
)

// tokenPayload is the per-action authorization claim. Fields are
// camel_case so that, if we ever expose token introspection (we won't
// in v1), the JSON matches the surrounding gateway envelope style.
type tokenPayload struct {
	EnvelopeID string `json:"envelope_id"`
	ArtifactID string `json:"artifact_id"`
	ActionID   string `json:"action_id"`
	ExpUnixMs  int64  `json:"exp_unix_ms"`
}

// signToken builds a fresh ?t=<...> token for the action button. The
// expiry is computed by the caller (the adapter, holding TokenTTL)
// rather than here so a single Send call can stamp consistent expiries
// across all three of an envelope's actions.
func signToken(key []byte, p tokenPayload) (string, error) {
	if len(key) < minSigningKeyLen {
		return "", ErrSigningKeyTooShort
	}
	if p.EnvelopeID == "" || p.ArtifactID == "" || p.ActionID == "" {
		return "", fmt.Errorf("%w: payload missing required field", ErrTokenMalformed)
	}
	payloadBytes, err := json.Marshal(p)
	if err != nil {
		// json.Marshal of a struct with only string/int fields cannot
		// fail in practice, but we propagate rather than panic.
		return "", fmt.Errorf("%w: %v", ErrTokenMalformed, err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	sig := mac.Sum(nil)
	return base64URLEncode(payloadBytes) + tokenSeparator + base64URLEncode(sig), nil
}

// verifyToken parses, HMAC-checks, and expiry-checks a token string.
// On success returns the decoded payload. Error types are documented
// on the package vars above so the HTTP layer can branch.
//
// `now` is the comparison instant for the expiry check; the adapter's
// Config.Now hook is threaded down here so tests can pin time.
func verifyToken(key []byte, token string, now time.Time) (tokenPayload, error) {
	if len(key) < minSigningKeyLen {
		return tokenPayload{}, ErrSigningKeyTooShort
	}
	parts := strings.Split(token, tokenSeparator)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return tokenPayload{}, fmt.Errorf("%w: want <payload>.<sig>", ErrTokenMalformed)
	}
	payloadBytes, err := base64URLDecode(parts[0])
	if err != nil {
		return tokenPayload{}, fmt.Errorf("%w: payload not base64url: %v", ErrTokenMalformed, err)
	}
	sigBytes, err := base64URLDecode(parts[1])
	if err != nil {
		return tokenPayload{}, fmt.Errorf("%w: sig not base64url: %v", ErrTokenMalformed, err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	expected := mac.Sum(nil)
	// Constant-time compare prevents a remote timing oracle from
	// reading the byte-by-byte equality progress of a forged HMAC.
	if !hmac.Equal(sigBytes, expected) {
		return tokenPayload{}, ErrTokenBadSignature
	}
	var p tokenPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return tokenPayload{}, fmt.Errorf("%w: payload not json: %v", ErrTokenMalformed, err)
	}
	if p.ExpUnixMs <= 0 {
		return tokenPayload{}, fmt.Errorf("%w: missing exp", ErrTokenMalformed)
	}
	if now.UnixMilli() >= p.ExpUnixMs {
		return tokenPayload{}, ErrTokenExpired
	}
	if p.EnvelopeID == "" || p.ArtifactID == "" || p.ActionID == "" {
		return tokenPayload{}, fmt.Errorf("%w: payload missing required field", ErrTokenMalformed)
	}
	return p, nil
}

// base64URLEncode is RFC 4648 §5 with padding stripped - same flavor
// JWTs use. Padding is purely a length marker for un-aligned inputs;
// the decoder reconstructs it from the segment length.
func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// base64URLDecode is the inverse of base64URLEncode. RawURLEncoding
// (no padding) is strict - it rejects '=' chars, which is what we want
// for a forgery check.
func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
