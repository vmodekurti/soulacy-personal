package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/soulacy/soulacy/pkg/message"
)

type opsAlertCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type opsAlertReadiness struct {
	Status      string          `json:"status"`
	Channel     string          `json:"channel"`
	To          string          `json:"to"`
	MinStatus   string          `json:"min_status"`
	Checks      []opsAlertCheck `json:"checks"`
	NextActions []string        `json:"next_actions,omitempty"`
}

func (s *Server) handleOpsAlertStatus(c *fiber.Ctx) error {
	return c.JSON(s.opsAlertReadiness())
}

func (s *Server) handleOpsAlertTest(c *fiber.Ctx) error {
	alerts := s.opsAlertReadiness()
	if alerts.Status != "ok" {
		return s.errMsg(c, fiber.StatusBadRequest, "ops alert destination is not ready: "+strings.Join(alerts.NextActions, "; "))
	}
	text := "Soulacy ops alert test at " + time.Now().UTC().Format(time.RFC3339) + ". SLO and budget alert delivery is configured."
	if err := s.sendOpsAlert(c.UserContext(), alerts, text, "ops.alerts.test"); err != nil {
		return s.errMsg(c, fiber.StatusBadGateway, "ops alert test failed: "+err.Error())
	}
	return c.JSON(fiber.Map{"ok": true, "channel": alerts.Channel, "to": alerts.To})
}

func (s *Server) handleOpsAlertEvaluate(c *fiber.Ctx) error {
	alerts := s.opsAlertReadiness()
	if alerts.Status != "ok" {
		return c.JSON(fiber.Map{
			"ok":      false,
			"sent":    false,
			"status":  alerts.Status,
			"reason":  "ops alert destination is not ready",
			"actions": alerts.NextActions,
		})
	}
	costs := s.costReadiness(c)
	slo := s.sloReadiness(c)
	shouldSend, reason := opsAlertShouldSend(alerts.MinStatus, costs.Status, slo.Status)
	if !shouldSend {
		return c.JSON(fiber.Map{"ok": true, "sent": false, "reason": reason, "costs": costs.Status, "slo": slo.Status})
	}
	text := opsAlertText(costs, slo)
	if err := s.sendOpsAlert(c.UserContext(), alerts, text, "ops.alerts.evaluate"); err != nil {
		return s.errMsg(c, fiber.StatusBadGateway, "ops alert delivery failed: "+err.Error())
	}
	return c.JSON(fiber.Map{"ok": true, "sent": true, "reason": reason, "channel": alerts.Channel, "to": alerts.To})
}

func (s *Server) opsAlertReadiness() opsAlertReadiness {
	channel := ""
	to := ""
	minStatus := "fail"
	if s != nil && s.cfg != nil {
		channel = strings.TrimSpace(s.cfg.Ops.AlertChannel)
		to = strings.TrimSpace(s.cfg.Ops.AlertTo)
		if raw := strings.TrimSpace(s.cfg.Ops.AlertMinStatus); raw != "" {
			minStatus = normalizeAlertStatus(raw)
		}
	}
	checks := []opsAlertCheck{
		{
			Key:    "destination",
			Label:  "Alert destination",
			Status: statusIf(channel != "" && to != "", "ok", "warn"),
			Detail: opsAlertDestinationDetail(channel, to),
		},
		{
			Key:    "registry",
			Label:  "Channel registry",
			Status: statusIf(s != nil && s.channels != nil, "ok", "fail"),
			Detail: statusDetail(s != nil && s.channels != nil, "Channel adapters are loaded.", "Channel registry is unavailable."),
		},
	}
	if channel != "" && to == "" && s != nil && s.cfg != nil {
		to = channelDefaultDestination(s.cfg.Channels[channel], channel, channel)
		if to != "" {
			checks[0].Status = "ok"
			checks[0].Detail = "Uses the default outbound destination for " + channel + "."
		}
	}
	if s != nil && s.channels != nil && channel != "" {
		statuses := s.channels.Statuses()
		status, registered := statuses[channel]
		checks = append(checks, opsAlertCheck{
			Key:    "adapter",
			Label:  "Adapter registered",
			Status: statusIf(registered, "ok", "fail"),
			Detail: statusDetail(registered, "Channel adapter "+channel+" is registered.", "Channel adapter "+channel+" is not registered; restart after saving channel settings."),
		})
		checks = append(checks, opsAlertCheck{
			Key:    "connected",
			Label:  "Adapter connected",
			Status: statusIf(registered && status.Connected, "ok", "warn"),
			Detail: statusDetail(registered && status.Connected, "Channel adapter reports live.", "Channel adapter is not live; delivery may fail until credentials, bot mappings, or network are fixed."),
		})
	}

	ready := 0
	state := "ok"
	next := make([]string, 0)
	for _, check := range checks {
		switch check.Status {
		case "ok":
			ready++
		case "warn":
			if state == "ok" {
				state = "warn"
			}
			next = append(next, opsAlertNextAction(check.Key))
		default:
			state = "fail"
			next = append(next, opsAlertNextAction(check.Key))
		}
	}
	if ready == 0 && len(checks) > 0 {
		state = "fail"
	}
	return opsAlertReadiness{
		Status:      state,
		Channel:     channel,
		To:          to,
		MinStatus:   minStatus,
		Checks:      checks,
		NextActions: uniqueStrings(next),
	}
}

