// studio_diff.go — preview the SOUL.yaml change a repair (or any edit) makes,
// before the user saves it. Serializes both the current and proposed workflow to
// SOUL.yaml and returns a line diff so the Studio UI can show exactly what will
// change.

package gateway

import (
	"github.com/gofiber/fiber/v2"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/studio"
)

type studioDiffRequest struct {
	Before studio.Draft `json:"before"`
	After  studio.Draft `json:"after"`
}

func (s *Server) handleStudioDiff(c *fiber.Ctx) error {
	var req studioDiffRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	beforeYAML, err := draftToYAML(req.Before)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "before: "+err.Error())
	}
	afterYAML, err := draftToYAML(req.After)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "after: "+err.Error())
	}
	lines, stats, text := studio.DiffYAML(beforeYAML, afterYAML)
	return c.JSON(fiber.Map{
		"before":  beforeYAML,
		"after":   afterYAML,
		"lines":   lines,
		"stats":   stats,
		"unified": text,
	})
}

func draftToYAML(d studio.Draft) (string, error) {
	def, err := studio.ToAgentDefinition(d, true)
	if err != nil {
		return "", err
	}
	out, err := yaml.Marshal(&def)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
