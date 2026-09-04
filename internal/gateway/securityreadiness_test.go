// securityreadiness_test.go — S4 (Cohort F) tests for the production
// defaults + security readiness journey item.
package gateway

import (
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// TestSecurityReadiness_CleanWorkspaceOK is the zero-state: a fresh
// gateway with no agents / no shared-channel bindings reports ok.
func TestSecurityReadiness_CleanWorkspaceOK(t *testing.T) {
	s := newTestGateway(t, "secret")
	rep := s.evaluateSecurityReadiness()
	if rep.Status != "ok" {
		t.Errorf("clean workspace status = %q, want ok (reasons=%v)", rep.Status, rep.Reasons)
	}
	if !rep.Ready {
		t.Errorf("clean workspace should be ready")
	}
	if len(rep.PrivilegedExposures) != 0 {
		t.Errorf("clean workspace should have no exposures; got %+v", rep.PrivilegedExposures)
	}
}

// TestSecurityReadiness_PrivilegedAgentOnTelegramFailsProduction
// upserts a Privileged agent (via write_file builtin) and binds it to
// a Telegram bot WITHOUT accept_privileged_exposure, then flips the
// deployment profile to production. The security readiness must fail.
func TestSecurityReadiness_PrivilegedAgentOnTelegramFailsProduction(t *testing.T) {
	s := newTestGateway(t, "secret")

	// Create a Privileged agent via the loader (bypass the create-agent
	// HTTP handler so we don't have to jump through channel-binding
	// validation for this test).
	dir := t.TempDir()
	privBuiltins := []string{"write_file"}
	def := &agent.Definition{
		ID:       "priv-agent",
		Name:     "Priv Bot",
		Enabled:  true,
		Builtins: &privBuiltins,
	}
	if err := s.loader.Upsert(dir, def); err != nil {
		t.Fatalf("upsert priv-agent: %v", err)
	}

	// Bind priv-agent to a Telegram bot without acceptance.
	s.cfg.Channels = map[string]map[string]any{
		"telegram": {
			"enabled": true,
			"bots": []any{
				map[string]any{
					"agent_id": "priv-agent",
					"bot_name": "Priv Bot",
					"token":    "tok",
					// accept_privileged_exposure omitted
				},
			},
		},
	}

	// Advisory mode (non-production): warn, but still ready.
	s.cfg.Deployment.Profile = "local"
	rep := s.evaluateSecurityReadiness()
	if rep.Status != "warn" {
		t.Errorf("local profile: status = %q, want warn", rep.Status)
	}
	if !rep.Ready {
		t.Error("local profile should still be Ready")
	}
	if len(rep.PrivilegedExposures) != 1 {
		t.Fatalf("expected 1 privileged exposure; got %+v", rep.PrivilegedExposures)
	}
	if rep.PrivilegedExposures[0].Accepted {
		t.Error("exposure marked accepted despite missing accept_privileged_exposure")
	}

	// Production: must fail launch.
	s.cfg.Deployment.Profile = "production"
	rep = s.evaluateSecurityReadiness()
	if rep.Status != "fail" {
		t.Errorf("production profile: status = %q, want fail", rep.Status)
	}
	if rep.Ready {
		t.Error("production profile with unaccepted privileged exposure should NOT be Ready")
	}
	if len(rep.NextActions) == 0 {
		t.Error("expected next actions for failed production readiness")
	}
}

// TestSecurityReadiness_AcceptedExposurePassesProduction is the
// counterpoint: the same agent + binding, but with
// accept_privileged_exposure:true, passes production readiness.
func TestSecurityReadiness_AcceptedExposurePassesProduction(t *testing.T) {
	s := newTestGateway(t, "secret")
	dir := t.TempDir()
	privBuiltins := []string{"write_file"}
	def := &agent.Definition{
		ID:       "priv-agent",
		Name:     "Priv Bot",
		Enabled:  true,
		Builtins: &privBuiltins,
	}
	if err := s.loader.Upsert(dir, def); err != nil {
		t.Fatalf("upsert priv-agent: %v", err)
	}
	s.cfg.Channels = map[string]map[string]any{
		"telegram": {
			"enabled": true,
			"bots": []any{
				map[string]any{
					"agent_id":                   "priv-agent",
					"bot_name":                   "Priv Bot",
					"token":                      "tok",
					"accept_privileged_exposure": true,
				},
			},
		},
	}
	s.cfg.Deployment.Profile = "production"
	rep := s.evaluateSecurityReadiness()
	if rep.Status != "ok" {
		t.Errorf("accepted exposure should be ok in production; got status=%q reasons=%v", rep.Status, rep.Reasons)
	}
	if !rep.Ready {
		t.Error("accepted exposure should be Ready in production")
	}
	if len(rep.PrivilegedExposures) != 1 {
		t.Fatalf("expected 1 exposure; got %+v", rep.PrivilegedExposures)
	}
	if !rep.PrivilegedExposures[0].Accepted {
		t.Error("expected exposure to be marked Accepted")
	}
}

// TestSecurityReadinessEndpoint verifies the HTTP surface returns the
// verdict and honours RBAC.
func TestSecurityReadinessEndpoint(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/security/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["status"] != "ok" {
		t.Errorf("expected clean workspace to report ok; body=%v", body)
	}
	if _, ok := body["profile"]; !ok {
		t.Error("expected profile field in response")
	}
}

// TestHasWildcardMCPDetectsWildcard pins the helper used to enumerate
// the wildcard-MCP list in the report.
func TestHasWildcardMCPDetectsWildcard(t *testing.T) {
	star := []string{"*"}
	if !hasWildcardMCP(&agent.Definition{MCPServers: &star}) {
		t.Error("wildcard mcp_servers not detected")
	}
	if !hasWildcardMCP(&agent.Definition{MCPTools: &star}) {
		t.Error("wildcard mcp_tools not detected")
	}
	explicit := []string{"filesystem"}
	if hasWildcardMCP(&agent.Definition{MCPServers: &explicit}) {
		t.Error("explicit list flagged as wildcard")
	}
	if hasWildcardMCP(nil) {
		t.Error("nil def flagged")
	}
}