func (s *Server) sendOpsAlert(ctx context.Context, alerts opsAlertReadiness, text, source string) error {
	if s == nil || s.channels == nil {
		return fmt.Errorf("channel registry is unavailable")
	}
	msg := message.Message{
		ID:        uuid.New().String(),
		SessionID: "ops-alert-" + uuid.New().String(),
		AgentID:   "system",
		Channel:   alerts.Channel,
		ThreadID:  alerts.To,
		UserID:    "ops",
		Username:  "ops",
		Role:      message.RoleAssistant,
		Parts:     message.Text(text),
		Metadata:  map[string]string{"source": source, "min_status": alerts.MinStatus},
		CreatedAt: time.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	return s.channels.Send(ctx, msg)
}

func opsAlertShouldSend(minStatus, costStatus, sloStatus string) (bool, string) {
	minRank := alertRank(normalizeAlertStatus(minStatus))
	if alertRank(costStatus) >= minRank {
		return true, "cost posture is " + costStatus
	}
	if alertRank(sloStatus) >= minRank {
		return true, "SLO posture is " + sloStatus
	}
	return false, fmt.Sprintf("cost posture is %s and SLO posture is %s, below alert threshold %s", costStatus, sloStatus, normalizeAlertStatus(minStatus))
}

func opsAlertText(costs costReadiness, slo sloReadiness) string {
	lines := []string{
		"## Soulacy Ops Alert",
		fmt.Sprintf("- Cost posture: **%s** (24h $%.2f, 30d $%.2f)", costs.Status, costs.Last24hUSD, costs.Last30dUSD),
		fmt.Sprintf("- SLO posture: **%s** (%s window)", slo.Status, slo.Window),
	}
	if slo.Summary != nil {
		lines = append(lines, fmt.Sprintf("- Recent runs: %d total, %.1f%% failed, %.1f%% incomplete, P95 %s", slo.Summary.TotalRuns, slo.Summary.FailureRate*100, slo.Summary.IncompleteRate*100, humanDurationMS(slo.Summary.P95DurationMS)))
	}
	for _, action := range append(costs.NextActions, slo.NextActions...) {
		if strings.TrimSpace(action) != "" {
			lines = append(lines, "- Next: "+action)
			break
		}
	}
	return strings.Join(lines, "\n")
}

func opsAlertDestinationDetail(channel, to string) string {
	if channel == "" {
		return "No ops alert channel configured."
	}
	if to == "" {
		return "Ops alert channel is " + channel + ", but no alert_to or default outbound destination is configured."
	}
	return "Ops alerts will send to " + channel + " destination " + to + "."
}

func opsAlertNextAction(key string) string {
	switch key {
	case "destination":
		return "Configure ops.alert_channel and ops.alert_to in Config, or set a default outbound destination for the channel."
	case "registry":
		return "Restart the gateway so channel adapters are loaded before testing alerts."
	case "adapter":
		return "Enable the selected channel and restart the gateway."
	case "connected":
		return "Open Channels and run Delivery Doctor for the selected alert channel."
	default:
		return "Review ops alert settings in Config."
	}
}

func normalizeAlertStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "warn", "warning":
		return "warn"
	case "fail", "failed", "error", "critical":
		return "fail"
	default:
		return "fail"
	}
}

func alertRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "warn":
		return 1
	case "fail", "failed", "error", "critical":
		return 2
	default:
		return 0
	}
}
