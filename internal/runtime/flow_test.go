package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"go.uber.org/zap"
)

// Story E25: graph-mode workflows on the existing executor + checkpoints.

func TestWorkflowExecutor_GraphMode_BoundedCycle(t *testing.T) {
	store := newTestCheckpointStore(t)
	e := &Engine{}
	judgeCalls := 0
	var path []string
	e.builtins = []BuiltinTool{
		{
			Name: "improve",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				path = append(path, "improve")
				return `"draft"`, nil
			},
		},
		{
			Name: "evaluate",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				path = append(path, "evaluate")
				judgeCalls++
				if judgeCalls >= 3 {
					return `{"ok": true}`, nil
				}
				return `{"ok": false}`, nil
			},
		},
		{
			Name: "publish",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				path = append(path, "publish")
				return `"published"`, nil
			},
		},
	}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "refine", Tool: "improve"},
			{ID: "judge", Tool: "evaluate", Output: "verdict"},
			{ID: "ship", Tool: "publish"},
		},
		Edges: []sdkr.FlowEdge{
			{From: "refine", To: "judge", MaxIterations: 10},
			{From: "judge", To: "refine", If: "{{not .verdict.ok}}", MaxIterations: 5},
			{From: "judge", To: "ship"},
		},
	}, e, store, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("go"), "flow-run-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rawJSONString(t, out); got != "published" {
		t.Fatalf("output = %q", got)
	}
	// refine judge ×3 (verdict ok on 3rd), then ship.
	want := "improve evaluate improve evaluate improve evaluate publish"
	if got := joinPath(path); got != want {
		t.Fatalf("path = %q\nwant   %q", got, want)
	}
	// Visit-indexed checkpoints persisted per cycle iteration.
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-1", "judge#1", CheckpointCompleted)
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-1", "judge#3", CheckpointCompleted)
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-1", "ship#1", CheckpointCompleted)
}

func TestWorkflowExecutor_LLMNodeExtractsJSONForTool(t *testing.T) {
	store := newTestCheckpointStore(t)
	router := llm.NewRouter("test")
	provider := &fakeHandleProvider{
		responses: []llm.CompletionResponse{{Content: "```json\n{\"location_query\":\"Mexico City, Mexico\",\"intent\":\"current_weather\"}\n```"}},
	}
	router.Register(provider)
	var gotQuery string
	e := &Engine{llmRouter: router}
	e.builtins = []BuiltinTool{{
		Name: "resolve_location",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			gotQuery, _ = args["query"].(string)
			return `{"ok":true}`, nil
		},
	}}
	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{
			{
				ID:     "extract",
				Kind:   sdkr.FlowNodeLLM,
				Input:  "{{ .trigger.text }}",
				Output: "intent",
				Params: map[string]any{
					"system":          "Extract weather intent. Return JSON.",
					"response_format": "json",
				},
				Outputs: []sdkr.FlowPort{{Name: "location_query"}},
			},
			{
				ID:     "resolve",
				Kind:   sdkr.FlowNodeTool,
				Tool:   "resolve_location",
				Output: "location",
				Inputs: []sdkr.FlowPort{{Name: "query", Field: "query"}},
			},
		},
		Edges: []sdkr.FlowEdge{{From: "extract", FromPort: "location_query", To: "resolve", ToPort: "query"}},
		Entry: "extract",
	}, e, store, zap.NewNop())

	_, err := w.Run(context.Background(), workflowTestMessage("What's the current weather in Mexico City, Mexico"), "flow-llm")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotQuery != "Mexico City, Mexico" {
		t.Fatalf("query = %q, want Mexico City, Mexico", gotQuery)
	}
	reqs := provider.requestsSnapshot()
	if len(reqs) != 1 || reqs[0].ResponseFormat != "json" {
		t.Fatalf("llm requests = %+v, want one JSON-mode request", reqs)
	}
}

