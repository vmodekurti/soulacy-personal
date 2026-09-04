package reasoning

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func levelIDs(levels [][]PlannedStep) [][]string {
	out := make([][]string, 0, len(levels))
	for _, lv := range levels {
		ids := make([]string, 0, len(lv))
		for _, ps := range lv {
			ids = append(ids, ps.ID)
		}
		out = append(out, ids)
	}
	return out
}

func TestPlanDependencyLevels(t *testing.T) {
	// Three independent searches then a dependent synthesis — the exact shape
	// that used to run strictly single-file.
	levels := planDependencyLevels(Plan{Steps: []PlannedStep{
		{ID: "s1", Tool: "web_search"},
		{ID: "s2", Tool: "web_search"},
		{ID: "s3", Tool: "web_search"},
		{ID: "sum", Tool: "summarize", DependsOn: []string{"s1", "s2", "s3"}},
	}})
	got := levelIDs(levels)
	if len(got) != 2 || len(got[0]) != 3 || len(got[1]) != 1 {
		t.Fatalf("expected [[s1 s2 s3] [sum]], got %v", got)
	}

	// A chain stays fully serial.
	chain := levelIDs(planDependencyLevels(Plan{Steps: []PlannedStep{
		{ID: "a", Tool: "t"},
		{ID: "b", Tool: "t", DependsOn: []string{"a"}},
		{ID: "c", Tool: "t", DependsOn: []string{"b"}},
	}}))
	if len(chain) != 3 {
		t.Fatalf("a dependency chain must not be parallelized: %v", chain)
	}
}

func TestPlanDependencyLevels_UndeclaredPlaceholderIsADependency(t *testing.T) {
	// b reads a's output via a placeholder but declares no depends_on. Running
	// them together would resolve the placeholder against an unfinished step,
	// so the level builder must treat the reference as a real dependency.
	got := levelIDs(planDependencyLevels(Plan{Steps: []PlannedStep{
		{ID: "a", Tool: "fetch_url"},
		{ID: "b", Tool: "summarize", Arguments: map[string]any{"text": "{{a.output}}"}},
	}}))
	if len(got) != 2 {
		t.Fatalf("placeholder reference must serialize the steps, got %v", got)
	}
	// Same via the legacy string Input map.
	got = levelIDs(planDependencyLevels(Plan{Steps: []PlannedStep{
		{ID: "a", Tool: "fetch_url"},
		{ID: "b", Tool: "summarize", Input: map[string]string{"text": "{{ a.result }}"}},
	}}))
	if len(got) != 2 {
		t.Fatalf("legacy Input placeholder must serialize the steps, got %v", got)
	}
}

func TestPlanDependencyLevels_UnknownAndForwardDepsSerialize(t *testing.T) {
	// An unknown dependency and a forward reference both fall back to the
	// conservative serial placement rather than being silently ignored.
	got := levelIDs(planDependencyLevels(Plan{Steps: []PlannedStep{
		{ID: "a", Tool: "t"},
		{ID: "b", Tool: "t", DependsOn: []string{"ghost"}},
		{ID: "c", Tool: "t", DependsOn: []string{"d"}}, // forward ref
		{ID: "d", Tool: "t"},
	}}))
	for _, lv := range got {
		for _, id := range lv {
			if id == "b" || id == "c" {
				if len(lv) != 1 {
					t.Fatalf("step %q with an unresolvable dep must run alone, got %v", id, got)
				}
			}
		}
	}
}

// countingExecutor records concurrency and returns canned results.
type countingExecutor struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	calls    int32
	delay    time.Duration
	failFor  map[string]bool
}

func (c *countingExecutor) Execute(ctx context.Context, call ToolCall) Observation {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.maxSeen {
		c.maxSeen = c.inFlight
	}
	c.mu.Unlock()
	atomic.AddInt32(&c.calls, 1)
	time.Sleep(c.delay)
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	if c.failFor[call.Tool] {
		return Observation{Error: fmt.Errorf("boom")}
	}
	return Observation{Content: "ok:" + call.Tool}
}

// planningBackend returns a fixed plan and a fixed reflection.
type planningBackend struct {
	plan    Plan
	planErr error
	output  string
}

func (p planningBackend) Think(context.Context, ThinkRequest) (ThinkResponse, error) {
	return ThinkResponse{IsDone: true, FinalAnswer: "react fallback answer"}, nil
}

func (p planningBackend) Plan(context.Context, string, string, int) (Plan, error) {
	return p.plan, p.planErr
}

func (p planningBackend) Reflect(context.Context, ReflectRequest) (ReflectResponse, error) {
	return ReflectResponse{Output: p.output}, nil
}

func planEnv(backend LLMBackend, exec ToolExecutor, tools []string) Env {
	return Env{
		Config: LoopConfig{
			MaxSteps: 10, MaxPlanSteps: 6,
			StepTimeout: 5 * time.Second, TotalTimeout: 30 * time.Second,
			ToolNames: tools,
		},
		LLM:   backend,
		Tools: exec,
	}
}

