package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/soulacy/soulacy/internal/jsoncontract"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// enforceOutputSchema permits one repair and validates it locally too. Invalid
// JSON must never be persisted or delivered as a successful structured answer.
func (e *Engine) enforceOutputSchema(ctx context.Context, def *agent.Definition, msg message.Message, history []llm.ChatMessage, content string) (string, error) {
	validate := func(text string) error {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		return jsoncontract.Validate(def.LLM.OutputSchema, value)
	}
	if err := validate(content); err != nil {
		e.sink.Emit(message.Event{Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{"stage": "output-schema", "error": err.Error(), "retry": true}, Timestamp: time.Now().UTC()})
		content = e.finalSynthesisStructured(ctx, def, msg.AgentID, msg.SessionID, history, content, err)
		if err := validate(content); err != nil {
			return "", fmt.Errorf("output schema validation failed after one repair: %w", err)
		}
	}
	return content, nil
}
