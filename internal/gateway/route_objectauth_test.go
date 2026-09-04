package gateway

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestAgentObjectRoutesDeclareObjectAuthorization is a CI tripwire: route
// additions carrying an agent identifier must name an object-aware middleware,
// not only a role-level rbacMW check.
func TestAgentObjectRoutesDeclareObjectAuthorization(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sourcePath := strings.TrimSuffix(file, "route_objectauth_test.go") + "server.go"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	required := []string{
		`api.Post("/chat", s.rbacAgentFromMW`,
		`api.Post("/chat/stream", s.rbacAgentFromMW`,
		`api.Get("/chat/stream", s.rbacAgentFromMW`,
		`api.Post("/webhooks/:agent_id", s.rbacAgentFromMW`,
		`api.Get("/chat/artifacts", s.rbacAgentFromMW`,
		`api.Post("/chat/attachments", s.rbacAgentFromMW`,
		`api.Get("/memory/:agent_id", s.rbacAgentFromMW`,
		`api.Get("/brain-memory/:agentID/episodic", s.rbacAgentFromMW`,
		`api.Get("/costs/:agent_id", s.rbacAgentFromMW`,
		`api.Get("/history/agent/:agent_id", s.rbacAgentFromMW`,
		`api.Get("/credentials/:agentID", s.denyGlobalCredentialScope, s.rbacAgentFromMW`,
	}
	for _, declaration := range required {
		if !strings.Contains(source, declaration) {
			t.Errorf("agent-object route is missing object authorization: %s", declaration)
		}
	}
}
