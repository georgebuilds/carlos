package telegram_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/gateway/telegram"
)

func TestEncodeDecodeCallbackData_RoundTrip(t *testing.T) {
	cases := []struct {
		action   string
		artifact string
	}{
		{"approve", "01HXYZ1234567890ABCDEFGHJK"},  // ULID-shaped
		{"revise", "art-1"},                        // short
		{"reject", "0123456789012345678901234567"}, // 28-char artifact
	}
	for _, c := range cases {
		data, err := telegram.EncodeCallbackData(c.action, c.artifact)
		if err != nil {
			t.Fatalf("encode %+v: %v", c, err)
		}
		gotAction, gotArtifact, err := telegram.DecodeCallbackData(data)
		if err != nil {
			t.Fatalf("decode %q: %v", data, err)
		}
		if gotAction != c.action || gotArtifact != c.artifact {
			t.Errorf("round-trip mismatch: in=(%q,%q) out=(%q,%q)",
				c.action, c.artifact, gotAction, gotArtifact)
		}
	}
}

func TestEncodeCallbackData_TooLongRejected(t *testing.T) {
	long := strings.Repeat("a", 100)
	_, err := telegram.EncodeCallbackData("approve", long)
	if err == nil {
		t.Fatal("expected too-long error")
	}
	if !errors.Is(err, telegram.ErrCallbackTooLong) {
		t.Errorf("want ErrCallbackTooLong, got %v", err)
	}
}

func TestEncodeCallbackData_RejectsEmptyAction(t *testing.T) {
	_, err := telegram.EncodeCallbackData("", "artifact")
	if err == nil {
		t.Fatal("expected error for empty action")
	}
	if !errors.Is(err, telegram.ErrCallbackMalformed) {
		t.Errorf("want ErrCallbackMalformed, got %v", err)
	}
}

func TestEncodeCallbackData_RejectsEmptyArtifact(t *testing.T) {
	_, err := telegram.EncodeCallbackData("approve", "")
	if err == nil {
		t.Fatal("expected error for empty artifact")
	}
	if !errors.Is(err, telegram.ErrCallbackMalformed) {
		t.Errorf("want ErrCallbackMalformed, got %v", err)
	}
}

func TestEncodeCallbackData_RejectsSeparatorInInput(t *testing.T) {
	if _, err := telegram.EncodeCallbackData("ap:prove", "artifact"); err == nil {
		t.Error("expected error for separator in action id")
	}
	if _, err := telegram.EncodeCallbackData("approve", "art:ifact"); err == nil {
		t.Error("expected error for separator in artifact id")
	}
}

func TestDecodeCallbackData_MissingSeparator(t *testing.T) {
	_, _, err := telegram.DecodeCallbackData("approveARTIFACT")
	if err == nil {
		t.Fatal("expected missing-separator error")
	}
	if !errors.Is(err, telegram.ErrCallbackMalformed) {
		t.Errorf("want ErrCallbackMalformed, got %v", err)
	}
}

func TestDecodeCallbackData_EmptyHalf(t *testing.T) {
	cases := []string{":artifact", "approve:", ":"}
	for _, in := range cases {
		_, _, err := telegram.DecodeCallbackData(in)
		if err == nil {
			t.Errorf("decode %q: expected empty-half error", in)
			continue
		}
		if !errors.Is(err, telegram.ErrCallbackMalformed) {
			t.Errorf("decode %q: want ErrCallbackMalformed, got %v", in, err)
		}
	}
}

func TestDecodeCallbackData_TooLong(t *testing.T) {
	long := strings.Repeat("a", 65)
	_, _, err := telegram.DecodeCallbackData(long)
	if err == nil {
		t.Fatal("expected too-long error")
	}
	if !errors.Is(err, telegram.ErrCallbackTooLong) {
		t.Errorf("want ErrCallbackTooLong, got %v", err)
	}
}

func TestEncodeCallbackData_BudgetFitsULID(t *testing.T) {
	// 26-char ULID + "approve" + ":" = 34 bytes; the spec promises this
	// fits in 64.
	data, err := telegram.EncodeCallbackData("approve", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(data) > 64 {
		t.Errorf("ULID encode exceeds budget: %d bytes", len(data))
	}
}