func TestWorkflowExecutor_RuntimeHealsTemplateShapeAndContinues(t *testing.T) {
	store := newTestCheckpointStore(t)
	router := llm.NewRouter("test")
	provider := &fakeHandleProvider{
		responses: []llm.CompletionResponse{{Content: `{"source_type":"text","text":"recovered source pack"}`}},
	}
	router.Register(provider)
	var addedText string
	e := &Engine{llmRouter: router, adaptiveNodes: true}
	e.builtins = []BuiltinTool{
		{
			Name: "produce_pack",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				return `{"items":[{"title":"AI article","url":"https://example.com/ai"}]}`, nil
			},
		},
		{
			Name: "add_source",
			Parameters: map[string]any{
				"type":     "object",
				"required": []any{"source_type", "text"},
				"properties": map[string]any{
					"source_type": map[string]any{"type": "string"},
					"text":        map[string]any{"type": "string"},
				},
			},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				addedText, _ = args["text"].(string)
				return `{"ok":true}`, nil
			},
		},
	}
	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "curate", Kind: sdkr.FlowNodeTool, Tool: "produce_pack", Output: "source_pack"},
			{
				ID:       "add",
				Kind:     sdkr.FlowNodeTool,
				Tool:     "add_source",
				Input:    `{"source_type":"text","text":{{toJson .source_pack.text}}}`,
				Adaptive: true,
			},
		},
		Edges: []sdkr.FlowEdge{{From: "curate", To: "add"}},
	}, e, store, zap.NewNop())

	if _, err := w.Run(context.Background(), workflowTestMessage("go"), "flow-heal-template"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if addedText != "recovered source pack" {
		t.Fatalf("add_source text = %q", addedText)
	}
	reqs := provider.requestsSnapshot()
	if len(reqs) != 1 || reqs[0].ResponseFormat != "json" {
		t.Fatalf("repair requests = %+v, want one JSON request", reqs)
	}
	if !strings.Contains(reqs[0].Messages[1].Content, `"items"`) ||
		!strings.Contains(reqs[0].Messages[1].Content, "Target tool JSON schema") {
		t.Fatalf("repair prompt missing live shape/schema:\n%s", reqs[0].Messages[1].Content)
	}
}

func TestWorkflowExecutor_RuntimeRepairsToolArgumentsAndRetriesRealTool(t *testing.T) {
	store := newTestCheckpointStore(t)
	router := llm.NewRouter("test")
	provider := &fakeHandleProvider{
		responses: []llm.CompletionResponse{{Content: `{"ticker":"SNDK"}`}},
	}
	router.Register(provider)
	calls := 0
	e := &Engine{llmRouter: router, adaptiveNodes: true}
	e.builtins = []BuiltinTool{{
		Name: "stock_info",
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"ticker"},
			"properties": map[string]any{
				"ticker": map[string]any{"type": "string"},
			},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			calls++
			if args["ticker"] == nil {
				return "", errors.New("validation error: field required: ticker")
			}
			return `{"ticker":"SNDK","price":42}`, nil
		},
	}}
	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{{
			ID:       "quote",
			Kind:     sdkr.FlowNodeTool,
			Tool:     "stock_info",
			Input:    `{"symbol":"SNDK"}`,
			Adaptive: true,
		}},
	}, e, store, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("quote SNDK"), "flow-heal-args")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 2 {
		t.Fatalf("tool calls = %d, want initial call plus one repaired retry", calls)
	}
	if !strings.Contains(string(out), `"ticker":"SNDK"`) {
		t.Fatalf("output = %s", out)
	}
}

func TestRuntimeHealingNeverRetriesSecurityOrTransportFailures(t *testing.T) {
	for _, reason := range []string{
		"consent grant is missing",
		"permission denied",
		"401 unauthorized",
		"403 forbidden",
		"connection refused",
		"context deadline exceeded",
	} {
		if isRepairableToolArgumentFailure(reason) {
			t.Errorf("%q must not be runtime-healable", reason)
		}
	}
	if !isRepairableToolArgumentFailure("validation error: field required: ticker") {
		t.Fatal("expected schema validation failure to be runtime-healable")
	}
}

