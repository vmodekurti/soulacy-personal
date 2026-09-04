package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/metrics"
)

func (s *Server) handleChannelMetrics(c *fiber.Ctx) error {
	stats, err := metrics.ChannelStats()
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "channel metrics unavailable")
	}
	return c.JSON(stats)
}
