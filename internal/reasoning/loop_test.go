package reasoning_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
)

// ─── Stub backends ───────────────────────────────────────────────────────────

// stubLLM is a controllable LLMBackend for testing.
type stubLLM struct {
	thinkCalls int
	doneOnStep int // Think returns IsDone=true on this call number
	planSteps  []reasoning.PlannedStep
	reflectOut string
	planSystem string
}

func (s *stubLLM) Think(_ context.Context, req reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	s.thinkCalls++
	if s.thinkCalls >= s.doneOnStep {
		return reasoning.ThinkResponse{IsDone: true, Thought: "done", FinalAnswer: "task complete"}, nil
	}
	return reasoning.ThinkResponse{
		Thought: "thinking",
		IsDone:  false,
		Action:  reasoning.ToolCall{Tool: "web_search", Input: map[string]string{"query": "test"}},
	}, nil
}

func (s *stubLLM) Plan(_ context.Context, systemPrompt, _ string, _ int) (reasoning.Plan, error) {
	s.planSystem = systemPrompt
	return reasoning.Plan{Goal: "test goal", Steps: s.planSteps}, nil
}

func (s *stubLLM) Reflect(_ context.Context, req reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	out := s.reflectOut
	if out == "" {
		out = "synthesised answer"
	}
	return reasoning.ReflectResponse{Output: out}, nil
}

// stubExecutor always succeeds with a canned observation.
type stubExecutor struct{}

func (s *stubExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	return reasoning.Observation{Content: "ok: " + call.Tool, Source: call.Tool}
}

// errorExecutor always returns an error (Scenario D).
type errorExecutor struct{}

func (e *errorExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	return reasoning.Observation{Error: errors.New("injected tool failure")}
}

type scriptedLLM struct {
	responses      []reasoning.ThinkResponse
	thinkCalls     int
	reflectOut     string
	reflectOutputs []string
	reflectCalls   int
}

func (s *scriptedLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	s.thinkCalls++
	if s.thinkCalls <= len(s.responses) {
		return s.responses[s.thinkCalls-1], nil
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "done"}, nil
}

func (s *scriptedLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (s *scriptedLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	s.reflectCalls++
	if s.reflectCalls <= len(s.reflectOutputs) {
		return reasoning.ReflectResponse{Output: s.reflectOutputs[s.reflectCalls-1]}, nil
	}
	out := s.reflectOut
	if out == "" {
		out = "done"
	}
	return reasoning.ReflectResponse{Output: out}, nil
}

type flakyThinkLLM struct {
	thinkCalls int
}

func (f *flakyThinkLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	f.thinkCalls++
	if f.thinkCalls == 1 {
		return reasoning.ThinkResponse{}, errors.New("invalid JSON reasoning response")
	}
	if f.thinkCalls == 2 {
		return reasoning.ThinkResponse{
			IsDone: false,
			Action: reasoning.ToolCall{
				Tool:  "fetch_url",
				Input: map[string]string{"url": "https://example.com"},
			},
		}, nil
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "fetched"}, nil
}

func (f *flakyThinkLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (f *flakyThinkLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "done"}, nil
}

type recordingExecutor struct {
	calls []reasoning.ToolCall
}

func (r *recordingExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	r.calls = append(r.calls, call)
	return reasoning.Observation{Content: "ok", Source: call.Tool}
}

type stockJSONExecutor struct {
	calls []reasoning.ToolCall
}

func (s *stockJSONExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	s.calls = append(s.calls, call)
	if _, ok := call.Arguments["symbol"]; ok {
		return reasoning.Observation{
			Content: `tool error: Error executing tool get_stock_info: ticker Field required [type=missing, input_value={"symbol":"V"}]`,
			Source:  call.Tool,
		}
	}
	switch call.Arguments["ticker"] {
	case "V":
		return reasoning.Observation{Source: call.Tool, Content: `{"symbol":"V","longName":"Visa Inc.","currentPrice":352.49,"marketCap":680000000000,"sector":"Financial Services","industry":"Credit Services","forwardPE":28.1,"targetMeanPrice":402.66,"recommendationKey":"buy"}`}
	case "MSFT":
		return reasoning.Observation{Source: call.Tool, Content: `{"symbol":"MSFT","longName":"Microsoft Corporation","currentPrice":505.75,"marketCap":3750000000000,"sector":"Technology","industry":"Software - Infrastructure","forwardPE":32.1,"targetMeanPrice":555.75,"recommendationKey":"buy"}`}
	default:
		return reasoning.Observation{Source: call.Tool, Content: `{"symbol":"WMT","longName":"Walmart Inc.","currentPrice":97.44,"marketCap":780000000000,"sector":"Consumer Defensive","recommendationKey":"buy"}`}
	}
}

type htmlExecutor struct{}

func (h *htmlExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	return reasoning.Observation{
		Source: call.Tool,
		Content: "URL: https://example.com\nStatus: 200\nContent-Type: text/html; charset=UTF-8\n\n" +
			`<!doctype html><html><head><title>Example Article</title><script>huge()</script></head><body><h1>Example Article</h1><p>Useful article text.</p></body></html>`,
	}
}

type pendingAsyncExecutor struct{}

func (p *pendingAsyncExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	if call.Tool == "mcp__notebooklm__studio_status" {
		return reasoning.Observation{
			Source:  call.Tool,
			Content: `{"status":"success","notebook_id":"nb_123","summary":{"total":1,"completed":0,"in_progress":1},"artifacts":[{"artifact_id":"audio_1","status":"in_progress","audio_url":null}]}`,
		}
	}
	return reasoning.Observation{Source: call.Tool, Content: `{"status":"success","artifact_id":"audio_1"}`}
}

type htmlHistoryLLM struct {
	thinkCalls int
	sawRawHTML bool
	sawText    bool
}

func (h *htmlHistoryLLM) Think(_ context.Context, req reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	h.thinkCalls++
	if h.thinkCalls == 1 {
		return reasoning.ThinkResponse{
			IsDone: false,
			Action: reasoning.ToolCall{
				Tool:  "fetch_url",
				Input: map[string]string{"url": "https://example.com"},
			},
		}, nil
	}
	if len(req.StepHistory) > 0 {
		obs := req.StepHistory[0].Obs.Content
		h.sawRawHTML = strings.Contains(strings.ToLower(obs), "<html") || strings.Contains(strings.ToLower(obs), "<script")
		h.sawText = strings.Contains(obs, "HTML fetched; readable text excerpt") && strings.Contains(obs, "Useful article text.")
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "done"}, nil
}

func (h *htmlHistoryLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (h *htmlHistoryLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "done"}, nil
}

type emptyReflectPlanner struct {
	planSteps []reasoning.PlannedStep
}

func (e *emptyReflectPlanner) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	return reasoning.ThinkResponse{}, errors.New("react fallback not expected")
}

