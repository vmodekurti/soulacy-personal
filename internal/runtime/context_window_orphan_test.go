package runtime

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/message"
	llm "github.com/soulacy/soulacy/sdk/llm"
)

// A huge tool result that forces trimming to drop the user + assistant turns
// must NOT leave an orphaned tool (function-response) message at the front —
// that produces a provider 400 ("function response with no preceding call").
func TestTrimMessagesToFit_NoOrphanLeadingToolResult(t *testing.T) {
	huge := strings.Repeat("x", 50000)
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "please read this URL"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "c1", Name: "fetch_url"}}},
		{Role: "tool", Content: huge, ToolCallID: "c1", Name: "fetch_url"},
	}
	out, dropped := trimMessagesToFit(msgs, nil, 200)
	if dropped == 0 {
		t.Fatalf("expected trimming to drop messages")
	}
	for i, m := range out {
		if i == 0 && m.Role == "tool" {
			t.Fatalf("surviving history starts with an orphan tool result")
		}
		if m.Role == "tool" {
			// Every surviving tool result must follow an assistant turn.
			if out[i-1].Role != "assistant" {
				t.Fatalf("tool result at %d not preceded by assistant turn (got %q)", i, out[i-1].Role)
			}
		}
	}
}
