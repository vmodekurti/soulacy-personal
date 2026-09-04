package reasoning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
	"gopkg.in/yaml.v3"
)

// recNode records each node execution and returns canned results.
type recRunner struct {
	calls   []string // "<nodeID>:<renderedInput>"
	results map[string]string
	errs    map[string]error
	errOnce map[string]bool // error on first call only (retry tests)
	seen    map[string]int
}

func (r *recRunner) run(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
	if r.seen == nil {
		r.seen = map[string]int{}
	}
	r.seen[node.ID]++
	r.calls = append(r.calls, node.ID+":"+renderedInput)
	if err := r.errs[node.ID]; err != nil {
		if !r.errOnce[node.ID] || r.seen[node.ID] == 1 {
			return nil, err
		}
	}
	if out, ok := r.results[node.ID]; ok {
		return json.RawMessage(out), nil
	}
	return json.RawMessage(fmt.Sprintf(`{"node":%q}`, node.ID)), nil
}

func nodeIDs(calls []string) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.SplitN(c, ":", 2)[0])
	}
	return out
}

func TestCompileFlow_Validation(t *testing.T) {
	cases := map[string]sdkr.FlowSpec{
		"no nodes": {},
		"dup node ids": {Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t"}, {ID: "a", Tool: "t"},
		}},
		"edge from unknown": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
			Edges: []sdkr.FlowEdge{{From: "ghost", To: "a"}},
		},
		"edge to unknown": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
			Edges: []sdkr.FlowEdge{{From: "a", To: "ghost"}},
		},
		"unknown entry": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
			Entry: "ghost",
		},
		"tool node without tool": {
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeTool}},
		},
		"agent node without agent": {
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeAgent}},
		},
		"bad kind": {
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: "warp", Tool: "t"}},
		},
		"missing node id": {
			Nodes: []sdkr.FlowNode{{Tool: "t"}},
		},
		"map on structural node": {
			Nodes: []sdkr.FlowNode{{ID: "a", Kind: sdkr.FlowNodeBranch, ForEach: `[]`}},
		},
		"invalid map variable": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", ForEach: `[]`, ItemVar: "bad-name"}},
		},
		"excessive map parallelism": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", ForEach: `[]`, MaxParallel: 33}},
		},
		"map settings without map": {
			Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", MaxParallel: 2}},
		},
	}
	for name, spec := range cases {
		if _, err := CompileFlow(spec); err == nil {
			t.Errorf("%s: expected compile error", name)
		}
	}

	// Valid graph compiles; kinds are inferred.
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t1"},    // inferred kind=tool
			{ID: "b", Agent: "peer"}, // inferred kind=agent
			{ID: "c"},                // inferred kind=branch
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	})
	if err != nil {
		t.Fatalf("CompileFlow: %v", err)
	}
	if g.Node("a").Kind != sdkr.FlowNodeTool || g.Node("b").Kind != sdkr.FlowNodeAgent || g.Node("c").Kind != sdkr.FlowNodeBranch {
		t.Errorf("kind inference wrong: %+v %+v %+v", g.Node("a"), g.Node("b"), g.Node("c"))
	}
}

func TestRunFlow_ForEachRunsConcurrentlyAndPreservesOrder(t *testing.T) {
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{
			ID:          "search_sources",
			Tool:        "web_search",
			ForEach:     `["hbr.org","technologyreview.com","gartner.com"]`,
			ItemVar:     "domain",
			MaxParallel: 3,
			Input:       `{"query":"site:{{ .domain }}"}`,
			Output:      "searches",
		}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	started := make(chan string, 3)
	release := make(chan struct{})
	var mu sync.Mutex
	var observed []FlowNodeRun
	run := func(_ context.Context, _ sdkr.FlowNode, input string) (json.RawMessage, error) {
		started <- input
		<-release
		var args map[string]string
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, err
		}
		return json.RawMessage(fmt.Sprintf(`{"query":%q}`, args["query"])), nil
	}

	type runResult struct {
		out json.RawMessage
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		out, runErr := RunFlow(context.Background(), g, nil, run, FlowHooks{
			Observe: func(rec FlowNodeRun) {
				mu.Lock()
				observed = append(observed, rec)
				mu.Unlock()
			},
		})
		done <- runResult{out: out, err: runErr}
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("mapped executions did not start concurrently")
		}
	}
	close(release)
	result := <-done
	if result.err != nil {
		t.Fatalf("RunFlow: %v", result.err)
	}
	want := `[{"query":"site:hbr.org"},{"query":"site:technologyreview.com"},{"query":"site:gartner.com"}]`
	if string(result.out) != want {
		t.Fatalf("ordered aggregate=%s, want %s", result.out, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 4 {
		t.Fatalf("observed %d records, want 3 item records plus one aggregate: %+v", len(observed), observed)
	}
}

func TestRunFlow_ForEachRejectsUnboundedInput(t *testing.T) {
	items := make([]int, maxFlowMapItems+1)
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "map", Tool: "t", ForEach: string(raw), MaxParallel: 2}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	called := false
	_, err = RunFlow(context.Background(), g, nil, func(context.Context, sdkr.FlowNode, string) (json.RawMessage, error) {
		called = true
		return nil, nil
	}, FlowHooks{})
	if err == nil || !strings.Contains(err.Error(), "exceeding the limit") {
		t.Fatalf("expected bounded-map error, got %v", err)
	}
	if called {
		t.Fatal("runner must not execute when for_each exceeds its safety bound")
	}
}