// TestWorkflowExecutor_TypedPortHandoffAndTrace is the Phase 1 end-to-end check
// through the real executor: a NotebookLM-shaped flow hands a created notebook's
// id to a later tool via a TYPED PORT WIRE (no {{ }} template), and the per-block
// run trace records what each block received, with the handoff marked wired.
func TestWorkflowExecutor_TypedPortHandoffAndTrace(t *testing.T) {
	store := newTestCheckpointStore(t)
	e := &Engine{}
	var genInput string
	e.builtins = []BuiltinTool{
		{
			Name: "create_nb",
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				return `{"id":"nb-1","title":"AI news"}`, nil
			},
		},
		{
			Name: "gen_audio",
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				// Capture the assembled args to prove the wired id arrived without
				// any template.
				if v, ok := args["notebook_id"].(string); ok {
					genInput = v
				}
				return `{"audio_url":"https://x/a.mp3"}`, nil
			},
		},
	}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{
			{ID: "create", Tool: "create_nb", Output: "notebook",
				Outputs: []sdkr.FlowPort{{Name: "id"}}},
			{ID: "gen", Tool: "gen_audio", Input: `{"action":"generate"}`, Output: "audio",
				Inputs: []sdkr.FlowPort{{Name: "notebook_id"}}},
		},
		Edges: []sdkr.FlowEdge{
			{From: "create", To: "gen", FromPort: "id", ToPort: "notebook_id"},
			{From: "gen", To: "end"},
		},
		Entry: "create",
	}, e, store, zap.NewNop())

	if _, err := w.Run(context.Background(), workflowTestMessage("go"), "trace-run-1"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The wired notebook id reached the second tool's argument — no template.
	if genInput != "nb-1" {
		t.Fatalf("gen received notebook_id %q, want %q (typed-port handoff)", genInput, "nb-1")
	}

	// The run trace recorded both blocks; the handoff block is marked wired and
	// its input carries the id and the static constant.
	tr, ok := e.FlowTrace("trace-run-1")
	if !ok {
		t.Fatal("no flow trace recorded")
	}
	if len(tr.Entries) != 2 {
		t.Fatalf("trace entries = %d, want 2: %+v", len(tr.Entries), tr.Entries)
	}
	gen := tr.Entries[1]
	if gen.NodeID != "gen" || !gen.WiredPorts {
		t.Errorf("gen entry = %+v, want NodeID=gen WiredPorts=true", gen)
	}
	if !strings.Contains(gen.Input, `"notebook_id":"nb-1"`) || !strings.Contains(gen.Input, `"action":"generate"`) {
		t.Errorf("gen input = %q, want wired id + static constant", gen.Input)
	}

	// LatestFlowTrace resolves the same run for the agent.
	if lt, ok := e.LatestFlowTrace("workflow-agent"); !ok || lt.RunID != "trace-run-1" {
		t.Errorf("LatestFlowTrace = %+v ok=%v, want run trace-run-1", lt, ok)
	}
}

