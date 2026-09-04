package gateway

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
)

type mobileCompanionCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type mobileCompanionReadiness struct {
	Status            string                 `json:"status"`
	Score             int                    `json:"score"`
	Ready             int                    `json:"ready"`
	Total             int                    `json:"total"`
	PendingApprovals  int                    `json:"pending_approvals"`
	PushSubscriptions int                    `json:"push_subscriptions"`
	ScheduledAgents   int                    `json:"scheduled_agents"`
	ChatAgents        int                    `json:"chat_agents"`
	DeliveryChannels  int                    `json:"delivery_channels"`
	ManagedCredential bool                   `json:"managed_credential"`
	Installable       bool                   `json:"installable"`
	DurableRunLedger  bool                   `json:"durable_run_ledger"`
	RecentRuns        int                    `json:"recent_runs"`
	Checks            []mobileCompanionCheck `json:"checks"`
	NextActions       []string               `json:"next_actions"`
}

func (s *Server) handleMobileStatus(c *fiber.Ctx) error {
	return c.JSON(s.mobileCompanionReadiness())
}

func (s *Server) mobileCompanionReadiness() mobileCompanionReadiness {
	var pendingApprovals int
	if s != nil && s.engine != nil && s.engine.Broker() != nil {
		pendingApprovals = len(s.engine.Broker().List())
	}

	pushSubscriptions := 0
	pushStatus := "warn"
	pushDetail := "Push service is not initialized."
	if svc, err := getPushService(); err == nil && svc != nil {
		pushSubscriptions = svc.Count()
		if pushSubscriptions > 0 {
			pushStatus = "ok"
			pushDetail = fmt.Sprintf("%d paired device(s) can receive approval and run alerts.", pushSubscriptions)
		} else {
			pushDetail = "Push service is ready, but no device has subscribed yet."
		}
	} else if err != nil {
		pushStatus = "fail"
		pushDetail = "Push service failed to initialize: " + err.Error()
	}

	_, _, chatAgents, scheduledAgents, _ := s.agentReadinessCounts()
	deliveryChannels := countUsableOutboundChannels(s.channelDoctorChecks())
	managedCredential := s != nil && s.apiKeyStore != nil
	recentRuns, durableRunLedger := s.mobileRecentRuns()
	installable := true

	checks := []mobileCompanionCheck{
		{
			Key:    "surface",
			Label:  "Companion Surface",
			Status: "ok",
			Detail: "Mobile operations page is available for Pocket Chat, approvals, schedules, delivery, and run review.",
		},
		{
			Key:    "approvals",
			Label:  "Approval Broker",
			Status: statusIf(s != nil && s.engine != nil, "ok", "fail"),
			Detail: statusDetail(s != nil && s.engine != nil, fmt.Sprintf("%d approval(s) pending right now.", pendingApprovals), "Runtime approval broker is unavailable."),
		},
		{
			Key:    "push",
			Label:  "Push Alerts",
			Status: pushStatus,
			Detail: pushDetail,
		},
		{
			Key:    "pairing",
			Label:  "Device Pairing",
			Status: statusIf(managedCredential, "ok", "warn"),
			Detail: statusDetail(managedCredential, "Pairing can issue scoped mobile credentials.", "Pairing works locally, but managed API keys are not enabled for scoped phone tokens."),
		},
		{
			Key:    "pwa_install",
			Label:  "Installable App",
			Status: statusIf(installable, "ok", "warn"),
			Detail: statusDetail(installable, "PWA manifest and service worker are bundled for phone install.", "Mobile is available in browser, but install assets are missing."),
		},
		{
			Key:    "run_review",
			Label:  "Run Review",
			Status: statusIf(durableRunLedger || recentRuns > 0, "ok", "warn"),
			Detail: statusDetail(durableRunLedger || recentRuns > 0, fmt.Sprintf("%d recent run(s) can be reviewed from Mobile.", recentRuns), "Enable durable action logging or run at least one agent so Mobile can review history."),
		},
		{
			Key:    "chat_agents",
			Label:  "Pocket Chat",
			Status: statusIf(chatAgents > 0, "ok", "warn"),
			Detail: countDetail(chatAgents, "chat-ready agent", "No agent is exposed to Mobile Pocket Chat yet."),
		},
		{
			Key:    "schedules",
			Label:  "Scheduled Agents",
			Status: statusIf(scheduledAgents > 0, "ok", "warn"),
			Detail: countDetail(scheduledAgents, "scheduled agent", "No scheduled agents are configured."),
		},
		{
			Key:    "delivery",
			Label:  "Outbound Delivery",
			Status: statusIf(deliveryChannels > 0, "ok", "warn"),
			Detail: countDetail(deliveryChannels, "usable outbound channel", "No usable outbound channel is configured for alerts."),
		},
	}

	ready := 0
	points := 0
	next := make([]string, 0, 4)
	for _, check := range checks {
		switch check.Status {
		case "ok":
			ready++
			points += 100
		case "warn":
			points += 55
			next = append(next, mobileCompanionNextAction(check.Key))
		default:
			next = append(next, mobileCompanionNextAction(check.Key))
		}
	}
	score := 0
	if len(checks) > 0 {
		score = points / len(checks)
	}
	if len(next) > 4 {
		next = next[:4]
	}

	return mobileCompanionReadiness{
		Status:            statusFromScore(score),
		Score:             score,
		Ready:             ready,
		Total:             len(checks),
		PendingApprovals:  pendingApprovals,
		PushSubscriptions: pushSubscriptions,
		ScheduledAgents:   scheduledAgents,
		ChatAgents:        chatAgents,
		DeliveryChannels:  deliveryChannels,
		ManagedCredential: managedCredential,
		Installable:       installable,
		DurableRunLedger:  durableRunLedger,
		RecentRuns:        recentRuns,
		Checks:            checks,
		NextActions:       compactStrings(next),
	}
}

