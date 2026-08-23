package app

import (
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	queuememory "github.com/soulacy/soulacy/internal/queue/memory"
)

func TestMultiUserNamedExecutorsCannotBypassWorkerBoundary(t *testing.T) {
	q := queuememory.New()
	defer q.Close()
	a := &App{cfg: &config.Config{Deployment: config.DeploymentConfig{Mode: config.DeploymentModeTeam}}}
	backends := a.wireNamedExecutors(nil, q)
	if len(backends) != 1 || backends["worker"] == nil {
		t.Fatalf("Team executors bypass worker boundary: %#v", backends)
	}
}
