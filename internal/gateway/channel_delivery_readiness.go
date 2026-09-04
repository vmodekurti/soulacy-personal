package gateway

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/channels"
)

type channelDeliveryTarget struct {
	Family    string `json:"family"`
	AdapterID string `json:"adapter_id"`
	Label     string `json:"label"`
	Mode      string `json:"mode"`
	AgentID   string `json:"agent_id,omitempty"`
	To        string `json:"to,omitempty"`
	Inbound   bool   `json:"inbound"`
	Outbound  bool   `json:"outbound"`
	Connected bool   `json:"connected"`
	Status    string `json:"status"`
	Issue     string `json:"issue,omitempty"`
	Next      string `json:"next,omitempty"`
}

type channelDeliveryReadiness struct {
	Status      string                  `json:"status"`
	Score       int                     `json:"score"`
	Ready       int                     `json:"ready"`
	Total       int                     `json:"total"`
	Targets     []channelDeliveryTarget `json:"targets"`
	NextActions []string                `json:"next_actions,omitempty"`
}

func (s *Server) handleChannelDeliveryReadiness(c *fiber.Ctx) error {
	return c.JSON(s.channelDeliveryReadiness())
}

func (s *Server) channelDeliveryReadiness() channelDeliveryReadiness {
	statuses := map[string]channels.AdapterStatus{}
	if s != nil && s.channels != nil {
		statuses = s.channels.Statuses()
	}
	targets := make([]channelDeliveryTarget, 0)
	for _, spec := range channelSpecs {
		if spec.Always {
			continue
		}
		cfg := map[string]any{}
		if s != nil && s.cfg != nil && s.cfg.Channels != nil {
			cfg = s.cfg.Channels[spec.ID]
		}
		if cfg == nil {
			cfg = map[string]any{}
		}
		enabled := channelEnabled(spec, cfg)
		defaultTo := channelDefaultDestination(cfg, spec.ID, spec.ID)
		if spec.ID == "webhook" && defaultTo == "" && valuePresent(cfg["url"]) {
			defaultTo = "configured webhook URL"
		}
		if spec.ID == "teams" && defaultTo == "" && (valuePresent(cfg["webhook_url"]) || valuePresent(cfg["url"])) {
			defaultTo = "configured Teams webhook"
		}
		if spec.ID == "google_chat" && defaultTo == "" && (valuePresent(cfg["webhook_url"]) || valuePresent(cfg["url"])) {
			defaultTo = "configured Google Chat webhook"
		}
		if channelHasDefaultDeliveryTarget(spec.ID, cfg) {
			targets = append(targets, s.channelDeliveryTarget(spec, spec.ID, "Default outbound", "default", "", defaultTo, false, true, enabled, statuses))
		}
		if channelSupportsBots(spec.ID) {
			bots := maskChannelBots(spec, cfg, statuses, s.loader)
			for _, bot := range bots {
				adapterID := deliveryString(bot["_adapter_id"])
				agentID := deliveryString(bot["agent_id"])
				outboundOnly := channels.ParseBoolValue(bot["outbound_only"], false)
				to := deliveryString(bot["default_output_to"])
				label := deliveryString(bot["bot_name"])
				if label == "" {
					label = adapterID
				}
				targets = append(targets, s.channelDeliveryTarget(spec, adapterID, label, "bot", agentID, to, !outboundOnly, true, enabled, statuses))
			}
		}
	}
	ready := 0
	next := make([]string, 0)
	state := "ok"
	for _, target := range targets {
		switch target.Status {
		case "ok":
			ready++
		case "warn":
			if state == "ok" {
				state = "warn"
			}
			next = append(next, target.Next)
		default:
			state = "fail"
			next = append(next, target.Next)
		}
	}
	if len(targets) == 0 {
		state = "warn"
		next = append(next, "Configure at least one delivery channel or bot mapping.")
	}
	score := 0
	if len(targets) > 0 {
		score = int(float64(ready) / float64(len(targets)) * 100)
	}
	return channelDeliveryReadiness{
		Status:      state,
		Score:       score,
		Ready:       ready,
		Total:       len(targets),
		Targets:     targets,
		NextActions: uniqueStrings(next),
	}
}

func channelHasDefaultDeliveryTarget(channelID string, cfg map[string]any) bool {
	if cfg == nil {
		return false
	}
	if channelID == "webhook" && valuePresent(cfg["url"]) {
		return true
	}
	if channelID == "teams" && (valuePresent(cfg["webhook_url"]) || valuePresent(cfg["url"])) {
		return true
	}
	if channelID == "google_chat" && (valuePresent(cfg["webhook_url"]) || valuePresent(cfg["url"])) {
		return true
	}
	return valuePresent(cfg["default_output_to"]) || valuePresent(cfg["token"]) || valuePresent(cfg["bot_token"]) || valuePresent(cfg["access_token"])
}

func (s *Server) channelDeliveryTarget(spec channelSpec, adapterID, label, mode, agentID, to string, inbound, outbound, enabled bool, statuses map[string]channels.AdapterStatus) channelDeliveryTarget {
	st, registered := statuses[adapterID]
	target := channelDeliveryTarget{
		Family:    spec.ID,
		AdapterID: adapterID,
		Label:     label,
		Mode:      mode,
		AgentID:   agentID,
		To:        strings.TrimSpace(to),
		Inbound:   inbound,
		Outbound:  outbound,
		Connected: registered && st.Connected,
		Status:    "ok",
	}
	if !enabled {
		target.Status = "warn"
		target.Issue = "Channel is disabled."
		target.Next = "Enable the channel and restart the gateway."
		return target
	}
	if !registered {
		target.Status = "fail"
		target.Issue = "Adapter is not registered in the live gateway."
		target.Next = "Restart the gateway after saving channel settings."
		return target
	}
	if !st.Connected {
		target.Status = "warn"
		target.Issue = "Adapter is not connected."
		if strings.TrimSpace(st.Detail) != "" {
			target.Issue += " " + strings.TrimSpace(st.Detail)
		}
		target.Next = "Run Delivery Doctor for this channel and check credentials, allowlists, and bot mappings."
		return target
	}
	if inbound && strings.TrimSpace(agentID) == "" {
		target.Status = "fail"
		target.Issue = "Interactive bot mapping has no target agent."
		target.Next = "Select an agent for this bot mapping or mark it send-only."
		return target
	}
	if outbound && strings.TrimSpace(to) == "" && adapterID != "webhook" && adapterID != "teams" && adapterID != "google_chat" {
		target.Status = "fail"
		target.Issue = "Outbound destination is missing."
		target.Next = "Set default_output_to for this channel or bot mapping."
		return target
	}
	return target
}

func channelEnabled(spec channelSpec, cfg map[string]any) bool {
	if spec.Always {
		return true
	}
	if v, ok := cfg["enabled"].(bool); ok {
		return v
	}
	return false
}

func deliveryString(v any) string {
	if !valuePresent(v) {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
