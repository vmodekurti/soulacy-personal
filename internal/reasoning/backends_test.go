// backends_test.go — unit tests for AnthropicBackend, OllamaBackend, and OpenAIBackend.
//
// All HTTP calls go through a fake http.RoundTripper — no real API calls are made.
// Tests cover the success path, API error path, HTTP error path, and network failure
// path for each of the three backends.
package reasoning_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/reasoning"
)

// ─── Fake transport ──────────────────────────────────────────────────────────

type fakeTransport struct {
	fn func(*http.Request) (*http.Response, error)
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f.fn(r)
}

// jsonResponse builds a minimal *http.Response with a JSON body.
func jsonResponse(status int, body any) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

// strResponse builds a *http.Response with a raw string body.
func strResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// injectTransport replaces the unexported http.Client inside an
// AnthropicBackend / OllamaBackend / OpenAIBackend using a helper that
// creates the backend with a custom *http.Client via the exported constructor
// and then replaces the Transport field on the embedded client.
// We use the WithClient option approach: since backends expose their *http.Client
// only via the struct field, we create a real *http.Client and swap its Transport.

func clientWithTransport(rt http.RoundTripper) *http.Client {
	return &http.Client{Transport: rt}
}

// ─── AnthropicBackend ────────────────────────────────────────────────────────

// anthropicOKBody returns the minimal JSON the Anthropic API would return.
func anthropicOKBody(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	}
}

// newAnthropicWithTransport creates an AnthropicBackend wired to the given transport.
func newAnthropicWithTransport(rt http.RoundTripper) *reasoning.AnthropicBackend {
	b := reasoning.NewAnthropicBackend("http://fake-anthropic", "test-key")
	b.SetClient(clientWithTransport(rt))
	return b
}

// ─── Think ───────────────────────────────────────────────────────────────────

func TestAnthropicBackend_Think_Success(t *testing.T) {
	thinkJSON := `{"thought":"need to search","is_done":false,"action":{"tool":"web_search","input":{"query":"soulacy"}},"final_answer":""}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header")
		}
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{
		TaskInput:    "what is soulacy?",
		SystemPrompt: "you are helpful",
		ToolNames:    []string{"web_search"},
	})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if resp.Thought != "need to search" {
		t.Errorf("expected thought 'need to search', got %q", resp.Thought)
	}
	if resp.IsDone {
		t.Error("expected IsDone=false")
	}
	if resp.Action.Tool != "web_search" {
		t.Errorf("expected action tool 'web_search', got %q", resp.Action.Tool)
	}
}

func TestAnthropicBackend_Think_RetriesWithoutDeprecatedSamplingParams(t *testing.T) {
	thinkJSON := `{"thought":"done","is_done":true,"final_answer":"ok"}`
	var calls int
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			if _, ok := got["temperature"]; !ok {
				t.Fatal("first request should include temperature")
			}
			if _, ok := got["top_p"]; ok {
				t.Fatalf("temperature and top_p must not be sent together: %#v", got)
			}
			return jsonResponse(400, map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "temperature is deprecated for this model.",
				},
			}), nil
		}
		if _, ok := got["temperature"]; ok {
			t.Fatalf("retry should omit deprecated temperature: %#v", got)
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("retry should not restore top_p after preferring temperature: %#v", got)
		}
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}
	b := newAnthropicWithTransport(ft)
	b.ThinkParams.Temperature = 0.1

	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{
		TaskInput:    "hi",
		SystemPrompt: "system",
	})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if !resp.IsDone || resp.FinalAnswer != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestAnthropicBackend_Think_OmitsSamplingForClaudeFiveAliases(t *testing.T) {
	thinkJSON := `{"thought":"done","is_done":true,"final_answer":"ok"}`
	var calls int
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := got["temperature"]; ok {
			t.Fatalf("temperature should be omitted for sampling-free Anthropic models: %#v", got)
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("top_p should be omitted for sampling-free Anthropic models: %#v", got)
		}
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}
	b := newAnthropicWithTransport(ft)
	b.ThinkModel = "claude-sonnet-5"
	b.ThinkParams.Temperature = 0.1
	b.ThinkParams.TopP = 0.8

	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "x"})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if calls != 1 || !resp.IsDone {
		t.Fatalf("calls=%d resp=%+v", calls, resp)
	}
}

func TestAnthropicBackend_Think_RetriesWhenSamplingParamsCannotBothBeSpecified(t *testing.T) {
	thinkJSON := `{"thought":"done","is_done":true,"final_answer":"ok"}`
	var calls int
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			if _, ok := got["temperature"]; !ok {
				t.Fatalf("first request should include temperature: %#v", got)
			}
			if _, ok := got["top_p"]; ok {
				t.Fatalf("request builder should not send temperature and top_p together: %#v", got)
			}
			return jsonResponse(400, map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "temperature and top_p cannot both be specified for this model. Please use only one.",
				},
			}), nil
		}
		if _, ok := got["temperature"]; ok {
			t.Fatalf("retry should omit temperature after provider sampling conflict: %#v", got)
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("retry should not add top_p after provider sampling conflict: %#v", got)
		}
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}
	b := newAnthropicWithTransport(ft)
	b.ThinkParams.Temperature = 0.1
	b.ThinkParams.TopP = 0.8

	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "x"})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if calls != 2 || !resp.IsDone {
		t.Fatalf("calls=%d resp=%+v", calls, resp)
	}
}

func TestAnthropicBackend_Think_RetriesWithoutUnsupportedTopP(t *testing.T) {
	thinkJSON := `{"thought":"done","is_done":true,"final_answer":"ok"}`
	var calls int
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			if _, ok := got["top_p"]; !ok {
				t.Fatalf("first request should include top_p: %#v", got)
			}
			return jsonResponse(400, map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "top_p is not supported for this model.",
				},
			}), nil
		}
		if _, ok := got["top_p"]; ok {
			t.Fatalf("retry should omit unsupported top_p: %#v", got)
		}
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}
	b := newAnthropicWithTransport(ft)
	b.ThinkParams.Temperature = 0
	b.ThinkParams.TopP = 0.8

	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "x"})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if calls != 2 || !resp.IsDone {
		t.Fatalf("calls=%d resp=%+v", calls, resp)
	}
}

func TestAnthropicBackend_Think_Done(t *testing.T) {
	thinkJSON := `{"thought":"done","is_done":true,"final_answer":"42"}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{
		TaskInput: "what is 6*7?",
	})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if !resp.IsDone {
		t.Error("expected IsDone=true")
	}
	if resp.FinalAnswer != "42" {
		t.Errorf("expected final_answer '42', got %q", resp.FinalAnswer)
	}
}