func (e *emptyReflectPlanner) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{Goal: "fetch and publish", Steps: e.planSteps}, nil
}

func (e *emptyReflectPlanner) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{}, nil
}

type alwaysBadThinkLLM struct {
	thinkCalls int
}

func (a *alwaysBadThinkLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	a.thinkCalls++
	return reasoning.ThinkResponse{}, errors.New("unexpected end of JSON input")
}

func (a *alwaysBadThinkLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (a *alwaysBadThinkLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "should not need reflect"}, nil
}

type interleavedBadThinkLLM struct {
	thinkCalls   int
	reflectCalls int
}

func (i *interleavedBadThinkLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	i.thinkCalls++
	if i.thinkCalls == 1 {
		return reasoning.ThinkResponse{
			Thought: "get queue",
			IsDone:  false,
			Action:  reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}},
		}, nil
	}
	if i.thinkCalls == 5 || i.thinkCalls == 9 {
		return reasoning.ThinkResponse{
			Thought: "refresh",
			IsDone:  false,
			Action:  reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}},
		}, nil
	}
	return reasoning.ThinkResponse{}, errors.New("invalid reasoning JSON")
}

func (i *interleavedBadThinkLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (i *interleavedBadThinkLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	i.reflectCalls++
	return reasoning.ReflectResponse{Output: "Recovered from the last useful queue observation."}, nil
}

type interleavedBadThinkBadReflectLLM struct {
	thinkCalls   int
	reflectCalls int
}

func (i *interleavedBadThinkBadReflectLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	i.thinkCalls++
	if i.thinkCalls == 1 {
		return reasoning.ThinkResponse{
			Thought: "get queue",
			IsDone:  false,
			Action:  reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}},
		}, nil
	}
	return reasoning.ThinkResponse{}, errors.New("invalid reasoning JSON")
}

func (i *interleavedBadThinkBadReflectLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (i *interleavedBadThinkBadReflectLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	i.reflectCalls++
	return reasoning.ReflectResponse{Output: "I will continue processing the queue now."}, nil
}

type fallbackPlanLLM struct {
	thinkCalls int
}

func (f *fallbackPlanLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	f.thinkCalls++
	if f.thinkCalls == 1 {
		return reasoning.ThinkResponse{
			Thought: "use available tool",
			IsDone:  false,
			Action:  reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}},
		}, nil
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "done"}, nil
}