func TestRunFlow_LinearChain(t *testing.T) {
	r := &recRunner{results: map[string]string{
		"fetch":   `{"items": 3}`,
		"publish": `"done"`,
	}}
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "fetch", Tool: "web_search", Input: `{"q":"{{.trigger}}"}`, Output: "found"},
			{ID: "publish", Tool: "post", Input: `{"count":{{.found.items}}}`},
		},
		Edges: []sdkr.FlowEdge{{From: "fetch", To: "publish"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := RunFlow(context.Background(), g, map[string]any{"trigger": "news"}, r.run, FlowHooks{})
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if string(out) != `"done"` {
		t.Errorf("final output = %s", out)
	}
	want := []string{`fetch:{"q":"news"}`, `publish:{"count":3}`}
	if len(r.calls) != 2 || r.calls[0] != want[0] || r.calls[1] != want[1] {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
}

func TestRunFlow_RepairsTemplateInputAndContinuesSameRun(t *testing.T) {
	r := &recRunner{results: map[string]string{
		"produce": `{"items":[{"url":"https://example.com"}]}`,
		"consume": `"done"`,
	}}
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "produce", Tool: "search", Output: "source_pack"},
			{ID: "consume", Tool: "add_source", Input: `{"text":{{toJson .source_pack.text}}}`},
		},
		Edges: []sdkr.FlowEdge{{From: "produce", To: "consume"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	repairCalls := 0
	var observed FlowNodeRun
	out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{
		RepairInput: func(_ context.Context, node sdkr.FlowNode, inputTemplate string, renderErr error, vars map[string]any) (string, bool) {
			repairCalls++
			if node.ID != "consume" || !strings.Contains(inputTemplate, ".source_pack.text") {
				t.Fatalf("unexpected repair request: node=%s template=%q", node.ID, inputTemplate)
			}
			if renderErr == nil || vars["source_pack"] == nil {
				t.Fatalf("repair request missing live failure context: err=%v vars=%v", renderErr, vars)
			}
			return `{"text":"recovered from live items"}`, true
		},
		Observe: func(rec FlowNodeRun) {
			if rec.NodeID == "consume" {
				observed = rec
			}
		},
	})
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if string(out) != `"done"` {
		t.Fatalf("output = %s, want done", out)
	}
	if repairCalls != 1 {
		t.Fatalf("repair calls = %d, want 1", repairCalls)
	}
	if len(r.calls) != 2 || r.calls[1] != `consume:{"text":"recovered from live items"}` {
		t.Fatalf("calls = %#v", r.calls)
	}
	if observed.Error != "" || observed.Input != `{"text":"recovered from live items"}` {
		t.Fatalf("observed repaired run = %+v", observed)
	}
}

func TestRunFlow_ObservesTemplateRenderFailure(t *testing.T) {
	r := &recRunner{results: map[string]string{
		"produce": `"plain text instead of an object"`,
	}}
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "produce", Tool: "fetch", Output: "source_pack"},
			{ID: "consume", Tool: "publish", Input: `{"text":{{ toJson .source_pack.text }}}`},
		},
		Edges: []sdkr.FlowEdge{{From: "produce", To: "consume"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var observed []FlowNodeRun
	_, err = RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{
		Observe: func(rec FlowNodeRun) { observed = append(observed, rec) },
	})
	if err == nil {
		t.Fatal("expected consumer render failure")
	}
	if len(observed) != 2 {
		t.Fatalf("observed %d nodes, want producer and failed consumer: %+v", len(observed), observed)
	}
	failed := observed[1]
	if failed.NodeID != "consume" || !strings.Contains(failed.Error, "can't evaluate field text") {
		t.Fatalf("failed trace = %+v", failed)
	}
	if failed.Input != `{"text":{{ toJson .source_pack.text }}}` {
		t.Fatalf("failed trace lost original template: %q", failed.Input)
	}
}

