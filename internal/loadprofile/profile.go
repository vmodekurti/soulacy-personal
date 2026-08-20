package loadprofile

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// profile.go — the documented Team Preview load profile.
//
// A p95 TARGET WITHOUT A PROFILE IS UNFALSIFIABLE, which is why this file
// exists as data rather than as a paragraph in a design doc. "p95 under 300ms"
// says nothing until somebody states at what concurrency, over what mix of
// operations, with how many workspaces — and once those are stated they can be
// disagreed with, measured against, and changed on purpose rather than by
// drift.
//
// The numbers below are DELIBERATELY MODEST and the reasoning is recorded with
// them. Team Preview is a handful of teams, not a public API; a profile
// inflated to look impressive produces a target nobody can meet on a laptop,
// which means the harness stops being run, which means the target stops being
// checked. A profile that a developer can run in a minute is a profile that
// gets run.

// Operation names one measured request class. The names are stable because
// they appear in reports somebody compares across releases.
type Operation string

const (
	OpListAgents     Operation = "GET /agents"
	OpGetAgent       Operation = "GET /agents/:id"
	OpWorkspaceIdent Operation = "GET /workspace/identity"
	OpListSchedule   Operation = "GET /schedule"
	OpHealth         Operation = "GET /health"
)

// Target is the latency budget for one operation.
//
// P95 is the committed number; P99 is recorded because a p95 that passes with
// a p99 ten times larger is a system with a stall in it, and the stall is the
// thing that generates support tickets. It is reported and NOT enforced,
// because a p99 target on a five-second sample is mostly noise — enforcing it
// would make the harness flaky, and a flaky gate is one people disable.
type Target struct {
	Operation Operation
	P95       time.Duration
	P99       time.Duration
	// Why records what the number is derived from, so a future change is an
	// argument rather than an edit.
	Why string
}

// Profile is one named load shape.
type Profile struct {
	Name string
	// Workspaces is how many distinct tenants the load is spread across. More
	// than one, always: a single-workspace profile measures a code path that
	// no Team deployment runs, and the per-workspace scoping added throughout
	// this branch is exactly what a one-tenant profile cannot see.
	Workspaces int
	// Concurrency is the number of independent client streams.
	Concurrency int
	// Rate is requests per second PER STREAM. Requests are scheduled on this
	// cadence and measured from the scheduled time, so a stalled server
	// produces the queue of late requests a real client population would —
	// see the package comment on coordinated omission.
	Rate float64
	// Duration is how long the harness runs.
	Duration time.Duration
	// Mix is the relative weight of each operation.
	Mix     map[Operation]int
	Targets []Target
}

// TeamPreview is the profile Team Preview is expected to carry.
//
// Sized from what a Team Preview deployment actually looks like: a handful of
// workspaces, a few operators each with a browser open, and the GUI's polling
// — which is the real steady-state load, since a dashboard refreshing every
// few seconds outnumbers human clicks by an order of magnitude.
//
// The mix is weighted towards LISTS because that is what the GUI does. A
// profile weighted towards writes would measure a path Team Preview exercises
// rarely, and would miss the one that is on every screen.
func TeamPreview() Profile {
	return Profile{
		Name:        "team-preview",
		Workspaces:  4,
		Concurrency: 16,
		Rate:        5,
		Duration:    10 * time.Second,
		Mix: map[Operation]int{
			OpListAgents:     40,
			OpGetAgent:       20,
			OpWorkspaceIdent: 20,
			OpListSchedule:   10,
			OpHealth:         10,
		},
		Targets: []Target{
			{Operation: OpListAgents, P95: 150 * time.Millisecond, P99: 400 * time.Millisecond,
				Why: "the GUI's most frequent read; it clones every definition, so it is the one list whose cost grows with the workspace"},
			{Operation: OpGetAgent, P95: 100 * time.Millisecond, P99: 300 * time.Millisecond,
				Why: "a single map lookup and a clone; anything slower is contention, not work"},
			{Operation: OpWorkspaceIdent, P95: 100 * time.Millisecond, P99: 300 * time.Millisecond,
				Why: "resolves membership, so it measures the tenancy resolver on the hot path"},
			{Operation: OpListSchedule, P95: 150 * time.Millisecond, P99: 400 * time.Millisecond,
				Why: "takes the scheduler's lock, so a slow one means the cron loop is holding it"},
			{Operation: OpHealth, P95: 50 * time.Millisecond, P99: 150 * time.Millisecond,
				Why: "does almost nothing; it is the control, and a slow health check means the process itself is starved"},
		},
	}
}

