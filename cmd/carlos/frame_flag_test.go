package main

import (
	"reflect"
	"testing"
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
	opts, prompt, err := parsePleaseArgs([]string{"-f", "work", "draft", "the", "PR"})
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
