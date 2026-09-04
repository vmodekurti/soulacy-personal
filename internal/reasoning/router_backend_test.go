package reasoning

import (
	"context"
	"strings"
	"testing"
)

// fakeCompleter returns a canned reply and records the last call, standing in
// for the gateway's router so RouterBackend can be tested without a real model.
type fakeCompleter struct {
	reply      string
	err        error
	lastModel  string
	lastSys    string
	lastUser   string
	lastParams PhaseParams
}

func (f *fakeCompleter) Complete(ctx context.Context, model, system, user string, params PhaseParams) (string, error) {
	f.lastModel, f.lastSys, f.lastUser = model, system, user
	f.lastParams = params
	return f.reply, f.err
}

func TestRouterBackend_ThinkParsesJSON(t *testing.T) {
	fc := &fakeCompleter{reply: `{"thought":"search first","is_done":false,"action":{"tool":"web_search","input":{"q":"flights"}}}`}
	b := NewRouterBackend(fc, "gemini-2.5-pro")

	resp, err := b.Think(context.Background(), ThinkRequest{TaskInput: "find flights", ToolNames: []string{"web_search"}})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if resp.IsDone {
		t.Fatal("expected not done")
	}
	if resp.Action.Tool != "web_search" {
		t.Fatalf("tool = %q, want web_search", resp.Action.Tool)
	}
	if fc.lastModel != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", fc.lastModel)
	}
	// The tool list and JSON instruction must reach the model.
	if !strings.Contains(fc.lastSys, "web_search") || !strings.Contains(fc.lastSys, "JSON") {
		t.Fatalf("system prompt missing tools/JSON instruction: %q", fc.lastSys)
	}
	if !strings.Contains(fc.lastSys, "exact Available tools") || !strings.Contains(fc.lastSys, "retry the same tool once") {
		t.Fatalf("system prompt missing tool-call hygiene guidance: %q", fc.lastSys)
	}
}

func TestRouterBackend_ThinkHandlesMarkdownFences(t *testing.T) {
	fc := &fakeCompleter{reply: "```json\n{\"thought\":\"done\",\"is_done\":true,\"final_answer\":\"hello\"}\n```"}
	b := NewRouterBackend(fc, "qwen3:32b")
	resp, err := b.Think(context.Background(), ThinkRequest{TaskInput: "x"})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if !resp.IsDone || resp.FinalAnswer != "hello" {
		t.Fatalf("fenced JSON not parsed: %+v", resp)
	}
}

func TestRouterBackend_ThinkRecoversInputEqualsTextualAction(t *testing.T) {
	fc := &fakeCompleter{reply: `Thought: Queue exists. Listing pending resources.
Action: queue_list(input={"queue":"pending_resources"})`}
	b := NewRouterBackend(fc, "gemini-2.5-pro")

	resp, err := b.Think(context.Background(), ThinkRequest{
		TaskInput: "process queue",
		ToolNames: []string{"queue_list"},
	})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if resp.IsDone || resp.Action.Tool != "queue_list" {
		t.Fatalf("recovered response = %+v", resp)
	}
	if got := resp.Action.Arguments["queue"]; got != "pending_resources" {
		t.Fatalf("queue arg = %#v", got)
	}
}

func TestRouterBackend_ThinkRecoversLegacyMapCodeAction(t *testing.T) {
	code := "import json\nprint(json.dumps({'ok': True}))"
	fc := &fakeCompleter{reply: "Thought: Extract metadata.\nAction: python_eval(map[code:" + code + "])"}
	b := NewRouterBackend(fc, "gemini-2.5-pro")

	resp, err := b.Think(context.Background(), ThinkRequest{
		TaskInput: "extract",
		ToolNames: []string{"python_eval"},
	})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if resp.Action.Tool != "python_eval" {
		t.Fatalf("tool = %q", resp.Action.Tool)
	}
	if got := resp.Action.Arguments["code"]; got != code {
		t.Fatalf("code arg = %#v, want %#v", got, code)
	}
}