func TestAnthropicBackend_Think_WithMarkdownFences(t *testing.T) {
	// The LLM wrapped the JSON in markdown fences — backend must strip them.
	thinkJSON := "```json\n{\"thought\":\"ok\",\"is_done\":true,\"final_answer\":\"yes\"}\n```"

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if !resp.IsDone {
		t.Error("expected IsDone=true after fence stripping")
	}
}

func TestAnthropicBackend_Think_HTTPError(t *testing.T) {
	apiErr := map[string]any{
		"error": map[string]any{
			"type":    "authentication_error",
			"message": "invalid api key",
		},
	}
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(401, apiErr), nil
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from API error response, got nil")
	}
	if !strings.Contains(err.Error(), "authentication_error") {
		t.Errorf("expected error to mention 'authentication_error', got: %v", err)
	}
}

func TestAnthropicBackend_Think_HTTP500(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return strResponse(500, `{"content":[]}`), nil
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from HTTP 500, got nil")
	}
}

func TestAnthropicBackend_Think_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from network failure, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected 'connection refused' in error, got: %v", err)
	}
}

func TestAnthropicBackend_Think_NoTextBlock(t *testing.T) {
	// Response has content but no "text" type block.
	body := map[string]any{
		"content": []map[string]any{
			{"type": "image", "text": ""},
		},
	}
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, body), nil
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error for missing text block, got nil")
	}
	if !strings.Contains(err.Error(), "no text block") {
		t.Errorf("expected 'no text block' in error, got: %v", err)
	}
}

func TestAnthropicBackend_Think_InvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, anthropicOKBody("not valid json at all")), nil
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// ─── Plan ────────────────────────────────────────────────────────────────────

