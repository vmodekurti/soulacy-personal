package app

// wire_gateway.go — gateway server construction + configuration extracted from
// App.Run (Story ARCH-4). Builds the HTTP/GUI server and attaches every
// host-side capability (plugin GUI mounts, installer, safety pipeline, package
// registries, voice, cost/workboard/ratelimit/apikey/dlq/history stores, file
// watcher). Owned resources register their Close on the LIFO shutdown stack.
// Behavior is preserved verbatim from the original inline block.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/internal/artifactstore"
	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/builder"
	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/channels"
	httpchan "github.com/soulacy/soulacy/internal/channels/http"
	wachan "github.com/soulacy/soulacy/internal/channels/whatsapp"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/gateway"
	"github.com/soulacy/soulacy/internal/introspect"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/queue"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/ratelimit"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runs"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/schedules"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/skills"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/voice"
	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/internal/workspacepolicy"
	"github.com/soulacy/soulacy/internal/workspacesettings"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// gatewayDeps bundles the already-constructed subsystems the gateway server
// wires together. Keeps wireGateway's signature readable.
type gatewayDeps struct {
	ws              config.Paths
	engine          *runtime.Engine
	loader          *runtime.Loader
	llmRouter       *llm.Router
	chanReg         *channels.Registry
	sched           *scheduler.Scheduler
	httpAdapter     *httpchan.Adapter
	waAdapter       *wachan.Adapter
	skillLoader     *skills.Loader
	skillStores     *skills.Stores
	runStore        *runs.Store
	actionBackend   storage.ActionLogBackend
	mcpClient       *mcp.Client
	hub             *gateway.EventHub
	authEngine      *auth.Engine
	rbacManager     *rbac.Manager
	credVault       credentials.Vault
	pluginLoader    *plugins.Loader
	pluginStores    *plugins.Stores
	queueBackend    queue.Backend
	openedCostStore *costs.Store
	approvalStore   *approvals.Store
	scheduleStore   *schedules.Store
	tenantResolver  tenancy.Resolver
	// workspaceLifecycle is nil on deployments with no workspace lifecycle to
	// manage, which is what makes the deletion routes answer 503 there rather
	// than reporting a deletion nothing recorded.
	workspaceLifecycle tenancy.WorkspaceLifecycle
	// workspacePolicies holds the limits each workspace has set on itself
	// (MU-030 criterion 1). Nil keeps the flat YAML ceilings as the only
	// limits, which is what every existing deployment has.
	workspacePolicies *workspacepolicy.Store
	workspaceSettings *workspacesettings.Store
	// workspaceMCPServers is the durable registry of servers a workspace
	// defined for itself, as opposed to the operator's config.yaml template.
	workspaceMCPServers *mcpstore.Store
	costGovernor        *costs.Governor
	tenantPool          *pgxpool.Pool
}

