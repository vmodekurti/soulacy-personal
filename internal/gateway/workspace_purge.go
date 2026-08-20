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

	if store, ok := s.actions.(interface {
		PurgeWorkspace(context.Context, string) (workspacepurge.Removed, error)
	}); ok {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "events",
			Purge:    store.PurgeWorkspace,
		})
	}
	hotMemory, hotOK := s.memoryStore.(interface {
		PurgeWorkspace(context.Context, string) (workspacepurge.Removed, error)
	})
	archiveMemory, archiveOK := s.memoryArchive.(interface {
		PurgeWorkspace(context.Context, string) (workspacepurge.Removed, error)
	})
	if hotOK && archiveOK {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "memory",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				removed, err := hotMemory.PurgeWorkspace(ctx, workspaceID)
				if err != nil {
					return removed, err
				}
				archiveRemoved, err := archiveMemory.PurgeWorkspace(ctx, workspaceID)
				removed.Rows += archiveRemoved.Rows
				removed.Bytes += archiveRemoved.Bytes
				removed.Note = "hot memory files and durable archive rows"
				return removed, err
			},
		})
	}
	if s.vectorMemory != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "vectors",
			Purge:    s.vectorMemory.PurgeWorkspace,
		})
	}

	if s.loader != nil && s.rbacManager != nil {
		loader, grants := s.loader, s.rbacManager
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "agents",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				files, err := loader.PurgeWorkspace(ctx, workspaceID)
				if err != nil {
					return files, err
				}
				rows, err := grants.PurgeWorkspace(ctx, workspaceID)
				files.Rows += rows.Rows
				if err != nil {
					return files, err
				}
				files.Note = "agent definitions, version snapshots, and object grants"
				return files, nil
			},
		})
		// Definitions and their immutable snapshots occupy the same scoped
		// loader trees as agents. Register the class independently so the
		// deletion report verifies the catalog entry instead of making an
		// implicit multi-class claim; the operation is idempotent after agents.
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "definitions",
			Purge:    loader.PurgeWorkspace,
		})
	}

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

	// PurgeWorkspace is deliberately optional on the operational DLQ
	// interface: third-party/no-op implementations can still receive failed
	// jobs, while durable stores advertise whether they can satisfy workspace
	// deletion. The production SQLite store does.
	if store, ok := s.dlqStore.(interface {
		PurgeWorkspace(context.Context, string) (workspacepurge.Removed, error)
	}); ok {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "queue-dlq",
			Purge:    store.PurgeWorkspace,
		})
	}
	if s.approvalStore != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "approvals",
			Purge:    s.approvalStore.PurgeWorkspace,
		})
	}
	if s.scheduleStore != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "schedules",
			Purge:    s.scheduleStore.PurgeWorkspace,
		})
	}
	if s.knowledgeStore != nil {
		store := s.knowledgeStore
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "knowledge",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				spoolRoot, err := ingestSpoolDir()
				if err != nil {
					return workspacepurge.Removed{}, err
				}
				return store.PurgeWorkspace(ctx, workspaceID, spoolRoot)
			},
		})
	}
	if store, ok := s.historyStore.(interface {
		PurgeWorkspace(context.Context, string) (workspacepurge.Removed, error)
	}); ok {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "messages",
			Purge:    store.PurgeWorkspace,
		})
	}
	owners, ownersOK := s.sessionOwnership.(interface {
		PurgeWorkspace(context.Context, string) (int64, error)
	})
	resources, resourcesOK := s.resourceStore.(interface {
		PurgeWorkspace(context.Context, string) (int64, error)
	})
	if ownersOK && resourcesOK && s.checkpointStore != nil {
		checkpoints := s.checkpointStore
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "sessions",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				var removed workspacepurge.Removed
				for _, purge := range []func(context.Context, string) (int64, error){
					owners.PurgeWorkspace, resources.PurgeWorkspace, checkpoints.PurgeWorkspace,
				} {
					rows, err := purge(ctx, workspaceID)
					removed.Rows += rows
					if err != nil {
						return removed, err
					}
				}
				removed.Note = "session ownership, attachments, and workflow checkpoints"
				return removed, nil
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