func TestRunFlow_ConditionalBranch(t *testing.T) {
	r := &recRunner{results: map[string]string{
		"check": `{"severity": "high"}`,
	}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "check", Tool: "probe", Output: "res"},
			{ID: "page", Tool: "pagerduty"},
			{ID: "log", Tool: "logger"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "check", To: "page", If: `{{eq .res.severity "high"}}`},
			{From: "check", To: "log"}, // fallback
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := nodeIDs(r.calls)
	if len(got) != 2 || got[1] != "page" {
		t.Errorf("path = %v, want [check page]", got)
	}
}

func TestRunFlow_BoundedCycle(t *testing.T) {
	// refine ↔ judge cycle: back edge capped at 3 traversals, judge never
	// says done, so the loop drains the budget then exits via the
	// fallback edge to finish.
	r := &recRunner{results: map[string]string{
		"judge": `{"ok": false}`,
	}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "refine", Tool: "improve"},
			{ID: "judge", Tool: "evaluate", Output: "verdict"},
			{ID: "finish", Tool: "publish"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "refine", To: "judge", MaxIterations: 4},
			{From: "judge", To: "refine", If: `{{not .verdict.ok}}`, MaxIterations: 3},
			{From: "judge", To: "finish"},
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := nodeIDs(r.calls)
	// refine judge ×4 (1 initial + 3 loop-backs), then finish.
	want := []string{"refine", "judge", "refine", "judge", "refine", "judge", "refine", "judge", "finish"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("path = %v\nwant  %v", got, want)
	}
}

func TestRunFlow_DefaultEdgeBudgetIsOne(t *testing.T) {
	// A cycle with no explicit max_iterations must run at most once around.
	r := &recRunner{}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t"},
			{ID: "b", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "a", To: "b", MaxIterations: 2},
			{From: "b", To: "a"}, // default budget 1: one loop-back only
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := strings.Join(nodeIDs(r.calls), " ")
	if got != "a b a b" {
		t.Errorf("path = %q, want \"a b a b\"", got)
	}
}

func TestRunFlow_GlobalBudgetAborts(t *testing.T) {
	r := &recRunner{}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t"},
			{ID: "b", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "a", To: "b", MaxIterations: 1000},
			{From: "b", To: "a", MaxIterations: 1000},
		},
		MaxNodeExecutions: 7,
	})
	_, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget abort, got %v (calls=%d)", err, len(r.calls))
	}
	if len(r.calls) != 7 {
		t.Errorf("executed %d nodes, want exactly 7", len(r.calls))
	}
}

func TestRunFlow_OnErrorSemantics(t *testing.T) {
	boom := errors.New("boom")

	// abort (default)
	r := &recRunner{errs: map[string]error{"a": boom}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}, {ID: "b", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err == nil {
		t.Error("abort: expected error")
	}

	// skip: error swallowed, edges still followed
	r = &recRunner{errs: map[string]error{"a": boom}}
	g, _ = CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", OnError: "skip"}, {ID: "b", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Errorf("skip: %v", err)
	}
	if got := strings.Join(nodeIDs(r.calls), " "); got != "a b" {
		t.Errorf("skip path = %q", got)
	}

	// retry: first call fails, retry succeeds
	r = &recRunner{errs: map[string]error{"a": boom}, errOnce: map[string]bool{"a": true}}
	g, _ = CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t", OnError: "retry"}, {ID: "b", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Errorf("retry: %v", err)
	}
	if got := strings.Join(nodeIDs(r.calls), " "); got != "a a b" {
		t.Errorf("retry path = %q", got)
	}
}