// wireGateway builds the gateway server and attaches every host capability.
// Owned resources push their Close on stack. Returns the configured server,
// ready for Listen().
func (a *App) wireGateway(d gatewayDeps, stack *closerStack) (*gateway.Server, error) {
	cfg, cfgPath, log := a.cfg, a.cfgPath, a.log
	ws := d.ws

	// Created BEFORE the watcher so the watcher can wire its OnPyChange hook
	// to the server's tool-catalog cache.
	srv := gateway.New(cfg, cfgPath, d.engine, d.loader, d.llmRouter, d.chanReg, d.sched, d.httpAdapter, d.waAdapter, d.skillLoader, d.actionBackend, d.mcpClient, d.hub, log)
	if raw := strings.TrimSpace(cfg.Deployment.SharedArtifactStore); raw != "" {
		artifactCtx, artifactCancel := context.WithTimeout(context.Background(), 15*time.Second)
		objects, objectErr := artifactstore.OpenStore(artifactCtx, raw)
		artifactCancel()
		if objectErr != nil {
			if cfg.DeploymentMode() == config.DeploymentModeScale {
				return nil, fmt.Errorf("shared artifact store: %w", objectErr)
			}
			log.Warn("shared artifact store unavailable", zap.Error(objectErr))
		} else {
			stack.pushClose("shared-artifact-store", objects)
			srv.SetArtifactStore(objects)
			log.Info("shared artifact store ready", zap.String("root", raw))
		}
	}
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		srv.SetWorkspaceLayoutRoot(ws.Root)
	}
	// Per-workspace skill inventory. Without it the gateway falls back to the
	// single loader, which is what a personal deployment wants.
	// Per-workspace MCP servers. Set before any request can arrive; after this
	// no gateway handler reaches the deployment-wide client, which a guard
	// test in the gateway package enforces.
	if a.mcpPool != nil {
		srv.SetMCPPool(a.mcpPool)
	}
	// AFTER SetMCPPool: the store is handed to the pool through the server, so
	// installing it first would attach nothing.
	if d.workspaceMCPServers != nil {
		srv.SetMCPServerStore(d.workspaceMCPServers)
	}
	// A durable replay cache, in every mode.
	//
	// Not gated on multi-user, unlike the credential rules: the in-memory
	// cache broke a personal installation too. A restart between a client's
	// original request and its retry emptied the map, and the retry then
	// executed the mutation a second time — and the restart is usually WHY the
	// client retried. Multi-user adds the second replica to the same bug; it
	// did not create it.
	if store, err := gateway.OpenIdempotencyCache(d.ws.DB("idempotency")); err != nil {
		log.Warn("durable idempotency cache unavailable; a retry after a restart may run twice",
			zap.Error(err))
	} else {
		stack.pushClose("idempotency", store)
		srv.SetIdempotencyStore(store)
	}
	// Per-workspace plugin inventory, and the invalidation that makes a
	// revocation take effect on the next tool call rather than the next
	// restart.
	if d.pluginStores != nil {
		srv.SetPluginStores(d.pluginStores)
	}
	srv.SetReadinessProbes(a.readinessProbes(d))
	// Channels apply live (confighot: tenant-scoped, so a restart is not an
	// option). The gateway knows WHEN a channel changed; only the app can
	// build an adapter, because that needs the agent loader and the
	// capability-tier binding gate.
	if a.secretsManager != nil {
		srv.SetSecretsOverlay(a.secretsManager)
	}
	srv.SetChannelApplier(func(ctx context.Context, channelID string, previous, next map[string]any) error {
		return a.applyChannelLive(ctx, d.chanReg, d.loader, ws, channelID, previous, next)
	})
	// MU-035 criterion 3: the directory, not a list of databases. ReportDir
	// discovers what is actually there, so a store added without versioning
	// shows up as a database with no components rather than not at all.
	srv.SetSchemaDir(ws.Data)
	srv.SetSkillStores(d.skillStores)
	srv.SetAuth(d.authEngine)
	if d.tenantResolver != nil {
		srv.SetTenantResolver(d.tenantResolver)
	}
	if d.workspaceLifecycle != nil {
		srv.SetWorkspaceLifecycle(d.workspaceLifecycle)
	}
	if d.workspacePolicies != nil {
		srv.SetWorkspacePolicyStore(d.workspacePolicies)
	}
	if d.workspaceSettings != nil {
		srv.SetWorkspaceSettingsStore(d.workspaceSettings)
	}
	if d.approvalStore != nil {
		srv.SetApprovalStore(d.approvalStore)
	}
	if d.scheduleStore != nil {
		srv.SetScheduleStore(d.scheduleStore)
	}
	if knowledge := d.engine.Knowledge(); knowledge != nil && knowledge.Store != nil {
		srv.SetKnowledgeStore(knowledge.Store)
	}
	if checkpoints := d.engine.CheckpointStore(); checkpoints != nil {
		srv.SetCheckpointStore(checkpoints)
	}
	hotMemory, archiveMemory, vectorMemory := d.engine.MemoryStores()
	srv.SetMemoryStores(hotMemory, archiveMemory, vectorMemory)
	if d.costGovernor != nil {
		srv.SetCostGovernor(d.costGovernor)
	}
	logEffectiveSecuritySummary(log, cfg, d.authEngine != nil && d.authEngine.Effective())
	srv.SetRBAC(d.rbacManager)
	if d.credVault != nil {
		srv.SetCredentialVault(d.credVault)
	}

	// Plugin GUI mounts + capability enforcement for scoped plugin tokens
	// (Story E8). Every loaded plugin's capability set registers with the
	// enforcer; GUI mounts surface in the shell nav.
	{
		capsEnforcer := caps.NewEnforcer(audit.NewWithRetention(cfg.Runtime.AuditDir, config.RetentionDuration(cfg.Runtime.Retention.AuditLogs, 30*24*time.Hour)), log)
		var uiMounts []gateway.PluginUIMount
		for _, lp := range d.pluginLoader.All() {
			if lp.Caps != nil {
				// d.pluginLoader is the PERSONAL workspace's loader (see
				// wireLoaders), so these are personal's grants. Other
				// workspaces get theirs when their plugins are first
				// resolved, and lose them when a plugin is revoked — see
				// Server.pluginsChanged.
				capsEnforcer.SetPluginSet(wsroot.PersonalWorkspaceID, lp.Caps)
			}
			if staticDir, nav, ok := lp.GUIMount(); ok {
				uiMounts = append(uiMounts, gateway.PluginUIMount{
					ID: lp.Manifest.ID, StaticDir: staticDir,
					NavLabel: nav.Label, NavIcon: nav.Icon,
				})
			}
		}
		srv.SetCapEnforcer(capsEnforcer)
		if len(uiMounts) > 0 {
			srv.SetPluginUI(uiMounts)
			log.Info("plugin GUI mounts ready", zap.Int("count", len(uiMounts)))
		}
	}

	// The durable run store is opened in wire.go, before the worker pool, so
	// the router and the API share one handle.
	srv.SetRunStore(d.runStore)

	// Plugin install & management (Story E13): installer rooted at the first
	// plugin_dirs entry. Staged plugins live under <root>/.staging and never
	// load; activation requires explicit approval through the API/GUI.
	if len(cfg.PluginDirs) > 0 {
		if pins, pierr := plugininstall.New(cfg.PluginDirs[0]); pierr != nil {
			log.Warn("plugin installer unavailable", zap.Error(pierr))
		} else {
			srv.SetPluginInstaller(pins)
			log.Info("plugin installer ready", zap.String("dir", cfg.PluginDirs[0]))
		}
		// Per-workspace installers over the same root: personal resolves to it
		// unchanged, every other tenant installs beneath its own namespace and
		// the loader for that workspace is the one that picks the plugin up.
		installers := plugininstall.NewInstallers(cfg.PluginDirs[0])
		if config.IsMultiUserMode(cfg.DeploymentMode()) {
			installers.SetWorkspaceLayoutRoot(ws.Root)
		}
		srv.SetPluginInstallers(installers)
	}

	// Pre-installation safety introspection (Story E20): static scan always
	// runs; the LLM audit uses the router's default provider (degrades to a
	// skip finding when no provider answers); the dry-run reuses the F1
	// rlimit sandbox when enabled.
	{
		pipeline := &introspect.Pipeline{}
		if len(d.llmRouter.ProviderIDs()) > 0 {
			pipeline.Auditor = &introspect.RouterAuditor{Router: d.llmRouter}
		}
		if sbx := cfg.Runtime.Sandbox; sbx.Enabled {
			if selfPath, e := os.Executable(); e == nil {
				pipeline.DryRun = &introspect.DryRunConfig{
					SelfPath: selfPath,
					Limits: sandbox.Limits{
						Enabled:    true,
						CPUSeconds: sbx.CPUSeconds,
						MemoryMB:   sbx.MemoryMB,
						OpenFiles:  sbx.OpenFiles,
						FileSizeMB: sbx.FileSizeMB,
					},
					Timeout: 5 * time.Second,
				}
			}
		} else {
			// Even unsandboxed hosts get the bounded dry-run (timeout +
			// write detection + dead HTTP proxy).
			pipeline.DryRun = &introspect.DryRunConfig{Timeout: 5 * time.Second}
		}
		srv.SetSafetyPipeline(pipeline)
	}

	// Package registries (Story E19): multi-registry resolution engine for
	// skill/plugin installs, built from the `registries:` config block.
	// Consumed by `sy skill install` (E18) and the GUI install flow; config
	// errors surface at boot but never block startup.
	if len(cfg.Registries) > 0 {
		regEngine, regErrs := pkgregistry.FromConfig(cfg.Registries, log)
		for _, re := range regErrs {
			log.Warn("package registry entry skipped", zap.Error(re))
		}
		if ids := regEngine.Providers(); len(ids) > 0 {
			log.Info("package registries configured", zap.Strings("ids", ids))
		}
	}

	// Voice is independent of the agent LLM. OpenAI keeps its direct realtime
	// WebRTC path; sidecar mode proxies STT/TTS to a local speech process and
	// feeds the transcript through Soulacy's ordinary chat pipeline.
	if cfg.Voice.Provider == "openai" {
		voiceKey := os.Getenv("OPENAI_API_KEY")
		if oc, ok := cfg.LLM.Providers["openai"]; ok && oc.APIKey != "" {
			voiceKey = oc.APIKey
		}
		minter := voice.NewOpenAIMinter(voiceKey, cfg.Voice.Model, cfg.Voice.BaseURL)
		srv.SetVoiceMinter(minter)
		if ready, detail := minter.Ready(); ready {
			log.Info("realtime voice ready", zap.String("provider", "openai"), zap.String("model", minter.Model()))
		} else {
			log.Warn("realtime voice configured but not ready", zap.String("detail", detail))
		}
	} else if cfg.Voice.Provider == "sidecar" {
		sidecar, err := voice.NewSidecar(cfg.Voice.SidecarURL, cfg.Voice.Voice, cfg.Voice.Timeout, cfg.Voice.AllowRemote)
		if err != nil {
			log.Warn("voice sidecar configuration rejected", zap.Error(err))
		} else {
			srv.SetVoiceSidecar(sidecar)
			if ready, detail := sidecar.Ready(); ready {
				log.Info("voice sidecar ready", zap.String("url", sidecar.URL()))
			} else {
				log.Warn("voice sidecar configured but not ready", zap.String("detail", detail))
			}
		}
	} else if cfg.Voice.Provider != "" {
		log.Warn("unsupported voice provider; voice panel disabled",
			zap.String("provider", cfg.Voice.Provider))
	}

	// Wire the cost store into the gateway so /api/v1/costs routes work.
	if d.openedCostStore != nil {
		srv.SetCostStore(d.openedCostStore)
	}

	// ── Workboard Store (Story 5) ─────────────────────────────────────────────
	workboardPath := ws.DB("workboard")
	if wbStore, wberr := workboard.NewStore(workboardPath); wberr != nil {
		log.Warn("workboard store unavailable", zap.Error(wberr))
	} else {
		stack.pushClose("workboard-store", wbStore)
		srv.SetWorkboardStore(wbStore)
		log.Info("workboard ready", zap.String("path", workboardPath))
	}

	// ── Rate Limiter (Task #33) ───────────────────────────────────────────────
	rlCfg := ratelimit.Config{
		Enabled:           cfg.RateLimit.Enabled,
		PerUserRPM:        cfg.RateLimit.PerUserRPM,
		PerAgentRPM:       cfg.RateLimit.PerAgentRPM,
		PerUserTokensDay:  cfg.RateLimit.PerUserTokensDay,
		PerAgentTokensDay: cfg.RateLimit.PerAgentTokensDay,
		Backend:           cfg.RateLimit.Backend,
		RedisURL:          cfg.RateLimit.RedisURL,
	}
	if !rlCfg.Enabled && rlCfg.PerUserRPM == 0 && rlCfg.PerAgentRPM == 0 {
		rlCfg = ratelimit.DefaultConfig()
	}
	if rlManager, rlErr := ratelimit.New(rlCfg, log); rlErr != nil {
		if config.IsMultiUserMode(cfg.DeploymentMode()) || strings.EqualFold(rlCfg.Backend, "redis") {
			return nil, fmt.Errorf("rate limiter: %w", rlErr)
		}
		log.Warn("rate limiter init failed, running without rate limiting", zap.Error(rlErr))
	} else {
		stack.pushClose("rate-limiter", rlManager)
		srv.SetRateLimiter(rlManager)
		log.Info("rate limiter ready",
			zap.Bool("enabled", rlCfg.Enabled),
			zap.Int("per_user_rpm", rlCfg.PerUserRPM),
			zap.Int("per_agent_rpm", rlCfg.PerAgentRPM),
			zap.Int("per_user_tokens_day", rlCfg.PerUserTokensDay),
			zap.String("backend", rlCfg.Backend),
		)
	}

	// ── API Key Store ─────────────────────────────────────────────────────────
	var akStore apikeys.Store
	var akErr error
	apiKeyLocation := ws.DB("apikeys")
	if d.tenantPool != nil {
		akStore, akErr = apikeys.NewPostgresStore(context.Background(), d.tenantPool)
		apiKeyLocation = "postgres"
	} else {
		akStore, akErr = apikeys.NewSQLiteStore(apiKeyLocation)
	}
	if akErr != nil {
		log.Warn("api key store unavailable", zap.Error(akErr))
	} else {
		stack.pushClose("apikey-store", akStore)
		srv.SetAPIKeyStore(akStore)
		d.authEngine.SetAPIKeyStore(akStore) // wire into auth middleware (sk_ prefix validation)
		log.Info("api key store ready", zap.String("backend", apiKeyLocation))
	}

	// ── Dead-Letter Queue ─────────────────────────────────────────────────────
	dlqPath := ws.DB("dlq")
	if dlqStore, dlqErr := dlq.NewSQLiteStore(dlqPath); dlqErr != nil {
		log.Warn("dead-letter queue unavailable", zap.Error(dlqErr))
	} else {
		stack.pushClose("dlq-store", dlqStore)
		srv.SetDLQStore(dlqStore)
		d.engine.SetDLQStore(&engineDLQAdapter{s: dlqStore}) // wire into engine failure path
		log.Info("dead-letter queue ready", zap.String("path", dlqPath))
	}

	// ── Conversation History Store ────────────────────────────────────────────
	var historyStore session.HistoryStore
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		historyCtx, historyCancel := context.WithTimeout(context.Background(), 15*time.Second)
		histStore, histErr := session.NewPostgresHistoryStore(historyCtx, cfg.Storage.PostgresDSN)
		historyCancel()
		if histErr != nil {
			return nil, fmt.Errorf("shared conversation history: %w", histErr)
		}
		historyStore = histStore
		log.Info("shared conversation history ready", zap.String("backend", "postgres"))
	} else {
		historyPath := ws.DB("history")
		histStore, histErr := session.NewSQLiteHistoryStore(historyPath, session.WithHistoryRetention(config.RetentionDuration(cfg.Runtime.Retention.ConversationHistory, 30*24*time.Hour)))
		if histErr != nil {
			log.Warn("conversation history store unavailable", zap.Error(histErr))
		} else {
			historyStore = histStore
			log.Info("conversation history ready", zap.String("path", historyPath))
		}
	}
	if historyStore != nil {
		stack.pushClose("history-store", historyStore)
		srv.SetHistoryStore(historyStore)
		d.engine.SetHistoryStore(historyStore)
	}

	// ── Session Ownership ─────────────────────────────────────────────────────
	// Durable, because an in-process map forgets who owns every open
	// conversation on restart and is never shared with a second replica.
	ownershipPath := ws.DB("session_owners")
	if ownerStore, ownerErr := session.NewSQLiteOwnershipStore(ownershipPath); ownerErr != nil {
		log.Warn("session ownership store unavailable — conversation authorization will not survive a restart", zap.Error(ownerErr))
	} else {
		stack.pushClose("session-ownership-store", ownerStore)
		srv.SetSessionOwnershipStore(ownerStore)
		log.Info("session ownership ready", zap.String("path", ownershipPath))
	}

	// ── Chat/Session Resource Store ───────────────────────────────────────────
	resourcePath := ws.DB("session_resources")
	if resStore, resErr := session.NewSQLiteStore(resourcePath, session.DefaultConfig()); resErr != nil {
		log.Warn("session resource store unavailable", zap.Error(resErr))
	} else {
		stack.pushClose("session-resource-store", resStore)
		srv.SetResourceStore(resStore)
		d.engine.SetResourceStore(resStore)
		log.Info("session resource store ready", zap.String("path", resourcePath))
	}

	// ── Builder Registry (E4 gap detection) ───────────────────────────────────
	builderReg := builder.NewRegistry()
	// Seed from MCP client's known tools, if available.
	// (Full integration with Python tool catalog is a follow-up.)
	srv.SetBuilderRegistry(builderReg)

	// ── File Watcher — hot-reload SOUL.yaml + invalidate tool catalog on .py edits ──
	fsWatcher, watchErr := runtime.NewWatcher(d.loader, d.sched, cfg.AgentDirs, log, srv.PythonToolDirs()...)
	if watchErr != nil {
		log.Warn("file watcher unavailable (agents require restart to reload)", zap.Error(watchErr))
	} else {
		fsWatcher.OnPyChange = srv.InvalidateToolCatalog
		fsWatcher.Start()
		srv.SetAgentWatcher(fsWatcher) // S2.13 — expose watcher liveness in deep health
		stack.push("file-watcher", func() error { fsWatcher.Stop(); return nil })
	}

	stack.pushClose("gateway-learning", srv)
	return srv, nil
}

