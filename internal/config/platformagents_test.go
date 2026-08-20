package config

import "testing"

// The policy itself, in one place, because every other layer takes a boolean
// from it rather than re-deriving it from the mode.
func TestOnlyPersonalGetsTheSystemAgentWithoutSayingSo(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		waived bool
		want   bool
	}{
		{name: "personal is unconditionally yes", mode: DeploymentModePersonal, want: true},
		{name: "an empty mode is personal", mode: "", want: true},
		{name: "team is no", mode: DeploymentModeTeam, want: false},
		{name: "scale is no", mode: DeploymentModeScale, want: false},
		{name: "team with the acknowledgement is yes", mode: DeploymentModeTeam, waived: true, want: true},
		{name: "scale with the acknowledgement is yes", mode: DeploymentModeScale, waived: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Deployment.Mode = tt.mode
			if tt.waived {
				cfg.Deployment.Acknowledgements = []string{UnsafeTenantSystemAgentAcknowledgement}
			}
			if got := cfg.PlatformAgentsEnabled(); got != tt.want {
				t.Fatalf("PlatformAgentsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The two acknowledgements waive different things. Writing one and getting the
// other is the kind of surprise that makes operators write both "just in case",
// which defeats the point of naming them.
func TestTheTwoAcknowledgementsAreNotInterchangeable(t *testing.T) {
	prereq := &Config{}
	prereq.Deployment.Mode = DeploymentModeTeam
	prereq.Deployment.Acknowledgements = []string{UnsafeDeploymentPrerequisitesAcknowledgement}
	if prereq.PlatformAgentsEnabled() {
		t.Error("waiving the infrastructure prerequisites also restored the System agent")
	}

	sysAgent := &Config{}
	sysAgent.Deployment.Mode = DeploymentModeTeam
	sysAgent.Deployment.Acknowledgements = []string{UnsafeTenantSystemAgentAcknowledgement}
	if sysAgent.HasUnsafeDeploymentAcknowledgement() {
		t.Error("restoring the System agent also waived the infrastructure prerequisites")
	}
}
