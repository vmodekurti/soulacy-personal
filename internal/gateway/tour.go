package gateway

// tour.go — GET /api/v1/tour/:page
//
// Serves the per-page walkthrough. The narrative itself lives in internal/tour,
// which is pure; this file only gathers the install snapshot it narrates
// against, from the same sources the Dashboard and readiness already read.

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/tour"
)

// installSnapshot counts what exists. Every lookup is defensive: a subsystem
// that is disabled or failed to start reports zero rather than breaking the
// tour, because a tour that 500s is worse than one that assumes less.
func (s *Server) installSnapshot() tour.InstallState {
	var st tour.InstallState

	if s.llmRouter != nil {
		st.Providers = len(s.llmRouter.ProviderIDs())
	}
	if s.loader != nil {
		for _, a := range s.loader.All() {
			if a == nil || isProtectedSystemAgent(a.ID) {
				continue // the built-in system agent is not the user's work
			}
			st.Agents++
			if a.Enabled {
				st.EnabledAgents++
			}
			if a.Schedule != nil {
				st.Schedules++
			}
		}
	}
	// Delivery channels: configured, and able to carry a result OUT. HTTP is
	// an inbound surface, so counting it here would tell someone they had a
	// destination when they do not — the exact confusion the Save step throws.
	for id, cfg := range s.cfg.Channels {
		if len(cfg) == 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(id)) {
		case "http", "webhook", "":
			continue
		}
		st.DeliveryChannels++
	}
	if s.mcp != nil {
		st.MCPServers = len(s.mcp.ServersSnapshot())
	}
	st.Plugins = len(s.cfg.PluginDirs)

	return st
}

// handleTour implements GET /api/v1/tour/:page.
func (s *Server) handleTour(c *fiber.Ctx) error {
	page := strings.TrimSpace(c.Params("page"))
	story, ok := tour.Narrate(page, s.installSnapshot())
	if !ok {
		return s.errMsg(c, fiber.StatusNotFound, "no tour for \""+page+"\"")
	}
	return c.JSON(story)
}