func TestRouterBackend_ThinkRecoversLegacyMapFileWriteAction(t *testing.T) {
	fc := &fakeCompleter{reply: "Thought: Persist resource.\nAction: write_file(map[path:workspace/resources/AI-ML/cisco.md content:# Cisco AI\nSummary with spaces])"}
	b := NewRouterBackend(fc, "gemini-2.5-pro")

	resp, err := b.Think(context.Background(), ThinkRequest{
		TaskInput: "persist",
		ToolNames: []string{"write_file"},
	})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if got := resp.Action.Arguments["path"]; got != "workspace/resources/AI-ML/cisco.md" {
		t.Fatalf("path arg = %#v", got)
	}
	if got := resp.Action.Arguments["content"]; got != "# Cisco AI\nSummary with spaces" {
		t.Fatalf("content arg = %#v", got)
	}
}

func TestRouterBackend_ThinkRecoversActionInputStyle(t *testing.T) {
	fc := &fakeCompleter{reply: "Thought: List queued resources.\nAction: queue_list\nAction Input: {\"queue\":\"pending_resources\"}"}
	b := NewRouterBackend(fc, "gemini-2.5-pro")

	resp, err := b.Think(context.Background(), ThinkRequest{
		TaskInput: "process",
		ToolNames: []string{"queue_list"},
	})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if resp.Action.Tool != "queue_list" || resp.Action.Input["queue"] != "pending_resources" {
		t.Fatalf("unexpected recovered action: %+v", resp.Action)
	}
}

func TestRouterBackend_ThinkRecoversJSONActionEnvelope(t *testing.T) {
	fc := &fakeCompleter{reply: `{"thought":"queue it","action":"queue_put","action_input":{"queue":"pending_resources","item":{"id":"abc","content":"https://example.com"}}}`}
	b := NewRouterBackend(fc, "gemini-2.5-pro")

	resp, err := b.Think(context.Background(), ThinkRequest{
		TaskInput: "capture url",
		ToolNames: []string{"queue_put"},
	})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if resp.Action.Tool != "queue_put" || resp.Action.Input["queue"] != "pending_resources" {
		t.Fatalf("unexpected recovered action: %+v", resp.Action)
	}
	item, ok := resp.Action.Arguments["item"].(map[string]any)
	if !ok || item["content"] != "https://example.com" {
		t.Fatalf("nested action_input not preserved: %#v", resp.Action.Arguments["item"])
	}
}

func TestRouterBackend_UsesPhaseParams(t *testing.T) {
	fc := &fakeCompleter{reply: `{"thought":"done","is_done":true,"final_answer":"ok"}`}
	b := NewRouterBackend(fc, "glm-5.2")
	b.ThinkParams = PhaseParams{Temperature: 0.2, TopP: 0.7, MaxTokens: 333, ResponseFormat: "json"}

	if _, err := b.Think(context.Background(), ThinkRequest{TaskInput: "x"}); err != nil {
		t.Fatalf("Think: %v", err)
	}
	if fc.lastParams.MaxTokens != 333 || fc.lastParams.Temperature != 0.2 || fc.lastParams.TopP != 0.7 {
		t.Fatalf("phase params not forwarded: %+v", fc.lastParams)
	}
}

func TestRouterBackend_ReflectParsesJSON(t *testing.T) {
	fc := &fakeCompleter{reply: `{"output":"Top 3 flights: ...","updated_rules":""}`}
	b := NewRouterBackend(fc, "glm-5.2")
	resp, err := b.Reflect(context.Background(), ReflectRequest{TaskInput: "find flights"})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if resp.Output != "Top 3 flights: ..." {
		t.Fatalf("output = %q", resp.Output)
	}
}

func TestRouterBackend_ReflectFallsBackToRawText(t *testing.T) {
	// A model that ignores the JSON instruction and replies in prose must still
	// yield a non-empty answer rather than an empty output.
	fc := &fakeCompleter{reply: "Here are your best options: Delta $210, United $245."}
	b := NewRouterBackend(fc, "glm-5.2")
	resp, err := b.Reflect(context.Background(), ReflectRequest{TaskInput: "find flights"})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if !strings.Contains(resp.Output, "Delta $210") {
		t.Fatalf("prose answer dropped: %q", resp.Output)
	}
}

func TestRouterBackend_PlanParsesJSON(t *testing.T) {
	fc := &fakeCompleter{reply: `{"goal":"book flight","steps":[{"id":"step-1","description":"search","tool":"web_search","depends_on":[]}]}`}
	b := NewRouterBackend(fc, "gpt-4o")
	plan, err := b.Plan(context.Background(), "system", "find flights", 5)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Tool != "web_search" {
		t.Fatalf("plan not parsed: %+v", plan)
	}
}
