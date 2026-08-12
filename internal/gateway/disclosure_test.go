package gateway

// Two endpoints handed credentials to the lowest-privilege role.
//
// MCP servers are configured with `env` and `headers` — where a GITHUB_TOKEN or
// an "Authorization: Bearer sk-…" lives — and GET /api/v1/mcp returned them
// verbatim under mcp:read, which the VIEWER role holds. The channels list and
// the providers list both redact for exactly this reason; this one did not.
//
// And GET /api/v1/credentials/:agentID/:key returned the DECRYPTED vault value
// under agents:read, also a viewer permission. Encrypting the vault achieves
// nothing if its plaintext is one read-only request away.

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/rbac"
)

func TestRedactMCPServers_MasksCredentialBearingFields(t *testing.T) {
	got := redactMCPServers([]mcp.ServerStatus{{
		ID:      "github",
		Env:     map[string]string{"GITHUB_TOKEN": "ghp_realtokenvalue"},
		Headers: map[string]string{"Authorization": "Bearer sk-realkeyvalue"},
	}})

	blob := strings.Join([]string{got[0].Env["GITHUB_TOKEN"], got[0].Headers["Authorization"]}, " ")
	if strings.Contains(blob, "ghp_realtokenvalue") || strings.Contains(blob, "sk-realkeyvalue") {
		t.Fatalf("MCP credentials still reach the caller: %q", blob)
	}
}

// An operator debugging a server needs to see THAT a token is configured. Names
// stay; only values are masked.
func TestRedactMCPServers_KeepsTheKeyNames(t *testing.T) {
	got := redactMCPServers([]mcp.ServerStatus{{
		Env: map[string]string{"GITHUB_TOKEN": "ghp_x", "LOG_LEVEL": "debug"},
	}})
	if _, ok := got[0].Env["GITHUB_TOKEN"]; !ok {
		t.Error("the key name was removed — an operator cannot tell the token is set")
	}
	if _, ok := got[0].Env["LOG_LEVEL"]; !ok {
		t.Error("a non-secret setting was removed")
	}
}

// An MCP server's env is arbitrary operator-supplied config, so there is no
// reliable way to tell a credential from a setting. Masking only the
// secret-looking ones means guessing, and a wrong guess leaks the credential.
func TestRedactMCPServers_MasksEvenInnocuousLookingValues(t *testing.T) {
	got := redactMCPServers([]mcp.ServerStatus{{
		Env: map[string]string{"REGION": "us-east-1"},
	}})
	if got[0].Env["REGION"] == "us-east-1" {
		t.Error("values are masked by name heuristic rather than wholesale")
	}
}

func TestRedactMCPServers_LeavesEmptyMapsAlone(t *testing.T) {
	got := redactMCPServers([]mcp.ServerStatus{{ID: "plain"}})
	if len(got) != 1 || got[0].ID != "plain" {
		t.Fatal("a server with no env/headers was mangled")
	}
}

// The route table is the enforcement point, so assert on it directly: reading a
// decrypted value must not be reachable with a read-only permission.
func TestCredentialValueRouteRequiresWrite(t *testing.T) {
	if rbac.HasPermission("viewer", rbac.ResourceAgents, rbac.ActionWrite) {
		t.Fatal("precondition changed: viewer now has agents:write, so this route is open again")
	}
	if !rbac.HasPermission("viewer", rbac.ResourceAgents, rbac.ActionRead) {
		t.Fatal("precondition changed: viewer lost agents:read")
	}
	// The guarantee: listing names is a read, fetching a value is not.
	src := readGatewaySource(t, "server.go")
	line := findLine(t, src, `api.Get("/credentials/:agentID/:key"`)
	if !strings.Contains(line, "rbac.ActionWrite") {
		t.Errorf("the credential-value route is not write-gated: %s", strings.TrimSpace(line))
	}
}
