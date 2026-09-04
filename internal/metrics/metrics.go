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

	AgentRunDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "soulacy_agent_run_duration_seconds",
			Help:    "End-to-end wall-clock for one engine.Handle() call.",
			Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800},
		},
		[]string{"agent"},
	)
	AgentRunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_agent_runs_total",
			Help: "Total engine.Handle() invocations.",
		},
		[]string{"agent", "outcome"},
	)
	// AgentPanicsTotal counts panics recovered inside engine.Handle (S2.1).
	// A non-zero value means a run hit a bug that would previously have
	// crashed the whole process; alert on any increase.
	AgentPanicsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_agent_panics_total",
			Help: "Panics recovered inside engine.Handle (would otherwise crash the process).",
		},
		[]string{"agent"},
	)
	// AgentBudgetHaltsTotal counts runs halted by the per-run token/call
	// budget gate (S3.1). A rising value points at a runaway agent, a prompt
	// injection, or a budget set too low for the task.
	AgentBudgetHaltsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_agent_budget_halts_total",
			Help: "Runs halted because they hit their per-run token or LLM-call budget.",
		},
		[]string{"agent"},
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
		[]string{"channel", "agent"},
	)
	ChannelOutboundTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "soulacy_channel_outbound_total",
			Help: "Outbound channel send attempts, by channel adapter, agent, and outcome.",
		},
		[]string{"channel", "agent", "outcome"}, // outcome: success|error|unregistered
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
