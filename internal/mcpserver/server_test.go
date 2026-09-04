package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeGateway struct {
	agents []Agent
	calls  []ChatRequest
}

func (f *fakeGateway) ListAgents(ctx context.Context) ([]Agent, error) {
	return f.agents, nil
}

func (f *fakeGateway) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	f.calls = append(f.calls, req)
	return ChatResponse{Reply: "reply from " + req.AgentID + ": " + req.Text}, nil
}

type platformGateway struct {
	fakeGateway
	scheduleCalls  int
	workboardCalls []string
	knowledgeCalls []string
	queueCalls     []string
}

func (f *platformGateway) ListSchedule(ctx context.Context) (json.RawMessage, error) {
	f.scheduleCalls++
	return json.RawMessage(`{"schedule":[{"agent_id":"daily"}]}`), nil
}

func (f *platformGateway) ScheduleStatus(ctx context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"running":{},"next":{"daily":"2026-07-06T07:00:00Z"}}`), nil
}

func (f *platformGateway) ListWorkboardTasks(ctx context.Context, status, agentID string) (json.RawMessage, error) {
	f.workboardCalls = append(f.workboardCalls, status+"|"+agentID)
	return json.RawMessage(`{"tasks":[{"id":7,"title":"ship"}]}`), nil
}

func (f *platformGateway) RunWorkboardTask(ctx context.Context, id string) (json.RawMessage, error) {
	f.workboardCalls = append(f.workboardCalls, "run:"+id)
	return json.RawMessage(`{"id":7,"status":"running"}`), nil
}

func (f *platformGateway) ListKnowledgeBases(ctx context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"knowledge_bases":[{"name":"AI Docs"}]}`), nil
}

func (f *platformGateway) SearchKnowledge(ctx context.Context, kb, query string, topK int) (json.RawMessage, error) {
	f.knowledgeCalls = append(f.knowledgeCalls, kb+"|"+query)
	return json.RawMessage(`{"result":"hit"}`), nil
}

func (f *platformGateway) ListQueues(ctx context.Context) (json.RawMessage, error) {
	f.queueCalls = append(f.queueCalls, "names")
	return json.RawMessage(`{"queues":[{"queue":"pending_resources","count":1}]}`), nil
}

func (f *platformGateway) CreateQueue(ctx context.Context, queue string) (json.RawMessage, error) {
	f.queueCalls = append(f.queueCalls, "create:"+queue)
	return json.RawMessage(`{"ok":true,"created":true}`), nil
}

func (f *platformGateway) ListQueueItems(ctx context.Context, queue string, limit int) (json.RawMessage, error) {
	f.queueCalls = append(f.queueCalls, "list:"+queue)
	return json.RawMessage(`{"items":[{"id":"q1"}]}`), nil
}

func (f *platformGateway) PutQueueItem(ctx context.Context, queue string, item json.RawMessage, ttlSeconds int) (json.RawMessage, error) {
	f.queueCalls = append(f.queueCalls, "put:"+queue+":"+string(item))
	return json.RawMessage(`{"ok":true,"id":"q2"}`), nil
}

func (f *platformGateway) TakeQueueItem(ctx context.Context, queue string) (json.RawMessage, error) {
	f.queueCalls = append(f.queueCalls, "take:"+queue)
	return json.RawMessage(`{"ok":true,"id":"q1"}`), nil
}

func (f *platformGateway) ClearQueue(ctx context.Context, queue string) (json.RawMessage, error) {
	f.queueCalls = append(f.queueCalls, "clear:"+queue)
	return json.RawMessage(`{"ok":true,"cleared":1}`), nil
}

func TestServerInitializeAndListTools(t *testing.T) {
	gw := &fakeGateway{agents: []Agent{
		{ID: "weather-agent", Name: "Weather Agent", Description: "Answers weather questions.", Enabled: true},
		{ID: "disabled-agent", Name: "Disabled", Enabled: false},
	}}
	srv := &Server{Client: gw, Version: "test"}

	reqs := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(reqs), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d responses, want 2: %s", len(lines), out.String())
	}
	var initResp map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	result := initResp["result"].(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}

	var listResp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	listResult := listResp["result"].(map[string]any)
	tools := listResult["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools len = %d, want generic + one enabled agent", len(tools))
	}
	agentTool := tools[1].(map[string]any)
	if !strings.HasPrefix(agentTool["name"].(string), "soulacy_agent_weather-agent_") {
		t.Fatalf("agent tool name = %q", agentTool["name"])
	}
	if !strings.Contains(agentTool["description"].(string), "Answers weather questions") {
		t.Fatalf("agent description = %q", agentTool["description"])
	}
}

