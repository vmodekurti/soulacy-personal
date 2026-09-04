package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ollamaStub returns a canned /api/chat response.
func ollamaStub(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
}

// The reported failure: every tool call succeeded, the model was handed ~10k
// tokens against a 4k window, Ollama truncated silently, the model emitted
// almost nothing, and the user saw "(no final response produced)" with no cause.
func TestOllamaReportsSilentPromptTruncation(t *testing.T) {
	srv := ollamaStub(t, map[string]any{
		"message":           map[string]any{"content": ""},
		"prompt_eval_count": 10823,
		"eval_count":        1,
	})
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "qwen3-coder:30b", "", map[string]any{"num_ctx": 4096})
	_, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "what is the top news"}},
	})
	if err == nil {
		t.Fatal("an empty answer from a truncated prompt was reported as success")
	}
	msg := err.Error()
	for _, want := range []string{"10823", "4096", "num_ctx"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q, so the operator cannot act on it: %s", want, msg)
		}
	}
	// It must name a concrete value to raise num_ctx to, not just "increase it".
	if !strings.Contains(msg, "16384") {
		t.Errorf("error does not suggest a large enough window: %s", msg)
	}
}

// An empty answer that is WITHIN the window is a different problem and must not
// be misreported as truncation.
func TestOllamaDoesNotBlameContextWhenPromptFits(t *testing.T) {
	srv := ollamaStub(t, map[string]any{
		"message":           map[string]any{"content": ""},
		"prompt_eval_count": 900,
		"eval_count":        1,
	})
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "m", "", map[string]any{"num_ctx": 16384})
	res, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil && strings.Contains(err.Error(), "num_ctx") {
		t.Errorf("blamed the context window for a prompt that fits: %v", err)
	}
	if err == nil && res.Content != "" {
		t.Errorf("unexpected content %q", res.Content)
	}
}

// A model that returns ONLY a tool call has not failed — that is the normal
// shape of a tool-calling turn, and erroring there would break every agent.
func TestOllamaAllowsEmptyContentWithAToolCall(t *testing.T) {
	srv := ollamaStub(t, map[string]any{
		"message": map[string]any{
			"content": "",
			"tool_calls": []any{
				map[string]any{"function": map[string]any{
					"name": "get_top_news", "arguments": map[string]any{"language": "en"},
				}},
			},
		},
		"prompt_eval_count": 20000,
		"eval_count":        12,
	})
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "m", "", map[string]any{"num_ctx": 4096})
	res, err := p.Complete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "news?"}},
	})
	if err != nil {
		t.Fatalf("a tool-call turn was rejected as a truncation failure: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Errorf("tool call lost: %+v", res.ToolCalls)
	}
}

func TestNextContextSizeSuggestsAUsableWindow(t *testing.T) {
	cases := map[int]int{
		10823: 16384,
		3000:  8192,
		100:   4096,
		20000: 32768,
	}
	for prompt, want := range cases {
		if got := nextContextSize(prompt); got != want {
			t.Errorf("nextContextSize(%d) = %d, want %d", prompt, got, want)
		}
	}
}
