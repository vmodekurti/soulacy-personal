package reasoning_test

// scenarios_test.go — broad SCENARIO regression suite for the reasoning
// strategies (ReAct / Plan-Execute / auto-detect).
//
// The existing tests here are unit tests of the loop's mechanics. What was
// missing is coverage of the real agent SHAPES the product ships — the ones a
// release actually has to keep working:
//
//	channel capture · queue worker · KB ingestion · weather ·
//	stock/deals · interactive channel (Slack/Telegram) · scheduled cron
//
// Every scenario runs with a scripted LLM and a canned tool executor, so the
// whole suite is hermetic: no network, no model, no secrets, no clock skew.
// It asserts the things that actually break in production — that the agent calls
// the RIGHT tools in the RIGHT order, that a tool failure is surfaced rather
// than silently swallowed, and that the loop always terminates.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
)

// ── scenario harness ─────────────────────────────────────────────────────────

// cannedExecutor returns a per-tool scripted observation and records the exact
// call order, so a scenario can assert the tool sequence an agent shape must
// follow. Unknown tools return an error — an agent inventing a tool is a bug we
// want to catch, not paper over.
type cannedExecutor struct {
	results map[string]string // tool name → observation content
	fail    map[string]error  // tool name → injected failure

	// plan_execute runs independent steps concurrently, and the ToolExecutor
	// contract requires Execute to be safe for concurrent use — so the call
	// recorder needs a lock, exactly like a real executor's would.
	mu    sync.Mutex
	calls []reasoning.ToolCall
}

func (c *cannedExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	c.mu.Unlock()
	if err, bad := c.fail[call.Tool]; bad {
		return reasoning.Observation{Error: err, Source: call.Tool}
	}
	if out, ok := c.results[call.Tool]; ok {
		return reasoning.Observation{Content: out, Source: call.Tool}
	}
	return reasoning.Observation{
		Error:  errors.New("no such tool: " + call.Tool),
		Source: call.Tool,
	}
}

func (c *cannedExecutor) toolsCalled() []string {
	out := make([]string, 0, len(c.calls))
	for _, c := range c.calls {
		out = append(out, c.Tool)
	}
	return out
}

// act is a scripted "call this tool" turn.
func act(tool string, args map[string]any) reasoning.ThinkResponse {
	return reasoning.ThinkResponse{
		Thought: "calling " + tool,
		Action:  reasoning.ToolCall{Tool: tool, Arguments: args},
	}
}

// finish is a scripted "I'm done" turn.
func finish(answer string) reasoning.ThinkResponse {
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: answer}
}

// runScenario drives one agent shape to completion under a bounded budget.
func runScenario(t *testing.T, strategy string, turns []reasoning.ThinkResponse, exec *cannedExecutor, final string) reasoning.Result {
	t.Helper()
	llm := &scriptedLLM{responses: turns, reflectOut: final}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     strategy,
		MaxSteps:     8,
		StepTimeout:  2 * time.Second,
		TotalTimeout: 10 * time.Second,
	}, llm, exec)
	return loop.Run(context.Background(), "scenario-agent", "do the thing")
}

func assertToolOrder(t *testing.T, exec *cannedExecutor, want ...string) {
	t.Helper()
	got := exec.toolsCalled()
	if len(got) != len(want) {
		t.Fatalf("tool sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool sequence = %v, want %v", got, want)
		}
	}
}

// ── the seven shipped agent shapes ───────────────────────────────────────────

// Weather: the canonical single-tool lookup. Fetch, then answer.
func TestScenario_WeatherAgent(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"get_weather": `{"temp_c":18,"condition":"cloudy","city":"London"}`,
	}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("get_weather", map[string]any{"city": "London"}),
		finish("It's 18°C and cloudy in London."),
	}, exec, "It's 18°C and cloudy in London.")

	assertToolOrder(t, exec, "get_weather")
	if !res.Confident {
		t.Error("a clean single-tool run must be confident")
	}
	if !strings.Contains(res.Output, "18") {
		t.Errorf("final answer lost the tool result: %q", res.Output)
	}
}

