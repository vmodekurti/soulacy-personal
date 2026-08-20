// loadprofile_test.go — the percentile arithmetic and, more importantly, the
// property that makes the harness worth trusting: it does NOT get more
// optimistic when the server stalls.
package loadprofile

import (
	"context"
	"strings"
	"testing"
	"time"
)

func recorderWith(latencies ...time.Duration) *Recorder {
	base := time.Unix(0, 0)
	r := &Recorder{}
	for _, latency := range latencies {
		r.Observe(base, base.Add(latency), nil)
	}
	return r
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// Nearest-rank, so every reported percentile is a latency some request
// actually experienced. An interpolated p95 is a number nobody saw, which is
// the wrong thing to put in a service target.
func TestPercentilesAreNearestRankAndReturnRealObservations(t *testing.T) {
	// 1..10ms. Nearest rank: p50 → ceil(0.5*10)=5 → 5ms. p95 → ceil(9.5)=10 → 10ms.
	r := recorderWith(ms(1), ms(2), ms(3), ms(4), ms(5), ms(6), ms(7), ms(8), ms(9), ms(10))
	cases := map[float64]time.Duration{
		0:   ms(1),
		10:  ms(1),
		50:  ms(5),
		90:  ms(9),
		95:  ms(10),
		99:  ms(10),
		100: ms(10),
	}
	for p, want := range cases {
		if got := r.Percentile(p); got != want {
			t.Errorf("p%v = %v, want %v", p, got, want)
		}
	}
	// And every answer is one of the observations, not between them.
	observed := map[time.Duration]bool{}
	for i := 1; i <= 10; i++ {
		observed[ms(i)] = true
	}
	for p := 0.0; p <= 100; p += 7 {
		if got := r.Percentile(p); !observed[got] {
			t.Errorf("p%v = %v, which no request experienced", p, got)
		}
	}
}

// The boundaries are where percentile code goes wrong: rank 0 indexes out of
// bounds, and floating point can push the top rank past the sample count.
func TestPercentileBoundariesDoNotPanicOrOverrun(t *testing.T) {
	if got := (&Recorder{}).Percentile(95); got != 0 {
		t.Fatalf("an empty recorder returned %v", got)
	}
	single := recorderWith(ms(7))
	for _, p := range []float64{0, 1, 50, 99, 99.9, 100} {
		if got := single.Percentile(p); got != ms(7) {
			t.Fatalf("a single sample at p%v = %v", p, got)
		}
	}
	// The float64 case the epsilon exists for: 0.3*10 is 3.0000000000000004,
	// whose naive ceil is 4 where the definition wants 3.
	ten := recorderWith(ms(1), ms(2), ms(3), ms(4), ms(5), ms(6), ms(7), ms(8), ms(9), ms(10))
	if got := ten.Percentile(30); got != ms(3) {
		t.Fatalf("p30 = %v, want 3ms — floating point pushed the rank up", got)
	}
	if got := ten.Percentile(70); got != ms(7) {
		t.Fatalf("p70 = %v, want 7ms", got)
	}
}

// A failed request still contributes its latency. Dropping errors flatters the
// percentiles exactly when the server is struggling, which is the condition
// the measurement exists to detect.
func TestAFailedRequestStillContributesItsLatency(t *testing.T) {
	base := time.Unix(0, 0)
	r := &Recorder{}
	r.Observe(base, base.Add(ms(1)), nil)
	r.Observe(base, base.Add(ms(500)), context.DeadlineExceeded)

	if r.Count() != 2 {
		t.Fatalf("count = %d, want 2 — a failed request was dropped", r.Count())
	}
	if r.Errors() != 1 {
		t.Fatalf("errors = %d, want 1", r.Errors())
	}
	if got := r.Percentile(100); got != ms(500) {
		t.Fatalf("max = %v — the failed request's latency was excluded", got)
	}
}

// THE PROPERTY THE HARNESS EXISTS FOR. A server that stalls must produce
// WORSE numbers, not better ones. A closed-loop harness reports better ones,
// because it issues no request while stalled and the slow samples are never
// taken.
func TestAStallMakesTheMeasurementWorseNotBetter(t *testing.T) {
	profile := Profile{
		Name: "test", Workspaces: 2, Concurrency: 1, Rate: 1000, Duration: time.Second,
		Mix:     map[Operation]int{OpHealth: 1},
		Targets: []Target{{Operation: OpHealth, P95: time.Second, Why: "test"}},
	}
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}

	// A fake clock. The server is fast for the first half of the run and then
	// stalls for 500ms — long enough that the stream falls far behind its own
	// schedule.
	current := time.Unix(0, 0)
	var served int
	opts := Options{
		Profile: profile,
		Now:     func() time.Time { return current },
		// Sleeping advances the clock rather than waiting, so the test is
		// instant and deterministic.
		Sleep: func(_ context.Context, d time.Duration) { current = current.Add(d) },
		Requests: map[Operation]Request{
			OpHealth: func(context.Context, string) error {
				served++
				if served == 500 {
					current = current.Add(500 * time.Millisecond) // the stall
					return nil
				}
				current = current.Add(time.Millisecond)
				return nil
			},
		},
	}
	summaries, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	health := summaries[OpHealth]
	if health.Count != 1000 {
		t.Fatalf("count = %d, want 1000 — the harness stopped issuing requests during the stall", health.Count)
	}
	// The stall pushed the stream 500ms behind its 1ms cadence, so the ~500
	// requests after it carry increasing scheduled-time latency. A closed-loop
	// harness would report ~1ms at every percentile.
	if health.P95 < 100*time.Millisecond {
		t.Fatalf("p95 = %v after a 500ms stall — the harness is measuring from the send time, "+
			"not the scheduled time, so it is hiding the queue the stall created", health.P95)
	}
	if health.Max < 400*time.Millisecond {
		t.Fatalf("max = %v after a 500ms stall", health.Max)
	}
}

