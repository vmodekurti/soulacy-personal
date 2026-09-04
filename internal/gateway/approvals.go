package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/metrics"
)

// isTruthy reports whether a header/query string represents an affirmative flag.
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// handleListApprovals returns the tool calls currently awaiting a human
// decision, so any paired device (including the mobile companion) can review and
// resolve them — not just the browser tab that started the run.
func (s *Server) handleListApprovals(c *fiber.Ctx) error {
	principal, _, admin := authenticatedPrincipal(c)
	return c.JSON(fiber.Map{"approvals": s.engine.Broker().ListForPrincipal(principal, admin)})
}

// handleResolveApproval approves or denies a pending tool call by id. `decide`
// is "approve" or "deny". Idempotent-ish: a callID that already resolved or
// timed out returns 404.
func (s *Server) handleResolveApproval(decide bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Params("id")
		if id == "" {
			return s.errMsg(c, fiber.StatusBadRequest, "call id is required")
		}
		principal, _, admin := authenticatedPrincipal(c)
		if !s.engine.Broker().ResolveForPrincipal(id, decide, principal, admin) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "call_id not found — it may have already been resolved or timed out",
			})
		}
		outcome := "denied"
		if decide {
			outcome = "approved"
		}
		metrics.ApprovalsResolvedTotal.WithLabelValues(outcome).Inc()
		return c.JSON(fiber.Map{"ok": true, "approved": decide})
	}
}
