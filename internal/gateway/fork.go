package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/session"
)

// Chat checkpoints & branching (Story 8, milestone M2).
//
// POST /api/v1/history/:session_id/fork
//
//	{"agent_id": "bot", "upto_entry_id": 42, "new_session_id": "optional"}
//
// Copies the source session's persisted conversation up to (and including)
// the checkpoint entry into a fresh session, seeds the engine's in-memory
// session so the branch continues with full context, and returns the new
// session id plus the copied entries for the GUI to render.

func (s *Server) handleForkSession(c *fiber.Ctx) error {
	if s.historyStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "conversation history not enabled",
		})
	}
	forker, ok := s.historyStore.(*session.SQLiteHistoryStore)
	if !ok {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{
			"error": "history store does not support forking",
		})
	}

	srcSession := c.Params("session_id")
	var body struct {
		AgentID      string `json:"agent_id"`
		UptoEntryID  int64  `json:"upto_entry_id"`
		NewSessionID string `json:"new_session_id"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	if body.UptoEntryID <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "upto_entry_id is required (the checkpoint entry to fork after)",
		})
	}
	newSession := strings.TrimSpace(body.NewSessionID)
	if newSession == "" {
		newSession = fmt.Sprintf("fork-%d", time.Now().UnixNano())
	}

	copied, err := forker.Fork(c.Context(), srcSession, newSession, body.UptoEntryID)
	if err != nil {
		// Target-collision and self-fork are caller errors → 409.
		return s.errJSON(c, fiber.StatusConflict, err)
	}
	if copied == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "source session has no entries at or before that checkpoint",
		})
	}

	entries, err := s.historyStore.Load(c.Context(), newSession, 0)
	if err != nil {
		s.log.Error("fork: load new branch failed", zap.Error(err))
		return s.errMsg(c, fiber.StatusInternalServerError, "fork created but could not be read back")
	}

	// Seed the engine so the next chat turn on the branch has the copied
	// context (the engine builds LLM context from in-memory history).
	if s.engine != nil && body.AgentID != "" {
		s.engine.SeedSessionHistory(body.AgentID, newSession, entries)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"session_id":    newSession,
		"forked_from":   srcSession,
		"upto_entry_id": body.UptoEntryID,
		"copied":        copied,
		"entries":       entries,
	})
}