func TestPlanExecute_IndependentStepsRunConcurrently(t *testing.T) {
	exec := &countingExecutor{delay: 60 * time.Millisecond}
	backend := planningBackend{
		plan: Plan{Steps: []PlannedStep{
			{ID: "s1", Tool: "web_search"},
			{ID: "s2", Tool: "web_search"},
			{ID: "s3", Tool: "web_search"},
		}},
		output: "done",
	}
	start := time.Now()
	steps, _ := planExecuteStrategy{}.Run(context.Background(),
		planEnv(backend, exec, []string{"web_search"}), "task")
	elapsed := time.Since(start)

	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if exec.maxSeen < 2 {
		t.Errorf("independent steps did not overlap (max in flight = %d)", exec.maxSeen)
	}
	// Serial would be ~180ms; concurrent ~60ms. Generous bound for CI jitter.
	if elapsed > 150*time.Millisecond {
		t.Errorf("independent steps appear to have run serially: %v", elapsed)
	}
	// Order must still follow the plan, not completion order.
	for i, want := range []string{"s1", "s2", "s3"} {
		if steps[i].ID != want {
			t.Errorf("step %d: got %q want %q — plan order must be preserved", i, steps[i].ID, want)
		}
	}
}

func TestPlanExecute_MaxParallelStepsOneForcesSerial(t *testing.T) {
	// The escape hatch for a custom ToolExecutor that isn't concurrency-safe.
	exec := &countingExecutor{delay: 20 * time.Millisecond}
	backend := planningBackend{
		plan: Plan{Steps: []PlannedStep{
			{ID: "s1", Tool: "web_search"},
			{ID: "s2", Tool: "web_search"},
			{ID: "s3", Tool: "web_search"},
		}},
		output: "done",
	}
	env := planEnv(backend, exec, []string{"web_search"})
	env.Config.MaxParallelSteps = 1

	steps, _ := planExecuteStrategy{}.Run(context.Background(), env, "task")
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if exec.maxSeen != 1 {
		t.Errorf("max_parallel_steps=1 must keep execution serial, saw %d in flight", exec.maxSeen)
	}
}

func TestPlanExecute_DependencyChainStaysSerialAndSkipsOnFailure(t *testing.T) {
	exec := &countingExecutor{failFor: map[string]bool{"broken": true}}
	backend := planningBackend{
		plan: Plan{Steps: []PlannedStep{
			{ID: "a", Tool: "broken"},
			{ID: "b", Tool: "web_search", DependsOn: []string{"a"}},
		}},
		output: "done",
	}
	steps, _ := planExecuteStrategy{}.Run(context.Background(),
		planEnv(backend, exec, []string{"broken", "web_search"}), "task")

	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
	if !strings.Contains(steps[1].Obs.Content, "skipped: dependency") {
		t.Errorf("dependent step should be skipped after upstream failure: %q", steps[1].Obs.Content)
	}
	if !strings.Contains(steps[1].Obs.Content, `"a"`) {
		t.Errorf("skip record should name the unmet dependency: %q", steps[1].Obs.Content)
	}
}

func TestPlanExecute_DowngradeToReActIsVisible(t *testing.T) {
	exec := &countingExecutor{}
	// The plan asks for a tool this agent cannot call — the exact silent
	// downgrade that made a trace say plan_execute while ReAct actually ran.
	backend := planningBackend{
		plan: Plan{Steps: []PlannedStep{
			{ID: "s1", Tool: "read_file"},
		}},
		output: "done",
	}
	steps, _ := planExecuteStrategy{}.Run(context.Background(),
		planEnv(backend, exec, []string{"web_search"}), "task")

	if len(steps) == 0 {
		t.Fatal("expected at least the downgrade step")
	}
	first := steps[0]
	if first.Obs.Source != "planner" {
		t.Errorf("downgrade step source = %q, want \"planner\"", first.Obs.Source)
	}
	for _, want := range []string{"read_file", "ReAct"} {
		if !strings.Contains(first.Obs.Content, want) {
			t.Errorf("downgrade record missing %q: %q", want, first.Obs.Content)
		}
	}
	// A ReAct fallback still routinely answers well, so the downgrade record
	// alone must not mark the run degraded.
	if containsToolErrors(steps) {
		t.Error("a visible downgrade must not by itself flip the run's confidence")
	}
}

func TestPlanUnavailableTool_NamesTheCause(t *testing.T) {
	reason, bad := planUnavailableTool(Plan{Steps: []PlannedStep{
		{ID: "s1", Tool: "web_search"},
		{ID: "s2", Tool: "read_file"},
	}}, []string{"web_search"})
	if !bad {
		t.Fatal("expected the plan to be rejected")
	}
	for _, want := range []string{"s2", "read_file"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason missing %q: %q", want, reason)
		}
	}
	if _, bad := planUnavailableTool(Plan{Steps: []PlannedStep{
		{ID: "s1", Tool: "web_search"},
	}}, []string{"web_search"}); bad {
		t.Error("a fully satisfiable plan must be accepted")
	}
}

var _ = sdkr.FlowNode{} // keep the sdk import meaningful across refactors
