// Package metrics owns Soulacy's Prometheus instrumentation.
//
// PRODUCTION_AUDIT → MED/Observability: previously there were no
// counters/histograms to observe — only zap logs. This package exposes
// metric handles for the gateway hot paths so operators can SLO on latency
// distributions and error rates instead of timestamp-guessing through logs.
//
// All metrics live in a single registry so the /metrics endpoint can be
// served as a simple promhttp.Handler. Naming follows Prometheus conventions:
//   - `soulacy_<subsystem>_<thing>_<unit>` for observation metrics
//   - `soulacy_<subsystem>_<thing>_total` for monotonic counters
//
// Counters / histograms exposed:
//   - HTTP request latency histogram (per-method, per-route, per-status)
//   - HTTP request total (counter, labeled with method/route/status)
//   - LLM call duration histogram (per-provider, per-model)
//   - LLM call total + error counter (per-provider)
//   - Tool call duration histogram (per-tool-name)
//   - Tool call total + error counter (per-tool-name)
//   - Agent run duration histogram (per-agent-id)
//   - Agent run total + error counter (per-agent-id)
//   - Actionlog queue depth (gauge)
//   - Actionlog batch size (histogram of batch sizes flushed)
//   - Worker pool active runs (gauge)
//
// Operators can scrape /metrics via the existing API auth gate (it's
// registered as a normal /api/v1/metrics handler so the API key applies).

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

var (
	// Registry is the single namespace shared by all soulacy metrics. We
	// don't use the global default registry so a test or embedder doesn't
	// pick up our metrics by accident.
	Registry = prometheus.NewRegistry()

	// HTTP

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "soulacy_http_request_duration_seconds",
			Help:    "Inbound HTTP request latency, by method/route/status.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route", "status"},
	)
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_http_requests_total",
			Help: "Inbound HTTP request count.",
		},
		[]string{"method", "route", "status"},
	)

	// LLM provider calls

	LLMCallDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "soulacy_llm_call_duration_seconds",
			Help:    "Time spent in an LLM provider Complete() call.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		},
		[]string{"provider", "model"},
	)
	LLMCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_llm_calls_total",
			Help: "Total LLM provider Complete() calls.",
		},
		[]string{"provider", "model", "outcome"}, // outcome: success|error
	)
	LLMInputTokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_llm_input_tokens_total",
			Help: "Total input tokens consumed by LLM calls (when reported).",
		},
		[]string{"provider", "model"},
	)
	LLMOutputTokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_llm_output_tokens_total",
			Help: "Total output tokens produced by LLM calls (when reported).",
		},
		[]string{"provider", "model"},
	)

	// Tool calls (Python tools, MCP tools, agent peer calls, built-ins)

	ToolCallDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "soulacy_tool_call_duration_seconds",
			Help:    "Time spent executing one tool call.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		},
		[]string{"tool"},
	)
	ToolCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_tool_calls_total",
			Help: "Total tool invocations.",
		},
		[]string{"tool", "outcome"}, // outcome: success|error
	)

	// Agent runs (one inbound user message → one engine.Handle() call)
	//
	// UNLABELLED BY AGENT, and that is a deliberate removal rather than an
	// omission. These four carried an `agent` label whose value is a raw,
	// user-authored agent ID. Two things follow from that in a multi-tenant
	// deployment, and the second is the serious one:
	//
	//  1. Cardinality grows with tenants × agents, unbounded, on a metric
	//     nobody can garbage-collect. That is the argument the run-latency
	//     histograms below already make about workspace labels.
	//  2. AGENT IDS ARE TENANT-IDENTIFYING. They are names people choose —
	//     `acme-invoice-reconciliation`, `project-titan-briefing`. The
	//     Prometheus exposition is ONE global text blob: it cannot be scoped
	//     per request, so every workspace owner or admin scraping
	//     /api/v1/metrics read every other workspace's agent names. The role
	//     gate on that route restricts WHO may scrape; it cannot restrict
	//     WHAT a scrape contains.
	//
	// The workspace label was excluded here from the start, with the
	// cardinality reasoning written down beside it. The agent label — same
	// endpoint, same blob, same problem, worse because the values are prose —
	// was left alone. That is what a rule looks like when it is applied where
	// somebody last thought about it rather than everywhere it holds.
	//
	// The per-agent view has not disappeared; it moved to where it can be
	// authorized. internal/runs and the action log are workspace-scoped by
	// construction and carry the same runs with the same outcomes and
	// timestamps, behind an API that resolves the caller's workspace.

	AgentRunDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "soulacy_agent_run_duration_seconds",
			Help:    "End-to-end wall-clock for one engine.Handle() call.",
			Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800},
		},
	)
	AgentRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_agent_runs_total",
			Help: "Total engine.Handle() invocations.",
		},
		[]string{"outcome"},
	)
	// AgentPanicsTotal counts panics recovered inside engine.Handle (S2.1).
	// A non-zero value means a run hit a bug that would previously have
	// crashed the whole process; alert on any increase.
	AgentPanicsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "soulacy_agent_panics_total",
			Help: "Panics recovered inside engine.Handle (would otherwise crash the process).",
		},
	)
	// AgentBudgetHaltsTotal counts runs halted by the per-run token/call
	// budget gate (S3.1). A rising value points at a runaway agent, a prompt
	// injection, or a budget set too low for the task.
	AgentBudgetHaltsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "soulacy_agent_budget_halts_total",
			Help: "Runs halted because they hit their per-run token or LLM-call budget.",
		},
	)

	// Actionlog + worker pool gauges

	ActionlogQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "soulacy_actionlog_queue_depth",
		Help: "Events currently buffered in the actionlog writer's queue.",
	})
	ActionlogBatchSize = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "soulacy_actionlog_batch_size",
		Help:    "Number of events flushed per actionlog batch.",
		Buckets: []float64{1, 5, 10, 25, 50, 100, 256, 512, 1024},
	})
	ActionlogDropsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "soulacy_actionlog_drops_total",
		Help: "Events dropped because the actionlog queue was full.",
	})

	WorkerPoolActiveRuns = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "soulacy_worker_pool_active_runs",
		Help: "Channel-message agent runs currently executing.",
	})

	// Channel inbox drops — messages silently discarded because the inbox
	// buffer was full when Enqueue() was called.
	ChannelInboxDropsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_channel_inbox_drops_total",
			Help: "Messages dropped because the shared channel inbox was full.",
		},
		[]string{"channel"}, // channel ID, or "unknown" when not determinable
	)
	ChannelInboundTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_channel_inbound_total",
			Help: "Inbound channel messages accepted by the shared channel inbox.",
		},
		[]string{"channel"},
	)
	ChannelOutboundTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_channel_outbound_total",
			Help: "Outbound channel send attempts, by channel adapter and outcome.",
		},
		[]string{"channel", "outcome"}, // outcome: success|error|unregistered
	)

	// --- Mobile companion: approvals, pairing, push ---

	// ApprovalsResolvedTotal counts tool-approval decisions by outcome
	// ("approved" or "denied"), regardless of which device resolved them.
	ApprovalsResolvedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_approvals_resolved_total",
			Help: "Tool-call approval decisions, labeled by outcome.",
		},
		[]string{"outcome"},
	)
	// PairingTokensTotal counts pairing token lifecycle events ("issued",
	// "redeemed", "rejected").
	PairingTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_pairing_tokens_total",
			Help: "Mobile pairing token events, labeled by result.",
		},
		[]string{"result"},
	)
	// PushSubscriptions is the current number of stored web-push subscriptions.
	PushSubscriptions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "soulacy_push_subscriptions",
		Help: "Web-push subscriptions currently registered.",
	})
	// PushSentTotal counts push notifications by delivery result ("sent" or
	// "gone").
	PushSentTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_push_sent_total",
			Help: "Web-push notifications attempted, labeled by result.",
		},
		[]string{"result"},
	)
)