func (f *fallbackPlanLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{Goal: "bad plan", Steps: []reasoning.PlannedStep{
		{ID: "step-1", Description: "run a tool that is not installed", Tool: "missing_tool"},
	}}, nil
}

func (f *fallbackPlanLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "done"}, nil
}

// ─── Scenario A — ReAct loop ─────────────────────────────────────────────────

// Scenario A: stub LLM returns IsDone=true on step 2.
// Assert Result.Output non-empty, Steps has exactly 1 entry (the step before done),
// Result.Duration > 0.
func TestScenarioA_ReActLoop(t *testing.T) {
	llm := &stubLLM{doneOnStep: 2} // IsDone=true on second Think call
	exec := &stubExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  5 * time.Second,
		TotalTimeout: 30 * time.Second,
	}, llm, exec)

	result := loop.Run(context.Background(), "research-agent", "What is Soulacy?")

	if result.Output == "" {
		t.Error("expected non-empty Output")
	}
	// Step 1 executes (Think call 1 returns IsDone=false → tool runs).
	// Think call 2 returns IsDone=true → Reflect is called immediately with 1 step.
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(result.Steps))
	}
	if result.Duration <= 0 {
		t.Error("expected Duration > 0")
	}
}

func TestReActRecoversTextualToolCallFinalAnswer(t *testing.T) {
	llm := &scriptedLLM{responses: []reasoning.ThinkResponse{
		{
			IsDone:      true,
			Thought:     "queue the URL",
			FinalAnswer: `queue_put({"name":"pending_resources","item":{"id":"abc","type":"url","content":"https://example.com"}})`,
		},
		{IsDone: true, FinalAnswer: "queued"},
	}}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_put"},
	}, llm, exec)

	result := loop.Run(context.Background(), "research-librarian", "add this url https://example.com")

	if len(exec.calls) != 1 {
		t.Fatalf("expected recovered queue_put call, got %d", len(exec.calls))
	}
	if exec.calls[0].Tool != "queue_put" {
		t.Fatalf("tool = %q, want queue_put", exec.calls[0].Tool)
	}
	item, ok := exec.calls[0].Arguments["item"].(map[string]any)
	if !ok || item["content"] != "https://example.com" {
		t.Fatalf("nested item was not preserved: %#v", exec.calls[0].Arguments["item"])
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(result.Steps))
	}
}

func TestReActDoesNotStopOnProgressNoteFinalAnswer(t *testing.T) {
	llm := &scriptedLLM{responses: []reasoning.ThinkResponse{
		{IsDone: true, Thought: "auth complete", FinalAnswer: "Starting daily processing. Proceeding to Step 1: checking for pending resources."},
		{IsDone: false, Thought: "check queue", Action: reasoning.ToolCall{Tool: "queue_names"}},
		{IsDone: true, FinalAnswer: "No pending resources."},
	}}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_names"},
	}, llm, exec)

	result := loop.Run(context.Background(), "research-librarian", "__trigger:manual__")

	if len(exec.calls) != 1 || exec.calls[0].Tool != "queue_names" {
		t.Fatalf("expected loop to continue into queue_names, calls=%#v", exec.calls)
	}
	if len(result.Steps) < 2 {
		t.Fatalf("expected controller step plus tool step, got %d", len(result.Steps))
	}
}

func TestReActDoesNotStopOnProgressNoteReflectOutput(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{IsDone: false, Thought: "check queues", Action: reasoning.ToolCall{Tool: "queue_names"}},
			{IsDone: true, Thought: "queue exists", FinalAnswer: "ready to process"},
			{IsDone: false, Thought: "list queue", Action: reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"name": "pending_resources"}}},
			{IsDone: true, FinalAnswer: "processed"},
		},
		reflectOut: "Daily processing triggered. The pending_resources queue exists with 1 item. Proceeding to list and process pending items.",
	}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     5,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_names", "queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "research-librarian", "__trigger:manual__")

	if len(exec.calls) < 2 {
		t.Fatalf("expected loop to reject progress-note reflection and continue, calls=%#v", exec.calls)
	}
	if exec.calls[0].Tool != "queue_names" || exec.calls[1].Tool != "queue_list" {
		t.Fatalf("unexpected calls: %#v", exec.calls)
	}
	if len(result.Steps) < 3 {
		t.Fatalf("expected queue_names, controller note, and queue_list steps, got %d", len(result.Steps))
	}
}

