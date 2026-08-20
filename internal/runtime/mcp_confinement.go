package runtime

import (
	"context"
	"fmt"

	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/sandbox"
)

// mcp_confinement.go — the engine answers where a workspace's MCP servers run.
//
// IT LIVES HERE, not in internal/mcp, because the engine is where the answer
// already exists. workspaceScratchDir is the same function that decides where
// a privileged subprocess starts and where read_file resolves; an MCP server
// is a subprocess of exactly that kind, and giving it a second derivation of
// "this workspace's tree" would be two answers to a question that must have
// one. When MU-021's roots move, this moves with them for free.
//
// The engine deliberately does NOT import internal/mcp: the pool takes an
// interface, so the dependency points from the extension layer at the runtime
// rather than the other way round. The engine has no opinion about MCP.

// WorkspaceConfinement implements mcp.Confinement.
//
// An empty working directory is an ERROR here rather than a permissive
// default. workspaceScratchDir already returns empty when a workspace's tree
// cannot be established, and every caller of it treats that as a refusal —
// runPrivilegedCommand refuses to run. Translating it into "no directory
// specified, inherit the gateway's" would make the one failure mode that
// matters look like the absence of a setting.
func (e *Engine) WorkspaceConfinement(workspaceID string) (string, sandbox.Limits, string, error) {
	if e == nil {
		return "", sandbox.Limits{}, "", fmt.Errorf("no engine")
	}
	dir := e.workspaceScratchDir(workspaceID)
	if dir == "" {
		return "", sandbox.Limits{}, "", fmt.Errorf("workspace %q has no filesystem confinement", workspaceID)
	}
	return dir, e.sandboxLimits, e.selfPath, nil
}

// SetMCPPool installs the per-workspace MCP client pool.
//
// Set AFTER construction, not passed to New, because the pool needs the engine
// to answer WorkspaceConfinement and the engine needs the pool to route calls.
// Threading a half-built engine into the pool's constructor would make the
// cycle implicit; a setter makes the order visible at the one call site that
// has to get it right.
func (e *Engine) SetMCPPool(pool *mcp.Pool) {
	if e == nil {
		return
	}
	e.mcpPool = pool
}

// mcpFor returns the client whose servers this run may call.
//
// The pool wins whenever it is set. There is deliberately no "pool, but fall
// back to the shared client if this workspace has none" — a workspace the pool
// declines to start servers for has no MCP tools, and answering it with the
// deployment-wide client would hand it every other tenant's server. Falling
// back is the bug, not the safety net.
func (e *Engine) mcpFor(ctx context.Context) *mcp.Client {
	if e == nil {
		return nil
	}
	if e.mcpPool == nil {
		return e.mcpClient
	}
	return e.mcpPool.For(WorkspaceFromContext(ctx))
}
