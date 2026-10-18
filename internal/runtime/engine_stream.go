package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

// collectCompletionStream relays partial text to the live event stream while
// preserving the complete response for history, memory, and final delivery.
func (e *Engine) collectCompletionStream(ctx context.Context, resp *llm.CompletionResponse, agentID, sessionID string, cb func(string)) error {
	if resp == nil || resp.Stream == nil {
		return nil
	}
	var content strings.Builder
	for token := range resp.Stream {
		if cb != nil {
			cb(token)
		}
		e.sink.Emit(message.Event{
			Type: "assistant.delta", AgentID: agentID, SessionID: sessionID,
			Payload: map[string]any{"text": token}, Timestamp: time.Now().UTC(),
		})
		content.WriteString(token)
	}
	resp.Content = content.String()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: streaming model response interrupted: %w", err)
	}
	return nil
}
