package gateway

// Story E23: versioned rulebook API — history, single versions, rollback,
// lock. Locked agents refuse writes with 423.

import (
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/internal/agentmemory"
)

func rulebookGateway(t *testing.T) (*Server, *agentmemory.CompositeStore) {
	t.Helper()
	srv := newTestGateway(t, "secret")
	brain := agentmemory.NewCompositeStore(t.TempDir(), nil)
	t.Cleanup(func() { _ = brain.Close() })
	srv.engine.SetBrainMemory(brain)
	return srv, brain
}

func TestRulebookAPI_HistoryRollbackLock(t *testing.T) {
	srv, brain := rulebookGateway(t)
	if _, err := brain.UpdateProceduralVersioned("bot", "# v1 rules", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := brain.UpdateProceduralVersioned("bot", "# v2 drifted", "auto_update"); err != nil {
		t.Fatal(err)
	}

	// History: current + lock state + versions newest-first.
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/brain-memory/bot/rulebook", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("history: %d %v", status, body)
	}
	if body["current"] != "# v2 drifted" || body["locked"] != false {
		t.Errorf("history envelope = %v", body)
	}
	versions := body["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("versions = %v", versions)
	}
	first := versions[0].(map[string]any)
	if int(first["version"].(float64)) != 2 || first["source"] != "auto_update" {
		t.Errorf("versions[0] = %v", first)
	}

	// Single version fetch.
	status, body = gatewayJSON(t, srv, http.MethodGet, "/api/v1/brain-memory/bot/rulebook/1", "secret", "")
	if status != http.StatusOK || body["rules"] != "# v1 rules" {
		t.Errorf("version fetch: %d %v", status, body)
	}
	if status, _ = gatewayJSON(t, srv, http.MethodGet, "/api/v1/brain-memory/bot/rulebook/9", "secret", ""); status != http.StatusNotFound {
		t.Errorf("unknown version status = %d, want 404", status)
	}

	// Rollback to v1 → creates v3.
	status, body = gatewayJSON(t, srv, http.MethodPost, "/api/v1/brain-memory/bot/rulebook/rollback", "secret",
		`{"version": 1}`)
	if status != http.StatusOK || int(body["version"].(float64)) != 3 {
		t.Fatalf("rollback: %d %v", status, body)
	}
	if got := brain.ProceduralRules("bot"); got != "# v1 rules" {
		t.Errorf("rules after rollback = %q", got)
	}

	// Lock → PUT and rollback refuse with 423.
	if status, _ = gatewayJSON(t, srv, http.MethodPost, "/api/v1/brain-memory/bot/rulebook/lock", "secret",
		`{"locked": true}`); status != http.StatusOK {
		t.Fatalf("lock: %d", status)
	}
	if status, _ = gatewayJSON(t, srv, http.MethodPut, "/api/v1/brain-memory/bot/procedural", "secret",
		`{"rules": "# manual override"}`); status != http.StatusLocked {
		t.Errorf("locked PUT status = %d, want 423", status)
	}
	if status, _ = gatewayJSON(t, srv, http.MethodPost, "/api/v1/brain-memory/bot/rulebook/rollback", "secret",
		`{"version": 2}`); status != http.StatusLocked {
		t.Errorf("locked rollback status = %d, want 423", status)
	}

	// Unlock → writes flow again.
	if status, _ = gatewayJSON(t, srv, http.MethodPost, "/api/v1/brain-memory/bot/rulebook/lock", "secret",
		`{"locked": false}`); status != http.StatusOK {
		t.Fatalf("unlock: %d", status)
	}
	if status, _ = gatewayJSON(t, srv, http.MethodPut, "/api/v1/brain-memory/bot/procedural", "secret",
		`{"rules": "# back to manual"}`); status != http.StatusOK {
		t.Errorf("post-unlock PUT status = %d", status)
	}
}

func TestRulebookAPI_NoBrainStore503(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/brain-memory/bot/rulebook", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
}
