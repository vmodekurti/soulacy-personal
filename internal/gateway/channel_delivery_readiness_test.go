package gateway

import (
	"net/http"
	"testing"
)

func TestChannelDeliveryReadinessListsDefaultAndBotTargets(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Channels = map[string]map[string]any{
		"telegram": {
			"enabled":           true,
			"token":             "tok",
			"default_output_to": "-10042",
			"bots": []any{
				map[string]any{
					"bot_name":          "Research Bot",
					"agent_id":          "research-librarian",
					"token":             "tok2",
					"default_output_to": "-10043",
				},
			},
		},
	}
	s.channels.Register(&fakeOpsAlertAdapter{id: "telegram", live: true})
	s.channels.Register(&fakeOpsAlertAdapter{id: "telegram-research-librarian", live: true})
	s.channels.Register(&fakeOpsAlertAdapter{id: "mobile", live: true})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels/delivery-readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delivery readiness = %d body=%v", status, body)
	}
	if body["status"] != "ok" || body["ready"] != float64(3) || body["total"] != float64(3) {
		t.Fatalf("unexpected readiness summary: %v", body)
	}
	targets, ok := body["targets"].([]any)
	if !ok || len(targets) != 3 {
		t.Fatalf("expected 3 targets, got %v", body["targets"])
	}
}

func TestChannelDeliveryReadinessFlagsMissingDestination(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Channels = map[string]map[string]any{
		"slack": {
			"enabled": true,
			"token":   "tok",
		},
	}
	s.channels.Register(&fakeOpsAlertAdapter{id: "slack", live: true})
	s.channels.Register(&fakeOpsAlertAdapter{id: "mobile", live: true})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels/delivery-readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delivery readiness = %d body=%v", status, body)
	}
	if body["status"] != "fail" {
		t.Fatalf("expected fail for missing destination, got %v", body)
	}
}

func TestChannelDeliveryReadinessTreatsWebhookURLAsDefaultTarget(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Channels = map[string]map[string]any{
		"webhook": {
			"enabled": true,
			"url":     "https://hooks.example.test/soulacy",
		},
	}
	s.channels.Register(&fakeOpsAlertAdapter{id: "webhook", live: true})
	s.channels.Register(&fakeOpsAlertAdapter{id: "mobile", live: true})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels/delivery-readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delivery readiness = %d body=%v", status, body)
	}
	if body["status"] != "ok" || body["ready"] != float64(2) || body["total"] != float64(2) {
		t.Fatalf("webhook and mobile should count as ready outbound targets without default_output_to: %v", body)
	}
	targets, ok := body["targets"].([]any)
	if !ok || len(targets) != 2 {
		t.Fatalf("expected webhook and mobile targets, got %v", body["targets"])
	}
	var webhook map[string]any
	for _, raw := range targets {
		target, _ := raw.(map[string]any)
		if target["family"] == "webhook" {
			webhook = target
		}
	}
	if webhook == nil || webhook["to"] != "configured webhook URL" {
		t.Fatalf("webhook readiness should not expose the secret URL, got targets=%v", targets)
	}
}

func TestChannelDeliveryReadinessTreatsMobileAsAlwaysOnBroadcast(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.channels.Register(&fakeOpsAlertAdapter{id: "mobile", live: true})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels/delivery-readiness", "secret", "")
	if status != http.StatusOK || body["status"] != "ok" || body["ready"] != float64(1) || body["total"] != float64(1) {
		t.Fatalf("mobile should be an always-on delivery target: status=%d body=%v", status, body)
	}
	targets, _ := body["targets"].([]any)
	target, _ := targets[0].(map[string]any)
	if target["family"] != "mobile" || target["to"] != "all" {
		t.Fatalf("mobile should default to all registered devices: %v", target)
	}
}