func TestReActContinuesAfterThinkError(t *testing.T) {
	llm := &flakyThinkLLM{}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "fetch https://example.com")

	if len(exec.calls) != 1 || exec.calls[0].Tool != "fetch_url" {
		t.Fatalf("expected loop to continue after Think error and call fetch_url, calls=%#v", exec.calls)
	}
	if len(result.Steps) < 2 {
		t.Fatalf("expected controller note plus recovered tool step, got %d", len(result.Steps))
	}
}

func TestReActRecoversLegacyActionFromReflectOutput(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{IsDone: true, Thought: "need to fetch", FinalAnswer: "ready"},
			{IsDone: true, FinalAnswer: "done"},
		},
		reflectOutputs: []string{
			"I need to fetch the URL content first.\nAction: fetch_url(map[url:https://example.com/article])",
			"fetched",
		},
	}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "process queued resource")

	if len(exec.calls) != 1 {
		t.Fatalf("expected recovered legacy fetch_url call, got %d", len(exec.calls))
	}
	if exec.calls[0].Tool != "fetch_url" || exec.calls[0].Input["url"] != "https://example.com/article" {
		t.Fatalf("unexpected recovered call: %#v", exec.calls[0])
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 recovered tool step", len(result.Steps))
	}
}

func TestReActCompactsHTMLObservationBeforeNextThink(t *testing.T) {
	llm := &htmlHistoryLLM{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url"},
	}, llm, &htmlExecutor{})

	_ = loop.Run(context.Background(), "any-react-agent", "fetch https://example.com")

	if llm.sawRawHTML {
		t.Fatalf("expected raw HTML/script tags to be compacted before the next Think")
	}
	if !llm.sawText {
		t.Fatalf("expected readable HTML text excerpt in step history")
	}
}

func TestReActStopsAfterRepeatedThinkErrors(t *testing.T) {
	llm := &alwaysBadThinkLLM{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     30,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
	}, llm, &recordingExecutor{})

	result := loop.Run(context.Background(), "any-react-agent", "do work")

	if llm.thinkCalls != 4 {
		t.Fatalf("think calls = %d, want 4 before controller stop", llm.thinkCalls)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("steps = %d, want 4 controller error steps", len(result.Steps))
	}
	if !strings.Contains(result.Output, "invalid ReAct JSON too many times") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("repeated invalid Think output should mark the run not confident")
	}
}

func TestReActStopsAfterRepeatedMissingAction(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{Thought: "I should use a tool", IsDone: false},
			{Thought: "Still working", IsDone: false},
			{Thought: "Continuing", IsDone: false},
			{Thought: "Proceeding", IsDone: false},
			{IsDone: true, FinalAnswer: "should not get here"},
		},
	}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     30,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "process pending queue")

	if llm.thinkCalls != 4 {
		t.Fatalf("think calls = %d, want 4 before controller stop", llm.thinkCalls)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("empty tool action should not dispatch, got calls=%+v", exec.calls)
	}
	if !strings.Contains(result.Output, "without a usable action.tool") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(result.Steps) == 0 {
		t.Fatalf("expected controller repair steps")
	}
	repairHint := result.Steps[0].Obs.Content
	for _, want := range []string{
		`"is_done":false`,
		`"action":{"tool":"TOOL_NAME","arguments":{}}`,
		"queue_list",
		"Do not include Markdown",
	} {
		if !strings.Contains(repairHint, want) {
			t.Fatalf("controller repair hint missing %q: %s", want, repairHint)
		}
	}
	if result.Confident {
		t.Fatalf("missing action controller errors should mark the run not confident")
	}
}

func TestReActFallsBackToUsefulObservationAfterRepeatedMissingAction(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{Thought: "check queue", IsDone: false, Action: reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}}},
			// Three missing actions: the first now buys a corrected turn (the
			// repair instruction gets one chance), the second triggers the
			// salvage Reflect, the third gives up to the synthesized fallback.
			{Thought: "I should continue", IsDone: false},
			{Thought: "Still continuing", IsDone: false},
			{Thought: "Still continuing", IsDone: false},
			{IsDone: true, FinalAnswer: "should not get here"},
		},
		reflectOutputs: []string{
			"I will continue processing the queue now.",
			"I will continue processing the queue now.",
		},
	}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     30,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "process pending queue")

	if llm.thinkCalls != 4 {
		t.Fatalf("think calls = %d, want one corrected turn before salvage, then stop", llm.thinkCalls)
	}
	if llm.reflectCalls != 2 {
		t.Fatalf("reflect calls = %d, want one failed recovery reflection per missing action past the threshold", llm.reflectCalls)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("tool calls = %d, want the initial valid call only", len(exec.calls))
	}
	if !strings.Contains(result.Output, "without a usable action.tool") || !strings.Contains(result.Output, "best available result") {
		t.Fatalf("unexpected fallback output: %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("invalid action fallback should mark the run not confident")
	}
}

