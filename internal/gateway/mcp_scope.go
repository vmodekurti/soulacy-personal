package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/runtime"
)

// mcp_scope.go — MU-017 criterion 5. The request's view of MCP.
//
// WHAT `s.mcp` WAS. One *mcp.Client, built at boot from config.yaml, shared by
// every handler and every agent in the deployment. Every tool a server exposed
// was offered to every workspace, every tool call ran in one set of
// subprocesses, and those subprocesses started in the gateway's own working
// directory. Two tenants calling a filesystem MCP server's read_file with the
// same relative path read the same file.
//
// It is the shape trap #2 in the handoff describes for agent IDs, one layer
// out: a registry keyed by server ID alone, where server IDs are unique per
// deployment only because there is one tenant.
//
// The scoped accessor returns the pool's client for the request's workspace.
// When no pool is wired — a directly constructed test gateway, or an
// embedding that never built an engine — it falls back to the shared client,
// which is what those deployments have always had and are: single-tenant.
// TestNoHandlerReachesPastTheScopedMCPAccessor keeps that fallback from
// becoming a path a handler can take by accident.
// SetMCPPool installs the per-workspace MCP pool. Once set, every accessor in
// this file routes through it and s.mcp is unreachable from a handler.
func (s *Server) SetMCPPool(pool *mcp.Pool) {
	if s == nil {
		return
	}
	s.mcpPool = pool
}

func (s *Server) mcpFor(c *fiber.Ctx) *mcp.Client {
	if s == nil {
		return nil
	}
	if s.mcpPool == nil {
		return s.mcp
	}
	return s.mcpPool.For(mcpWorkspace(c))
}

// mcpWorkspace resolves the workspace whose servers a request may reach.
//
// Personal on an unidentified request, matching every other scoped accessor in
// this package: an unauthenticated call in open development mode is the
// single-user installation talking to itself, and giving it a DIFFERENT
// workspace than agents(c) and schedules(c) give it would mean one request
// operating in two tenants at once.
// mcpForWorkspace is the same accessor for call sites that hold a workspace
// ID rather than a request — Studio's catalog grounding, the install
// snapshot, the browser-automation readiness view. They already carry a scope
// with a workspace on it, so threading the ID is honest; giving them a
// ctx-free helper that quietly meant Personal would put the deployment's
// widest MCP surface behind the least visible default.
func (s *Server) mcpForWorkspace(workspaceID string) *mcp.Client {
	if s == nil {
		return nil
	}
	if s.mcpPool == nil {
		return s.mcp
	}
	return s.mcpPool.For(workspaceID)
}

func mcpWorkspace(c *fiber.Ctx) string {
	if c == nil {
		return runtime.PersonalWorkspaceID
	}
	if identity, ok := requestIdentity(c); ok {
		return runtime.NormalizeWorkspace(identity.WorkspaceID())
	}
	return runtime.PersonalWorkspaceID
}

// mcpAddServer and mcpRemoveServer route a mutation to the request's workspace.
//
// Separate from mcpFor because adding a server has to reach the POOL, not the
// client: the pool records the addition so it survives that workspace's client
// being rebuilt, and — the part that matters — so one workspace's install does
// not land in another's. Calling client.AddServer directly would start the
// server in the right workspace and lose it on the next rebuild, which is the
// worst of both.
func (s *Server) mcpAddServer(c *fiber.Ctx, id string, cfg mcp.ServerConfig) error {
	if s == nil {
		return nil
	}
	if s.mcpPool != nil {
		return s.mcpPool.AddServer(mcpWorkspace(c), id, cfg)
	}
	if s.mcp == nil {
		return nil
	}
	return s.mcp.AddServer(id, cfg)
}

func (s *Server) mcpRemoveServer(c *fiber.Ctx, id string) error {
	if s == nil {
		return nil
	}
	if s.mcpPool != nil {
		return s.mcpPool.RemoveServer(mcpWorkspace(c), id)
	}
	if s.mcp == nil {
		return nil
	}
	return s.mcp.RemoveServer(id)
}

// mcpReplaceTemplate applies a reloaded config.yaml server set.
//
// The single-tenant fallback lives HERE, beside the other two, rather than at
// the call site in ReloadConfig. Every direct use of the shared client is in
// this file, so the guard that forbids them elsewhere needs no exemptions —
// and an exemption list is how a rule like this stops being one.
func (s *Server) mcpReplaceTemplate(servers map[string]mcp.ServerConfig) {
	if s == nil {
		return
	}
	if s.mcpPool != nil {
		s.mcpPool.ReplaceTemplate(mcp.Config{Servers: servers})
		return
	}
	if s.mcp == nil {
		return
	}
	for _, current := range s.mcp.ServersSnapshot() {
		if _, kept := servers[current.ID]; !kept {
			_ = s.mcp.RemoveServer(current.ID)
		}
	}
	for id, cfg := range servers {
		_ = s.mcp.AddServer(id, cfg)
	}
}