func TestAnthropicBackend_Plan_Success(t *testing.T) {
	planJSON := `{"goal":"research soulacy","steps":[{"id":"step-1","description":"search","tool":"web_search","depends_on":[]},{"id":"step-2","description":"summarize","tool":"summarize","depends_on":["step-1"]}]}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, anthropicOKBody(planJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	plan, err := b.Plan(context.Background(), "you are helpful", "research soulacy", 5)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Goal != "research soulacy" {
		t.Errorf("expected goal 'research soulacy', got %q", plan.Goal)
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ID != "step-1" {
		t.Errorf("expected step-1, got %q", plan.Steps[0].ID)
	}
	if plan.Steps[1].DependsOn[0] != "step-1" {
		t.Errorf("expected step-2 to depend on step-1")
	}
}

func TestAnthropicBackend_Plan_HTTPError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: no route to host")
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Plan(context.Background(), "sys", "task", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAnthropicBackend_Plan_InvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, anthropicOKBody("not a plan")), nil
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Plan(context.Background(), "sys", "task", 5)
	if err == nil {
		t.Fatal("expected error for invalid plan JSON, got nil")
	}
}

// ─── Reflect ─────────────────────────────────────────────────────────────────

func TestAnthropicBackend_Reflect_Success(t *testing.T) {
	reflectJSON := `{"output":"The answer is 42","updated_rules":""}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, anthropicOKBody(reflectJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{
		TaskInput:    "what is 6*7?",
		SystemPrompt: "you are helpful",
		OutputFormat: "plain",
		Steps: []reasoning.Step{
			{ID: "step-1", Thought: "compute", Obs: reasoning.Observation{Content: "42"}},
		},
	})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.Output != "The answer is 42" {
		t.Errorf("expected output 'The answer is 42', got %q", resp.Output)
	}
}

func TestAnthropicBackend_Reflect_FallbackOnInvalidJSON(t *testing.T) {
	// When JSON parse fails, Reflect falls back to using the raw body as output.
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, anthropicOKBody("plain text answer")), nil
	}}

	b := newAnthropicWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{
		TaskInput: "test",
	})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.Output == "" {
		t.Error("expected non-empty Output from fallback path")
	}
}

func TestAnthropicBackend_Reflect_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection timeout")
	}}

	b := newAnthropicWithTransport(ft)
	_, err := b.Reflect(context.Background(), reasoning.ReflectRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from network failure, got nil")
	}
}

func TestAnthropicBackend_Reflect_WithOutputFormat(t *testing.T) {
	reflectJSON := `{"output":"## Summary\nDone.","updated_rules":"- remember X"}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		// Verify the output format hint reaches the API.
		var reqBody map[string]any
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &reqBody)
		sys, _ := reqBody["system"].(string)
		if !strings.Contains(sys, "structured_markdown") {
			t.Errorf("expected system prompt to contain output format hint, got: %s", sys)
		}
		return jsonResponse(200, anthropicOKBody(reflectJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{
		TaskInput:    "test",
		OutputFormat: "structured_markdown",
	})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.UpdatedRules == "" {
		t.Error("expected updated_rules to be populated")
	}
}

// ─── OllamaBackend ───────────────────────────────────────────────────────────

// ollamaOKBody returns the minimal JSON the Ollama /api/chat endpoint returns.
func ollamaOKBody(content string) map[string]any {
	return map[string]any{
		"message": map[string]any{
			"content": content,
		},
	}
}

// newOllamaWithTransport creates an OllamaBackend wired to the given transport.
func newOllamaWithTransport(rt http.RoundTripper) *reasoning.OllamaBackend {
	b := reasoning.NewOllamaBackend("http://fake-ollama")
	b.SetClient(clientWithTransport(rt))
	return b
}

// ─── Think ───────────────────────────────────────────────────────────────────

func TestOllamaBackend_Think_Success(t *testing.T) {
	thinkJSON := `{"thought":"searching","is_done":false,"action":{"tool":"web_search","input":{"query":"go lang"}},"final_answer":""}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if body["format"] != "json" {
			t.Errorf("expected format:json, got %v", body["format"])
		}
		return jsonResponse(200, ollamaOKBody(thinkJSON)), nil
	}}

	b := newOllamaWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{
		TaskInput:    "research go lang",
		SystemPrompt: "you are helpful",
		ToolNames:    []string{"web_search"},
	})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if resp.Thought != "searching" {
		t.Errorf("expected thought 'searching', got %q", resp.Thought)
	}
	if resp.Action.Tool != "web_search" {
		t.Errorf("expected tool 'web_search', got %q", resp.Action.Tool)
	}
}

