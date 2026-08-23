package gateway

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/entitlements"
)

func (s *Server) handleStripeWebhook(c *fiber.Ctx) error {
	if s.stripeEntitlementStore == nil || s.config() == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "billing service unavailable")
	}
	tolerance, _ := time.ParseDuration(s.config().Billing.WebhookTolerance)
	h := entitlements.StripeWebhook{Secret: s.config().Billing.StripeWebhookSecret, Tolerance: tolerance, Store: s.stripeEntitlementStore}
	if err := h.Handle(c.UserContext(), c.Body(), c.Get("Stripe-Signature"), time.Now().UTC()); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// entitlementMW is provider-independent: Stripe, a contract system, or a
// manual grant can all feed the same service. Login and reads remain available
// so owners can diagnose and remediate a billing suspension; mutations and
// executions fail closed when the service itself is unavailable.
func (s *Server) entitlementMW() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if s.entitlementService == nil || !s.authorizationRequired() {
			return c.Next()
		}
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		identity, ok := requestIdentity(c)
		if !ok {
			return s.errMsg(c, fiber.StatusForbidden, "verified workspace membership is required")
		}
		allowed, reason, err := s.entitlementService.Allowed(c.UserContext(), identity.WorkspaceID(), entitlements.CapabilityRuns)
		if err != nil {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "entitlement verification unavailable")
		}
		if !allowed {
			return c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{"error": "workspace subscription does not permit this operation", "reason": strings.TrimSpace(reason)})
		}
		return c.Next()
	}
}
