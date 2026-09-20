package gateway

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/platform"
)

func TestRestartUsesSupervisor(t *testing.T) {
	tests := []struct {
		name        string
		inContainer bool
		detected    platform.Info
		want        bool
	}{
		{"host", false, platform.Info{Name: "self-hosted", Kind: platform.Host}, false},
		{"container marker", true, platform.Info{Name: "self-hosted", Kind: platform.Host}, true},
		{"managed platform", false, platform.Info{Name: "Railway", Kind: platform.Managed}, true},
		{"detected container", false, platform.Info{Name: "Kubernetes", Kind: platform.Container}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restartUsesSupervisor(tt.inContainer, tt.detected); got != tt.want {
				t.Fatalf("restartUsesSupervisor() = %v, want %v", got, tt.want)
			}
		})
	}
}

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