func TestOllamaBackend_Think_Done(t *testing.T) {
	thinkJSON := `{"thought":"finished","is_done":true,"final_answer":"Go is awesome"}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, ollamaOKBody(thinkJSON)), nil
	}}

	b := newOllamaWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if !resp.IsDone {
		t.Error("expected IsDone=true")
	}
	if resp.FinalAnswer != "Go is awesome" {
		t.Errorf("expected 'Go is awesome', got %q", resp.FinalAnswer)
	}
}

func TestOllamaBackend_Think_APIError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		body := map[string]any{"error": "model not found"}
		return jsonResponse(200, body), nil
	}}

	b := newOllamaWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from API error response, got nil")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("expected 'model not found' in error, got: %v", err)
	}
}

func TestOllamaBackend_Think_HTTP500(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return strResponse(500, `{"message":{}}`), nil
	}}

	b := newOllamaWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from HTTP 500, got nil")
	}
}

func TestOllamaBackend_Think_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}}

	b := newOllamaWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from network failure, got nil")
	}
}

func TestOllamaBackend_Think_InvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, ollamaOKBody("not valid json")), nil
	}}

	b := newOllamaWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// ─── Plan ────────────────────────────────────────────────────────────────────

func TestOllamaBackend_Plan_Success(t *testing.T) {
	planJSON := `{"goal":"find info","steps":[{"id":"step-1","description":"search","tool":"web_search","depends_on":[]},{"id":"step-2","description":"summarize","tool":"summarize","depends_on":["step-1"]}]}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, ollamaOKBody(planJSON)), nil
	}}

	b := newOllamaWithTransport(ft)
	plan, err := b.Plan(context.Background(), "sys", "find info", 5)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Goal != "find info" {
		t.Errorf("expected goal 'find info', got %q", plan.Goal)
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}
}

func TestOllamaBackend_Plan_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("no route to host")
	}}

	b := newOllamaWithTransport(ft)
	_, err := b.Plan(context.Background(), "sys", "task", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestOllamaBackend_Plan_InvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, ollamaOKBody("garbage")), nil
	}}

	b := newOllamaWithTransport(ft)
	_, err := b.Plan(context.Background(), "sys", "task", 5)
	if err == nil {
		t.Fatal("expected error for invalid plan JSON, got nil")
	}
}

// ─── Reflect ─────────────────────────────────────────────────────────────────

func TestOllamaBackend_Reflect_Success(t *testing.T) {
	reflectJSON := `{"output":"Final answer here","updated_rules":""}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, ollamaOKBody(reflectJSON)), nil
	}}

	b := newOllamaWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{
		TaskInput: "test task",
		Steps: []reasoning.Step{
			{ID: "step-1", Thought: "ok", Obs: reasoning.Observation{Content: "done"}},
		},
	})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.Output != "Final answer here" {
		t.Errorf("expected 'Final answer here', got %q", resp.Output)
	}
}

func TestOllamaBackend_Reflect_FallbackOnInvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, ollamaOKBody("plain text response")), nil
	}}

	b := newOllamaWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{TaskInput: "test"})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.Output == "" {
		t.Error("expected non-empty Output from fallback path")
	}
}

func TestOllamaBackend_Reflect_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection timeout")
	}}

	b := newOllamaWithTransport(ft)
	_, err := b.Reflect(context.Background(), reasoning.ReflectRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from network failure, got nil")
	}
}

func TestOllamaBackend_Reflect_WithOutputFormat(t *testing.T) {
	reflectJSON := `{"output":"## Result","updated_rules":""}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		msgs, _ := body["messages"].([]any)
		systemContent := ""
		for _, m := range msgs {
			msg, _ := m.(map[string]any)
			if msg["role"] == "system" {
				systemContent, _ = msg["content"].(string)
			}
		}
		if !strings.Contains(systemContent, "decision_brief") {
			t.Errorf("expected system prompt to include output format, got: %s", systemContent)
		}
		return jsonResponse(200, ollamaOKBody(reflectJSON)), nil
	}}

	b := newOllamaWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{
		TaskInput:    "test",
		OutputFormat: "decision_brief",
	})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.Output != "## Result" {
		t.Errorf("expected '## Result', got %q", resp.Output)
	}
}

// ─── NewOllamaBackendSingleModel ─────────────────────────────────────────────

