package gateway

import (
	"context"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

// workspace_purge.go — which resource classes this build can actually delete.
//
// The registry is deliberately SHORTER than the ownership catalog, and
// deliberately visible. internal/workspacepurge seeds its report from the
// catalog and marks every class with no registered purger as surviving the
// deletion, so this function can be incomplete and cannot be quietly
// incomplete: a customer reading the deletion report sees the classes whose
// data is still there, by name.
//
// `TestTheWorkspacePurgeCoverageGapIsAKnownList` holds the gap to an explicit
// allowlist, so a resource class added to the catalog without a purger fails
// the build rather than silently joining the survivors.

// workspacePurgers builds the purger set for this server's wired stores.
//
// TAKES NO REQUEST, and that is not incidental. A purge runs after a recovery
// window measured in days: there is no *fiber.Ctx left to read, and a closure
// holding one would be reading whatever request Fiber recycled that buffer
// into. Everything comes from the server's own wiring.
func (s *Server) workspacePurgers() []workspacepurge.Purger {
	var purgers []workspacepurge.Purger

	if s.runStore != nil {
		store := s.runStore
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "runs",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				return store.PurgeWorkspace(ctx, workspaceID)
			},
		})
	}
	if s.workboardStore != nil {
		store := s.workboardStore
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "workboard",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				return store.PurgeWorkspace(ctx, workspaceID)
			},
		})
	}

	if s.idempotency != nil && s.idempotency.durable != nil {
		store := s.idempotency.durable
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "idempotency",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				return store.PurgeWorkspace(ctx, workspaceID)
			},
		})
	}

	if s.mcpServers != nil {
		store := s.mcpServers
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "mcp",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := store.PurgeWorkspace(ctx, workspaceID)
				return workspacepurge.Removed{Rows: int64(rows)}, err
			},
		})
	}

	if s.workspacePolicies != nil {
		store := s.workspacePolicies
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "workspace-policy",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := store.PurgeWorkspace(ctx, workspaceID)
				return workspacepurge.Removed{Rows: rows}, err
			},
		})
	}

	// File-backed classes are a SUBTREE REMOVAL PER CLASS, not one removal for
	// all of them, and the reason is worth stating because the opposite is the
	// natural assumption. `wsroot.Dir(base, id)` is applied per store with a
	// different base each time — the agent dirs, `<root>/studio/drafts`, the
	// trace dir, the skills base — so a workspace's files live in a dozen
	// `.workspaces/<id>` directories under a dozen different parents. There is
	// no single tree whose removal settles them.
	//
	// Only the export archives are registered here so far, because their base
	// is the one this package owns and can name without guessing. Every other
	// file-backed class reports as surviving, by name, until its own purger
	// arrives — which is the point of the report.
	//
	// The personal workspace is refused inside PurgeTree: wsroot resolves it
	// to the shared base, so a delete there would take the whole installation.
	if ws, err := config.ResolveWorkspace(); err == nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "workspace-exports",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				// The same base workspaceexport.NewStore is given, so the
				// directory purged is the directory written.
				return workspacepurge.PurgeSubtree(ctx, ws.Root, workspaceID, "exports")
			},
		})
	}

	return purgers
}
