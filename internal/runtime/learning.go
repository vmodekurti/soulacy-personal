package runtime

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func (e *Engine) proposeLearning(ctx context.Context, def *agent.Definition, msg message.Message, finalContent string) {
	if e.learningStore == nil || def == nil || !def.Learning.Enabled {
		return
	}
	proposals := learning.BuildProposals(learning.BuildInput{
		AgentID:      msg.AgentID,
		AgentName:    def.Name,
		SessionID:    msg.SessionID,
		Channel:      msg.Channel,
		UserText:     flattenParts(msg.Parts),
		ReplyText:    finalContent,
		Source:       "post_run",
		MinChars:     def.Learning.MinChars,
		MaxProposals: def.Learning.MaxProposals,
	})
	for _, p := range proposals {
		select {
		case <-ctx.Done():
			return
		default:
		}
		p.CreatedAt = time.Now().UTC()
		if _, err := e.learningStore.Add(p); err != nil {
			e.log.Warn("learning proposal failed", zap.String("agent", msg.AgentID), zap.Error(err))
			continue
		}
		e.sink.Emit(message.Event{
			Type:      "learning.proposal",
			AgentID:   msg.AgentID,
			SessionID: msg.SessionID,
			Payload:   p,
			Timestamp: time.Now().UTC(),
		})
	}
}
