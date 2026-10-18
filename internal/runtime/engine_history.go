package runtime

import (
	"context"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
)

func (e *Engine) recordWorkflowTurn(ctx context.Context, msg message.Message, replyText string, metadata ...map[string]string) {
	replyMetadata := map[string]string{"run_id": msg.ID}
	if len(metadata) > 0 {
		for key, value := range metadata[0] {
			replyMetadata[key] = value
		}
	}
	userText := flattenParts(msg.Parts)
	sess := e.getOrCreateSession(msg.SessionID, msg.AgentID)
	sess.mu.Lock()
	e.appendHistoryLocked(sess,
		llm.ChatMessage{Role: "user", Content: userText},
		llm.ChatMessage{Role: "assistant", Content: replyText},
	)
	sess.mu.Unlock()

	if e.historyStore != nil {
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			SessionID: msg.SessionID, AgentID: msg.AgentID, Role: "user", Content: userText,
		}); err != nil {
			e.log.Warn("history store: append workflow user turn failed", zap.Error(err))
		}
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			SessionID: msg.SessionID, AgentID: msg.AgentID, Role: "assistant", Content: replyText, Metadata: replyMetadata,
		}); err != nil {
			e.log.Warn("history store: append workflow assistant turn failed", zap.Error(err))
		}
	}
}
