package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/connectors"
)

// handleListConnectors returns product-safe setup metadata. It deliberately
// contains secret slot names only. Values remain behind the Secrets API.
func (s *Server) handleListConnectors(c *fiber.Ctx) error {
	query := strings.TrimSpace(c.Query("q"))
	category := strings.TrimSpace(c.Query("category"))
	items := connectors.Filter(query, category)
	if items == nil {
		items = []connectors.Definition{}
	}
	return c.JSON(fiber.Map{
		"connectors": items,
		"categories": connectors.Categories(),
		"count":      len(items),
	})
}
