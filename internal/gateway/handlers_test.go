package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestGatewayPingReportsOpenAndAuthenticatedModes(t *testing.T) {
	open := newTestGateway(t, "")
	status, body := gatewayJSON(t, open, http.MethodGet, "/ping", "", "")
	if status != http.StatusOK {
		t.Fatalf("open ping status = %d body=%s", status, body)
	}
	if body["auth"] != "open" || body["mode"] != "none" {
		t.Fatalf("open ping body = %#v", body)
	}

	locked := newTestGateway(t, "secret")
	status, body = gatewayJSON(t, locked, http.MethodGet, "/ping", "", "")
	if status != http.StatusOK {
		t.Fatalf("locked ping status = %d body=%s", status, body)
	}
	if body["auth"] != "required" || body["mode"] != "apikey" {
		t.Fatalf("locked ping body = %#v", body)
	}
}

func TestGatewayAuthMiddlewareProtectsAPIWhenAPIKeySet(t *testing.T) {
	s := newTestGateway(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/agents", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d body=%s", status, body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/agents", "wrong", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong auth status = %d body=%s", status, body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/agents", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("valid auth status = %d body=%s", status, body)
	}
}

func TestGatewayAgentCRUDHandlers(t *testing.T) {
	s := newTestGateway(t, "secret")

	createBody := `{
		"id": "assistant",
		"name": "Assistant",
		"description": "Helpful",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "openai", "model": "gpt-4o-mini"},
		"system_prompt": "Be helpful.",
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", status, body)
	}
	if body["id"] != "assistant" {
		t.Fatalf("create body = %#v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/agents/assistant", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get status = %d body=%s", status, body)
	}
	if body["name"] != "Assistant" {
		t.Fatalf("get body = %#v", body)
	}

	updateBody := `{
		"name": "Assistant Edited",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "openai", "model": "gpt-4o-mini"},
		"system_prompt": "Be even more helpful.",
		"enabled": true
	}`
	status, body = gatewayJSON(t, s, http.MethodPut, "/api/v1/agents/assistant", "secret", updateBody)
	if status != http.StatusOK {
		t.Fatalf("update status = %d body=%s", status, body)
	}
	if body["id"] != "assistant" || body["name"] != "Assistant Edited" {
		t.Fatalf("update body = %#v", body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/agents", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d body=%s", status, body)
	}
	if !agentListContains(body, "assistant", "Assistant Edited") {
		t.Fatalf("list body = %#v", body)
	}

	status, _ = gatewayJSON(t, s, http.MethodDelete, "/api/v1/agents/assistant", "secret", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d", status)
	}

	status, _ = gatewayJSON(t, s, http.MethodGet, "/api/v1/agents/assistant", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("get deleted status = %d", status)
	}
}

func TestGatewayAgentVersionsAndRollback(t *testing.T) {
	s := newTestGateway(t, "secret")

	createBody := `{
		"id": "version-api",
		"name": "Original",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "openai", "model": "gpt-4o-mini"},
		"system_prompt": "Original prompt.",
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", status, body)
	}

	updateBody := `{
		"name": "Edited",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "openai", "model": "gpt-4o-mini"},
		"system_prompt": "Edited prompt.",
		"enabled": true
	}`
	status, body = gatewayJSON(t, s, http.MethodPut, "/api/v1/agents/version-api", "secret", updateBody)
	if status != http.StatusOK {
		t.Fatalf("update status = %d body=%s", status, body)
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/agents/version-api/versions", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("versions status = %d body=%s", status, body)
	}
	versions, ok := body["versions"].([]any)
	if !ok || len(versions) != 1 {
		t.Fatalf("versions body = %#v", body)
	}
	versionID, _ := versions[0].(map[string]any)["id"].(string)
	if versionID == "" {
		t.Fatalf("missing version id: %#v", versions[0])
	}

	status, body = gatewayJSON(t, s, http.MethodGet, "/api/v1/agents/version-api/versions/"+versionID, "secret", "")
	if status != http.StatusOK {
		t.Fatalf("get version status = %d body=%s", status, body)
	}
	if yamlText, _ := body["yaml"].(string); !strings.Contains(yamlText, "name: Original") {
		t.Fatalf("version YAML = %q", yamlText)
	}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/version-api/rollback", "secret", `{"version":"`+versionID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("rollback status = %d body=%s", status, body)
	}
	agentBody, ok := body["agent"].(map[string]any)
	if !ok || agentBody["name"] != "Original" {
		t.Fatalf("rollback body = %#v", body)
	}
}

func TestGatewayReplayAgentRunFromActionLog(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	provider.content = "replayed reply"

	createBody := `{
		"id": "replay-agent",
		"name": "Replay Agent",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "test", "model": "fake-model"},
		"system_prompt": "Be helpful.",
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", status, body)
	}

	orig := message.Message{
		ID:        "msg-original",
		SessionID: "sess-original",
		AgentID:   "replay-agent",
		Channel:   "telegram",
		ThreadID:  "chat-1",
		UserID:    "user-1",
		Username:  "User",
		Role:      message.RoleUser,
		Parts:     message.Text("run this again"),
		CreatedAt: time.Now().UTC(),
	}
	s.actions = &fakeTailBackend{events: []message.Event{{
		Type:      "message.in",
		AgentID:   "replay-agent",
		SessionID: "sess-original",
		Payload:   orig,
		Timestamp: time.Now().UTC(),
	}}}

	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/replay-agent/replay", "secret", `{"session_id":"sess-original"}`)
	if status != http.StatusOK {
		t.Fatalf("replay status = %d body=%s", status, body)
	}
	if body["result"] != "replayed reply" || body["source_channel"] != "telegram" || body["replayed_as_channel"] != "http" {
		t.Fatalf("replay body = %#v", body)
	}
	if got, _ := body["replay_session_id"].(string); !strings.HasPrefix(got, "replay-sess-original-") {
		t.Fatalf("replay_session_id = %q", got)
	}
	req := provider.lastRequest()
	if len(req.Messages) == 0 || !strings.Contains(req.Messages[len(req.Messages)-1].Content, "run this again") {
		t.Fatalf("provider request did not include replayed prompt: %#v", req.Messages)
	}
}

