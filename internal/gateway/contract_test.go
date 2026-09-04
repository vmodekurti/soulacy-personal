package gateway

// Story E22 (2): /api/v1 contract tests. These golden-shape assertions pin
// the REST response envelopes the GUI, CLI, and plugins depend on. A
// failing test here means a BREAKING API change — additive fields are fine
// (the assertions check required keys, not exhaustive sets); removing or
// renaming a pinned key requires a deprecation cycle.

import (
	"fmt"
	"net/http"
	"testing"
)

// requireKeys asserts every key exists in m (value may be anything,
// including null — presence is the contract).
func requireKeys(t *testing.T, what string, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: required key %q missing (have %v)", what, k, mapKeys(m))
		}
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func seedAgent(t *testing.T, srv *Server) {
	t.Helper()
	def := `{"id": "contract-agent", "name": "Contract Agent", "enabled": true}`
	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/agents", "secret", def)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("seed agent: %d %v", status, body)
	}
}

func TestContract_AgentsList(t *testing.T) {
	srv := newTestGateway(t, "secret")
	seedAgent(t, srv)

	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/agents", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	requireKeys(t, "GET /agents", body, "agents", "count")
	agents := body["agents"].([]any)
	if len(agents) == 0 {
		t.Fatal("seeded agent missing")
	}
	entry := agents[0].(map[string]any)
	requireKeys(t, "agents[0]", entry, "id", "name", "enabled")
}

func TestContract_AgentDetail(t *testing.T) {
	srv := newTestGateway(t, "secret")
	seedAgent(t, srv)

	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/agents/contract-agent", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	requireKeys(t, "GET /agents/:id", body, "id", "name", "enabled", "llm")
}

func TestContract_Config(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/config", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	// The config envelope: the GUI Config page renders these blocks; the
	// E17/E18 plugin settings flow depends on plugins_config being present
	// (even when empty).
	requireKeys(t, "GET /config", body, "server", "llm", "runtime", "plugins_config")
}

func TestContract_ProvidersList(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/providers", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	requireKeys(t, "GET /providers", body, "providers")
}

func TestContract_SkillsList(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/skills", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	requireKeys(t, "GET /skills", body, "skills", "count")
}

func TestContract_PluginsInstalled(t *testing.T) {
	srv, src := installFixture(t)

	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/plugins/installed", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	requireKeys(t, "GET /plugins/installed", body, "plugins", "count")

	// Stage → preview envelope (the E13/E20 approval dialog contract).
	status, body = gatewayJSON(t, srv, http.MethodPost, "/api/v1/plugins/install", "secret",
		fmt.Sprintf(`{"source":%q}`, src))
	if status != http.StatusCreated {
		t.Fatalf("stage status = %d", status)
	}
	requireKeys(t, "POST /plugins/install", body, "preview", "note")
	pv := body["preview"].(map[string]any)
	requireKeys(t, "preview", pv, "staged_id", "plugin_id", "source", "permissions", "credentials", "fingerprint")
}

func TestContract_ErrorEnvelope(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, srv, http.MethodGet, "/api/v1/agents/no-such-agent", "secret", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	requireKeys(t, "404 envelope", body, "error")
}

// Unauthenticated requests are refused with the same envelope.
func TestContract_AuthRequired(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, srv, http.MethodGet, "/api/v1/agents", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status without token = %d, want 401", status)
	}
}
