package gateway

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/webpush"
)

// pushService is a lazily-initialized, process-wide Web Push service. Keys and
// subscriptions live under the workspace so they survive restarts. It powers the
// mobile companion's notifications (e.g. a tool needs approval).
var (
	pushOnce sync.Once
	pushSvc  *webpush.Service
	pushErr  error
)

func getPushService() (*webpush.Service, error) {
	pushOnce.Do(func() {
		ws, err := config.ResolveWorkspace()
		if err != nil {
			pushErr = err
			return
		}
		keyPath := filepath.Join(ws.Root, "push", "vapid.key")
		subsPath := filepath.Join(ws.Root, "push", "subscriptions.jsonl")
		pub, priv, kerr := webpush.LoadOrCreateKeys(keyPath)
		if kerr != nil {
			pushErr = kerr
			return
		}
		pushSvc, pushErr = webpush.NewService(pub, priv, "mailto:admin@localhost", subsPath)
		if pushErr == nil && pushSvc != nil {
			// Register the process-wide default so the run-failure notifier and
			// scheduler (which don't hold this *Service) can also fire pushes.
			webpush.SetDefault(pushSvc)
		}
	})
	return pushSvc, pushErr
}

// wirePushNotifications connects the approval broker to the push service so a new
// pending tool approval fans out a notification to every paired device. Called
// once during route setup; a push init failure is non-fatal.
func (s *Server) wirePushNotifications() {
	svc, err := getPushService()
	if err != nil {
		svc = nil
	}
	s.engine.Broker().SetOnRegister(func(p runtime.PendingApproval) {
		s.notifyPhonesOfApproval(p)
		if svc == nil || svc.Count() == 0 {
			return
		}
		res := svc.NotifyDetailed(webpush.Notification{
			Title: "Approval needed",
			Body:  approvalBody(p),
			URL:   "/mobile",
			Tag:   "approval-" + p.CallID,
		})
		metrics.PushSentTotal.WithLabelValues("sent").Add(float64(res.Sent))
		if res.Gone > 0 {
			metrics.PushSentTotal.WithLabelValues("gone").Add(float64(res.Gone))
		}
		metrics.PushSubscriptions.Set(float64(svc.Count()))
	})
}

func approvalBody(p runtime.PendingApproval) string {
	who := p.AgentID
	if who == "" {
		who = "An agent"
	}
	reason := p.Reason
	if reason == "" {
		reason = "wants to run " + p.Tool
	}
	return who + ": " + reason
}

// handlePushPublicKey returns the VAPID application server key browsers need to
// subscribe.
func (s *Server) handlePushPublicKey(c *fiber.Ctx) error {
	svc, err := getPushService()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "push unavailable: "+err.Error())
	}
	return c.JSON(fiber.Map{"public_key": svc.PublicKey()})
}

// handlePushSubscribe stores a browser PushSubscription.
func (s *Server) handlePushSubscribe(c *fiber.Ctx) error {
	svc, err := getPushService()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "push unavailable: "+err.Error())
	}
	var sub webpush.Subscription
	if err := c.BodyParser(&sub); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid subscription JSON")
	}
	if err := svc.Subscribe(sub); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	metrics.PushSubscriptions.Set(float64(svc.Count()))
	return c.JSON(fiber.Map{"ok": true, "subscriptions": svc.Count()})
}

// handlePushUnsubscribe removes a subscription by endpoint.
func (s *Server) handlePushUnsubscribe(c *fiber.Ctx) error {
	svc, err := getPushService()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "push unavailable")
	}
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := c.BodyParser(&body); err != nil || strings.TrimSpace(body.Endpoint) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "endpoint is required")
	}
	if err := svc.Unsubscribe(body.Endpoint); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}

// handlePushTest sends a test notification to all subscriptions.
func (s *Server) handlePushTest(c *fiber.Ctx) error {
	svc, err := getPushService()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "push unavailable")
	}
	sent := svc.Notify(webpush.Notification{Title: "Soulacy", Body: "Push notifications are working.", URL: "/mobile"})
	return c.JSON(fiber.Map{"ok": true, "sent": sent})
}

// notifyPhonesOfApproval pushes an actionable approval notification to the
// requesting owner's paired phones (E50). The phone attaches Approve / Deny
// actions to the SOULACY_APPROVAL category and answers through the same
// approvals API as the web, so the broker sees one decision either way.
func (s *Server) notifyPhonesOfApproval(p runtime.PendingApproval) {
	adapter := mobilechan.DefaultAdapter()
	if adapter == nil || !adapter.CanPush() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.httpRequestTimeout())
	defer cancel()
	n := mobilechan.Notification{
		Title:         "Approval needed",
		Body:          approvalBody(p),
		Category:      mobilechan.CategoryApproval,
		ThreadID:      "approval-" + p.AgentID,
		DeepLink:      "soulacy://approval/" + p.CallID,
		TimeSensitive: true,
		Data:          map[string]string{"call_id": p.CallID, "agent_id": p.AgentID, "session_id": p.SessionID, "tool": p.Tool},
	}
	if err := adapter.Notify(ctx, "personal", approvalDestination(p.Principal), n); err != nil {
		s.log.Warn("approval push to phones failed", zap.String("call_id", p.CallID), zap.Error(err))
	}
}

// approvalDestination maps the broker principal ("admin" or "role:subject")
// onto the mobile store's user destination; unknown principals fan out to
// every paired phone in this single-owner workspace.
func approvalDestination(principal string) string {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return ""
	}
	if i := strings.Index(principal, ":"); i >= 0 {
		principal = principal[i+1:]
	}
	return "user:" + principal
}