func TestGatewayValidateAgentHandler(t *testing.T) {
	s := newTestGateway(t, "secret")

	valid := `{
		"id": "assistant",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "test", "model": "fake-model"},
		"system_prompt": "Be helpful."
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/validate", "secret", valid)
	if status != http.StatusOK {
		t.Fatalf("valid status = %d body=%s", status, body)
	}
	if body["valid"] != true {
		t.Fatalf("valid body = %#v", body)
	}

	invalid := `{"trigger": "definitely-not-real"}`
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/validate", "secret", invalid)
	if status != http.StatusOK {
		t.Fatalf("invalid status = %d body=%s", status, body)
	}
	if body["valid"] != false {
		t.Fatalf("invalid body = %#v", body)
	}
}

func TestGatewayChatHandlerRunsEngineAndAppliesOverrides(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")

	createBody := `{
		"id": "chat-agent",
		"name": "Chat Agent",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "test", "model": "base-model"},
		"system_prompt": "Reply tersely.",
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", status, body)
	}

	chatBody := `{
		"agent_id": "chat-agent",
		"session_id": "session-1",
		"user_id": "user-1",
		"username": "Ada",
		"text": "Hello",
		"overrides": {
			"model": "override-model",
			"temperature": 0.7,
			"max_tokens": 123,
			"max_turns": 3,
			"tool_choice": "none"
		}
	}`
	status, body = gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", chatBody)
	if status != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", status, body)
	}
	if body["reply"] != "sync reply" {
		t.Fatalf("chat body = %#v", body)
	}

	req := provider.lastRequest()
	if req.Model != "override-model" {
		t.Fatalf("model = %q, want override-model", req.Model)
	}
	if req.Temperature != 0.7 {
		t.Fatalf("temperature = %v, want 0.7", req.Temperature)
	}
	if req.MaxTokens != 123 {
		t.Fatalf("max tokens = %d, want 123", req.MaxTokens)
	}
	if req.ToolChoice != "none" {
		t.Fatalf("tool choice = %q, want none", req.ToolChoice)
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Content != "Hello" {
		t.Fatalf("messages = %#v", req.Messages)
	}
}

func TestGatewayChatHandlerValidatesRequiredFields(t *testing.T) {
	s := newTestGateway(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/chat", "secret", `{"agent_id":"chat-agent"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("missing text status = %d body=%s", status, body)
	}
	if !strings.Contains(body["error"].(string), "agent_id and text are required") {
		t.Fatalf("missing text body = %#v", body)
	}
}

