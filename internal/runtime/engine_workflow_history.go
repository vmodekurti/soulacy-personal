package runtime

import (
	"context"
	"strings"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/pkg/message"
)

// flowHistoryMaxMsgs caps how many recent chat messages a workflow run pulls
// in for conversation continuity (about six user and assistant turns).
const flowHistoryMaxMsgs = 12

func (e *Engine) flowHistoryTranscript(sessionID, agentID string, maxMsgs int) string {
	if sessionID == "" {
		return ""
	}
	sess := e.getOrCreateSession(sessionID, agentID)
	sess.mu.Lock()
	hist := sess.History
	if maxMsgs > 0 && len(hist) > maxMsgs {
		hist = hist[len(hist)-maxMsgs:]
	}
	cp := make([]llm.ChatMessage, len(hist))
	copy(cp, hist)
	sess.mu.Unlock()

	var b strings.Builder
	for _, m := range cp {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		} else if m.Role != "user" {
			continue
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// recordWorkflowTurn gives workflow agents the same conversational continuity
// and durable history as agents that finish through finalizeReply.
func (e *Engine) recordWorkflowTurn(ctx context.Context, msg message.Message, replyText string) {
	userText := flattenParts(msg.Parts)
	sess := e.getOrCreateSession(msg.SessionID, msg.AgentID)
	sess.mu.Lock()
	e.appendHistoryLocked(sess,
		llm.ChatMessage{Role: "user", Content: userText},
		llm.ChatMessage{Role: "assistant", Content: replyText},
	)
	sess.mu.Unlock()

	if e.historyStore == nil {
		return
	}
	if err := e.historyStore.Append(ctx, session.ConversationEntry{
		SessionID: msg.SessionID, AgentID: msg.AgentID, Role: "user", Content: userText,
	}); err != nil {
		e.log.Warn("history store: append workflow user turn failed", zap.Error(err))
	}
	if err := e.historyStore.Append(ctx, session.ConversationEntry{
		SessionID: msg.SessionID, AgentID: msg.AgentID, Role: "assistant", Content: replyText,
	}); err != nil {
		e.log.Warn("history store: append workflow assistant turn failed", zap.Error(err))
	}
}