func TestRunFlow_BranchNodeRunsNothing(t *testing.T) {
	r := &recRunner{results: map[string]string{"probe": `{"n": 2}`}}
	g, _ := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "probe", Tool: "t", Output: "p"},
			{ID: "fork"}, // branch
			{ID: "small", Tool: "t"},
			{ID: "big", Tool: "t"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "probe", To: "fork"},
			{From: "fork", To: "big", If: `{{gt (printf "%v" .p.n) "5"}}`},
			{From: "fork", To: "small"},
		},
	})
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	got := strings.Join(nodeIDs(r.calls), " ")
	if got != "probe small" {
		t.Errorf("path = %q, want \"probe small\" (branch node must not execute)", got)
	}
}

func TestRunFlow_HooksAndResume(t *testing.T) {
	// First run: node b aborts. Second run: hooks replay a's completed
	// visit (restore) so only b (and c) execute.
	completed := map[string]json.RawMessage{} // visitKey → state

	hooks := FlowHooks{
		Restore: func(visitKey string) (json.RawMessage, bool) {
			st, ok := completed[visitKey]
			return st, ok
		},
		Completed: func(visitKey string, state json.RawMessage) {
			completed[visitKey] = state
		},
	}

	spec := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Output: "ares"},
			{ID: "b", Tool: "t", Output: "bres"},
			{ID: "c", Tool: "t", Input: `{{.ares.v}}-{{.bres.v}}`},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	}
	g, _ := CompileFlow(spec)

	r1 := &recRunner{
		results: map[string]string{"a": `{"v":"A"}`},
		errs:    map[string]error{"b": errors.New("crash")},
	}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r1.run, hooks); err == nil {
		t.Fatal("first run should fail at b")
	}
	if len(completed) != 1 {
		t.Fatalf("completed after crash = %v, want only a's visit", completed)
	}

	r2 := &recRunner{results: map[string]string{
		"a": `{"v":"WRONG"}`, // must NOT be called
		"b": `{"v":"B"}`,
	}}
	if _, err := RunFlow(context.Background(), g, map[string]any{}, r2.run, hooks); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	got := nodeIDs(r2.calls)
	if strings.Join(got, " ") != "b c" {
		t.Fatalf("resume path = %v, want [b c]", got)
	}
	// c's input proves a's state was RESTORED, not re-run.
	if want := "c:A-B"; r2.calls[1] != want {
		t.Errorf("c call = %q, want %q", r2.calls[1], want)
	}
}

func TestFlowStrategy_RegisteredAndRuns(t *testing.T) {
	// The "flow" strategy must be in the registry and execute the graph
	// through env.Tools.
	exec := &fakeFlowExecutor{out: map[string]string{
		"greet": "hello from tool",
	}}
	strat, ok, err := registry.NewReasoningStrategy("flow", nil)
	if !ok || err != nil || strat == nil {
		t.Fatalf("flow strategy not registered: ok=%v err=%v", ok, err)
	}
	env := Env{
		Config: LoopConfig{
			Strategy: "flow",
			Flow: &sdkr.FlowSpec{
				Nodes: []sdkr.FlowNode{{ID: "n1", Tool: "greet", Input: `{{.trigger}}`, Output: "g"}},
			},
		},
		Tools: exec,
	}
	steps, resp := strat.Run(context.Background(), env, "hi there")
	if len(steps) != 1 || steps[0].Action.Tool != "greet" {
		t.Fatalf("steps = %+v", steps)
	}
	if !strings.Contains(resp.Output, "hello from tool") {
		t.Errorf("output = %q", resp.Output)
	}
	if exec.calls[0].Tool != "greet" {
		t.Errorf("executor saw %+v", exec.calls)
	}
}

type fakeFlowExecutor struct {
	calls []ToolCall
	out   map[string]string
}

func (f *fakeFlowExecutor) Execute(ctx context.Context, call ToolCall) Observation {
	f.calls = append(f.calls, call)
	return Observation{Content: f.out[call.Tool], Source: call.Tool}
}

// --- Story S0.3: typed ports & per-node params ---

