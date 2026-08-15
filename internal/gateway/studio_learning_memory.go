package gateway

import (
	"context"

	"github.com/gofiber/fiber/v2"
)

// handleStudioLearningMemory provides review/provenance surfaces instead of
// making learned instructions invisible, permanent system-prompt state.
func (s *Server) handleStudioLearningMemory(c *fiber.Ctx) error {
	owner := studioLearningOwner(c)
	var macros, lessons, preferences any = []any{}, []any{}, []any{}
	if store := s.studio(c).macros(); store != nil {
		macros = store.All()
	}
	if store := s.studio(c).lessons(); store != nil {
		lessons = store.All()
	}
	if store := s.studio(c).preferences(); store != nil {
		preferences = store.RulesFor(owner)
	}
	return c.JSON(fiber.Map{"workflow_patterns": macros, "lessons": lessons, "preferences": preferences, "owner_scope": owner})
}

func (s *Server) handleStudioDeleteLearningMemory(c *fiber.Ctx) error {
	id, kind := c.Params("id"), c.Params("kind")
	var err error
	switch kind {
	case "workflow-pattern":
		if store := s.studio(c).macros(); store != nil {
			err = store.Delete(id)
		}
	case "lesson":
		if store := s.studio(c).lessons(); store != nil {
			ctx, cancel := context.WithTimeout(c.Context(), s.studioLearningTimeout())
			defer cancel()
			err = store.Delete(ctx, id)
		}
	case "preference":
		if store := s.studio(c).preferences(); store != nil {
			err = store.DeleteRule(studioLearningOwner(c), id)
		}
	default:
		return s.errMsg(c, fiber.StatusBadRequest, "unknown learning memory kind")
	}
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"deleted": true, "kind": kind, "id": id})
}