// Validate refuses a profile that cannot produce a meaningful measurement.
//
// The sample-count floor is the one worth having. A p95 computed from twenty
// samples is one observation away from a different answer, and a harness that
// reports it as a number rather than as noise invites somebody to tune against
// it.
func (p Profile) Validate() error {
	var problems []string
	if p.Workspaces < 2 {
		problems = append(problems, "workspaces must be at least 2: a single-tenant profile cannot see per-workspace scoping")
	}
	if p.Concurrency < 1 {
		problems = append(problems, "concurrency must be at least 1")
	}
	if p.Rate <= 0 {
		problems = append(problems, "rate must be positive")
	}
	if p.Duration <= 0 {
		problems = append(problems, "duration must be positive")
	}
	if len(p.Mix) == 0 {
		problems = append(problems, "the mix names no operations")
	}
	for operation, weight := range p.Mix {
		if weight <= 0 {
			problems = append(problems, fmt.Sprintf("%s has a non-positive weight", operation))
		}
	}
	if len(p.Targets) == 0 {
		problems = append(problems, "the profile states no targets, so nothing can pass or fail")
	}
	named := map[Operation]bool{}
	for _, target := range p.Targets {
		if _, mixed := p.Mix[target.Operation]; !mixed {
			problems = append(problems, fmt.Sprintf("%s has a target but is not in the mix, so it would never be measured", target.Operation))
		}
		if target.P95 <= 0 {
			problems = append(problems, fmt.Sprintf("%s has no p95 target", target.Operation))
		}
		if strings.TrimSpace(target.Why) == "" {
			problems = append(problems, fmt.Sprintf("%s states a number with no reasoning, so a future change cannot be argued about", target.Operation))
		}
		named[target.Operation] = true
	}
	for operation := range p.Mix {
		if !named[operation] {
			problems = append(problems, fmt.Sprintf("%s is measured but has no target, so its latency is recorded and never checked", operation))
		}
	}
	if expected := p.ExpectedSamples(); expected < minimumSamples {
		problems = append(problems, fmt.Sprintf(
			"the profile produces about %d samples, below the %d needed for a p95 that is a measurement rather than noise",
			expected, minimumSamples))
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("loadprofile: %s", strings.Join(problems, "; "))
}

// minimumSamples is the floor below which a p95 is one observation away from a
// different answer.
const minimumSamples = 200

// ExpectedSamples is roughly how many requests the profile issues.
func (p Profile) ExpectedSamples() int {
	return int(float64(p.Concurrency) * p.Rate * p.Duration.Seconds())
}

// Result is one operation's measurement against its target.
type Result struct {
	Summary
	TargetP95 time.Duration `json:"target_p95"`
	TargetP99 time.Duration `json:"target_p99"`
	// Met is false when the p95 exceeded its target, or when the operation
	// produced no samples at all. An operation that was never exercised has
	// not met its target — reporting it as met is how a mix that stopped
	// including an operation passes forever.
	Met bool `json:"met"`
	// Note explains a failure, or an unenforced p99 overshoot worth reading.
	Note string `json:"note,omitempty"`
}

// Evaluate scores measured summaries against the profile's targets.
func (p Profile) Evaluate(summaries map[Operation]Summary) []Result {
	results := make([]Result, 0, len(p.Targets))
	for _, target := range p.Targets {
		summary, measured := summaries[target.Operation]
		result := Result{Summary: summary, TargetP95: target.P95, TargetP99: target.P99}
		result.Operation = string(target.Operation)
		switch {
		case !measured || summary.Count == 0:
			result.Met = false
			result.Note = "no samples: this operation was never exercised, so its target is unverified rather than met"
		case summary.P95 > target.P95:
			result.Met = false
			result.Note = fmt.Sprintf("p95 %v exceeds the %v target (%s)", summary.P95, target.P95, target.Why)
		default:
			result.Met = true
			if target.P99 > 0 && summary.P99 > target.P99 {
				// Reported, not enforced. A p95 that passes with a p99 far
				// above it is a system with a stall in it, and the stall is
				// what generates support tickets — but a p99 target on a short
				// sample is mostly noise, and a flaky gate is one people
				// disable.
				result.Note = fmt.Sprintf("p95 met, but p99 %v exceeds the %v guideline — a stall, not a slow path",
					summary.P99, target.P99)
			}
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Operation < results[j].Operation })
	return results
}

// Met reports whether every enforced target passed.
func Met(results []Result) bool {
	if len(results) == 0 {
		// An empty result set is not a pass. A harness that measured nothing
		// and reported success is the failure this whole file is trying to
		// avoid.
		return false
	}
	for _, result := range results {
		if !result.Met {
			return false
		}
	}
	return true
}