func TestGatewayChatStreamHandlerStreamsTokens(t *testing.T) {
	s, provider := newTestGatewayWithLLM(t, "secret")
	provider.streamTokens = []string{"hello"}

	createBody := `{
		"id": "stream-agent",
		"name": "Stream Agent",
		"trigger": "channel",
		"channels": ["http"],
		"llm": {"provider": "test", "model": "stream-model"},
		"builtins": [],
		"system_prompt": "Stream.",
		"stream_reply": true,
		"enabled": true
	}`
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", status, body)
	}

	status, raw := gatewayRaw(t, s, http.MethodPost, "/api/v1/chat/stream", "secret", `{
		"agent_id": "stream-agent",
		"user_id": "user-1",
		"text": "stream please"
	}`)
	if status != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", status, raw)
	}
	// The stream now always opens with a "run" event carrying the run id (so the
	// client can cancel the run); tokens and [DONE] follow asynchronously and may
	// not be captured synchronously. Any of these is a valid body.
	if raw != "" &&
		!strings.Contains(raw, "event: run") &&
		!strings.Contains(raw, "data: hello\n\n") &&
		!strings.Contains(raw, "data: [DONE]\n\n") {
		t.Fatalf("stream body = %q", raw)
	}
	if !provider.waitForStreamRequest(500 * time.Millisecond) {
		t.Fatalf("provider request Stream = false, want true")
	}
}

func agentListContains(body map[string]any, id, name string) bool {
	agents, ok := body["agents"].([]any)
	if !ok {
		return false
	}
	for _, item := range agents {
		agentBody, ok := item.(map[string]any)
		if ok && agentBody["id"] == id && agentBody["name"] == name {
			return true
		}
	}
	return false
}

func newTestGateway(t *testing.T, apiKey string) *Server {
	s, _ := newTestGatewayWithLLM(t, apiKey)
	return s
}

func newTestGatewayWithLLM(t *testing.T, apiKey string) (*Server, *fakeLLMProvider) {
	t.Helper()
	agentDir := filepath.Join(t.TempDir(), "agents")
	loader := runtime.NewLoader([]string{agentDir})
	cfg := &config.Config{
		Server:    config.ServerConfig{APIKey: apiKey},
		AgentDirs: []string{agentDir},
		LLM: config.LLMConfig{
			DefaultProvider: "openai",
			Providers: map[string]config.ProviderConfig{
				"openai": {Model: "gpt-4o-mini"},
			},
		},
	}
	router := llm.NewRouter("openai")
	provider := &fakeLLMProvider{id: "test", content: "sync reply"}
	router.Register(provider)
	mem, err := memory.NewFileStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	engine := runtime.NewEngine(loader, router, mem, nil, "", 0, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)
	sched := scheduler.New(nil, loader, zap.NewNop(), nil)
	return New(
		cfg,
		"",
		engine,
		loader,
		router,
		channels.NewRegistry(1),
		sched,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		zap.NewNop(),
	), provider
}

func gatewayJSON(t *testing.T, s *Server, method, path, apiKey, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := s.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if len(raw) == 0 {
		return resp.StatusCode, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON status=%d body=%s: %v", resp.StatusCode, string(raw), err)
	}
	return resp.StatusCode, out
}

// gatewayJSONWithHeader mirrors gatewayJSON but lets callers set one
// additional request header (e.g. the Cohort A / Story 5
// X-Acknowledge-Audit consent header, or the S3/S4 confirmation
// headers). Kept as a variadic-free variant so call sites read
// naturally at the test line.
func gatewayJSONWithHeader(t *testing.T, s *Server, method, path, apiKey, body, headerKey, headerVal string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if headerKey != "" {
		req.Header.Set(headerKey, headerVal)
	}
	resp, err := s.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if len(raw) == 0 {
		return resp.StatusCode, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON status=%d body=%s: %v", resp.StatusCode, string(raw), err)
	}
	return resp.StatusCode, out
}

func gatewayRaw(t *testing.T, s *Server, method, path, apiKey, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := s.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, string(raw)
}

type fakeLLMProvider struct {
	id           string
	content      string
	streamTokens []string

	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (p *fakeLLMProvider) ID() string { return p.id }

func (p *fakeLLMProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	if req.Stream {
		ch := make(chan string, len(p.streamTokens))
		for _, token := range p.streamTokens {
			ch <- token
		}
		close(ch)
		return &llm.CompletionResponse{Stream: ch}, nil
	}
	return &llm.CompletionResponse{Content: p.content}, nil
}

func (p *fakeLLMProvider) Models(context.Context) ([]string, error) {
	return []string{"fake-model"}, nil
}

func (p *fakeLLMProvider) lastRequest() llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return llm.CompletionRequest{}
	}
	return p.requests[len(p.requests)-1]
}

func (p *fakeLLMProvider) waitForStreamRequest(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.lastRequest().Stream {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return p.lastRequest().Stream
}
