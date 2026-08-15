package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// handleGenericWebhook accepts arbitrary JSON and maps it to an agent message.
//
// POST /api/v1/webhooks/:agent_id
//
// The agent may declare:
//
//	webhook:
//	  text_path: issue.title
//	  user_id_path: sender.login
//	  session_id_path: repository.full_name
//
// Without mapping, common fields such as text/message/body/content/title are
// used, falling back to a compact JSON representation of the whole payload.
func (s *Server) handleGenericWebhook(c *fiber.Ctx) error {
	agentID := strings.TrimSpace(c.Params("agent_id"))
	if agentID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id is required")
	}
	def := s.agents(c).Get(agentID)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}
	if def.Trigger != agent.TriggerWebhook && def.Trigger != agent.TriggerChannel {
		return s.errMsg(c, fiber.StatusBadRequest, "agent is not configured for webhook/channel trigger")
	}

	var payload any
	dec := json.NewDecoder(bytes.NewReader(c.Body()))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	wcfg := def.Webhook
	text := strings.TrimSpace(mappedValue(payload, pathOf(wcfg, "text")))
	if text == "" {
		text = defaultWebhookText(payload)
	}
	if text == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "webhook payload did not contain text and could not be summarized")
	}
	userID := strings.TrimSpace(mappedValue(payload, pathOf(wcfg, "user_id")))
	if userID == "" {
		userID = firstPayloadString(payload, "user_id", "user.id", "sender.id", "sender.login", "actor.login", "from.id")
	}
	if userID == "" {
		userID = "webhook"
	}
	username := strings.TrimSpace(mappedValue(payload, pathOf(wcfg, "username")))
	if username == "" {
		username = firstPayloadString(payload, "username", "user.name", "sender.name", "sender.login", "actor.login", "from.username")
	}
	if username == "" {
		username = userID
	}
	threadID := strings.TrimSpace(mappedValue(payload, pathOf(wcfg, "thread_id")))
	if threadID == "" {
		threadID = firstPayloadString(payload, "thread_id", "conversation.id", "repository.full_name", "repo.name", "id")
	}
	if threadID == "" {
		threadID = userID
	}
	sessionID := strings.TrimSpace(mappedValue(payload, pathOf(wcfg, "session_id")))
	if sessionID == "" {
		sessionID = "webhook-" + sanitizeWebhookID(agentID+"-"+threadID)
	}

	meta := map[string]string{
		"webhook.agent_id":  agentID,
		"webhook.thread_id": threadID,
	}
	if wcfg != nil && wcfg.IncludeRaw {
		meta["webhook.raw"] = truncateWebhookString(compactJSON(payload), 8000)
	}
	msg := message.Message{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		AgentID:   agentID,
		Channel:   "webhook",
		ThreadID:  threadID,
		UserID:    userID,
		Username:  username,
		Role:      message.RoleUser,
		Parts:     message.Text(text),
		Metadata:  meta,
		CreatedAt: time.Now().UTC(),
	}

	ctx, cancel := context.WithTimeout(detachedRequestContext(c), s.resolveRunTimeout(def))
	defer cancel()
	ctx = withRequestPrincipal(c, ctx)
	if s.runReg != nil {
		s.runReg.Register(sessionID, cancel)
		defer s.runReg.Done(sessionID)
	}
	reply, err := s.engine.Handle(ctx, msg)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	replyText := ""
	for _, p := range reply.Parts {
		if p.Type == message.ContentText && p.Text != "" {
			replyText = p.Text
			break
		}
	}
	return c.JSON(fiber.Map{
		"reply":      replyText,
		"parts":      reply.Parts,
		"session_id": sessionID,
		"thread_id":  threadID,
		"user_id":    userID,
	})
}

func pathOf(cfg *agent.WebhookConfig, field string) string {
	if cfg == nil {
		return ""
	}
	switch field {
	case "text":
		return cfg.TextPath
	case "user_id":
		return cfg.UserIDPath
	case "username":
		return cfg.UsernamePath
	case "session_id":
		return cfg.SessionIDPath
	case "thread_id":
		return cfg.ThreadIDPath
	default:
		return ""
	}
}

func defaultWebhookText(payload any) string {
	if s := firstPayloadString(payload, "text", "message", "body", "content", "title", "summary", "description", "issue.title", "alert.title"); s != "" {
		return s
	}
	return compactJSON(payload)
}

func firstPayloadString(payload any, paths ...string) string {
	for _, p := range paths {
		if s := strings.TrimSpace(mappedValue(payload, p)); s != "" {
			return s
		}
	}
	return ""
}

func mappedValue(payload any, path string) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "$."))
	if path == "" {
		return ""
	}
	cur := payload
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return ""
		}
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return ""
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(v) {
				return ""
			}
			cur = v[idx]
		default:
			return ""
		}
	}
	switch v := cur.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64, bool:
		return fmt.Sprint(v)
	default:
		return compactJSON(v)
	}
}

func compactJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func sanitizeWebhookID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
}

func truncateWebhookString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