// TestCompileFlow_PortValidation covers edge port wiring: an undeclared
// from/to port is a compile error; a declared one compiles; and an empty
// port (the legacy implicit single port) always compiles regardless of
// whether the node declares ports.
func TestCompileFlow_PortValidation(t *testing.T) {
	// Declared ports referenced by an edge → ok.
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Outputs: []sdkr.FlowPort{{Name: "out1"}}},
			{ID: "b", Tool: "t", Inputs: []sdkr.FlowPort{{Name: "in1"}}},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b", FromPort: "out1", ToPort: "in1"}},
	})
	if err != nil {
		t.Fatalf("declared ports should compile: %v", err)
	}
	if g == nil {
		t.Fatal("nil graph")
	}

	// Empty ports on nodes that DO declare ports → still legacy implicit, ok.
	if _, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Outputs: []sdkr.FlowPort{{Name: "out1"}}},
			{ID: "b", Tool: "t", Inputs: []sdkr.FlowPort{{Name: "in1"}}},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "b"}},
	}); err != nil {
		t.Fatalf("empty ports should compile (implicit): %v", err)
	}

	// Undeclared from_port / to_port → compile error.
	bad := map[string]sdkr.FlowSpec{
		"undeclared from_port": {
			Nodes: []sdkr.FlowNode{
				{ID: "a", Tool: "t", Outputs: []sdkr.FlowPort{{Name: "out1"}}},
				{ID: "b", Tool: "t"},
			},
			Edges: []sdkr.FlowEdge{{From: "a", To: "b", FromPort: "ghost"}},
		},
		"from_port on node with no outputs": {
			Nodes: []sdkr.FlowNode{
				{ID: "a", Tool: "t"},
				{ID: "b", Tool: "t"},
			},
			Edges: []sdkr.FlowEdge{{From: "a", To: "b", FromPort: "out1"}},
		},
		"undeclared to_port": {
			Nodes: []sdkr.FlowNode{
				{ID: "a", Tool: "t"},
				{ID: "b", Tool: "t", Inputs: []sdkr.FlowPort{{Name: "in1"}}},
			},
			Edges: []sdkr.FlowEdge{{From: "a", To: "b", ToPort: "ghost"}},
		},
	}
	for name, spec := range bad {
		if _, err := CompileFlow(spec); err == nil {
			t.Errorf("%s: expected compile error", name)
		}
	}
}

// TestCompileFlow_ParamsPreserved confirms typed per-node Params survive
// compilation onto the node and are untouched.
func TestCompileFlow_ParamsPreserved(t *testing.T) {
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Params: map[string]any{"limit": 5, "mode": "fast"}},
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	p := g.Node("a").Params
	if p == nil || p["limit"] != 5 || p["mode"] != "fast" {
		t.Errorf("params not preserved on compiled node: %+v", p)
	}
}

// TestRunFlow_PortsAndParamsRegression proves a flow that declares ports
// and params still compiles, runs, and templates Input EXACTLY as a
// portless/paramless flow would — ports/params are inert at runtime.
// TestRunFlow_ParamsPreservedAtRuntime verifies Params remain inert at run time:
// a node carrying Params executes exactly as if it had none (Params are
// pass-through config, not input).
func TestRunFlow_ParamsPreservedAtRuntime(t *testing.T) {
	r := &recRunner{results: map[string]string{"a": `"ok"`}}
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "a", Tool: "t", Input: `{"q":"{{.trigger}}"}`, Params: map[string]any{"k": "v"}},
		},
		Edges: []sdkr.FlowEdge{{From: "a", To: "end"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := RunFlow(context.Background(), g, map[string]any{"trigger": "news"}, r.run, FlowHooks{}); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if want := []string{`a:{"q":"news"}`}; len(r.calls) != 1 || r.calls[0] != want[0] {
		t.Errorf("calls = %v, want %v (params must not alter execution)", r.calls, want)
	}
}

// TestRunFlow_TypedPortHandoff is the Story S0.3 runtime-resolution contract:
// a node with a wired to_port receives its input ASSEMBLED from the upstream
// output port — no Go template. A specific from_port name ("id") carries that
// field; a generic one ("result") carries the whole output. Static constants the
// consumer declares in Input are preserved and the wires overlay on top.
func TestRunFlow_TypedPortHandoff(t *testing.T) {
	r := &recRunner{results: map[string]string{
		"create": `{"id":"nb-42","title":"My Notebook"}`,
		"gen":    `{"id":"nb-42","audio_url":"https://x/a.mp3"}`,
		"post":   `"sent"`,
	}}
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Tool: "create_notebook", Output: "notebook",
				Outputs: []sdkr.FlowPort{{Name: "id"}}},
			// gen carries a static constant (action) in Input and receives the
			// notebook id via a wired port — no template anywhere.
			{ID: "gen", Tool: "generate_audio", Input: `{"action":"generate"}`, Output: "audio",
				Inputs:  []sdkr.FlowPort{{Name: "notebook_id"}},
				Outputs: []sdkr.FlowPort{{Name: "audio_url"}, {Name: "result"}}},
			{ID: "post", Tool: "post_link",
				Inputs: []sdkr.FlowPort{{Name: "url"}, {Name: "whole"}}},
		},
		Edges: []sdkr.FlowEdge{
			{From: "create", To: "gen", FromPort: "id", ToPort: "notebook_id"},
			{From: "gen", To: "post", FromPort: "audio_url", ToPort: "url"},
			// A generic from_port ("result") + no such field → whole output wired.
			{From: "gen", To: "post", FromPort: "result", ToPort: "whole"},
			{From: "post", To: "end"},
		},
		Entry: "create",
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := RunFlow(context.Background(), g, map[string]any{}, r.run, FlowHooks{})
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if string(out) != `"sent"` {
		t.Errorf("final output = %s", out)
	}
	// create: no input. gen: constant + wired id. post: wired url + whole object.
	wantGen := `gen:{"action":"generate","notebook_id":"nb-42"}`
	wantPost := `post:{"url":"https://x/a.mp3","whole":{"audio_url":"https://x/a.mp3","id":"nb-42"}}`
	if len(r.calls) != 3 {
		t.Fatalf("calls = %v (want 3)", r.calls)
	}
	if r.calls[1] != wantGen {
		t.Errorf("gen call = %q\n          want %q", r.calls[1], wantGen)
	}
	if r.calls[2] != wantPost {
		t.Errorf("post call = %q\n           want %q", r.calls[2], wantPost)
	}
}