func init() {
	Registry.MustRegister(
		HTTPRequestDuration, HTTPRequestsTotal,
		LLMCallDuration, LLMCallsTotal, LLMInputTokens, LLMOutputTokens,
		ToolCallDuration, ToolCallsTotal,
		AgentRunDuration, AgentRunsTotal, AgentPanicsTotal, AgentBudgetHaltsTotal,
		ActionlogQueueDepth, ActionlogBatchSize, ActionlogDropsTotal,
		WorkerPoolActiveRuns,
		ChannelInboxDropsTotal, ChannelInboundTotal, ChannelOutboundTotal,
		ApprovalsResolvedTotal, PairingTokensTotal, PushSubscriptions, PushSentTotal,
		RunQueueDuration, RunProcessingDuration, RunExternalDuration, RunSubmitDuration,
		// Process + Go runtime collectors give us memory / CPU / GC for free.
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		prometheus.NewGoCollector(),
	)
}

// Handler returns an http.Handler that serves the Prometheus text exposition
// format from the package-local Registry. Mounted by the gateway under the
// auth-gated /api/v1/metrics endpoint.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// ── Run latency decomposition (MU-027 criterion 6) ─────────────────────────
//
// Deliberately UNLABELLED by workspace. A histogram labelled by tenant grows
// its cardinality with the product's success, and a metric that degrades the
// monitoring system as customers are added is worse than no metric. The
// per-tenant view comes from the run record, which is per-workspace by
// construction and carries the same timestamps — see internal/runs/latency.go.
var (
	// RunQueueDuration is submission → a worker claiming the run: the number
	// that answers "should I add workers", and the one nobody was measuring.
	RunQueueDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "soulacy_run_queue_duration_seconds",
			Help:    "Time a durable run waited between submission and a worker claiming it.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		},
	)
	// RunProcessingDuration is execution time MINUS provider and tool time:
	// what Soulacy itself spent. AgentRunDuration conflates the two and is
	// dominated by provider latency, so it reports mostly on somebody else's
	// system.
	RunProcessingDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "soulacy_run_processing_duration_seconds",
			Help:    "Time a durable run spent inside Soulacy, excluding LLM provider and tool calls.",
			Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		},
	)
	// RunExternalDuration is the provider and tool time the run waited on,
	// recorded beside processing so the two are comparable at a glance.
	RunExternalDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "soulacy_run_external_duration_seconds",
			Help:    "Time a durable run spent waiting on LLM providers and tool subprocesses.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		},
	)
	// RunSubmitDuration is the acknowledgement latency criterion 1 sets a p95
	// target on. Nothing measured it, so the target could not be evaluated.
	RunSubmitDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "soulacy_run_submit_duration_seconds",
			Help:    "Time to acknowledge a run submission, excluding identity-provider latency.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		},
	)
)

// ObserveRunLatency records one finished run's decomposition.
func ObserveRunLatency(queue, external, processing time.Duration) {
	if queue >= 0 {
		RunQueueDuration.Observe(queue.Seconds())
	}
	if external >= 0 {
		RunExternalDuration.Observe(external.Seconds())
	}
	if processing >= 0 {
		RunProcessingDuration.Observe(processing.Seconds())
	}
}
