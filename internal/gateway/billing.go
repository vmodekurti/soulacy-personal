package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/entitlements"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/tenancy"
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
		// Billing remediation must remain reachable after an entitlement has
		// expired. The handlers below still require a verified workspace owner;
		// this exemption removes only the circular "pay before you may pay" gate.
		if c.Path() == "/api/v1/billing/checkout" || c.Path() == "/api/v1/billing/portal" {
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

func billingOwner(c *fiber.Ctx) (requestctx.Identity, error) {
	identity, ok := requestIdentity(c)
	if !ok || identity.Role() != tenancy.RoleOwner || identity.PrincipalKind() != "user" {
		return requestctx.Identity{}, fiber.NewError(fiber.StatusForbidden, "workspace owner role is required for billing")
	}
	return identity, nil
}

func (s *Server) handleBillingStatus(c *fiber.Ctx) error {
	identity, err := billingOwner(c)
	if err != nil {
		return err
	}
	cfg := s.config().Billing
	plans := make([]string, 0, len(cfg.StripePrices))
	for plan, price := range cfg.StripePrices {
		if strings.TrimSpace(plan) != "" && strings.TrimSpace(price) != "" {
			plans = append(plans, plan)
		}
	}
	sort.Strings(plans)
	response := fiber.Map{
		"provider": strings.ToLower(strings.TrimSpace(cfg.Provider)), "enforcement": strings.ToLower(strings.TrimSpace(cfg.Enforcement)),
		"configured": s.billingSessions != nil, "default_plan": cfg.DefaultPlan, "plans": plans,
	}
	if s.stripeEntitlementStore == nil {
		response["status"] = "unconfigured"
		return c.JSON(response)
	}
	entitlement, lookupErr := s.stripeEntitlementStore.Get(c.UserContext(), identity.WorkspaceID())
	if errors.Is(lookupErr, entitlements.ErrNotFound) {
		response["status"] = "none"
		response["subscription_required"] = strings.EqualFold(cfg.Enforcement, "strict")
		return c.JSON(response)
	}
	if lookupErr != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "billing status is unavailable")
	}
	response["status"] = entitlement.Status
	response["plan"] = entitlement.Plan
	response["customer_portal_available"] = entitlement.CustomerID != "" && s.billingSessions != nil
	response["updated_at"] = entitlement.UpdatedAt
	return c.JSON(response)
}

func (s *Server) handleBillingCheckout(c *fiber.Ctx) error {
	identity, err := billingOwner(c)
	if err != nil {
		return err
	}
	if s.billingSessions == nil || s.stripeEntitlementStore == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "subscription checkout is not configured")
	}
	var body struct {
		Plan string `json:"plan"`
	}
	if err := c.BodyParser(&body); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid billing request")
	}
	cfg := s.config().Billing
	plan := strings.TrimSpace(body.Plan)
	if plan == "" {
		plan = strings.TrimSpace(cfg.DefaultPlan)
	}
	price := strings.TrimSpace(cfg.StripePrices[plan])
	if plan == "" || price == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "unknown subscription plan")
	}
	customerID := ""
	if current, lookupErr := s.stripeEntitlementStore.Get(c.UserContext(), identity.WorkspaceID()); lookupErr == nil {
		if current.Status == entitlements.StatusActive && strings.TrimSpace(current.SubscriptionID) != "" {
			return s.errMsg(c, fiber.StatusConflict, "active subscriptions must be changed through the billing portal")
		}
		customerID = current.CustomerID
	} else if !errors.Is(lookupErr, entitlements.ErrNotFound) {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "billing status is unavailable")
	}
	session, createErr := s.billingSessions.CreateCheckout(c.UserContext(), entitlements.CheckoutRequest{
		WorkspaceID: identity.WorkspaceID(), Plan: plan, PriceID: price, CustomerID: customerID,
		SuccessURL: cfg.CheckoutSuccessURL, CancelURL: cfg.CheckoutCancelURL,
		IdempotencyKey: billingIdempotencyKey(c, identity.WorkspaceID(), "checkout"),
	})
	s.recordAdminAudit(c, "billing.checkout_created", "workspace", identity.WorkspaceID(), auditOutcome(createErr), map[string]any{"plan": plan})
	if createErr != nil {
		return s.errMsg(c, fiber.StatusBadGateway, "subscription checkout could not be created")
	}
	return c.JSON(session)
}

func (s *Server) handleBillingPortal(c *fiber.Ctx) error {
	identity, err := billingOwner(c)
	if err != nil {
		return err
	}
	if s.billingSessions == nil || s.stripeEntitlementStore == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "billing portal is not configured")
	}
	current, err := s.stripeEntitlementStore.Get(c.UserContext(), identity.WorkspaceID())
	if errors.Is(err, entitlements.ErrNotFound) || strings.TrimSpace(current.CustomerID) == "" {
		return s.errMsg(c, fiber.StatusConflict, "workspace does not have a billing customer yet")
	}
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "billing status is unavailable")
	}
	session, createErr := s.billingSessions.CreatePortal(c.UserContext(), entitlements.PortalRequest{
		CustomerID: current.CustomerID, ReturnURL: s.config().Billing.PortalReturnURL,
		IdempotencyKey: billingIdempotencyKey(c, identity.WorkspaceID(), "portal"),
	})
	s.recordAdminAudit(c, "billing.portal_created", "workspace", identity.WorkspaceID(), auditOutcome(createErr), nil)
	if createErr != nil {
		return s.errMsg(c, fiber.StatusBadGateway, "billing portal could not be created")
	}
	return c.JSON(session)
}

func billingIdempotencyKey(c *fiber.Ctx, workspaceID, operation string) string {
	seed := strings.TrimSpace(c.Get("Idempotency-Key"))
	if seed == "" {
		seed = localString(c.Locals("request_id"))
	}
	if seed == "" {
		seed = uuid.NewString()
	}
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + operation + "\x00" + seed))
	return "soulacy-" + operation + "-" + hex.EncodeToString(sum[:])
}
