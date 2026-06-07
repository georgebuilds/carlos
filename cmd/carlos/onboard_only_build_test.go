package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/tui/onboarding"
)

func writeConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- buildOnboardOnlyFlow ---

func TestBuildOnboardOnlyFlow_UnknownScreenReturnsError(t *testing.T) {
	_, err := buildOnboardOnlyFlow("welcome", "/nonexistent")
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"unknown screen", "name", "models", "vault"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing hint %q", err.Error(), want)
		}
	}
}

func TestBuildOnboardOnlyFlow_MissingConfigStillBuilds(t *testing.T) {
	// First-time --only run: config file absent. The flow should
	// construct cleanly with nil ExistingConfig falling back to
	// defaults; the user can fill in the requested screen and we
	// won't crash before save.
	dir := t.TempDir()
	flow, err := buildOnboardOnlyFlow("daemon", filepath.Join(dir, "missing.yaml"))
	if err != nil {
		t.Fatalf("missing config should not error; got %v", err)
	}
	if flow == nil {
		t.Fatal("flow should not be nil")
	}
}

func TestBuildOnboardOnlyFlow_DaemonPreloadsExisting(t *testing.T) {
	path := writeConfig(t, &config.Config{
		UserName:  "George",
		Providers: map[string]config.ProviderConfig{"openai": {APIKey: "sk-x"}},
		Daemon:    config.DaemonConfig{Enabled: true},
	})
	flow, err := buildOnboardOnlyFlow("daemon", path)
	if err != nil {
		t.Fatal(err)
	}
	// We rely on NewWithOptions's preload path; the daemon model should
	// reflect the existing enabled state.
	if !daemonChoiceForTest(flow) {
		t.Error("daemon choice should preload to true from existing config")
	}
}

func TestBuildOnboardOnlyFlow_GatewaySkipsDecideStage(t *testing.T) {
	path := writeConfig(t, &config.Config{
		UserName:  "George",
		Providers: map[string]config.ProviderConfig{"openai": {APIKey: "sk-x"}},
		Daemon:    config.DaemonConfig{Enabled: true},
		Gateway:   config.GatewayConfig{Enabled: false},
	})
	flow, err := buildOnboardOnlyFlow("gateway", path)
	if err != nil {
		t.Fatal(err)
	}
	if gatewayIsDecideStageForTest(flow) {
		t.Error("--only gateway should auto-skip the set-later gate when an existing config is loaded")
	}
}

func TestBuildOnboardOnlyFlow_NameProvidersLoadCleanly(t *testing.T) {
	path := writeConfig(t, &config.Config{
		UserName:  "George",
		Providers: map[string]config.ProviderConfig{"anthropic": {APIKey: "sk-y"}},
		Daemon:    config.DaemonConfig{Enabled: false},
	})
	for _, screen := range []string{"name", "providers", "models", "skills", "vault"} {
		flow, err := buildOnboardOnlyFlow(screen, path)
		if err != nil {
			t.Errorf("--only %s: %v", screen, err)
			continue
		}
		if flow == nil {
			t.Errorf("--only %s: nil flow", screen)
		}
	}
}

// --- buildGatewayAddFlow ---

func TestBuildGatewayAddFlow_AlwaysSkipsDecideStage(t *testing.T) {
	cfg := &config.Config{
		UserName:  "George",
		Providers: map[string]config.ProviderConfig{"openai": {APIKey: "sk-x"}},
		Daemon:    config.DaemonConfig{Enabled: true},
	}
	flow := buildGatewayAddFlow(cfg)
	if flow == nil {
		t.Fatal("flow nil")
	}
	if gatewayIsDecideStageForTest(flow) {
		t.Error("gateway add flow should never land on the decide stage")
	}
}

func TestBuildGatewayAddFlow_NilCfgStillSkipsDecide(t *testing.T) {
	// Defensive: even without an existing config the wizard primes past
	// the gate because the user explicitly invoked `carlos gateway add`.
	flow := buildGatewayAddFlow(nil)
	if flow == nil {
		t.Fatal("flow nil")
	}
	if gatewayIsDecideStageForTest(flow) {
		t.Error("nil-cfg gateway add should still skip decide")
	}
}

func TestBuildGatewayAddFlow_PreservesExistingGateway(t *testing.T) {
	cfg := &config.Config{
		UserName: "George",
		Daemon:   config.DaemonConfig{Enabled: true},
		Gateway: config.GatewayConfig{
			Enabled: true,
			Ntfy: config.NtfyGatewayConfig{
				Enabled: true,
				Server:  "https://ntfy.example.com",
				Topic:   "carlos",
			},
		},
	}
	flow := buildGatewayAddFlow(cfg)
	if cfg := flowCfgForTest(flow); cfg.Gateway.Ntfy.Topic != "carlos" {
		t.Errorf("existing ntfy topic dropped; got %q", cfg.Gateway.Ntfy.Topic)
	}
}

// --- helpers ---

// daemonChoiceForTest reaches into the flow's daemon model field via
// the helper exported below in only_test_helpers.go. Kept as a single
// indirection so the test stays readable.
func daemonChoiceForTest(f *onboarding.Flow) bool {
	return onboarding.DaemonChoiceForTest(f)
}

func gatewayIsDecideStageForTest(f *onboarding.Flow) bool {
	return onboarding.GatewayIsDecideStageForTest(f)
}

func flowCfgForTest(f *onboarding.Flow) *config.Config {
	return onboarding.FlowCfgForTest(f)
}

// ensures the directory helper compiles before any test runs.
var _ = os.Getwd
