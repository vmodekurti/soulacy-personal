package runs

import (
	"context"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/wsroot"
)

// latency.go — MU-027 criterion 6: "provider/tool latency is separately visible
// from Soulacy queue and processing latency."
//
// WHY THIS MATTERS MORE THAN IT SOUNDS. Without the decomposition, a slow run
// is one number and every explanation fits it: the provider is slow, the queue
// is deep, the gateway is doing too much. Those have opposite remediations —
// switch model, add workers, profile the engine — so an operator with one
// number is guessing, and criteria 1 and 2's p95 targets cannot even be
// evaluated, let alone met.
//
// WHY IT IS DERIVED FROM THE RECORD RATHER THAN A LABELLED METRIC. The obvious
// move is a Prometheus histogram labelled by workspace, and it is the classic
// footgun: label cardinality grows with tenants, and a metric that degrades the
// monitoring system as the product succeeds is worse than no metric. The run
// record is already per-workspace by construction and already carries the
// timestamps, so the per-tenant view costs nothing and the process-wide
// histograms stay unlabelled.

// QueueLatency is submission → a worker claiming the run.
//
// The number that answers "should I add workers". It is Soulacy's own
// responsibility in a way provider time is not: nobody outside the deployment
// makes this number larger.
//
// Zero for a run that has not started; a caller distinguishing "instant" from
// "not yet" reads StartedAt.
func (r Run) QueueLatency() time.Duration {
	if r.StartedAt == nil {
		return 0
	}
	return r.StartedAt.Sub(r.CreatedAt)
}

// ExecutionLatency is claim → finish: everything the run spent actually
// running, provider time included.
func (r Run) ExecutionLatency() time.Duration {
	if r.StartedAt == nil || r.EndedAt == nil {
		return 0
	}
	return r.EndedAt.Sub(*r.StartedAt)
}

// ProcessingLatency is execution time MINUS provider and tool time: what
// Soulacy itself spent while the run was in flight.
//
// The interesting number, and the one nothing measured. A run that took 40
// seconds because a provider took 39 needs a different response from one that
// took 40 because the engine did. AgentRunDuration conflates them and is
// dominated by provider time, so it reports almost entirely on somebody else's
// system.
//
// Clamped at zero rather than allowed to go negative. External time is
// accumulated from concurrent calls, so parallel tool execution can legitimately
// sum to more than the wall clock — a negative "processing time" would be
// arithmetically explicable and operationally meaningless, and a histogram that
// silently accepts it produces percentiles nobody can act on.
func (r Run) ProcessingLatency() time.Duration {
	execution := r.ExecutionLatency()
	external := time.Duration(r.ExternalMicros) * time.Microsecond
	if external >= execution {
		return 0
	}
	return execution - external
}

// TotalLatency is submission → finish, the number a user experiences.
func (r Run) TotalLatency() time.Duration {
	if r.EndedAt == nil {
		return 0
	}
	return r.EndedAt.Sub(r.CreatedAt)
}

// LatencyBreakdown is the decomposition, in milliseconds, as the API reports it.
//
// Milliseconds rather than a Go duration string: the consumers are a GUI and a
// CLI doing arithmetic on these, and "1.234s" is a value they would have to
// parse back.
type LatencyBreakdown struct {
	QueueMS      int64 `json:"queue_ms"`
	ExecutionMS  int64 `json:"execution_ms"`
	ExternalMS   int64 `json:"external_ms"`
	ProcessingMS int64 `json:"processing_ms"`
	TotalMS      int64 `json:"total_ms"`
}

// Latency returns the breakdown for one run.
func (r Run) Latency() LatencyBreakdown {
	return LatencyBreakdown{
		QueueMS:      r.QueueLatency().Milliseconds(),
		ExecutionMS:  r.ExecutionLatency().Milliseconds(),
		ExternalMS:   (time.Duration(r.ExternalMicros) * time.Microsecond).Milliseconds(),
		ProcessingMS: r.ProcessingLatency().Milliseconds(),
		TotalMS:      r.TotalLatency().Milliseconds(),
	}
}

// RecordExternal adds time spent waiting on somebody else's system.
//
// Additive rather than set, and additive in SQL rather than read-modify-write:
// a run makes many provider and tool calls, concurrently, from different
// goroutines. Reading the current value and writing back the sum would lose
// increments under exactly the concurrency this is measuring.
func (s *Store) RecordExternal(ctx context.Context, workspaceID, id string, elapsed time.Duration) error {
	if elapsed <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_runs SET external_micros = external_micros + ? WHERE workspace_id = ? AND id = ?`,
		elapsed.Microseconds(), wsroot.Normalize(workspaceID), strings.TrimSpace(id))
	return err
}
