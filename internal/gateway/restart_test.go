package gateway

import (
	"strings"
	"testing"
)

func TestPlanGatewayRestart(t *testing.T) {
	tests := []struct {
		name         string
		inContainer  bool
		spawnChild   bool
		exitCode     int
		messageMatch string
	}{
		{
			name:         "host starts replacement process",
			spawnChild:   true,
			exitCode:     0,
			messageMatch: "replacement gateway process",
		},
		{
			name:         "container delegates to supervisor",
			inContainer:  true,
			exitCode:     1,
			messageMatch: "container supervisor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planGatewayRestart(tt.inContainer)
			if plan.spawnChild != tt.spawnChild {
				t.Fatalf("spawnChild = %v, want %v", plan.spawnChild, tt.spawnChild)
			}
			if plan.exitCode != tt.exitCode {
				t.Fatalf("exitCode = %d, want %d", plan.exitCode, tt.exitCode)
			}
			if !strings.Contains(plan.message, tt.messageMatch) {
				t.Fatalf("message = %q, want it to contain %q", plan.message, tt.messageMatch)
			}
		})
	}
}
