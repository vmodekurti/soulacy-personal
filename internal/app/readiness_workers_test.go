package app

import (
	"context"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/executor/process"
	executorremote "github.com/soulacy/soulacy/internal/executor/remote"
	queuememory "github.com/soulacy/soulacy/internal/queue/memory"
)

func TestReadinessPersonalDoesNotRequireExecutionWorker(t *testing.T) {
	a := &App{cfg: &config.Config{}}
	for _, probe := range a.readinessProbes(gatewayDeps{}) {
		if probe.Name == "execution_worker" {
			t.Fatal("personal readiness unexpectedly requires an execution worker")
		}
	}
}

func TestReadinessTeamRequiresLiveExecutionWorker(t *testing.T) {
	q := queuememory.New()
	defer q.Close()
	a := &App{cfg: &config.Config{}}
	a.cfg.Deployment.Mode = config.DeploymentModeTeam
	probes := a.readinessProbes(gatewayDeps{queueBackend: q})
	var check func(context.Context) error
	for _, probe := range probes {
		if probe.Name == "execution_worker" {
			if !probe.Required {
				t.Fatal("Team execution-worker probe is optional")
			}
			check = probe.Check
		}
	}
	if check == nil {
		t.Fatal("Team readiness has no execution-worker probe")
	}
	missingCtx, missingCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer missingCancel()
	if err := check(missingCtx); err == nil {
		t.Fatal("Team readiness accepted a queue with no worker")
	}

	w := executorremote.NewWorker(q, process.New("python3"), "test-workers", 1)
	liveCtx, liveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer liveCancel()
	if err := w.Start(liveCtx); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := check(liveCtx); err != nil {
		t.Fatalf("Team readiness rejected a live worker: %v", err)
	}
}
