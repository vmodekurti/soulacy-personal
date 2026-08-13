package gateway

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/pkg/message"
)

type chatFeedbackRequest struct {
	AgentID    string `json:"agent_id"`
	SessionID  string `json:"session_id"`
	RunID      string `json:"run_id"`
	ResponseID string `json:"response_id"`
	Rating     int    `json:"rating"`
	Comment    string `json:"comment"`
}

// handleChatFeedback records explicit response-level human feedback and feeds
// the signal into procedural macro-memory. Raw comments remain reviewable
// proposals; they never mutate an agent's rules automatically.
func (s *Server) handleChatFeedback(c *fiber.Ctx) error {
	var body chatFeedbackRequest
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	body.AgentID = strings.TrimSpace(body.AgentID)
	body.SessionID = strings.TrimSpace(body.SessionID)
	body.RunID = strings.TrimSpace(body.RunID)
	body.ResponseID = strings.TrimSpace(body.ResponseID)
	body.Comment = strings.TrimSpace(body.Comment)
	if body.AgentID == "" || body.SessionID == "" || body.RunID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "agent_id, session_id, and run_id are required")
	}
	if body.Rating != 1 && body.Rating != -1 {
		return s.errMsg(c, fiber.StatusBadRequest, "rating must be 1 or -1")
	}
	if len(body.Comment) > 2000 {
		return s.errMsg(c, fiber.StatusBadRequest, "comment exceeds 2000 bytes")
	}
	if err := s.claimSession(c, body.AgentID, body.SessionID); err != nil {
		return err
	}
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning feedback store not configured")
	}
	if s.actions != nil {
		events, err := s.actions.Tail(body.AgentID, 5000)
		if err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
		if !feedbackRunExists(events, body.AgentID, body.SessionID, body.RunID) {
			return s.errMsg(c, fiber.StatusNotFound, "completed run not found")
		}
	}
	userID := "local-user"
	if claims := auth.ClaimsFromCtx(c); claims != nil && strings.TrimSpace(claims.Subject) != "" {
		userID = claims.Subject
	}
	item, err := store.AddFeedback(learning.Feedback{
		AgentID: body.AgentID, SessionID: body.SessionID, RunID: body.RunID,
		ResponseID: body.ResponseID, UserID: userID, Rating: body.Rating, Comment: body.Comment,
	})
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if macros := s.macroStore(); macros != nil {
		if err := macros.RecordFeedback(body.RunID, body.Rating); err != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, err)
		}
	}

	var proposal any
	if body.Comment != "" {
		label := "Reinforce"
		if body.Rating < 0 {
			label = "Correct"
		}
		added, addErr := store.Add(learning.Proposal{
			AgentID: body.AgentID, SessionID: body.SessionID, Kind: "procedure",
			Title:   label + " response behavior from user feedback",
			Content: body.Comment, Source: "user_feedback", Confidence: 0.9,
			Meta:      map[string]string{"feedback_id": item.ID, "run_id": body.RunID, "rating": feedbackRatingName(body.Rating)},
			Rationale: "Explicit user feedback tied to a completed response; review before applying it to the agent rulebook.",
		})
		if addErr != nil {
			return s.errJSON(c, fiber.StatusInternalServerError, addErr)
		}
		proposal = learningProposalView(added)
	}
	if s.hub != nil {
		s.hub.Emit(message.Event{Type: "run.feedback", AgentID: body.AgentID, SessionID: body.SessionID,
			Payload:   map[string]any{"run_id": body.RunID, "response_id": body.ResponseID, "rating": body.Rating, "feedback_id": item.ID},
			Timestamp: time.Now().UTC()})
	}
	return c.JSON(fiber.Map{"ok": true, "feedback": item, "proposal": proposal})
}

func (s *Server) handleListChatFeedback(c *fiber.Ctx) error {
	store := s.engine.LearningStore()
	if store == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "learning feedback store not configured")
	}
	limit := c.QueryInt("limit", 100)
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	items, err := store.ListFeedback(strings.TrimSpace(c.Query("agent_id")), limit)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"feedback": items})
}

func feedbackRatingName(rating int) string {
	if rating > 0 {
		return "helpful"
	}
	return "unhelpful"
}

func feedbackRunExists(events []message.Event, agentID, sessionID, runID string) bool {
	for _, event := range events {
		if event.Type != "run.completed" || event.AgentID != agentID || event.SessionID != sessionID {
			continue
		}
		data, _ := json.Marshal(event.Payload)
		var payload struct {
			RunID string `json:"run_id"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.RunID == runID {
			return true
		}
	}
	return false
}
