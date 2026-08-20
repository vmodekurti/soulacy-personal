// Package loadprofile is the documented Team Preview load profile and the
// harness that measures a gateway against it.
//
// MU-027's remaining criterion: p95 latency targets, which need two things
// that did not exist — a written statement of what load Team Preview is
// expected to carry, and something that measures it. A target with no profile
// is unfalsifiable; a profile with no harness is a paragraph.
//
// THE MEASUREMENT PROBLEM THIS PACKAGE EXISTS TO GET RIGHT is coordinated
// omission, and it is worth stating plainly because almost every hand-rolled
// load harness has it. A closed-loop harness — send, wait for the response,
// send the next — cannot issue a request while the server is stalled. So the
// requests that WOULD have been slow are never made, and a server that freezes
// for a second produces a handful of one-second samples instead of the
// thousands of increasingly-late ones a real client population would see. The
// p95 it reports can be an order of magnitude better than the p95 a user
// experiences, and it gets MORE optimistic the worse the server behaves.
//
// The fix is to measure from the moment a request was SCHEDULED to be sent
// rather than the moment it was actually sent, and to keep scheduling on the
// original cadence regardless of whether earlier requests have come back. That
// is what Recorder.Observe takes: a scheduled time, not a duration.
package loadprofile

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Recorder collects latency samples for one operation.
type Recorder struct {
	mu      sync.Mutex
	samples []time.Duration
	errors  int
}

// Observe records one request's latency, measured from when it was SCHEDULED.
//
// Taking `scheduledAt` rather than a duration is the whole point: a caller
// holding a duration has already decided what to measure from, and the
// convenient choice — the moment the goroutine actually got to send — is the
// one that hides a stall. This signature makes the correct measurement the
// easy one and the wrong one impossible to express.
func (r *Recorder) Observe(scheduledAt, completedAt time.Time, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.errors++
		// A failed request still contributes its latency. Dropping errors
		// flatters the percentiles exactly when the server is struggling,
		// which is the condition the measurement exists to detect.
	}
	r.samples = append(r.samples, completedAt.Sub(scheduledAt))
}

// Count and Errors report the sample population.
func (r *Recorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}

func (r *Recorder) Errors() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errors
}

// Percentile returns the nearest-rank percentile of the recorded samples.
//
// NEAREST-RANK, not linear interpolation, and the choice is deliberate: an
// interpolated p95 is a number that no request actually experienced, which is
// the wrong thing to put in a service target. Nearest-rank returns a real
// observation — "95% of requests were at least this fast" is a claim about
// requests that happened.
//
// The rank is ceil(p/100 * n), clamped to [1, n]. The clamp is not decoration:
// at p=0 the naive formula gives rank 0 and indexes out of bounds, and at
// p=100 floating-point can push the product a hair past n.
func (r *Recorder) Percentile(p float64) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), r.samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	rank := int(ceilRank(p, len(sorted)))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// ceilRank is split out so the arithmetic can be tested at the boundaries
// without building a Recorder, and so the +0.5 is not mistaken for rounding.
//
// The epsilon compensates for binary floating point: 0.7*10 is 6.999999999
// in float64, and a plain math.Ceil of that gives rank 7 where the definition
// wants 7 — but 0.3*10 is 3.0000000000000004, whose ceil is 4 where the
// definition wants 3. Nudging down by less than any real fractional part fixes
// the second without breaking the first.
func ceilRank(p float64, n int) float64 {
	exact := p / 100 * float64(n)
	truncated := float64(int64(exact))
	if exact-truncated < 1e-9 {
		return truncated
	}
	return truncated + 1
}

// Summary is one operation's measured latencies.
type Summary struct {
	Operation string        `json:"operation"`
	Count     int           `json:"count"`
	Errors    int           `json:"errors"`
	P50       time.Duration `json:"p50"`
	P95       time.Duration `json:"p95"`
	P99       time.Duration `json:"p99"`
	Max       time.Duration `json:"max"`
}

func (r *Recorder) Summarise(operation string) Summary {
	return Summary{
		Operation: operation,
		Count:     r.Count(),
		Errors:    r.Errors(),
		P50:       r.Percentile(50),
		P95:       r.Percentile(95),
		P99:       r.Percentile(99),
		Max:       r.Percentile(100),
	}
}

func (s Summary) String() string {
	return fmt.Sprintf("%-24s n=%-6d err=%-4d p50=%-10v p95=%-10v p99=%-10v max=%v",
		s.Operation, s.Count, s.Errors, s.P50, s.P95, s.P99, s.Max)
}