func TestReActReflectsAfterRepeatedThinkErrorsWithUsefulObservation(t *testing.T) {
	llm := &interleavedBadThinkLLM{}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     30,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "process pending queue")

	// Two consecutive bad Think responses are now required before salvage: the
	// first appends a repair instruction and buys one corrected turn.
	if llm.thinkCalls != 3 {
		t.Fatalf("think calls = %d, want 3 before reflective recovery", llm.thinkCalls)
	}
	if llm.reflectCalls != 1 {
		t.Fatalf("reflect calls = %d, want 1 recovery reflection", llm.reflectCalls)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("tool calls = %d, want the initial valid call only", len(exec.calls))
	}
	if !strings.Contains(result.Output, "Recovered from the last useful queue observation") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("reflective recovery after invalid Think output should mark the run not confident")
	}
}

func TestReActFallsBackToUsefulObservationWhenThinkAndReflectAreInvalid(t *testing.T) {
	llm := &interleavedBadThinkBadReflectLLM{}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     30,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "process pending queue")

	if llm.thinkCalls != 4 {
		t.Fatalf("think calls = %d, want one corrected turn, then two salvage attempts, then stop", llm.thinkCalls)
	}
	if llm.reflectCalls != 2 {
		t.Fatalf("reflect calls = %d, want one failed recovery reflection per bad Think past the threshold", llm.reflectCalls)
	}
	if !strings.Contains(result.Output, "best available result") || !strings.Contains(result.Output, "ok") {
		t.Fatalf("unexpected fallback output: %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("invalid Think fallback should mark the run not confident")
	}
}

func TestReActFallbackPrefersSuccessfulStructuredObservationsOverEarlierToolError(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{Thought: "wrong arg", IsDone: false, Action: reasoning.ToolCall{Tool: "mcp__yahoo-finance__get_stock_info", Arguments: map[string]any{"symbol": "V"}}},
			{Thought: "retry visa", IsDone: false, Action: reasoning.ToolCall{Tool: "mcp__yahoo-finance__get_stock_info", Arguments: map[string]any{"ticker": "V"}}},
			{Thought: "get microsoft", IsDone: false, Action: reasoning.ToolCall{Tool: "mcp__yahoo-finance__get_stock_info", Arguments: map[string]any{"ticker": "MSFT"}}},
			// Three malformed steps: one corrected turn, then two salvage
			// attempts, then the synthesized fallback.
			{},
			{},
			{},
		},
		reflectOutputs: []string{
			"I will now summarize the successful stock data.",
			"I will now summarize the successful stock data.",
		},
	}
	exec := &stockJSONExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     10,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"mcp__yahoo-finance__get_stock_info"},
	}, llm, exec)

	result := loop.Run(context.Background(), "stock-advisor", "best stocks")

	if !strings.Contains(result.Output, "Visa Inc.") || !strings.Contains(result.Output, "Microsoft Corporation") {
		t.Fatalf("fallback did not include successful structured observations: %q", result.Output)
	}
	if strings.Contains(result.Output, "ticker Field required") || strings.Contains(result.Output, "tool error:") {
		t.Fatalf("fallback should not prefer earlier tool error over later success: %q", result.Output)
	}
	if strings.Contains(result.Output, `{"symbol"`) {
		t.Fatalf("fallback should compact structured JSON instead of dumping it: %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("invalid Think fallback should mark the run not confident")
	}
}

func TestReActStopsRepeatingIdenticalFailedToolCall(t *testing.T) {
	call := reasoning.ToolCall{Tool: "channel.send", Arguments: map[string]any{"channel": "telegram"}}
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{Thought: "send acknowledgement", IsDone: false, Action: call},
			{Thought: "retry send", IsDone: false, Action: call},
			{Thought: "retry send again", IsDone: false, Action: call},
			{IsDone: true, FinalAnswer: "should not get here"},
		},
		reflectOut: "Telegram send failed repeatedly because the destination was missing.",
	}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     10,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"channel.send"},
	}, llm, &errorExecutor{})

	result := loop.Run(context.Background(), "any-react-agent", "send a message")

	if llm.thinkCalls != 3 {
		t.Fatalf("think calls = %d, want stop after third identical failed tool call", llm.thinkCalls)
	}
	if llm.reflectCalls != 1 {
		t.Fatalf("reflect calls = %d, want one recovery reflection", llm.reflectCalls)
	}
	if !strings.Contains(result.Output, "destination was missing") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	controllerSteps := 0
	for _, step := range result.Steps {
		if step.Obs.Source == "controller" && strings.Contains(step.Obs.Content, "exact same") {
			controllerSteps++
		}
	}
	if controllerSteps != 2 {
		t.Fatalf("controller repeated-failure steps = %d, want 2", controllerSteps)
	}
	if result.Confident {
		t.Fatalf("repeated failed tool calls should mark the run not confident")
	}
}