func TestServerCallsAgentTool(t *testing.T) {
	gw := &fakeGateway{agents: []Agent{{ID: "research-librarian", Name: "Research Librarian", Enabled: true}}}
	srv := &Server{Client: gw, UserID: "default-user"}
	tool := srv.toolName("research-librarian")

	req := `{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"` + tool + `","arguments":{"text":"summarize this","session_id":"sess-1"}}}` + "\n"
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(req), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("gateway calls = %d, want 1", len(gw.calls))
	}
	call := gw.calls[0]
	if call.AgentID != "research-librarian" || call.Text != "summarize this" || call.SessionID != "sess-1" || call.UserID != "default-user" {
		t.Fatalf("unexpected call: %#v", call)
	}
	var resp map[string]any
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	content := resp["result"].(map[string]any)["content"].([]any)
	if got := content[0].(map[string]any)["text"].(string); !strings.Contains(got, "reply from research-librarian") {
		t.Fatalf("content text = %q", got)
	}
}

func TestServerCallsGenericChatTool(t *testing.T) {
	gw := &fakeGateway{agents: []Agent{{ID: "weather", Enabled: true}}}
	srv := &Server{Client: gw}
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"soulacy_chat","arguments":{"agent_id":"weather","text":"hello"}}}` + "\n"
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(req), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if len(gw.calls) != 1 || gw.calls[0].AgentID != "weather" {
		t.Fatalf("calls = %#v", gw.calls)
	}
	if !strings.Contains(out.String(), "reply from weather") {
		t.Fatalf("response = %s", out.String())
	}
}

func TestServerListsPlatformToolsWhenClientSupportsThem(t *testing.T) {
	gw := &platformGateway{}
	srv := &Server{Client: gw}
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(req), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	tools := resp["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, name := range []string{"soulacy_schedule_list", "soulacy_workboard_tasks", "soulacy_knowledge_search", "soulacy_queue_put", "soulacy_queue_take"} {
		if !names[name] {
			t.Fatalf("tool %q missing from names %#v", name, names)
		}
	}
}

func TestServerCallsPlatformTools(t *testing.T) {
	gw := &platformGateway{}
	srv := &Server{Client: gw}
	reqs := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"soulacy_schedule_list","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"soulacy_workboard_tasks","arguments":{"status":"todo","agent_id":"bot"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"soulacy_knowledge_search","arguments":{"kb":"AI Docs","query":"governance","top_k":3}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"soulacy_queue_put","arguments":{"queue":"pending_resources","item":{"url":"https://example.com"}}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"soulacy_queue_take","arguments":{"queue":"pending_resources"}}}`,
		"",
	}, "\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(reqs), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if gw.scheduleCalls != 1 {
		t.Fatalf("scheduleCalls = %d, want 1", gw.scheduleCalls)
	}
	if len(gw.workboardCalls) != 1 || gw.workboardCalls[0] != "todo|bot" {
		t.Fatalf("workboardCalls = %#v", gw.workboardCalls)
	}
	if len(gw.knowledgeCalls) != 1 || gw.knowledgeCalls[0] != "AI Docs|governance" {
		t.Fatalf("knowledgeCalls = %#v", gw.knowledgeCalls)
	}
	if len(gw.queueCalls) != 2 || !strings.HasPrefix(gw.queueCalls[0], "put:pending_resources:") || gw.queueCalls[1] != "take:pending_resources" {
		t.Fatalf("queueCalls = %#v", gw.queueCalls)
	}
	if !strings.Contains(out.String(), `schedule`) || !strings.Contains(out.String(), `tasks`) || !strings.Contains(out.String(), `hit`) {
		t.Fatalf("response = %s", out.String())
	}
}