func TestNewOllamaBackendSingleModel(t *testing.T) {
	thinkJSON := `{"thought":"ok","is_done":true,"final_answer":"done"}`

	var capturedModel string
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		capturedModel, _ = body["model"].(string)
		return jsonResponse(200, ollamaOKBody(thinkJSON)), nil
	}}

	b := reasoning.NewOllamaBackendSingleModel("http://fake-ollama", "my-model")
	b.SetClient(clientWithTransport(ft))

	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if capturedModel != "my-model" {
		t.Errorf("expected model 'my-model', got %q", capturedModel)
	}
}

// ─── OpenAIBackend ───────────────────────────────────────────────────────────

// openaiOKBody returns the minimal JSON from OpenAI's /chat/completions.
func openaiOKBody(content string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	}
}

// newOpenAIWithTransport creates an OpenAIBackend wired to the given transport.
func newOpenAIWithTransport(rt http.RoundTripper) *reasoning.OpenAIBackend {
	b := reasoning.NewOpenAICompatibleBackend("http://fake-openai/v1", "test-key", "gpt-4o-mini")
	b.SetClient(clientWithTransport(rt))
	return b
}

// ─── Think ───────────────────────────────────────────────────────────────────

func TestOpenAIBackend_Think_Success(t *testing.T) {
	thinkJSON := `{"thought":"researching","is_done":false,"action":{"tool":"web_search","input":{"query":"openai"}},"final_answer":""}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("expected Bearer token in Authorization header")
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		rf, _ := body["response_format"].(map[string]any)
		if rf["type"] != "json_object" {
			t.Errorf("expected response_format.type=json_object, got %v", rf["type"])
		}
		if body["stream"] != false {
			t.Errorf("expected stream:false, got %v", body["stream"])
		}
		return jsonResponse(200, openaiOKBody(thinkJSON)), nil
	}}

	b := newOpenAIWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{
		TaskInput:    "research openai",
		SystemPrompt: "you are helpful",
		ToolNames:    []string{"web_search"},
	})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if resp.Thought != "researching" {
		t.Errorf("expected thought 'researching', got %q", resp.Thought)
	}
	if resp.IsDone {
		t.Error("expected IsDone=false")
	}
}

func TestOpenAIBackend_Think_Done(t *testing.T) {
	thinkJSON := `{"thought":"finished","is_done":true,"final_answer":"the answer"}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, openaiOKBody(thinkJSON)), nil
	}}

	b := newOpenAIWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if !resp.IsDone {
		t.Error("expected IsDone=true")
	}
	if resp.FinalAnswer != "the answer" {
		t.Errorf("expected 'the answer', got %q", resp.FinalAnswer)
	}
}

// A reasoning model can return empty `content` while leaving the JSON step in
// `reasoning_content`. We must recover the step from there instead of failing
// with an empty-parse error.
func TestOpenAIBackend_Think_RecoversFromReasoningContent(t *testing.T) {
	step := `{"thought":"done","is_done":true,"final_answer":"MU is a hold."}`
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		body := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{
					"content":           "",
					"reasoning_content": "Let me think... " + step + " that's my step.",
				}},
			},
		}
		return jsonResponse(200, body), nil
	}}

	b := newOpenAIWithTransport(ft)
	resp, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "MU stock?"})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if !resp.IsDone || resp.FinalAnswer != "MU is a hold." {
		t.Errorf("expected recovered final answer, got done=%v ans=%q", resp.IsDone, resp.FinalAnswer)
	}
}

// Empty content with finish_reason=length must yield an actionable token-limit
// error, not a bare "unexpected end of JSON input".
func TestOpenAIBackend_Think_EmptyContentTokenLimit(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		body := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": ""}, "finish_reason": "length"},
			},
		}
		return jsonResponse(200, body), nil
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "MU stock?"})
	if err == nil {
		t.Fatal("expected an error for empty content")
	}
	if !strings.Contains(err.Error(), "token limit") {
		t.Errorf("expected a token-limit error, got %v", err)
	}
}

func TestOpenAIBackend_Think_APIError(t *testing.T) {
	body := map[string]any{
		"error": map[string]any{
			"message": "invalid api key",
			"type":    "invalid_request_error",
		},
	}
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(401, body), nil
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from API error response, got nil")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("expected 'invalid api key' in error, got: %v", err)
	}
}

func TestOpenAIBackend_Think_HTTP500(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(500, map[string]any{"choices": []any{}}), nil
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from HTTP 500, got nil")
	}
}

func TestOpenAIBackend_Think_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from network failure, got nil")
	}
}

func TestOpenAIBackend_Think_NoChoices(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, map[string]any{"choices": []any{}}), nil
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected 'no choices' in error, got: %v", err)
	}
}

