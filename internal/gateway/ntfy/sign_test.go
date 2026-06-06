package ntfy

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

// testKey is a deterministic 32-byte HMAC key used across token tests.
// Production keys are generated at onboarding; the constant here just
// stands in for "any well-formed key" so failures point at logic, not
// at key shape.
var testKey = bytes32("ntfy-test-key-please-do-not-real")

func bytes32(s string) []byte {
	b := make([]byte, 32)
	copy(b, s)
	return b
}

func TestSignToken_RoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	payload := tokenPayload{
		EnvelopeID: "env-1",
		ArtifactID: "art-1",
		ActionID:   "approve",
		ExpUnixMs:  now.Add(time.Hour).UnixMilli(),
	}
	tok, err := signToken(testKey, payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.Contains(tok, ".") {
		t.Errorf("expected dotted token, got %q", tok)
	}
	got, err := verifyToken(testKey, tok, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != payload {
		t.Errorf("payload mismatch: got %+v want %+v", got, payload)
	}
}

func TestSignToken_MultipleActionIDs(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	for _, action := range []string{"approve", "revise", "reject"} {
		t.Run(action, func(t *testing.T) {
			p := tokenPayload{
				EnvelopeID: "env-x",
				ArtifactID: "art-x",
				ActionID:   action,
				ExpUnixMs:  now.Add(time.Hour).UnixMilli(),
			}
			tok, err := signToken(testKey, p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := verifyToken(testKey, tok, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.ActionID != action {
				t.Errorf("action: got %q want %q", got.ActionID, action)
			}
		})
	}
}

func TestSignToken_ShortKey(t *testing.T) {
	_, err := signToken([]byte("too-short"), tokenPayload{EnvelopeID: "e", ArtifactID: "a", ActionID: "approve", ExpUnixMs: 1})
	if !errors.Is(err, ErrSigningKeyTooShort) {
		t.Errorf("got %v want ErrSigningKeyTooShort", err)
	}
}

func TestSignToken_MissingFields(t *testing.T) {
	cases := []tokenPayload{
		{ArtifactID: "a", ActionID: "approve", ExpUnixMs: 1},
		{EnvelopeID: "e", ActionID: "approve", ExpUnixMs: 1},
		{EnvelopeID: "e", ArtifactID: "a", ExpUnixMs: 1},
	}
	for i, c := range cases {
		_, err := signToken(testKey, c)
		if !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("case %d: got %v want ErrTokenMalformed", i, err)
		}
	}
}

func TestVerifyToken_WrongKey(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := bytes32("DIFFERENT-key-bytes-for-this-tes")
	_, err = verifyToken(wrong, tok, now)
	if !errors.Is(err, ErrTokenBadSignature) {
		t.Errorf("got %v want ErrTokenBadSignature", err)
	}
}

func TestVerifyToken_TamperedPayload(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %q", tok)
	}
	// Flip a byte in the encoded payload to simulate tampering.
	tamperedPayload := flipFirstChar(parts[0])
	tampered := tamperedPayload + "." + parts[1]
	_, err = verifyToken(testKey, tampered, now)
	if !errors.Is(err, ErrTokenBadSignature) && !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("got %v want ErrTokenBadSignature or ErrTokenMalformed", err)
	}
}

// flipFirstChar swaps the first byte for a different one in the
// base64url alphabet, producing a valid-shape but different-content
// segment.
func flipFirstChar(s string) string {
	if len(s) == 0 {
		return "A"
	}
	first := s[0]
	repl := byte('A')
	if first == 'A' {
		repl = 'B'
	}
	return string(repl) + s[1:]
}

func TestVerifyToken_Expired(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Hour)
	_, err = verifyToken(testKey, tok, later)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("got %v want ErrTokenExpired", err)
	}
}

func TestVerifyToken_ExpAtBoundary(t *testing.T) {
	// The boundary check is `now.UnixMilli() >= exp`, so a token whose
	// exp equals now must verify as expired.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	tok, err := signToken(testKey, tokenPayload{
		EnvelopeID: "e", ArtifactID: "a", ActionID: "approve",
		ExpUnixMs: now.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyToken(testKey, tok, now); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("boundary: got %v want ErrTokenExpired", err)
	}
}

func TestVerifyToken_Malformed(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cases := []string{
		"",                  // empty
		"no-separator-here", // no dot
		".",                 // both halves empty
		"abc.",              // empty sig
		".abc",              // empty payload
		"!!!.!!!",           // not base64
		"YWJj.!!!",          // bad sig encoding
	}
	for _, c := range cases {
		_, err := verifyToken(testKey, c, now)
		if err == nil {
			t.Errorf("token %q: expected error, got nil", c)
		}
	}
}

func TestVerifyToken_NotJSONPayload(t *testing.T) {
	// Build a token whose payload bytes are valid base64url but not
	// JSON. We need a real HMAC over those bytes; signToken won't help
	// because it marshals struct → JSON. So we hand-craft.
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	raw := []byte("not json at all")
	pEnc := base64URLEncode(raw)
	// We compute the HMAC inline rather than re-export the internal.
	tok, err := signRawForTest(testKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, pEnc+".") {
		t.Fatalf("encoded payload mismatch: %s vs %s", tok, pEnc)
	}
	_, err = verifyToken(testKey, tok, now)
	if !errors.Is(err, ErrTokenMalformed) {
		t.Errorf("got %v want ErrTokenMalformed", err)
	}
}

// signRawForTest mirrors signToken but takes raw payload bytes — used
// in TestVerifyToken_NotJSONPayload to drive a non-JSON payload past
// the HMAC check.
func signRawForTest(key, raw []byte) (string, error) {
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	return base64URLEncode(raw) + "." + base64URLEncode(mac.Sum(nil)), nil
}
