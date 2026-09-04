package agent

import "testing"

// TestAllowShellGrantsSystem verifies the Story 6 allow_shell alias maps to the
// "system" capability, and that the default (all flags false) denies it.
func TestAllowShellGrantsSystem(t *testing.T) {
	if (&Definition{}).HasCapability("system") {
		t.Fatal("default agent must NOT have system capability")
	}
	if !(&Definition{AllowShell: true}).HasCapability("system") {
		t.Fatal("allow_shell:true should grant system capability")
	}
	if !(&Definition{SystemTools: true}).HasCapability("system") {
		t.Fatal("legacy system_tools:true should still grant system capability")
	}
	if !(&Definition{Capabilities: []string{"system"}}).HasCapability("system") {
		t.Fatal("capabilities:[system] should grant system capability")
	}
}