// Stock / deals: gather → compute → answer. The multi-tool pipeline shape.
func TestScenario_StockDealsAgent(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"get_stock_price": `{"AAPL":231.4,"TSLA":402.1}`,
		"generate_chart":  `{"chart":"ok"}`,
	}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("get_stock_price", map[string]any{"tickers": "AAPL,TSLA"}),
		act("generate_chart", map[string]any{"series": "prices"}),
		finish("AAPL 231.40, TSLA 402.10 — chart attached."),
	}, exec, "AAPL 231.40, TSLA 402.10 — chart attached.")

	assertToolOrder(t, exec, "get_stock_price", "generate_chart")
	if !res.Confident {
		t.Error("clean multi-tool run must be confident")
	}
}

// Research / KB ingestion: search the KB, then write back what was learned.
func TestScenario_KBIngestionAgent(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"kb_search": `{"hits":[{"content":"refund window is 30 days"}]}`,
		"kb_write":  `{"doc_id":"d1"}`,
	}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("kb_search", map[string]any{"query": "refund window"}),
		act("kb_write", map[string]any{"content": "Refund window: 30 days."}),
		finish("Stored the refund policy in the knowledge base."),
	}, exec, "Stored the refund policy in the knowledge base.")

	assertToolOrder(t, exec, "kb_search", "kb_write")
	if !res.Confident {
		t.Error("clean KB run must be confident")
	}
}

// Queue worker: take an item, act on it, mark it done. The drain-the-queue shape.
func TestScenario_QueueWorkerAgent(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"queue_take": `{"item":{"url":"https://example.com/a"}}`,
		"fetch_url":  `{"title":"Example","text":"hello"}`,
		"kb_write":   `{"doc_id":"d2"}`,
	}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("queue_take", map[string]any{"queue": "inbox"}),
		act("fetch_url", map[string]any{"url": "https://example.com/a"}),
		act("kb_write", map[string]any{"content": "hello"}),
		finish("Processed 1 queued item."),
	}, exec, "Processed 1 queued item.")

	assertToolOrder(t, exec, "queue_take", "fetch_url", "kb_write")
	if !res.Confident {
		t.Error("clean queue-worker run must be confident")
	}
}

// Channel capture: read from a channel and persist. The inbound-capture shape.
func TestScenario_ChannelCaptureAgent(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"queue_put": `{"ok":true}`,
	}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("queue_put", map[string]any{"queue": "inbox", "item": "https://example.com/x"}),
		finish("Captured the link."),
	}, exec, "Captured the link.")

	assertToolOrder(t, exec, "queue_put")
	if !res.Confident {
		t.Error("clean capture run must be confident")
	}
}

// Interactive channel agent (Slack/Telegram): answer, then deliver to the channel.
// The delivery step is the one that silently rots, so assert it actually happens.
func TestScenario_InteractiveChannelAgent(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"web_search":   `{"results":["Soulacy is an agent runtime"]}`,
		"channel.send": `{"delivered":true}`,
	}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("web_search", map[string]any{"query": "what is soulacy"}),
		act("channel.send", map[string]any{"to": "C123", "text": "Soulacy is an agent runtime"}),
		finish("Replied in the channel."),
	}, exec, "Replied in the channel.")

	assertToolOrder(t, exec, "web_search", "channel.send")
	if !res.Confident {
		t.Error("clean interactive run must be confident")
	}
}

// Scheduled cron agent: unattended gather → deliver. Nobody is watching this one
// run, so a swallowed failure here is the worst case — see the failure test below.
func TestScenario_ScheduledCronAgent(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"web_search":   `{"results":["headline"]}`,
		"channel.send": `{"delivered":true}`,
	}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("web_search", map[string]any{"query": "top ai news"}),
		act("channel.send", map[string]any{"to": "@briefing", "text": "headline"}),
		finish("Daily briefing delivered."),
	}, exec, "Daily briefing delivered.")

	assertToolOrder(t, exec, "web_search", "channel.send")
	if !res.Confident {
		t.Error("clean scheduled run must be confident")
	}
}

// ── cross-cutting guarantees every shape must uphold ─────────────────────────

