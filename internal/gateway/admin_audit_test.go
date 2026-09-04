package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAdminAuditRecordsConfigPatchWithoutSecrets(t *testing.T) {
	cfgPath := t.TempDir() + "/config.yaml"
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	backend := &fakeTailBackend{}
	s.actions = backend

	patch := `{
		"server": {"api_key": "super-secret-api-key"},
		"log": {"level": "debug"}
	}`
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret", patch)
	if status != http.StatusOK {
		t.Fatalf("PATCH /config status=%d body=%v", status, body)
	}

	events, err := backend.QueryEvents(adminAuditAgentID, "", 10, adminAuditEventTypes())
	if err != nil {
		t.Fatalf("query audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1", len(events))
	}
	rec, ok := adminAuditRecordFromPayload(events[0].Payload)
	if !ok {
		t.Fatalf("payload did not decode as admin audit: %#v", events[0].Payload)
	}
	if rec.Action != "config.patch" || rec.Resource != "config" || rec.Status != "ok" {
		t.Fatalf("unexpected record: %#v", rec)
	}
	if rec.Actor != "api-key" {
		t.Fatalf("actor=%q, want api-key", rec.Actor)
	}
	sections, _ := rec.Details["sections"].([]string)
	if !containsString(sections, "server") || !containsString(sections, "log") {
		t.Fatalf("sections=%v, want server and log", rec.Details["sections"])
	}
	if strings.Contains(fmt.Sprint(rec), "super-secret-api-key") {
		t.Fatalf("audit payload leaked secret: %#v", rec)
	}

	status, auditBody := gatewayJSON(t, s, http.MethodGet, "/api/v1/admin/audit?limit=10", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("GET /admin/audit status=%d body=%v", status, auditBody)
	}
	if got := int(auditBody["count"].(float64)); got != 1 {
		t.Fatalf("audit count=%d, want 1 body=%v", got, auditBody)
	}
	if strings.Contains(fmt.Sprint(auditBody), "super-secret-api-key") {
		t.Fatalf("audit endpoint leaked secret: %#v", auditBody)
	}
}

func TestAdminAuditRecordsChannelMutations(t *testing.T) {
	cfgPath := t.TempDir() + "/config.yaml"
	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	backend := &fakeTailBackend{}
	s.actions = backend

	patch := `{"enabled":true,"settings":{"bot_token":"real-token","default_to":"12345"},"bots":[{"name":"Notify","token":"bot-token","to":"12345","agent_id":"weather-agent"}]}`
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/channels/telegram", "secret", patch)
	if status != http.StatusOK {
		t.Fatalf("PATCH /channels/telegram status=%d body=%v", status, body)
	}

	events, err := backend.QueryEvents(adminAuditAgentID, "", 10, adminAuditEventTypes())
	if err != nil {
		t.Fatalf("query audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1", len(events))
	}
	rec, ok := adminAuditRecordFromPayload(events[0].Payload)
	if !ok {
		t.Fatalf("payload did not decode as admin audit: %#v", events[0].Payload)
	}
	if rec.Action != "channel.update" || rec.Target != "telegram" {
		t.Fatalf("unexpected channel audit: %#v", rec)
	}
	if strings.Contains(fmt.Sprint(rec), "real-token") || strings.Contains(fmt.Sprint(rec), "bot-token") {
		t.Fatalf("channel audit leaked token: %#v", rec)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