func TestOpenAIBackend_Think_InvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, openaiOKBody("not json")), nil
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response, got nil")
	}
}

// ─── Plan ────────────────────────────────────────────────────────────────────

func TestOpenAIBackend_Plan_Success(t *testing.T) {
	planJSON := `{"goal":"research topic","steps":[{"id":"step-1","description":"search","tool":"web_search","depends_on":[]},{"id":"step-2","description":"write","tool":"write","depends_on":["step-1"]}]}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, openaiOKBody(planJSON)), nil
	}}

	b := newOpenAIWithTransport(ft)
	plan, err := b.Plan(context.Background(), "sys", "research topic", 5)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Goal != "research topic" {
		t.Errorf("expected goal 'research topic', got %q", plan.Goal)
	}
	if len(plan.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(plan.Steps))
	}
}

func TestOpenAIBackend_Plan_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("timeout")
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Plan(context.Background(), "sys", "task", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestOpenAIBackend_Plan_InvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, openaiOKBody("just text")), nil
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Plan(context.Background(), "sys", "task", 5)
	if err == nil {
		t.Fatal("expected error for invalid plan JSON, got nil")
	}
}

// ─── Reflect ─────────────────────────────────────────────────────────────────

func TestOpenAIBackend_Reflect_Success(t *testing.T) {
	reflectJSON := `{"output":"The final answer","updated_rules":""}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, openaiOKBody(reflectJSON)), nil
	}}

	b := newOpenAIWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{
		TaskInput: "test task",
		Steps: []reasoning.Step{
			{ID: "step-1", Thought: "ok", Obs: reasoning.Observation{Content: "done"}},
		},
	})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.Output != "The final answer" {
		t.Errorf("expected 'The final answer', got %q", resp.Output)
	}
}

func TestOpenAIBackend_Reflect_FallbackOnInvalidJSON(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, openaiOKBody("plain text answer")), nil
	}}

	b := newOpenAIWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{TaskInput: "test"})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.Output == "" {
		t.Error("expected non-empty Output from fallback path")
	}
}

func TestOpenAIBackend_Reflect_NetworkError(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection timeout")
	}}

	b := newOpenAIWithTransport(ft)
	_, err := b.Reflect(context.Background(), reasoning.ReflectRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from network failure, got nil")
	}
}

func TestOpenAIBackend_Reflect_WithUpdatedRules(t *testing.T) {
	reflectJSON := `{"output":"done","updated_rules":"- always check twice"}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, openaiOKBody(reflectJSON)), nil
	}}

	b := newOpenAIWithTransport(ft)
	resp, err := b.Reflect(context.Background(), reasoning.ReflectRequest{
		TaskInput: "test",
	})
	if err != nil {
		t.Fatalf("Reflect returned error: %v", err)
	}
	if resp.UpdatedRules != "- always check twice" {
		t.Errorf("expected updated_rules '- always check twice', got %q", resp.UpdatedRules)
	}
}

// ─── NewOpenAIBackend constructor ────────────────────────────────────────────

func TestNewOpenAIBackend_DefaultModel(t *testing.T) {
	thinkJSON := `{"thought":"ok","is_done":true,"final_answer":"done"}`

	var capturedModel string
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		capturedModel, _ = body["model"].(string)
		return jsonResponse(200, openaiOKBody(thinkJSON)), nil
	}}

	// NewOpenAIBackend with empty model → defaults to gpt-4o
	b := reasoning.NewOpenAICompatibleBackend("http://fake-openai/v1", "key", "")
	b.SetClient(clientWithTransport(ft))

	_, err := b.Think(context.Background(), reasoning.ThinkRequest{TaskInput: "test"})
	if err != nil {
		t.Fatalf("Think returned error: %v", err)
	}
	if capturedModel != "gpt-4o" {
		t.Errorf("expected default model 'gpt-4o', got %q", capturedModel)
	}
}

// ─── Anthropic constructor defaults ──────────────────────────────────────────

func TestNewAnthropicBackend_DefaultBaseURL(t *testing.T) {
	// Pass empty baseURL — constructor should use defaultAnthropicBase.
	b := reasoning.NewAnthropicBackend("", "key")
	// We can't inspect the private field, but we can verify the constructor
	// does not panic and returns a non-nil backend.
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestNewAnthropicBackend_ModelDefaults(t *testing.T) {
	b := reasoning.NewAnthropicBackend("", "key")
	if b.ThinkModel == "" {
		t.Error("expected non-empty ThinkModel")
	}
	if b.PlanModel == "" {
		t.Error("expected non-empty PlanModel")
	}
	if b.ReflectModel == "" {
		t.Error("expected non-empty ReflectModel")
	}
}

// ─── OllamaBackend constructor defaults ──────────────────────────────────────

func TestNewOllamaBackend_DefaultBaseURL(t *testing.T) {
	b := reasoning.NewOllamaBackend("")
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestNewOllamaBackend_ModelDefaults(t *testing.T) {
	b := reasoning.NewOllamaBackend("")
	if b.ThinkModel == "" {
		t.Error("expected non-empty ThinkModel")
	}
	if b.PlanReflectModel == "" {
		t.Error("expected non-empty PlanReflectModel")
	}
}

// ─── Request body validation ─────────────────────────────────────────────────

func TestAnthropicBackend_Think_RequestBody(t *testing.T) {
	// Verify the request body contains the expected fields.
	thinkJSON := `{"thought":"ok","is_done":true,"final_answer":"done"}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not valid JSON: %v", err)
		}
		if body["model"] == "" || body["model"] == nil {
			t.Error("expected model field in request body")
		}
		if body["max_tokens"] == nil {
			t.Error("expected max_tokens field in request body")
		}
		sys, _ := body["system"].(string)
		if !strings.Contains(sys, "web_search") {
			t.Errorf("expected system prompt to contain tool names, got: %s", sys)
		}
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	b.Think(context.Background(), reasoning.ThinkRequest{
		TaskInput:    "test",
		SystemPrompt: "base",
		ToolNames:    []string{"web_search"},
	})
}

