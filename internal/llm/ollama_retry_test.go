package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOllamaRetriesTransientFailureBeforeAnswerOrStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			provider := NewOllamaProvider("http://ollama.test", "model", "", map[string]any{"num_ctx": 8192})
			attempts := 0
			var original string
			provider.client = clientWithRoundTripper(func(req *http.Request) (*http.Response, error) {
				attempts++
				body, _ := io.ReadAll(req.Body)
				if attempts == 1 {
					original = string(body)
					return jsonResponse(500, `{"error":"runner temporarily unavailable"}`), nil
				}
				if string(body) != original {
					t.Fatal("retry changed the request body")
				}
				return jsonResponse(200, "{\"message\":{\"role\":\"assistant\",\"content\":\"recovered\"},\"done\":true}\n"), nil
			})
			response, err := provider.Complete(context.Background(), CompletionRequest{Stream: stream, Messages: []ChatMessage{{Role: "user", Content: "hello"}}})
			if err != nil || attempts != 2 {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
			answer := response.Content
			if response.Stream != nil {
				for chunk := range response.Stream {
					answer += chunk
				}
			}
			if answer != "recovered" {
				t.Fatalf("answer=%q", answer)
			}
		})
	}
}

func TestOllamaPreservesNonRetryableContextErrorDetail(t *testing.T) {
	provider := NewOllamaProvider("http://ollama.test", "model", "", map[string]any{"num_ctx": 8192})
	attempts := 0
	provider.client = clientWithRoundTripper(func(req *http.Request) (*http.Response, error) {
		attempts++
		return jsonResponse(400, `{"error":"context length exceeded"}`), nil
	})
	_, err := provider.Complete(context.Background(), CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hello"}}})
	if err == nil || !strings.Contains(err.Error(), "context length exceeded") || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