// readinessProbes builds the dependency checks for GET /ready.
//
// REQUIREDNESS COMES FROM THE DEPLOYMENT MODE, and deliberately from the same
// place startup validation gets it: internal/config already refuses to boot a
// team deployment without Postgres, or a scale one without a durable queue.
// Readiness asks that same question at RUNTIME. A second list here would
// eventually disagree with the one that gates startup, and the disagreement
// would surface as a replica that booted and then never became ready, or —
// worse — one that stayed ready without the dependency it was told it needed.
//
// A personal deployment requires nothing shared, so its probes are all
// optional and its readiness reduces to its liveness. That is invariant 7 for
// this endpoint: a single-user install is always ready, exactly as it was
// before there was a readiness endpoint at all.
func (a *App) readinessProbes(d gatewayDeps) []gateway.ReadinessProbe {
	multiUser := config.IsMultiUserMode(a.cfg.DeploymentMode())
	scale := a.cfg.DeploymentMode() == config.DeploymentModeScale

	var probes []gateway.ReadinessProbe
	if d.tenantPool != nil {
		probes = append(probes, gateway.ReadinessProbe{
			Name:     "postgres",
			Required: multiUser,
			Check:    func(ctx context.Context) error { return d.tenantPool.Ping(ctx) },
		})
	} else if multiUser {
		// Required and ABSENT. Reported with a nil Check, which readiness
		// treats as "unprobeable" and therefore not ready — rather than
		// omitting the probe, which would make a deployment missing its
		// required database indistinguishable from one whose database is
		// fine. Silence is the failure mode this endpoint exists to remove.
		probes = append(probes, gateway.ReadinessProbe{Name: "postgres", Required: true})
	}

	if d.queueBackend != nil {
		// Probed through the interface the rest of the code uses, when the
		// backend offers a health check. The in-memory backend does not need
		// one — it cannot be unreachable — and scale mode refuses to start on
		// it, so an unprobeable queue in scale mode means a backend that
		// should have been rejected at boot.
		if pinger, ok := d.queueBackend.(interface{ Ping(context.Context) error }); ok {
			probes = append(probes, gateway.ReadinessProbe{
				Name:     "queue",
				Required: scale,
				Check:    func(ctx context.Context) error { return pinger.Ping(ctx) },
			})
		} else if scale {
			probes = append(probes, gateway.ReadinessProbe{Name: "queue", Required: true})
		}
	} else if scale {
		probes = append(probes, gateway.ReadinessProbe{Name: "queue", Required: true})
	}
	return probes
}