func TestOllamaBackend_Plan_RequestBody(t *testing.T) {
	planJSON := `{"goal":"g","steps":[]}`

	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if body["stream"] != false {
			t.Errorf("expected stream:false, got %v", body["stream"])
		}
		if body["format"] != "json" {
			t.Errorf("expected format:json, got %v", body["format"])
		}
		opts, _ := body["options"].(map[string]any)
		if opts == nil {
			t.Error("expected options field in request body")
		}
		return jsonResponse(200, ollamaOKBody(planJSON)), nil
	}}

	b := newOllamaWithTransport(ft)
	b.Plan(context.Background(), "sys", "task", 3)
}

// ─── Context cancellation ────────────────────────────────────────────────────

func TestAnthropicBackend_Think_ContextCanceled(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}}

	b := newAnthropicWithTransport(ft)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := b.Think(ctx, reasoning.ThinkRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestOllamaBackend_Reflect_ContextCanceled(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}}

	b := newOllamaWithTransport(ft)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := b.Reflect(ctx, reasoning.ReflectRequest{TaskInput: "test"})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestOpenAIBackend_Plan_ContextCanceled(t *testing.T) {
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}}

	b := newOpenAIWithTransport(ft)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := b.Plan(ctx, "sys", "task", 5)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

// ─── StepHistory formatting ───────────────────────────────────────────────────

func TestAnthropicBackend_Think_WithStepHistory(t *testing.T) {
	thinkJSON := `{"thought":"continuing","is_done":false,"action":{"tool":"web_search","input":{"query":"more"}}}`

	var capturedUser string
	ft := &fakeTransport{fn: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		msgs, _ := body["messages"].([]any)
		for _, m := range msgs {
			msg, _ := m.(map[string]any)
			if msg["role"] == "user" {
				capturedUser, _ = msg["content"].(string)
			}
		}
		return jsonResponse(200, anthropicOKBody(thinkJSON)), nil
	}}

	b := newAnthropicWithTransport(ft)
	b.Think(context.Background(), reasoning.ThinkRequest{
		TaskInput: "test task",
		StepHistory: []reasoning.Step{
			{
				ID:      "step-1",
				Thought: "searching",
				Action:  reasoning.ToolCall{Tool: "web_search", Input: map[string]string{"query": "q"}},
				Obs:     reasoning.Observation{Content: "some results"},
			},
		},
	})

	if !strings.Contains(capturedUser, "Thought: searching") {
		t.Errorf("expected step history in user message, got: %s", capturedUser)
	}
	if !strings.Contains(capturedUser, "some results") {
		t.Errorf("expected observation in user message, got: %s", capturedUser)
	}
}
