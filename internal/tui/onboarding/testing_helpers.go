package onboarding

import "github.com/georgebuilds/carlos/internal/config"

// DaemonChoiceForTest exposes the daemon model's current choice for
// cmd-level tests that can't reach into unexported fields. Only intended
// for tests; the production code reads choice directly within the
// package.
func DaemonChoiceForTest(f *Flow) bool {
	if f == nil {
		return false
	}
	return f.daemon.choice
}

// GatewayIsDecideStageForTest reports whether the gateway sub-model is
// still on the gwStageDecide gate. Used by cmd-level tests to verify
// the --only gateway and gateway-add paths skip the gate correctly.
func GatewayIsDecideStageForTest(f *Flow) bool {
	if f == nil {
		return false
	}
	return f.gateway.stage == gwStageDecide
}

// FlowCfgForTest hands tests a pointer to the in-progress config so
// they can verify pre-existing fields survive flow construction.
func FlowCfgForTest(f *Flow) *config.Config {
	if f == nil {
		return nil
	}
	return f.cfg
}