func parityMobileCompanion(m mobileCompanionReadiness) parityArea {
	detail := fmt.Sprintf("%d/%d companion checks are ready: %d push device(s), %d chat agent(s), %d scheduled agent(s), %d outbound channel(s), %d recent run(s).",
		m.Ready, m.Total, m.PushSubscriptions, m.ChatAgents, m.ScheduledAgents, m.DeliveryChannels, m.RecentRuns)
	next := "Use Mobile as the default approval, alert, schedule, and lightweight chat companion."
	if len(m.NextActions) > 0 {
		next = m.NextActions[0]
	}
	return parityArea{
		Key:       "mobile_companion",
		Label:     "Native/Mobile Companion",
		Status:    m.Status,
		Score:     m.Score,
		Detail:    detail,
		Next:      next,
		Benchmark: "OpenClaw",
		Href:      "#mobile",
	}
}

func statusFromScore(score int) string {
	switch {
	case score >= 75:
		return "ok"
	case score >= 45:
		return "warn"
	default:
		return "fail"
	}
}

func statusDetail(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func countDetail(count int, noun, fallback string) string {
	if count <= 0 {
		return fallback
	}
	return fmt.Sprintf("%d %s(s) ready.", count, noun)
}

func mobileCompanionNextAction(key string) string {
	switch key {
	case "approvals":
		return "Start the gateway with the runtime approval broker enabled."
	case "push":
		return "Open Mobile on your phone and enable push notifications."
	case "pairing":
		return "Enable managed API keys so pairing can issue scoped phone credentials."
	case "pwa_install":
		return "Open Mobile on your phone and install Soulacy from the browser menu."
	case "run_review":
		return "Enable durable action logging so Mobile can review cron, chat, and manual runs."
	case "chat_agents":
		return "Expose at least one agent to chat or a channel trigger."
	case "schedules":
		return "Add a scheduled agent so Mobile can operate daily jobs."
	case "delivery":
		return "Configure Telegram, Slack, Discord, Email, Teams, Google Chat, or Webhook for alerts."
	default:
		return "Review the Mobile companion setup."
	}
}

func (s *Server) mobileRecentRuns() (int, bool) {
	if s == nil {
		return 0, false
	}
	rows := []runLedgerRow{}
	durable := false
	if s.actions != nil {
		if q, ok := s.actions.(eventQuerier); ok {
			if events, err := q.QueryEvents("", "", 500, runLedgerEventTypes()); err == nil {
				rows = append(rows, s.buildRunLedger(events, 25)...)
				durable = true
			}
		}
	}
	rows = append(rows, s.flowRunLedgerRows("")...)
	rows = mergeRunLedgerRows(rows, 25)
	return len(rows), durable
}

func compactStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
