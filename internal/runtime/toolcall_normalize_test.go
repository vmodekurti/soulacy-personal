package runtime

import (
	"reflect"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestNormalizeToolCallCommonAliases(t *testing.T) {
	tests := []struct {
		name string
		call message.ToolCall
		want map[string]any
	}{
		{
			name: "channel send aliases",
			call: message.ToolCall{Name: "channel.send", Arguments: map[string]any{
				"adapter": "slack",
				"chat_id": "C123",
				"message": "hello",
			}},
			want: map[string]any{"channel": "slack", "to": "C123", "text": "hello"},
		},
		{
			name: "queue put aliases",
			call: message.ToolCall{Name: "queue_put", Arguments: map[string]any{
				"name":    "pending_resources",
				"payload": map[string]any{"url": "https://example.com"},
				"ttl":     "60",
			}},
			want: map[string]any{"queue": "pending_resources", "item": map[string]any{"url": "https://example.com"}, "ttl_seconds": "60"},
		},
		{
			name: "knowledge write aliases",
			call: message.ToolCall{Name: "kb_write", Arguments: map[string]any{
				"knowledge_base": "AI Docs",
				"artifact":       "stored text",
				"source_url":     "https://example.com",
			}},
			want: map[string]any{"kb": "AI Docs", "content": "stored text", "source": "https://example.com"},
		},
		{
			name: "fetch url aliases",
			call: message.ToolCall{Name: "fetch_url", Arguments: map[string]any{
				"link": "https://example.com",
			}},
			want: map[string]any{"url": "https://example.com"},
		},
		{
			name: "channel send tool alias with wrapped args",
			call: message.ToolCall{Name: "send_message", Arguments: map[string]any{
				"arguments": map[string]any{
					"platform": "telegram",
					"target":   "123",
					"body":     "queued",
				},
			}},
			want: map[string]any{"channel": "telegram", "to": "123", "text": "queued"},
		},
		{
			name: "teams send alias",
			call: message.ToolCall{Name: "teams.send", Arguments: map[string]any{
				"message": "deployment complete",
			}},
			want: map[string]any{"text": "deployment complete"},
		},
		{
			name: "queue enqueue alias preserves payload as item",
			call: message.ToolCall{Name: "enqueue", Arguments: map[string]any{
				"topic":   "pending_resources",
				"payload": map[string]any{"url": "https://example.com"},
			}},
			want: map[string]any{"queue": "pending_resources", "item": map[string]any{"url": "https://example.com"}},
		},
		{
			name: "knowledge search alias with json wrapper",
			call: message.ToolCall{Name: "knowledge.search", Arguments: map[string]any{
				"args": `{"knowledge_base":"AI Docs","q":"governance","limit":3}`,
			}},
			want: map[string]any{"kb": "AI Docs", "query": "governance", "top_k": float64(3)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeToolCall(tt.call)
			for key, want := range tt.want {
				if !reflect.DeepEqual(got.Arguments[key], want) {
					t.Fatalf("Arguments[%q] = %#v, want %#v; full args=%#v", key, got.Arguments[key], want, got.Arguments)
				}
			}
			if strings.Contains(tt.name, "teams send") && got.Name != "channel.send" {
				t.Fatalf("normalized name = %q, want channel.send", got.Name)
			}
		})
	}
}

func TestReasoningToolErrorObservationIncludesSchemaHint(t *testing.T) {
	x := reasoningToolExecutor{schemas: map[string]llm.ToolSchema{
		"channel.send": {
			Name: "channel.send",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"channel": map[string]any{"type": "string"},
					"to":      map[string]any{"type": "string"},
					"text":    map[string]any{"type": "string"},
				},
				"required": []string{"text"},
			},
		},
	}}

	got := x.toolErrorObservation(message.ToolCall{Name: "channel.send"}, errString("channel.send: text is required"))
	for _, want := range []string{"tool error:", "Expected arguments for channel.send", "text*:string", "Retry once with corrected arguments"} {
		if !strings.Contains(got, want) {
			t.Fatalf("observation missing %q: %s", want, got)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