// ─── Scenario B — Plan-Execute ───────────────────────────────────────────────

// Scenario B: 3-step plan; assert Steps has 3 entries, dependency ordering respected.
func TestScenarioB_PlanExecute(t *testing.T) {
	planSteps := []reasoning.PlannedStep{
		{ID: "step-1", Description: "gather data", Tool: "web_search", DependsOn: []string{}},
		{ID: "step-2", Description: "analyse data", Tool: "web_search", DependsOn: []string{"step-1"}},
		{ID: "step-3", Description: "write report", Tool: "web_search", DependsOn: []string{"step-2"}},
	}

	llm := &stubLLM{planSteps: planSteps}
	exec := &stubExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 6,
		StepTimeout:  5 * time.Second,
		TotalTimeout: 30 * time.Second,
	}, llm, exec)

	result := loop.Run(context.Background(), "decision-agent", "Produce a research report")

	if len(result.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(result.Steps))
	}

	// Verify step IDs match the plan.
	ids := map[string]int{}
	for i, s := range result.Steps {
		ids[s.ID] = i
	}
	if ids["step-1"] >= ids["step-2"] {
		t.Error("step-1 must complete before step-2")
	}
	if ids["step-2"] >= ids["step-3"] {
		t.Error("step-2 must complete before step-3")
	}
}

func TestPlanExecuteFallsBackToReActForUnavailablePlanTool(t *testing.T) {
	llm := &fallbackPlanLLM{}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxSteps:     3,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-agent", "process queue")

	if len(exec.calls) != 1 || exec.calls[0].Tool != "queue_list" {
		t.Fatalf("expected fallback ReAct queue_list call, calls=%#v", exec.calls)
	}
	// The downgrade is now RECORDED (it used to be silent, so a trace claimed
	// plan_execute while ReAct actually ran): one planner step naming the cause,
	// then the ReAct step itself.
	if len(result.Steps) != 2 {
		t.Fatalf("steps = %d, want a downgrade record plus one ReAct step", len(result.Steps))
	}
	if result.Steps[0].Obs.Source != "planner" {
		t.Fatalf("first step should be the downgrade record, got source %q", result.Steps[0].Obs.Source)
	}
	if !result.Confident {
		t.Error("a recorded downgrade must not by itself mark the run degraded")
	}
}

func TestPlanExecutePassesStructuredArguments(t *testing.T) {
	llm := &stubLLM{planSteps: []reasoning.PlannedStep{
		{
			ID:          "send",
			Description: "send the acknowledgement",
			Tool:        "channel.send",
			Arguments: map[string]any{
				"channel": "telegram",
				"to":      "12345",
				"text":    "Queued.",
			},
		},
	}}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"channel.send"},
	}, llm, exec)

	result := loop.Run(context.Background(), "planner", "notify the user")

	if len(exec.calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(exec.calls))
	}
	call := exec.calls[0]
	if call.Tool != "channel.send" || call.Input["channel"] != "telegram" || call.Input["to"] != "12345" || call.Input["text"] != "Queued." {
		t.Fatalf("structured arguments not passed through: %#v", call)
	}
	if call.Arguments["text"] != "Queued." {
		t.Fatalf("full arguments not preserved: %#v", call.Arguments)
	}
	if !strings.Contains(llm.planSystem, "Available tools: channel.send") {
		t.Fatalf("planner prompt did not include available tools: %q", llm.planSystem)
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Fatal("empty result")
	}
}

func TestPlanExecuteCanonicalizesCommonToolAliases(t *testing.T) {
	llm := &stubLLM{planSteps: []reasoning.PlannedStep{
		{
			ID:          "capture",
			Description: "queue the resource",
			Tool:        "enqueue",
			Arguments: map[string]any{
				"queue": "pending_resources",
				"item":  map[string]any{"url": "https://example.com"},
			},
		},
		{
			ID:          "notify",
			Description: "send acknowledgement",
			Tool:        "send_message",
			DependsOn:   []string{"capture"},
			Arguments: map[string]any{
				"channel": "slack",
				"to":      "C123",
				"text":    "Queued.",
			},
		},
	}}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_put", "channel.send"},
	}, llm, exec)

	_ = loop.Run(context.Background(), "planner", "queue and notify")

	if len(exec.calls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(exec.calls))
	}
	if exec.calls[0].Tool != "queue_put" || exec.calls[1].Tool != "channel.send" {
		t.Fatalf("tool aliases not canonicalized: %#v", exec.calls)
	}
}

