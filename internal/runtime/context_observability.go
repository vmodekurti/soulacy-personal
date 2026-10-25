package runtime

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	sdkhooks "github.com/soulacy/soulacy/sdk/hooks"
)

// ContextReport explains the context decision made before one model call.
// It records counts and provenance rather than prompt text.
type ContextReport struct {
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	Phase           string          `json:"phase,omitempty"`
	ContextLimit    int             `json:"context_limit_tokens"`
	InputBudget     int             `json:"input_budget_tokens"`
	EstimatedBefore int             `json:"estimated_tokens_before"`
	EstimatedAfter  int             `json:"estimated_tokens_after"`
	MessagesBefore  int             `json:"messages_before"`
	MessagesAfter   int             `json:"messages_after"`
	DroppedMessages int             `json:"dropped_messages"`
	ToolCount       int             `json:"tool_count"`
	Roles           map[string]int  `json:"roles"`
	Sources         map[string]bool `json:"sources"`
	Compacted       bool            `json:"compacted"`
}

func buildContextReport(def *agent.Definition, phase string, before, after []llm.ChatMessage, tools []llm.ToolSchema, contextLimit, inputBudget, dropped int) ContextReport {
	report := ContextReport{
		Provider: def.LLM.Provider, Model: def.LLM.Model, Phase: phase,
		ContextLimit: contextLimit, InputBudget: inputBudget,
		EstimatedBefore: estimateTokens(before, tools), EstimatedAfter: estimateTokens(after, tools),
		MessagesBefore: len(before), MessagesAfter: len(after), DroppedMessages: dropped,
		ToolCount: len(tools), Roles: map[string]int{}, Sources: map[string]bool{"system_prompt": true},
		Compacted: dropped > 0 || len(after) < len(before),
	}
	for _, msg := range after {
		report.Roles[msg.Role]++
		content := strings.ToLower(msg.Content)
		switch {
		case strings.Contains(content, "## memory"):
			report.Sources["session_memory"] = true
		case strings.Contains(content, "## relevant past conversations"):
			report.Sources["past_conversations"] = true
		}
		if msg.Role != "system" {
			report.Sources["conversation_history"] = true
		}
	}
	if len(tools) > 0 {
		report.Sources["tools"] = true
	}
	return report
}

func (e *Engine) observeContext(ctx context.Context, msg message.Message, report ContextReport) error {
	eventType := "context.prepared"
	if report.Compacted {
		eventType = "context.compacted"
	}
	if e.sink != nil {
		e.sink.Emit(message.Event{Type: eventType, AgentID: msg.AgentID, SessionID: msg.SessionID, Payload: report, Timestamp: time.Now().UTC()})
	}
	_ = e.runLifecycleHook(context.WithoutCancel(ctx), sdkhooks.ContextAfter, msg.AgentID, msg.SessionID, map[string]any{
		"phase": report.Phase, "provider": report.Provider, "model": report.Model,
		"context_limit_tokens": report.ContextLimit, "input_budget_tokens": report.InputBudget,
		"estimated_tokens": report.EstimatedAfter, "messages": report.MessagesAfter,
		"dropped_messages": report.DroppedMessages, "tool_count": report.ToolCount,
		"sources": report.Sources,
	})
	return nil
}

func (e *Engine) prepareContextForCall(ctx context.Context, def *agent.Definition, msg message.Message, phase string, messages []llm.ChatMessage, tools []llm.ToolSchema, contextLimit, inputBudget int) ([]llm.ChatMessage, error) {
	before := append([]llm.ChatMessage(nil), messages...)
	trimmed, dropped := compactMessagesToFit(messages, tools, inputBudget)
	if dropped > 0 {
		messages = trimmed
		e.log.Warn("engine: trimmed history to fit context window",
			zap.String("agent", msg.AgentID), zap.String("model", def.LLM.Model),
			zap.Int("dropped_messages", dropped), zap.Int("input_budget_tokens", inputBudget))
	}
	report := buildContextReport(def, phase, before, messages, tools, contextLimit, inputBudget, dropped)
	return messages, e.observeContext(ctx, msg, report)
}
