package gateway

import (
	"context"
	"path/filepath"

	"github.com/soulacy/soulacy/internal/artifactstore"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/credentials"
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
	if s.artifactObjects != nil {
		objects := s.artifactObjects
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "artifacts",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := objects.DeletePrefix(ctx, artifactstore.WorkspacePrefix(workspaceID))
				return workspacepurge.Removed{Rows: rows, Note: "shared artifact objects"}, err
			},
		})
	}

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
	if s.pluginStores != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "plugins",
			Purge:    s.pluginStores.PurgeWorkspace,
		})
	}
	if s.skillStores != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "skills",
			Purge:    s.skillStores.PurgeWorkspace,
		})
	}
	if s.buildTraces != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "studio-traces",
			Purge:    s.buildTraces.PurgeWorkspace,
		})
	}
	if s.workspaceLayout.Root() != "" {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "studio-learning",
			Purge:    s.purgeStudioLearning,
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
	if s.workspaceSettings != nil {
		store := s.workspaceSettings
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "workspace-settings",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := store.PurgeWorkspace(ctx, workspaceID)
				return workspacepurge.Removed{Rows: rows}, err
			},
		})
	}

	if vault, ok := s.credVault.(credentials.WorkspaceEraser); ok {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "secrets",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := vault.EraseWorkspace(ctx, workspaceID)
				return workspacepurge.Removed{Rows: rows, Note: "wrapped workspace DEKs destroyed before ciphertext and version history"}, err
			},
		})
	}
	if s.authConnections != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "authenticated-connections",
			Purge:    s.authConnections.PurgeWorkspace,
		})
	}
	if store, ok := s.apiKeyStore.(apikeys.WorkspaceCredentialPurger); ok {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "credentials",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := store.RevokeWorkspace(ctx, workspaceID)
				return workspacepurge.Removed{Rows: rows, Note: "workspace-bound managed credentials revoked before hash purge"}, err
			},
		})
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "api-keys",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := store.PurgeWorkspace(ctx, workspaceID)
				return workspacepurge.Removed{Rows: rows, Note: "revoked credential hashes purged"}, err
			},
		})
	}
	if s.channels != nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "channels",
			Purge: func(_ context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := s.channels.PurgeWorkspace(workspaceID)
				return workspacepurge.Removed{Rows: rows, Note: "live tenant channel connections disconnected and unbound"}, err
			},
		})
		// Webhook destinations are channel adapters in this architecture. The
		// explicit, idempotent second phase keeps the ownership catalog honest.
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "webhooks",
			Purge: func(_ context.Context, workspaceID string) (workspacepurge.Removed, error) {
				rows, err := s.channels.PurgeWorkspace(workspaceID)
				return workspacepurge.Removed{Rows: rows, Note: "webhook channel destinations disabled and unbound"}, err
			},
		})
	}
	purgers = append(purgers, workspacepurge.Purger{Resource: "shares", Purge: purgeWorkspaceShares})

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
				if s.runReg != nil && s.runReg.shared != nil {
					controls, err := s.runReg.shared.PurgeWorkspace(ctx, workspaceID)
					removed.Rows += controls.Rows
					if err != nil {
						return removed, err
					}
				}
				removed.Note = "session ownership, attachments, and workflow checkpoints"
				return removed, nil
			},
		})
	}

	// Team/Scale has one canonical filesystem boundary per workspace. Remove
	// the export subtree in its own catalog phase, then remove the entire
	// workspace tree in the final workspace-files phase. workspacepurge.Run
	// executes purgers in ownership-catalog order, so subsystem and database
	// cleanup has already run before that final tree removal.
	//
	// Personal mode deliberately retains the historical per-subsystem layout.
	// It can purge export archives, but it never registers a whole-workspace
	// tree purger: the personal root is the installation itself.
	if root := s.workspaceLayout.Root(); root != "" {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "studio-drafts",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				return workspacepurge.PurgeLayoutTree(ctx, s.workspaceLayout, filepath.Join(root, "studio", "drafts"), workspaceID)
			},
		})
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "workspace-exports",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				return workspacepurge.PurgeLayoutTree(ctx, s.workspaceLayout, filepath.Join(root, "exports"), workspaceID)
			},
		})
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "workspace-files",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				return workspacepurge.PurgeLayoutTree(ctx, s.workspaceLayout, root, workspaceID)
			},
		})
	} else if ws, err := config.ResolveWorkspace(); err == nil {
		purgers = append(purgers, workspacepurge.Purger{
			Resource: "workspace-exports",
			Purge: func(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
				return workspacepurge.PurgeSubtree(ctx, ws.Root, workspaceID, "exports")
			},
		})
	}

	return purgers
}