func TestReActRecoveredTextualToolCallCanonicalizesAlias(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{IsDone: true, FinalAnswer: `send_message({"channel":"telegram","to":"123","text":"ok"})`},
			{IsDone: true, FinalAnswer: "done"},
		},
		reflectOut: "done",
	}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"channel.send"},
	}, llm, exec)

	_ = loop.Run(context.Background(), "react-agent", "notify")

	if len(exec.calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(exec.calls))
	}
	if exec.calls[0].Tool != "channel.send" {
		t.Fatalf("recovered alias not canonicalized: %#v", exec.calls[0])
	}
}

func TestPlanExecuteResolvesPriorStepPlaceholders(t *testing.T) {
	llm := &stubLLM{planSteps: []reasoning.PlannedStep{
		{
			ID:          "fetch",
			Description: "fetch source",
			Tool:        "fetch_url",
			Arguments:   map[string]any{"url": "https://example.com"},
		},
		{
			ID:          "summarize",
			Description: "summarize source",
			Tool:        "summarize",
			DependsOn:   []string{"fetch"},
			Arguments: map[string]any{
				"content": "{{fetch.output}}",
				"meta":    map[string]any{"source": "{{fetch.content}}"},
			},
		},
	}}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url", "summarize"},
	}, llm, exec)

	_ = loop.Run(context.Background(), "planner", "fetch and summarize")

	if len(exec.calls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(exec.calls))
	}
	summarize := exec.calls[1]
	if summarize.Tool != "summarize" || summarize.Input["content"] != "ok" {
		t.Fatalf("placeholder not resolved in input: %#v", summarize)
	}
	meta, ok := summarize.Arguments["meta"].(map[string]any)
	if !ok || meta["source"] != "ok" {
		t.Fatalf("nested placeholder not resolved: %#v", summarize.Arguments)
	}
}

func TestPlanExecuteDoesNotCompleteFailedDependencies(t *testing.T) {
	planSteps := []reasoning.PlannedStep{
		{ID: "fetch", Description: "fetch source data", Tool: "fetch_url"},
		{ID: "summarize", Description: "summarize fetched data", Tool: "agent_summarize", DependsOn: []string{"fetch"}},
	}
	llm := &stubLLM{planSteps: planSteps, reflectOut: "I will now summarize the fetched data."}
	exec := &errorExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
	}, llm, exec)

	result := loop.Run(context.Background(), "planner", "fetch and summarize")

	if len(result.Steps) < 2 {
		t.Fatalf("steps = %d, want failed step plus skipped dependent step", len(result.Steps))
	}
	if result.Steps[0].Obs.Error == nil {
		t.Fatalf("first step should record tool failure: %#v", result.Steps[0])
	}
	if got := result.Steps[1].Obs.Content; !strings.Contains(got, "skipped: dependency") {
		t.Fatalf("dependent step should be skipped after failed prerequisite, got %q", got)
	}
	if strings.Contains(strings.ToLower(result.Output), "i will now") {
		t.Fatalf("plan-execute returned progress prose instead of fallback: %q", result.Output)
	}
	if !strings.Contains(result.Output, "failed") || !strings.Contains(result.Output, "skipped") {
		t.Fatalf("fallback output should explain failed/skipped plan, got %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("failed plan-execute run should not be confident")
	}
}

func TestPlanExecuteDoesNotPublishRawObservationWhenFinalReflectFails(t *testing.T) {
	planSteps := []reasoning.PlannedStep{
		{ID: "fetch", Description: "fetch source data", Tool: "fetch_url"},
	}
	llm := &emptyReflectPlanner{planSteps: planSteps}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url"},
	}, llm, &htmlExecutor{})

	result := loop.Run(context.Background(), "podcast-agent", "fetch content and create a podcast")

	if strings.Contains(result.Output, "HTML fetched") || strings.Contains(result.Output, "URL: https://example.com") {
		t.Fatalf("fallback should not expose raw fetch_url output: %q", result.Output)
	}
	if !strings.Contains(result.Output, "did not produce the required final deliverable") {
		t.Fatalf("fallback should explain incomplete deliverable, got %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("empty final reflection should mark plan-execute run not confident")
	}
}

