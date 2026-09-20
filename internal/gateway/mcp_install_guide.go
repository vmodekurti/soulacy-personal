package gateway

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/mcpinstall"
)

const mcpInstallGuideTimeout = 30 * time.Second

// handleMCPInstallGuide inspects declarative files in an MCP repository and
// recommends where that server should run. It is read-only: no repository code
// is executed and no Soulacy configuration is changed.
func (s *Server) handleMCPInstallGuide(c *fiber.Ctx) error {
	var body struct {
		SourceURL string `json:"source_url"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	body.SourceURL = strings.TrimSpace(body.SourceURL)
	u, err := url.ParseRequestURI(body.SourceURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "source_url must be a public HTTPS Git repository URL without embedded credentials")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), mcpInstallGuideTimeout)
	defer cancel()
	recommendation, err := mcpinstall.AnalyzeRepository(ctx, body.SourceURL)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(recommendation)
}
