package main

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/tui/onboarding"
)

// TestParseOnboardOnly_RecognizedFlags covers the canonical and
// short forms ("--only" / "-only") and confirms an empty arg list
// returns no error and no screen.
func TestParseOnboardOnly_RecognizedFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
		err  bool
	}{
		{nil, "", false},
		{[]string{"--only", "models"}, "models", false},
		{[]string{"-only", "providers"}, "providers", false},
		{[]string{"--only"}, "", true},
		{[]string{"weird"}, "", true},
	}
	for _, c := range cases {
		got, err := parseOnboardOnly(c.args)
		if c.err {
			if err == nil {
				t.Errorf("args %v: want error, got nil", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("args %v: unexpected error %v", c.args, err)
			continue
		}
		if got != c.want {
			t.Errorf("args %v: got %q want %q", c.args, got, c.want)
		}
	}
}

// TestOnboardScreenByName proves every documented screen name resolves
// to an onboarding.Screen and that typos return ok=false.
func TestOnboardScreenByName(t *testing.T) {
	cases := map[string]onboarding.Screen{
		"name":      onboarding.ScreenName,
		"providers": onboarding.ScreenProvider,
		"provider":  onboarding.ScreenProvider,
		"models":    onboarding.ScreenModel,
		"model":     onboarding.ScreenModel,
		"skills":    onboarding.ScreenSkills,
		"vault":     onboarding.ScreenVault,
		"daemon":    onboarding.ScreenDaemon,
		"gateway":   onboarding.ScreenGateway,
		"GATEWAY":   onboarding.ScreenGateway,
		"  vault ": onboarding.ScreenVault,
	}
	for name, want := range cases {
		got, ok := onboardScreenByName(name)
		if !ok {
			t.Errorf("onboardScreenByName(%q): want ok, got false", name)
			continue
		}
		if got != want {
			t.Errorf("onboardScreenByName(%q) = %v, want %v", name, got, want)
		}
	}
	// Unknown names: not ok.
	if _, ok := onboardScreenByName("welcome"); ok {
		t.Error("unknown screen welcome should not resolve")
	}
	if _, ok := onboardScreenByName(""); ok {
		t.Error("empty name should not resolve")
	}
}

// TestParseOnboardOnly_ErrorMessageListsScreens makes sure the error
// message a user sees when they typo is actually useful — it should
// hint at the valid screen names.
func TestParseOnboardOnly_ErrorMessageListsScreens(t *testing.T) {
	_, err := parseOnboardOnly([]string{"--only"})
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"name", "providers", "models", "vault", "daemon", "gateway"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should list %q", err.Error(), want)
		}
	}
}
