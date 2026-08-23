//go:build loadtest

package quota

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/loadprofile"
)

// TestNoisyTenantQueueLatencyProfile publishes queue wait percentiles and
// proves the quiet tenant remains bounded while another submits 20x more work.
func TestNoisyTenantQueueLatencyProfile(t *testing.T) {
	const (
		capacity  = 8
		noisyJobs = 800
		quietJobs = 40
	)
	scheduler := NewFairShare(capacity)
	recorders := map[string]*loadprofile.Recorder{
		"noisy": {},
		"quiet": {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	submit := func(workspace string, jobs int) {
		for i := 0; i < jobs; i++ {
			scheduled := time.Now()
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := scheduler.Acquire(ctx, workspace)
				admitted := time.Now()
				recorders[workspace].Observe(scheduled, admitted, err)
				if err != nil {
					return
				}
				time.Sleep(2 * time.Millisecond)
				release()
			}()
		}
	}
	submit("noisy", noisyJobs)
	deadline := time.Now().Add(2 * time.Second)
	for len(scheduler.Held()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	submit("quiet", quietJobs)
	wg.Wait()

	for _, workspace := range []string{"noisy", "quiet"} {
		summary := recorders[workspace].Summarise("queue-wait/" + workspace)
		t.Log(summary.String())
		if summary.Errors != 0 || summary.Count == 0 {
			t.Errorf("%s error rate = %d/%d", workspace, summary.Errors, summary.Count)
		}
	}
	quiet := recorders["quiet"].Summarise("queue-wait/quiet")
	if quiet.Count != quietJobs {
		t.Fatalf("quiet tenant completed %d/%d jobs", quiet.Count, quietJobs)
	}
	if quiet.P95 > time.Second {
		t.Fatalf("quiet tenant queue p95 %v exceeded 1s under a 20x noisy neighbour", quiet.P95)
	}
	t.Log(fmt.Sprintf("SOULACY_QUEUE_LOAD_GATE=passed noisy_ratio=%dx", noisyJobs/quietJobs))
}
