package loadprofile

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

// harness.go — the open-loop driver.
//
// OPEN LOOP is the whole design. Each stream computes the times at which its
// requests SHOULD be issued, from a fixed cadence, before it issues any of
// them. If the server stalls, the stream falls behind its own schedule and the
// requests it eventually sends carry their original scheduled times — so the
// measured latency includes the queueing a real client population would have
// experienced.
//
// A closed loop — send, await, send again — cannot do this: it issues no
// request while stalled, so the samples that would have been slow are never
// taken. That is coordinated omission, and it makes a harness report BETTER
// numbers the worse the server behaves.

// Request is one measured call. It returns an error for a failed request; the
// latency is measured by the harness, not by the caller, so an implementation
// cannot accidentally exclude its own setup time.
type Request func(ctx context.Context, workspaceID string) error

// Options configures a run.
type Options struct {
	Profile Profile
	// Requests maps each operation in the profile's mix to its driver.
	Requests map[Operation]Request
	// Workspaces the load is spread across. Generated from the profile's count
	// when empty.
	Workspaces []string
	// Now is injectable so the scheduling arithmetic is testable without
	// waiting in real time.
	Now func() time.Time
	// Sleep is injectable for the same reason.
	Sleep func(context.Context, time.Duration)
}

// Run drives the profile and returns one summary per operation.
func Run(ctx context.Context, opts Options) (map[Operation]Summary, error) {
	profile := opts.Profile
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	for operation := range profile.Mix {
		if opts.Requests[operation] == nil {
			// Refusing beats measuring a partial mix. A missing driver would
			// otherwise produce a report where one operation has no samples,
			// which Evaluate correctly scores as unmet — but the reason would
			// be invisible, and somebody would spend an afternoon looking for
			// a latency problem that is a wiring gap.
			return nil, fmt.Errorf("loadprofile: no request driver for %q, which the mix includes", operation)
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) {
			if d <= 0 {
				return
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
		}
	}
	workspaces := opts.Workspaces
	if len(workspaces) == 0 {
		workspaces = make([]string, 0, profile.Workspaces)
		for i := 0; i < profile.Workspaces; i++ {
			workspaces = append(workspaces, fmt.Sprintf("ws_load_%02d", i))
		}
	}

	recorders := map[Operation]*Recorder{}
	for operation := range profile.Mix {
		recorders[operation] = &Recorder{}
	}

	// The weighted mix, expanded once into a slice so a stream's per-request
	// choice is one index rather than a walk over the map — and so the mix is
	// identical across streams, which a per-stream re-derivation would not
	// guarantee for a map whose iteration order is random.
	var draw []Operation
	for operation, weight := range profile.Mix {
		for i := 0; i < weight; i++ {
			draw = append(draw, operation)
		}
	}
	sortOperations(draw)

	interval := time.Duration(float64(time.Second) / profile.Rate)
	perStream := int(profile.Duration.Seconds() * profile.Rate)
	start := now()

	var wg sync.WaitGroup
	for stream := 0; stream < profile.Concurrency; stream++ {
		wg.Add(1)
		go func(stream int) {
			defer wg.Done()
			// Seeded per stream so a run is reproducible in shape while the
			// streams do not march in lockstep — every stream issuing the same
			// operation at the same instant would measure a thundering herd
			// rather than a mix.
			source := rand.New(rand.NewPCG(uint64(stream)+1, 0x5EED))
			for i := 0; i < perStream; i++ {
				scheduledAt := start.Add(time.Duration(i) * interval)
				if wait := scheduledAt.Sub(now()); wait > 0 {
					sleep(ctx, wait)
				}
				if ctx.Err() != nil {
					return
				}
				operation := draw[source.IntN(len(draw))]
				workspace := workspaces[(stream+i)%len(workspaces)]
				err := opts.Requests[operation](ctx, workspace)
				// Measured from scheduledAt, NOT from the moment the call was
				// made. This is the line that makes the harness honest.
				recorders[operation].Observe(scheduledAt, now(), err)
			}
		}(stream)
	}
	wg.Wait()

	out := make(map[Operation]Summary, len(recorders))
	for operation, recorder := range recorders {
		out[operation] = recorder.Summarise(string(operation))
	}
	return out, nil
}

func sortOperations(ops []Operation) {
	for i := 1; i < len(ops); i++ {
		for j := i; j > 0 && ops[j] < ops[j-1]; j-- {
			ops[j], ops[j-1] = ops[j-1], ops[j]
		}
	}
}
