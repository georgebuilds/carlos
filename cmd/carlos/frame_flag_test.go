package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/memory"
)

func TestParseLeadingFrameFlag_short(t *testing.T) {
	frame, rest, err := parseLeadingFrameFlag([]string{"-f", "work", "what's", "next"})
	if err != nil {
		t.Fatal(err)
	}
	if frame != "work" {
		t.Errorf("frame = %q, want work", frame)
	}
	if !reflect.DeepEqual(rest, []string{"what's", "next"}) {
		t.Errorf("rest = %v, want [what's next]", rest)
	}
}

func TestParseLeadingFrameFlag_long(t *testing.T) {
	frame, rest, err := parseLeadingFrameFlag([]string{"--frame", "research", "WebGPU", "in", "Safari"})
	if err != nil {
		t.Fatal(err)
	}
	if frame != "research" {
		t.Errorf("frame = %q, want research", frame)
	}
	if len(rest) != 3 {
		t.Errorf("rest = %v, want 3 args", rest)
	}
}

func TestParseLeadingFrameFlag_absent(t *testing.T) {
	frame, rest, err := parseLeadingFrameFlag([]string{"what's", "up"})
	if err != nil {
		t.Fatal(err)
	}
	if frame != "" {
		t.Errorf("frame = %q, want empty", frame)
	}
	if !reflect.DeepEqual(rest, []string{"what's", "up"}) {
		t.Errorf("rest = %v, want unchanged", rest)
	}
}

func TestParseLeadingFrameFlag_emptySlice(t *testing.T) {
	frame, rest, err := parseLeadingFrameFlag(nil)
	if err != nil {
		t.Fatal(err)
	}
	if frame != "" || rest != nil {
		t.Errorf("frame=%q rest=%v, want empty/nil", frame, rest)
	}
}

func TestParseLeadingFrameFlag_missingValue(t *testing.T) {
	if _, _, err := parseLeadingFrameFlag([]string{"-f"}); err == nil {
		t.Error("expected error when -f has no value")
	}
	if _, _, err := parseLeadingFrameFlag([]string{"--frame"}); err == nil {
		t.Error("expected error when --frame has no value")
	}
}

func TestParsePleaseArgs_frameFlag(t *testing.T) {
	opts, prompt, err := parsePleaseArgs([]string{"-f", "work", "draft the PR"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.frame != "work" {
		t.Errorf("opts.frame = %q, want work", opts.frame)
	}
	if prompt != "draft the PR" {
		t.Errorf("prompt = %q, want \"draft the PR\"", prompt)
	}
}

func TestParsePleaseArgs_frameFlagInterleaved(t *testing.T) {
	opts, prompt, err := parsePleaseArgs([]string{"-y", "-f", "work", "-m", "claude-opus-4-7", "thinking"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.autoApprove {
		t.Error("autoApprove not set")
	}
	if opts.frame != "work" {
		t.Errorf("opts.frame = %q, want work", opts.frame)
	}
	if opts.model != "claude-opus-4-7" {
		t.Errorf("opts.model = %q, want claude-opus-4-7", opts.model)
	}
	if prompt != "thinking" {
		t.Errorf("prompt = %q, want thinking", prompt)
	}
}

func TestParsePleaseArgs_frameFlagMissingValue(t *testing.T) {
	if _, _, err := parsePleaseArgs([]string{"-f"}); err == nil {
		t.Error("expected error when -f has no value")
	}
}

// --- parseLeadingFrameFilter (memory search flavoured) -------------

// matchedAs verifies that a FrameFilter matches against the
// public-API constructor it should equal. We can't compare
// FrameFilter values directly because the kind/name fields are
// unexported - instead we run them through the read API by inspecting
// behaviour through public helpers; here we just confirm the parser
// returned what looks like the expected variant by re-running the
// parse and comparing the SQL the predicate emits via SearchInFrame
// on a known-empty store would be the same. Simpler: compare via
// reflect on the underlying struct values, since both come from
// memory.* constructors so the unexported fields are equal.
func sameFilter(t *testing.T, got, want memory.FrameFilter) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filter mismatch: got %+v, want %+v", got, want)
	}
}

func TestParseLeadingFrameFilter_NoFlag(t *testing.T) {
	got, rest, err := parseLeadingFrameFilter([]string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	sameFilter(t, got, memory.AnyFrames())
	if !reflect.DeepEqual(rest, []string{"alpha", "beta"}) {
		t.Errorf("rest=%v", rest)
	}
}

func TestParseLeadingFrameFilter_EmptyArgs(t *testing.T) {
	got, rest, err := parseLeadingFrameFilter(nil)
	if err != nil {
		t.Fatal(err)
	}
	sameFilter(t, got, memory.AnyFrames())
	if rest != nil {
		t.Errorf("rest=%v want nil", rest)
	}
}

func TestParseLeadingFrameFilter_DashF(t *testing.T) {
	got, rest, err := parseLeadingFrameFilter([]string{"-f", "work", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	sameFilter(t, got, memory.InFrame("work"))
	if !reflect.DeepEqual(rest, []string{"alpha"}) {
		t.Errorf("rest=%v", rest)
	}
}

func TestParseLeadingFrameFilter_LongFrame(t *testing.T) {
	got, _, err := parseLeadingFrameFilter([]string{"--frame", "personal", "x"})
	if err != nil {
		t.Fatal(err)
	}
	sameFilter(t, got, memory.InFrame("personal"))
}

func TestParseLeadingFrameFilter_Unframed(t *testing.T) {
	got, rest, err := parseLeadingFrameFilter([]string{"--unframed", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	sameFilter(t, got, memory.Unframed())
	if !reflect.DeepEqual(rest, []string{"alpha"}) {
		t.Errorf("rest=%v", rest)
	}
}

func TestParseLeadingFrameFilter_DashFMissingValue(t *testing.T) {
	if _, _, err := parseLeadingFrameFilter([]string{"-f"}); err == nil {
		t.Error("expected error when -f has no value")
	}
}

func TestParseLeadingFrameFilter_DashFEmptyName(t *testing.T) {
	_, _, err := parseLeadingFrameFilter([]string{"-f", "", "alpha"})
	if err == nil {
		t.Fatal("expected error for empty frame name")
	}
	if !strings.Contains(err.Error(), "--unframed") {
		t.Errorf("error should point user at --unframed; got %q", err.Error())
	}
}

func TestParseLeadingFrameFilter_MutuallyExclusive(t *testing.T) {
	// -f then --unframed
	_, _, err := parseLeadingFrameFilter([]string{"-f", "work", "--unframed", "alpha"})
	if err == nil {
		t.Error("expected error when -f and --unframed combined (case 1)")
	}
	// --unframed then -f
	_, _, err = parseLeadingFrameFilter([]string{"--unframed", "-f", "work", "alpha"})
	if err == nil {
		t.Error("expected error when -f and --unframed combined (case 2)")
	}
	_, _, err = parseLeadingFrameFilter([]string{"--unframed", "--frame", "work", "alpha"})
	if err == nil {
		t.Error("expected error when --frame and --unframed combined")
	}
}
