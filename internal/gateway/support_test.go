package gateway

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestGatewaySupportBundleDownloadsRedactedZip(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agents")
	logDir := filepath.Join(root, "logs")
	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.MkdirAll(filepath.Join(agentDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: top-secret-api-key-value-1234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "demo", "SOUL.yaml"), []byte("id: demo\nbot_token: xoxb-agent-secret-token-1234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "demo.log"), []byte("token=xoxb-log-secret-token-value-abcdefghijklmnopqrstuvwxyz"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.cfg.AgentDirs = []string{agentDir}
	s.cfg.Log.File = filepath.Join(logDir, "soulacy.log")
	s.engine.TagFlowRun("demo", "flow-only", "http")
	base := time.Date(2026, 7, 13, 7, 0, 0, 0, time.UTC)
	s.actions = &fakeTailBackend{events: []message.Event{
		{
			Type: "message.in", AgentID: "demo", SessionID: "cron-1", Timestamp: base,
			Payload: message.Message{Channel: "http", Metadata: map[string]string{"trigger": "cron"}, Parts: message.Text("__trigger:cron__")},
		},
		{
			Type: "message.out", AgentID: "demo", SessionID: "cron-1", Timestamp: base.Add(time.Second),
			Payload: message.Message{Parts: message.Text("daily support report")},
		},
		{
			Type: "schedule.output", AgentID: "demo", SessionID: "cron-1", Timestamp: base.Add(2 * time.Second),
			Payload: map[string]any{"delivered": false, "channel": "telegram", "to": "123", "reason": "chat not found", "reply_preview": "daily support report"},
		},
		{
			Type: "admin.audit", AgentID: adminAuditAgentID, SessionID: "req-1", Timestamp: base.Add(3 * time.Second),
			Payload: adminAuditRecord{Timestamp: base.Add(3 * time.Second), Action: "config.patch", Resource: "config", Actor: "api-key", Status: "ok", Details: map[string]any{"sections": []string{"log"}}},
		},
	}}

	req, err := http.NewRequest(http.MethodGet, "/api/v1/support/bundle", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := s.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, string(raw))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/zip") {
		t.Fatalf("content-type = %q", ct)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	names := map[string]bool{}
	files := map[string]string{}
	var joined strings.Builder
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = string(body)
		joined.Write(body)
	}
	for _, want := range []string{"manifest.json", "doctor.json", "readiness.json", "browser_status.json", "mobile_status.json", "chat_status.json", "release.json", "run_ledger.json", "admin_audit.json", "config.redacted.yaml", "agents/demo.SOUL.redacted.yaml"} {
		if !names[want] {
			t.Fatalf("bundle missing %s; got %#v", want, names)
		}
	}
	for _, artifact := range []string{"browser_status.json", "mobile_status.json", "chat_status.json"} {
		var posture map[string]any
		if err := json.Unmarshal([]byte(files[artifact]), &posture); err != nil {
			t.Fatalf("%s is not JSON: %v\n%s", artifact, err, files[artifact])
		}
		if _, ok := posture["status"].(string); !ok {
			t.Fatalf("%s missing status: %#v", artifact, posture)
		}
		if _, ok := posture["checks"].([]any); !ok {
			t.Fatalf("%s missing checks: %#v", artifact, posture)
		}
	}
	var ledger map[string]any
	if err := json.Unmarshal([]byte(files["run_ledger.json"]), &ledger); err != nil {
		t.Fatalf("run_ledger.json is not JSON: %v\n%s", err, files["run_ledger.json"])
	}
	if ledger["available"] != true || ledger["source"] != "action-log+flow" {
		t.Fatalf("run ledger unavailable or wrong source: %#v", ledger)
	}
	if got := int(ledger["event_limit"].(float64)); got != 10000 {
		t.Fatalf("event_limit = %d, want 10000", got)
	}
	if got := ledger["event_truncated"]; got != false {
		t.Fatalf("event_truncated = %v, want false", got)
	}
	runs, ok := ledger["runs"].([]any)
	if !ok || len(runs) != 2 {
		t.Fatalf("run ledger runs = %#v, want durable plus flow run", ledger["runs"])
	}
	var run map[string]any
	seenFlow := false
	for _, raw := range runs {
		candidate := raw.(map[string]any)
		switch candidate["runId"] {
		case "cron-1":
			run = candidate
		case "flow-only":
			seenFlow = true
		}
	}
	if run == nil || !seenFlow {
		t.Fatalf("run ledger did not include both durable cron run and flow-only history: %#v", runs)
	}
	if got := run["deliveryStatus"]; got != "failed" {
		t.Fatalf("deliveryStatus = %v, want failed", got)
	}
	if got := run["deliveryError"]; got != "chat not found" {
		t.Fatalf("deliveryError = %v, want chat not found", got)
	}
	if got := run["output"]; got != "daily support report" {
		t.Fatalf("output = %v, want daily support report", got)
	}
	var audit map[string]any
	if err := json.Unmarshal([]byte(files["admin_audit.json"]), &audit); err != nil {
		t.Fatalf("admin_audit.json is not JSON: %v\n%s", err, files["admin_audit.json"])
	}
	if audit["available"] != true || audit["source"] != "action-log" {
		t.Fatalf("admin audit unavailable or wrong source: %#v", audit)
	}
	if got := int(audit["count"].(float64)); got != 1 {
		t.Fatalf("admin audit count = %d, want 1", got)
	}
	all := joined.String()
	for _, forbidden := range []string{"top-secret-api-key", "xoxb-agent-secret", "xoxb-log-secret"} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("support bundle leaked %q:\n%s", forbidden, all)
		}
	}
}
