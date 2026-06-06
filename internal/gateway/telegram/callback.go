package telegram

import (
	"errors"
	"fmt"
	"strings"
)

// Telegram limits callback_data to 64 bytes (UTF-8). We pack two pieces
// into that budget:
//
//	<actionID>:<artifactID>
//
// actionID is one of the canonical Decision IDs ("approve" | "revise" |
// "reject") — at most 7 bytes — plus a single-byte separator. The
// broker mints ArtifactIDs as ULIDs (26 bytes), so a well-formed
// callback_data clocks in at 26 + 1 + 7 = 34 bytes, comfortably under
// the 64-byte ceiling.
//
// We enforce the ceiling explicitly so a future change that lets the
// broker use longer IDs surfaces here rather than at the Telegram edge
// where the failure mode is "the button silently does nothing."

// callbackDataMaxBytes is Telegram's hard limit on callback_data length.
// Documented at https://core.telegram.org/bots/api#inlinekeyboardbutton.
const callbackDataMaxBytes = 64

// callbackDataSeparator splits actionID from artifactID. Picked because
// neither ULIDs nor our canonical action IDs contain a colon, so the
// parse leg is unambiguous.
const callbackDataSeparator = ":"

// ErrCallbackTooLong is returned by EncodeCallbackData when the encoded
// form would exceed Telegram's 64-byte limit. The caller is responsible
// for either truncating the inputs or surfacing the error to the user.
var ErrCallbackTooLong = errors.New("telegram: callback_data exceeds 64-byte limit")

// ErrCallbackMalformed is returned by DecodeCallbackData when the
// payload is missing the separator or one half is empty.
var ErrCallbackMalformed = errors.New("telegram: malformed callback_data")

// EncodeCallbackData packs actionID and artifactID into Telegram's
// callback_data field. Returns ErrCallbackTooLong if the result would
// exceed 64 bytes — see file-level comment for the budget rationale.
//
// Both inputs are validated: each must be non-empty and free of the
// separator byte. We do not escape; the contract is that callers use
// known-safe IDs (the three Decision constants and broker-minted
// ULIDs).
func EncodeCallbackData(actionID, artifactID string) (string, error) {
	if actionID == "" {
		return "", fmt.Errorf("%w: empty action id", ErrCallbackMalformed)
	}
	if artifactID == "" {
		return "", fmt.Errorf("%w: empty artifact id", ErrCallbackMalformed)
	}
	if strings.Contains(actionID, callbackDataSeparator) {
		return "", fmt.Errorf("%w: action id contains separator", ErrCallbackMalformed)
	}
	if strings.Contains(artifactID, callbackDataSeparator) {
		return "", fmt.Errorf("%w: artifact id contains separator", ErrCallbackMalformed)
	}
	out := actionID + callbackDataSeparator + artifactID
	if len(out) > callbackDataMaxBytes {
		return "", fmt.Errorf("%w: %d bytes", ErrCallbackTooLong, len(out))
	}
	return out, nil
}

// DecodeCallbackData reverses EncodeCallbackData. Returns the actionID,
// artifactID pair on success; ErrCallbackMalformed if the input is
// missing the separator or has an empty half.
//
// We deliberately do not validate that actionID is one of the canonical
// Decision constants here — that's the broker's job. Adapters stay
// dumb; if someone wires a non-canonical action button, the broker is
// the right place to drop the inbound.
func DecodeCallbackData(data string) (actionID, artifactID string, err error) {
	if len(data) > callbackDataMaxBytes {
		return "", "", fmt.Errorf("%w: %d bytes", ErrCallbackTooLong, len(data))
	}
	idx := strings.Index(data, callbackDataSeparator)
	if idx < 0 {
		return "", "", fmt.Errorf("%w: missing separator", ErrCallbackMalformed)
	}
	actionID = data[:idx]
	artifactID = data[idx+1:]
	if actionID == "" || artifactID == "" {
		return "", "", fmt.Errorf("%w: empty half", ErrCallbackMalformed)
	}
	return actionID, artifactID, nil
}