func TestWorkflowExecutor_GraphMode_EmitsNodeStartedEvent(t *testing.T) {
	store := newTestCheckpointStore(t)
	sink := &captureSink6{}
	e := &Engine{sink: sink}
	e.builtins = []BuiltinTool{{
		Name: "slow_store",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return `{"ok":true}`, nil
		},
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{{ID: "store", Kind: sdkr.FlowNodeTool, Tool: "slow_store"}},
		Edges: []sdkr.FlowEdge{{From: "store", To: "end"}},
		Entry: "store",
	}, e, store, zap.NewNop())

	if _, err := w.Run(context.Background(), workflowTestMessage("go"), "started-run-1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var sawStarted, sawCompleted bool
	for _, ev := range sink.events {
		if ev.Type == "flow.node.started" {
			sawStarted = true
		}
		if ev.Type == "flow.node" {
			sawCompleted = true
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("events: sawStarted=%v sawCompleted=%v all=%+v", sawStarted, sawCompleted, sink.events)
	}
}

func TestWorkflowExecutor_GraphMode_TrimsLargeTracePayloads(t *testing.T) {
	store := newTestCheckpointStore(t)
	sink := &captureSink6{}
	large := strings.Repeat("x", flowTraceFieldMaxBytes+4096)
	e := &Engine{sink: sink}
	e.builtins = []BuiltinTool{{
		Name: "large_fetch",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return large, nil
		},
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{{ID: "fetch", Kind: sdkr.FlowNodeTool, Tool: "large_fetch"}},
		Edges: []sdkr.FlowEdge{{From: "fetch", To: "end"}},
		Entry: "fetch",
	}, e, store, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("go"), "large-trace-run-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rawJSONString(t, out); len(got) != len(large) {
		t.Fatalf("runtime output length = %d, want full %d", len(got), len(large))
	}

	trace, ok := e.FlowTrace("large-trace-run-1")
	if !ok || len(trace.Entries) != 1 {
		t.Fatalf("trace = %+v ok=%v", trace, ok)
	}
	var shown string
	if err := json.Unmarshal(trace.Entries[0].Output, &shown); err != nil {
		t.Fatalf("trace output is not a JSON string: %v", err)
	}
	if len(shown) >= len(large) || !strings.Contains(shown, "[truncated:") {
		t.Fatalf("trace output was not trimmed, len=%d", len(shown))
	}
}

func TestWorkflowExecutor_GraphMode_ResumeSkipsCompletedVisits(t *testing.T) {
	store := newTestCheckpointStore(t)

	build := func(failSecond bool, calls *[]string) *WorkflowExecutor {
		e := &Engine{}
		e.builtins = []BuiltinTool{
			{
				Name: "stage1",
				Handler: func(_ context.Context, _ map[string]any) (string, error) {
					*calls = append(*calls, "stage1")
					return `{"v":"ONE"}`, nil
				},
			},
			{
				Name: "stage2",
				Handler: func(_ context.Context, args map[string]any) (string, error) {
					*calls = append(*calls, "stage2")
					if failSecond {
						return "", errors.New("crash")
					}
					return `"` + args["carry"].(string) + `-TWO"`, nil
				},
			},
		}
		return NewWorkflowExecutor(agent.WorkflowSpec{
			Nodes: []sdkr.FlowNode{
				{ID: "one", Tool: "stage1", Output: "r1"},
				{ID: "two", Tool: "stage2", Input: `{"carry":"{{.r1.v}}"}`},
			},
			Edges: []sdkr.FlowEdge{{From: "one", To: "two"}},
		}, e, store, zap.NewNop())
	}

	var calls1 []string
	if _, err := build(true, &calls1).Run(context.Background(), workflowTestMessage("x"), "flow-run-2"); err == nil {
		t.Fatal("first run should fail at node two")
	}
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-2", "one#1", CheckpointCompleted)
	assertCheckpointStatus(t, store, "workflow-agent", "flow-run-2", "two#1", CheckpointFailed)

	var calls2 []string
	out, err := build(false, &calls2).Run(context.Background(), workflowTestMessage("x"), "flow-run-2")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// stage1 must NOT re-run; stage2 gets the RESTORED r1 var.
	if got := joinPath(calls2); got != "stage2" {
		t.Fatalf("resume calls = %q, want only stage2", got)
	}
	if got := rawJSONString(t, out); got != "ONE-TWO" {
		t.Fatalf("output = %q, want ONE-TWO (proves restored vars)", got)
	}
}

func TestWorkflowExecutor_GraphMode_InvalidGraphRefused(t *testing.T) {
	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Nodes: []sdkr.FlowNode{{ID: "a", Tool: "t"}},
		Edges: []sdkr.FlowEdge{{From: "a", To: "ghost"}},
	}, &Engine{}, newTestCheckpointStore(t), zap.NewNop())
	if _, err := w.Run(context.Background(), workflowTestMessage("x"), ""); err == nil {
		t.Fatal("expected compile error for edge to unknown node")
	}
}

func joinPath(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