// A profile that cannot produce a meaningful measurement is refused, rather
// than producing a number somebody tunes against.
func TestAProfileThatCannotMeasureAnythingIsRefused(t *testing.T) {
	valid := TeamPreview()
	if err := valid.Validate(); err != nil {
		t.Fatalf("the documented profile is invalid: %v", err)
	}

	tooFewSamples := valid
	tooFewSamples.Duration = time.Millisecond
	if err := tooFewSamples.Validate(); err == nil || !strings.Contains(err.Error(), "noise") {
		t.Fatalf("a profile producing a handful of samples was accepted: %v", err)
	}

	singleTenant := valid
	singleTenant.Workspaces = 1
	if err := singleTenant.Validate(); err == nil {
		t.Fatal("a single-workspace profile was accepted; it cannot see per-workspace scoping")
	}

	untargeted := valid
	untargeted.Targets = untargeted.Targets[:1]
	if err := untargeted.Validate(); err == nil || !strings.Contains(err.Error(), "recorded and never checked") {
		t.Fatalf("a profile measuring operations with no target was accepted: %v", err)
	}

	unmeasured := valid
	unmeasured.Mix = map[Operation]int{OpHealth: 1}
	if err := unmeasured.Validate(); err == nil || !strings.Contains(err.Error(), "never be measured") {
		t.Fatalf("a profile targeting an operation it does not exercise was accepted: %v", err)
	}
}

// Every target states its reasoning, so a future change is an argument rather
// than an edit.
func TestEveryTargetSaysWhereItsNumberCameFrom(t *testing.T) {
	for _, target := range TeamPreview().Targets {
		if strings.TrimSpace(target.Why) == "" {
			t.Errorf("%s states a p95 with no reasoning", target.Operation)
		}
	}
}

// An operation that was never exercised has NOT met its target. Scoring it as
// met is how a mix that stopped including an operation passes forever.
func TestAnUnexercisedOperationIsNotScoredAsMet(t *testing.T) {
	profile := TeamPreview()
	results := profile.Evaluate(map[Operation]Summary{})
	if Met(results) {
		t.Fatal("a run that measured nothing reported every target met")
	}
	for _, result := range results {
		if result.Met {
			t.Errorf("%s was scored as met with no samples", result.Operation)
		}
		if !strings.Contains(result.Note, "unverified") {
			t.Errorf("%s: the note does not say the target is unverified: %q", result.Operation, result.Note)
		}
	}
	// And an empty result set is a failure, not a vacuous pass.
	if Met(nil) {
		t.Fatal("an empty result set reported success")
	}
}

// A p95 over target fails; a p99 over its guideline is reported and does not.
func TestP95IsEnforcedAndP99IsReported(t *testing.T) {
	profile := Profile{
		Name: "test", Workspaces: 2, Concurrency: 1, Rate: 100, Duration: 10 * time.Second,
		Mix:     map[Operation]int{OpHealth: 1},
		Targets: []Target{{Operation: OpHealth, P95: ms(50), P99: ms(150), Why: "test"}},
	}
	over95 := profile.Evaluate(map[Operation]Summary{
		OpHealth: {Count: 100, P95: ms(80), P99: ms(90)},
	})
	if Met(over95) {
		t.Fatal("a p95 over target passed")
	}
	if !strings.Contains(over95[0].Note, "exceeds") {
		t.Fatalf("the failure gives no usable reason: %q", over95[0].Note)
	}

	over99 := profile.Evaluate(map[Operation]Summary{
		OpHealth: {Count: 100, P95: ms(10), P99: ms(900)},
	})
	if !Met(over99) {
		t.Fatal("a p99 overshoot failed the run; it is a guideline, not a gate")
	}
	if !strings.Contains(over99[0].Note, "stall") {
		t.Fatalf("a p99 overshoot was not reported: %q", over99[0].Note)
	}
}

// A missing driver is refused up front rather than producing a report with an
// unexplained empty operation.
func TestAMissingRequestDriverIsRefusedRatherThanMeasuredAsZero(t *testing.T) {
	_, err := Run(context.Background(), Options{
		Profile:  TeamPreview(),
		Requests: map[Operation]Request{OpHealth: func(context.Context, string) error { return nil }},
	})
	if err == nil || !strings.Contains(err.Error(), "no request driver") {
		t.Fatalf("a partial driver set was accepted: %v", err)
	}
}

// The load is spread across workspaces, because a single-tenant run measures a
// code path no Team deployment takes.
func TestTheLoadIsSpreadAcrossWorkspaces(t *testing.T) {
	profile := Profile{
		Name: "test", Workspaces: 4, Concurrency: 4, Rate: 100, Duration: time.Second,
		Mix:     map[Operation]int{OpHealth: 1},
		Targets: []Target{{Operation: OpHealth, P95: time.Second, Why: "test"}},
	}
	seen := map[string]int{}
	current := time.Unix(0, 0)
	var mu chan struct{} = make(chan struct{}, 1)
	mu <- struct{}{}
	_, err := Run(context.Background(), Options{
		Profile: profile,
		Now:     func() time.Time { return current },
		Sleep:   func(context.Context, time.Duration) {},
		Requests: map[Operation]Request{
			OpHealth: func(_ context.Context, workspaceID string) error {
				<-mu
				seen[workspaceID]++
				mu <- struct{}{}
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 {
		t.Fatalf("the load reached %d workspaces, want 4: %v", len(seen), seen)
	}
}
