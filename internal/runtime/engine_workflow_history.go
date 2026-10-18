package runtime

import (
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
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