// TestFlowPorts_YAMLRoundTrip verifies the new fields serialize via YAML
// with snake_case keys and survive a marshal/unmarshal cycle.
func TestFlowPorts_YAMLRoundTrip(t *testing.T) {
	in := sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{
			ID:          "a",
			Tool:        "t",
			Inputs:      []sdkr.FlowPort{{Name: "in1", Type: "string", Label: "In"}},
			Outputs:     []sdkr.FlowPort{{Name: "out1"}},
			Params:      map[string]any{"k": "v"},
			ForEach:     `["one","two"]`,
			ItemVar:     "entry",
			MaxParallel: 2,
		}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "end", FromPort: "out1"}},
	}
	b, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"inputs:", "outputs:", "params:", "for_each:", "item_var:", "max_parallel:", "from_port:"} {
		if !strings.Contains(string(b), key) {
			t.Errorf("expected YAML key %q in:\n%s", key, b)
		}
	}
	var got sdkr.FlowSpec
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	n := got.Nodes[0]
	if len(n.Inputs) != 1 || n.Inputs[0].Name != "in1" || n.Inputs[0].Type != "string" || n.Inputs[0].Label != "In" {
		t.Errorf("inputs round-trip wrong: %+v", n.Inputs)
	}
	if len(n.Outputs) != 1 || n.Outputs[0].Name != "out1" {
		t.Errorf("outputs round-trip wrong: %+v", n.Outputs)
	}
	if n.Params["k"] != "v" {
		t.Errorf("params round-trip wrong: %+v", n.Params)
	}
	if n.ForEach != `["one","two"]` || n.ItemVar != "entry" || n.MaxParallel != 2 {
		t.Errorf("map settings round-trip wrong: %+v", n)
	}
	if got.Edges[0].FromPort != "out1" {
		t.Errorf("from_port round-trip wrong: %q", got.Edges[0].FromPort)
	}
}

// TestCompileFlow_PythonKind covers the Studio "Custom Python" node (Phase 1):
// inline Code infers kind=python and compiles; an explicit python node may
// instead reference a deployed tool; a python node with neither is rejected.
func TestCompileFlow_PythonKind(t *testing.T) {
	g, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "transform", Code: "print(1)"}},
		Entry: "transform",
	})
	if err != nil {
		t.Fatalf("inline-code node should compile: %v", err)
	}
	if got := g.Node("transform").Kind; got != sdkr.FlowNodePython {
		t.Fatalf("expected inferred kind=python, got %q", got)
	}

	if _, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "t", Kind: sdkr.FlowNodePython, Tool: "notebooklm.gen"}},
		Entry: "t",
	}); err != nil {
		t.Fatalf("python node referencing a tool should compile: %v", err)
	}

	if _, err := CompileFlow(sdkr.FlowSpec{
		Nodes: []sdkr.FlowNode{{ID: "empty", Kind: sdkr.FlowNodePython}},
		Entry: "empty",
	}); err == nil {
		t.Fatalf("python node with neither code nor tool should error")
	}
}