func TestPlanExecuteDoesNotPublishPendingAsyncStatus(t *testing.T) {
	pending := `{"status":"success","notebook_id":"nb_123","summary":{"total":1,"completed":0,"in_progress":1},"artifacts":[{"artifact_id":"audio_1","status":"in_progress","audio_url":null}]}`
	planSteps := []reasoning.PlannedStep{
		{ID: "audio", Description: "create NotebookLM audio", Tool: "mcp__notebooklm__studio_create"},
		{ID: "poll", Description: "poll until audio is ready", Tool: "mcp__notebooklm__studio_status", DependsOn: []string{"audio"}},
	}
	llm := &stubLLM{planSteps: planSteps, reflectOut: pending}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"mcp__notebooklm__studio_create", "mcp__notebooklm__studio_status"},
	}, llm, &pendingAsyncExecutor{})

	result := loop.Run(context.Background(), "podcast-agent", "create a NotebookLM podcast")

	if strings.HasPrefix(strings.TrimSpace(result.Output), "{") || strings.Contains(result.Output, `"in_progress"`) {
		t.Fatalf("pending async JSON leaked into final output: %q", result.Output)
	}
	if !strings.Contains(strings.ToLower(result.Output), "still processing") {
		t.Fatalf("expected pending async fallback, got %q", result.Output)
	}
	if result.Confident {
		t.Fatalf("pending async finalization should mark plan-execute run not confident")
	}
}

// ─── Scenario D — Tool failure resilience ────────────────────────────────────

// Scenario D: errorExecutor always errors; loop must not panic, failed tool
// calls are recorded, and repeated identical failures trigger controller
// recovery instead of burning the whole step budget.
func TestScenarioD_ToolFailureResilience(t *testing.T) {
	llm := &stubLLM{doneOnStep: 99} // never done on its own → exhaust MaxSteps
	exec := &errorExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     3,
		StepTimeout:  5 * time.Second,
		TotalTimeout: 30 * time.Second,
	}, llm, exec)

	// Must not panic.
	result := loop.Run(context.Background(), "research-agent", "failing task")

	if len(result.Steps) != 5 {
		t.Errorf("expected 3 tool failures plus 2 controller recovery steps, got %d", len(result.Steps))
	}
	toolErrorSteps := 0
	controllerSteps := 0
	for i, s := range result.Steps {
		if s.Obs.Source == "controller" {
			controllerSteps++
			continue
		}
		if s.Obs.Error == nil && s.Obs.Content == "" {
			t.Errorf("step %d: expected error observation, got empty", i)
		}
		toolErrorSteps++
	}
	if toolErrorSteps != 3 {
		t.Errorf("expected 3 failed tool observations, got %d", toolErrorSteps)
	}
	if controllerSteps != 2 {
		t.Errorf("expected 2 controller recovery observations, got %d", controllerSteps)
	}

	// Result must still have output from Reflect() (graceful degradation).
	if result.Output == "" {
		t.Error("expected non-empty Output even after all tool failures")
	}

	// Confident must be false when tools errored.
	if result.Confident {
		t.Error("expected Confident=false when all tools errored")
	}
}

// ─── Misc unit tests ─────────────────────────────────────────────────────────

// TestLoop_AvailableToolNames: LoopConfig.ToolNames is exposed via AvailableToolNames().
func TestLoop_AvailableToolNames(t *testing.T) {
	llm := &stubLLM{doneOnStep: 1}
	exec := &stubExecutor{}
	tools := []string{"web_search", "memory_read", "memory_write"}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:  reasoning.StrategyReAct,
		ToolNames: tools,
	}, llm, exec)

	got := loop.AvailableToolNames()
	if len(got) != len(tools) {
		t.Errorf("expected %d tools, got %d", len(tools), len(got))
	}
}

// TestLoop_TotalTimeoutRespected: a very short TotalTimeout must cause the loop
// to return before MaxSteps is exhausted.
func TestLoop_TotalTimeoutRespected(t *testing.T) {
	llm := &stubLLM{doneOnStep: 999} // never done — relies on timeout
	exec := &stubExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     100,
		StepTimeout:  500 * time.Millisecond,
		TotalTimeout: 50 * time.Millisecond, // very short
	}, llm, exec)

	start := time.Now()
	result := loop.Run(context.Background(), "test-agent", "long running task")
	elapsed := time.Since(start)

	// Should complete well within 1 second despite MaxSteps=100.
	if elapsed > 2*time.Second {
		t.Errorf("expected loop to respect TotalTimeout, took %v", elapsed)
	}
	// Result is still returned (graceful degradation).
	if result.Duration <= 0 {
		t.Error("expected Duration > 0")
	}
}
