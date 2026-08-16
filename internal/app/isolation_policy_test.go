// isolation_policy_test.go — MU-021 criterion 1: Team and Scale mode default
// to container isolation, and the escape hatch cannot reach them.
package app

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestUnsandboxedExecutionIsRefusedInMultiUserModes(t *testing.T) {
	for _, mode := range []string{config.DeploymentModeTeam, config.DeploymentModeScale} {
		for _, sbx := range []struct {
			enabled bool
			mode    string
			label   string
		}{
			{false, "docker", "sandbox disabled"},
			{true, "unsandboxed", "mode=unsandboxed"},
			{true, "  UNSANDBOXED  ", "mode with case and whitespace"},
		} {
			decision, reason := privilegedIsolationFor(sbx.enabled, sbx.mode, mode)
			if decision != IsolationRefused {
				t.Errorf("%s in %s mode: decision = %s, want refused", sbx.label, mode, decision)
			}
			// The log line is the whole remediation path for an operator who
			// just lost their tools. A refusal with no reason is a mystery.
			if !strings.Contains(reason, mode) {
				t.Errorf("%s in %s mode: reason does not name the mode: %q", sbx.label, mode, reason)
			}
		}
	}
}

// Personal deployments keep the escape hatch. Invariant 7: a single-tenant
// install must not lose a capability because the engine became tenant-aware,
// and with one tenant there is no one to isolate from.
func TestPersonalModeKeepsTheUnsandboxedEscapeHatch(t *testing.T) {
	for _, mode := range []string{config.DeploymentModePersonal, "", "  "} {
		if decision, _ := privilegedIsolationFor(true, "unsandboxed", mode); decision != IsolationHost {
			t.Errorf("personal mode %q: decision = %s, want host", mode, decision)
		}
	}
}

// The default is container isolation everywhere, including personal.
func TestContainerIsolationIsTheDefaultInEveryMode(t *testing.T) {
	for _, mode := range []string{config.DeploymentModePersonal, config.DeploymentModeTeam, config.DeploymentModeScale} {
		if decision, _ := privilegedIsolationFor(true, "docker", mode); decision != IsolationContainer {
			t.Errorf("%s mode: decision = %s, want container", mode, decision)
		}
		// An unrecognised mode string must not fall through to host
		// execution. Anything that is not the explicit opt-out is isolated.
		if decision, _ := privilegedIsolationFor(true, "gvisor-someday", mode); decision != IsolationContainer {
			t.Errorf("%s mode with an unknown sandbox mode: decision = %s, want container", mode, decision)
		}
	}
}