// A failing tool must make the run NOT confident. This is the signal the
// self-heal / failure-notifier paths key off — if a scheduled agent's delivery
// fails and the run still reports confident, the failure is invisible.
func TestScenario_ToolFailureIsNotConfident(t *testing.T) {
	exec := &cannedExecutor{
		results: map[string]string{"web_search": `{"results":["x"]}`},
		fail:    map[string]error{"channel.send": errors.New("chat not found")},
	}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("web_search", map[string]any{"query": "q"}),
		act("channel.send", map[string]any{"to": "bad"}),
		finish("Delivered."),
	}, exec, "Delivered.")

	if res.Confident {
		t.Fatal("a run whose delivery tool FAILED must not report confident — the failure would be invisible")
	}
	if len(exec.calls) < 2 {
		t.Errorf("the failing tool should still have been attempted: %v", exec.toolsCalled())
	}
}

// Calling a tool the agent doesn't have must surface as an error, not a silent
// success. (A model inventing `send_telegram` when only `channel.send` exists.)
func TestScenario_UnknownToolSurfacesAsError(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{"channel.send": "ok"}}
	res := runScenario(t, reasoning.StrategyReAct, []reasoning.ThinkResponse{
		act("send_telegram", map[string]any{"to": "x"}), // invented
		finish("Sent."),
	}, exec, "Sent.")

	if res.Confident {
		t.Fatal("invoking a nonexistent tool must not report confident")
	}
}

// Every shape must terminate. A model that never says "done" must still be
// stopped by the step budget rather than looping forever.
func TestScenario_RunawayAgentTerminatesOnStepBudget(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{"web_search": "ok"}}
	// Script far more tool turns than MaxSteps allows.
	turns := make([]reasoning.ThinkResponse, 50)
	for i := range turns {
		turns[i] = act("web_search", map[string]any{"query": "again"})
	}
	llm := &scriptedLLM{responses: turns}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     5,
		StepTimeout:  time.Second,
		TotalTimeout: 10 * time.Second,
	}, llm, exec)

	done := make(chan reasoning.Result, 1)
	go func() { done <- loop.Run(context.Background(), "runaway", "loop forever") }()

	select {
	case <-done:
		if len(exec.calls) > 5 {
			t.Errorf("MaxSteps=5 must bound tool calls, got %d", len(exec.calls))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runaway agent did not terminate — the step budget is not enforced")
	}
}

// Plan-Execute: the plan must actually drive tool execution, in order.
func TestScenario_PlanExecuteDecomposesAndExecutes(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{
		"web_search":   `{"results":["r"]}`,
		"channel.send": `{"delivered":true}`,
	}}
	llm := &stubLLM{planSteps: []reasoning.PlannedStep{
		{Description: "search the web", Tool: "web_search"},
		{Description: "deliver the result", Tool: "channel.send"},
	}}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxSteps:     8,
		MaxPlanSteps: 4,
		StepTimeout:  2 * time.Second,
		TotalTimeout: 10 * time.Second,
	}, llm, exec)

	res := loop.Run(context.Background(), "planner", "research and deliver")
	if len(res.Steps) == 0 {
		t.Fatal("plan_execute produced no steps")
	}
	called := exec.toolsCalled()
	if len(called) == 0 {
		t.Fatal("plan_execute planned but never executed any tool")
	}
	// The planned tools must be the ones actually invoked.
	for _, tool := range called {
		if tool != "web_search" && tool != "channel.send" {
			t.Errorf("plan_execute invoked an unplanned tool: %q (called=%v)", tool, called)
		}
	}
}

// An unknown strategy name must FAIL LOUDLY rather than silently running a
// different strategy than the one configured (P0-6). Executing tools under a
// substituted strategy is worse than not running: the operator believes their
// configured strategy is in force and every guarantee it implies is absent.
func TestScenario_UnknownStrategyIsRefused(t *testing.T) {
	exec := &cannedExecutor{results: map[string]string{"web_search": "ok"}}
	res := runScenario(t, "not_a_real_strategy", []reasoning.ThinkResponse{
		act("web_search", map[string]any{"query": "q"}),
		finish("Answered."),
	}, exec, "Answered.")

	if len(exec.calls) != 0 {
		t.Errorf("no tool may run under an unresolvable strategy, got %d calls", len(exec.calls))
	}
	if res.Confident {
		t.Error("a configuration error must not be reported as a confident run")
	}
	if !strings.Contains(res.Output, "configuration error") {
		t.Errorf("output should name the misconfiguration: %q", res.Output)
	}
}
