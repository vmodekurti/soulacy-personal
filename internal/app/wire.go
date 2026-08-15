package app

// wire.go — App.Run: the full subsystem wiring, extracted verbatim from
// cmd/soulacy/main.go (Story E10 part 3). Order matters and is documented
// inline; deferred closes run when Run returns, mirroring the original
// run() semantics exactly.

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/channels"
	httpchan "github.com/soulacy/soulacy/internal/channels/http"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/events"
	"github.com/soulacy/soulacy/internal/gateway"
	"github.com/soulacy/soulacy/internal/hooks"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/ownership"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
)

// Run wires every subsystem and blocks until the gateway exits (parent ctx
// cancellation and SIGINT/SIGTERM both shut the process down cleanly).
func (a *App) Run(parent context.Context) error {
	cfg, log := a.cfg, a.log
	defer log.Sync() //nolint:errcheck

	log.Info("Soulacy starting", zap.String("version", config.Version))
	if err := ownership.ValidateCatalog(); err != nil {
		return fmt.Errorf("resource ownership catalog: %w", err)
	}
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		if blockers := ownership.MultiUserBlockers(); len(blockers) > 0 {
			preview := blockers
			if len(preview) > 5 {
				preview = preview[:5]
			}
			return fmt.Errorf("multi-user storage isolation is incomplete (%d personal-only tables, first: %s)", len(blockers), strings.Join(preview, ", "))
		}
	}

	// ── Ordered shutdown stack (Story ARCH-4) ────────────────────────────────
	// Subsystems register their resource closers here as they come up; the
	// stack drains in LIFO order (reverse of construction) when Run returns,
	// mirroring the original deferred-Close() semantics exactly.
	stack := newCloserStack(log)
	defer func() { _ = stack.Close() }()

	// Workspace paths ("soulspace"): one organized root for every default
	// location. Legacy flat ~/.soulacy installations resolve to their
	// historical paths untouched (migrate with `sy workspace migrate`).
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	log.Info("workspace", zap.String("root", ws.Root), zap.Bool("legacy", ws.Legacy))
	var personalTenant *tenancy.PersonalTenant
	var tenantResolver tenancy.Resolver
	var tenantIdentityLinker auth.IdentityLinker
	var tenantPool *pgxpool.Pool
	if cfg.DeploymentMode() == config.DeploymentModePersonal {
		tenant, plan, tenantErr := tenancy.EnsurePersonalTenant(parent, ws)
		if tenantErr != nil {
			// The migration is additive and its fresh-file path is atomic. Keep
			// Personal installations usable while making the recovery action loud.
			log.Error("implicit personal tenant bootstrap failed; continuing in legacy-compatible mode",
				zap.Error(tenantErr),
				zap.String("catalog", plan.DatabasePath),
				zap.String("recovery", "run `sy workspace migrate --plan`, fix the reported filesystem/database error, then restart"))
		} else {
			personalTenant = &tenant
			tenantResolver = tenancy.NewPersonalResolver(tenant)
			log.Info("implicit personal tenant ready",
				zap.String("organization_id", tenant.OrganizationID),
				zap.String("workspace_id", tenant.WorkspaceID),
				zap.Int("migration_version", plan.Version))
		}
	}

	// Sweep stale per-run scratch dirs (Story E24 shared mounts) left by a
	// crashed previous run; live ones are recreated by their owners below.
	a.sweepScratch(ws)

	// ── Credential vault + global secrets (SEC-8) ────────────────────────────
	// Built early so vault-stored secrets are migrated out of config.yaml and
	// overlaid onto the in-memory config BEFORE the LLM router and channel
	// adapters read their api_keys / tokens. Secrets live only at runtime in the
	// encrypted vault under the workspace (~/.soulacy/soulspace/credentials.db).
	credVault := a.wireCredentialVault(ws, stack)
	a.wireSecrets(credVault)

	// ── Agent brain memory (episodic / semantic / procedural) ────────────────
	brainStores := a.wireBrainMemory(ws, stack)
	learningStore := a.wireLearning(ws)

	// ── Memory ───────────────────────────────────────────────────────────────
	fileStore, archive, err := a.wireMemory(stack)
	if err != nil {
		return err
	}

	// ── Storage backend (action log + memory archive) ─────────────────────
	actionBackend, memBackend, err := a.wireStorageBackend(parent, ws, archive, stack)
	if err != nil {
		return err
	}
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		poolConfig, parseErr := pgxpool.ParseConfig(cfg.Storage.PostgresDSN)
		if parseErr != nil {
			return fmt.Errorf("tenancy postgres configuration: %w", parseErr)
		}
		poolConfig.MaxConns = 20
		poolConfig.MinConns = 2
		var poolErr error
		tenantPool, poolErr = pgxpool.NewWithConfig(parent, poolConfig)
		if poolErr != nil {
			return fmt.Errorf("tenancy postgres pool: %w", poolErr)
		}
		stack.push("tenancy-postgres", func() error { tenantPool.Close(); return nil })
		pingCtx, pingCancel := context.WithTimeout(parent, 15*time.Second)
		poolErr = tenantPool.Ping(pingCtx)
		pingCancel()
		if poolErr != nil {
			return fmt.Errorf("tenancy postgres ping: %w", poolErr)
		}
		store, storeErr := tenancy.OpenPostgres(parent, tenantPool)
		if storeErr != nil {
			return fmt.Errorf("tenancy postgres store: %w", storeErr)
		}
		tenantResolver = store
		tenantIdentityLinker = store
		log.Info("multi-user tenancy catalog ready", zap.String("mode", cfg.DeploymentMode()))
	}

	// ── Plugin database migrations (Story E16) ───────────────────────────────
	a.applyCompiledPluginMigrations(ws)

	// ── LLM Router ───────────────────────────────────────────────────────────
	llmRouter := a.wireLLMRouter()
	ollamaCfg := cfg.LLM.Providers["ollama"]

	// ── Loaders (agent / plugin / skill) + python pre-flight ─────────────────
	loader, pluginLoader, skillLoader := a.wireLoaders(ws)

	// ── Event Hub (GUI real-time stream + action-log persistence) ─────────────
	hub := gateway.NewEventHub(log, actionBackend)

	// Plugin load diagnostics → Logs GUI (Story E22): every plugin the
	// loader refused or skipped at boot becomes a visible error event, so a
	// silently absent plugin is always explainable without shell access.
	for _, d := range pluginLoader.Diagnostics() {
		hub.Emit(message.Event{
			Type: "error",
			Payload: map[string]any{
				"stage":  "plugin-load",
				"dir":    d.Dir,
				"error":  d.Reason,
				"action": "plugin skipped — gateway continues without it",
			},
			Timestamp: time.Now().UTC(),
		})
	}

	// ── Engine ───────────────────────────────────────────────────────────────
	toolTimeout, _ := time.ParseDuration(cfg.Runtime.ToolTimeout)
	if toolTimeout == 0 {
		toolTimeout = 30 * time.Second
	}
	// Web Search configuration: Ollama, Tavily, Serper.
	searchProvider := cfg.Search.Provider
	if searchProvider == "" {
		searchProvider = "ollama"
	}
	searchAPIKey := cfg.Search.APIKey
	if searchAPIKey == "" {
		switch searchProvider {
		case "ollama":
			searchAPIKey = os.Getenv("OLLAMA_API_KEY")
			if searchAPIKey == "" {
				if oc, ok := cfg.LLM.Providers["ollama"]; ok {
					searchAPIKey = oc.APIKey
				}
			}
		case "tavily":
			searchAPIKey = os.Getenv("TAVILY_API_KEY")
		case "serper":
			searchAPIKey = os.Getenv("SERPER_API_KEY")
		}
	}

	// Legacy fallback for compatibility
	ollamaAPIKey := ""
	if searchProvider == "ollama" {
		ollamaAPIKey = searchAPIKey
	} else {
		ollamaAPIKey = os.Getenv("OLLAMA_API_KEY")
		if ollamaAPIKey == "" {
			if oc, ok := cfg.LLM.Providers["ollama"]; ok {
				ollamaAPIKey = oc.APIKey
			}
		}
	}

	// ── MCP client (connects to configured MCP servers; tools auto-injected into every agent) ──
	mcpServers := make(map[string]mcp.ServerConfig, len(cfg.MCP.Servers))
	for id, sc := range cfg.MCP.Servers {
		mcpServers[id] = mcp.ServerConfig{
			Transport: sc.Transport,
			Command:   sc.Command,
			Args:      sc.Args,
			Env:       sc.Env,
			URL:       sc.URL,
			Headers:   sc.Headers,
		}
	}
	mcpClient := mcp.New(mcp.Config{Servers: mcpServers}, log)
	stack.pushClose("mcp-client", mcpClient)

	// ── Knowledge (RAG) — SQLite + sqlite-vec + Ollama embeddings ─────────────
	// Disabled silently when the DB path is empty. An unreachable embedder is
	// not fatal — it surfaces at kb_search call time with a clear error.
	knowledgeSvc := a.wireKnowledge(ollamaCfg.BaseURL, llmRouter, stack)

	// ── Vector Memory (optional semantic tier) ───────────────────────────────
	//   vector.backend = "qdrant"     → Qdrant REST
	//   vector.backend = "sqlite-vec" → built-in sqlite-vec
	//   memory.vector_db (legacy key) → same
	// When both are unset, vector memory is disabled.
	vectorStore, vecBackend := a.wireVector(archive, llmRouter)
	_ = vecBackend // available for future memory-tool use; engine uses vectorStore directly
	if brainStores != nil && vectorStore != nil {
		// One adapter per workspace, each binding the tenant once. The vector
		// store itself is shared and scopes internally; binding here means the
		// agentmemory side never has to carry a workspace through an interface
		// that has nowhere to put one.
		brainStores.SetSemanticStores(func(workspaceID string) agentmemory.SemanticStore {
			return &agentMemoryVectorAdapter{store: vectorStore, workspaceID: wsroot.Normalize(workspaceID)}
		})
		log.Info("agent brain semantic memory enabled (sqlite-vec)")
	}

	// ── Python Executor Backend ───────────────────────────────────────────────
	// "process" (default): one python3 subprocess per call, simple + compatible.
	// "pool": N pre-forked persistent workers, eliminates interpreter cold-start.
	pyExecutor := a.wirePythonExecutor(stack, credVault)
	// Named backends agents can opt into via `execution.backend` (local/docker/ssh).
	namedExecutors := a.wireNamedExecutors(credVault)

	// ── Queue Backend ─────────────────────────────────────────────────────────
	// Resolved through the SDK factory registry (Story E10).
	queueBackend, err := a.wireQueue(stack)
	if err != nil {
		return err
	}

	// ── Event publishing (extensibility E1) ───────────────────────────────────
	// Every EventHub emission is wrapped in a schema-v1 envelope and published
	// to the queue backend on "soulacy.events.<type>" (see docs/EVENTS.md).
	eventPublisher := events.NewPublisher(queueBackend, log)
	stack.pushClose("event-publisher", eventPublisher)
	hub.SetEventPublisher(eventPublisher)
	log.Info("event publishing ready", zap.String("subject", "soulacy.events.>"))

	// ── Outbound webhooks (extensibility E2) ──────────────────────────────────
	// Queue-buffered, HMAC-signed, best-effort with bounded retries.
	if len(cfg.Hooks) > 0 {
		hookDispatcher := hooks.NewDispatcher(queueBackend, cfg.Hooks, log)
		if err := hookDispatcher.Start(context.Background()); err != nil {
			log.Warn("webhook dispatcher failed to start", zap.Error(err))
		} else {
			stack.pushClose("webhook-dispatcher", hookDispatcher)
			log.Info("webhooks ready", zap.Int("endpoints", len(cfg.Hooks)))
		}
	}

	// pluginAdapter bridges plugins.Loader → runtime.PluginToolProvider.
	var pluginProvider runtime.PluginToolProvider
	if pluginLoader.Count() > 0 {
		pluginProvider = &pluginToolAdapter{loader: pluginLoader}
	}

	// ── Engine ───────────────────────────────────────────────────────────────
	// Construction + all host-side configuration (executor, reasoning keys,
	// sandbox, brain memory, audit, allowlist, SSRF) is delegated to wireEngine.
	engine := a.wireEngine(engineDeps{
		loader:         loader,
		llmRouter:      llmRouter,
		fileStore:      fileStore,
		actionBackend:  actionBackend,
		memBackend:     memBackend,
		hub:            hub,
		skillLoader:    skillLoader,
		mcpClient:      mcpClient,
		knowledgeSvc:   knowledgeSvc,
		vectorStore:    vectorStore,
		pluginProvider: pluginProvider,
		pyExecutor:     pyExecutor,
		namedExecutors: namedExecutors,
		brainStores:    brainStores,
		learningStore:  learningStore,
		ollamaAPIKey:   ollamaAPIKey,
		searchProvider: searchProvider,
		searchAPIKey:   searchAPIKey,
		toolTimeout:    toolTimeout,
	})

	// App-lifetime context. Created here (before scheduler + channels) so
	// every long-running subsystem derives from it and SIGTERM (or parent
	// cancellation) cancels them all in unison.
	ctx, cancel := context.WithCancel(parent)
	stack.push("app-context-cancel", func() error { cancel(); return nil })

	learningSweeper := learning.NewSweeper(learning.SweeperConfig{
		Stores:  learningStore,
		Actions: actionBackend,
		Agents:  loader,
		Logger:  log.Named("learning-sweeper"),
	})
	learningSweeper.Start(ctx)

	// ── Scheduler ────────────────────────────────────────────────────────────
	sched := scheduler.New(engine, loader, log, ctx)
	sched.RequirePrincipal(config.IsMultiUserMode(cfg.DeploymentMode()))
	if personalTenant != nil {
		sched.SetPrincipal(runtime.Principal{
			Subject: "scheduler", OrganizationID: personalTenant.OrganizationID,
			WorkspaceID: personalTenant.WorkspaceID, MembershipID: personalTenant.MembershipID,
			Role: "admin", CredentialID: "service:scheduler", Kind: "service",
		})
	}
	sched.SetStatePath(filepath.Join(cfg.Memory.Dir, "scheduler-state.json"))
	sched.SetEventSink(hub) // record scheduled-delivery outcomes in Activity
	// Readiness gate (ST-16): a Studio-deployed agent may only fire on a
	// schedule once its deployment carries passing certification. The store is
	// re-read on every tick, so re-certifying unblocks the schedule without a
	// restart; agents with no deployment record are unaffected.
	sched.SetReadinessGate(deploymentReadinessGate(studio.NewDeploymentStore(studio.DeploymentsDir(ws.Root)), sched.PrincipalWorkspace))
	for _, def := range loader.All() {
		if err := sched.RegisterAgent(def); err != nil {
			log.Warn("scheduler register failed", zap.String("agent", def.ID), zap.Error(err))
		}
	}

	// Credential vault + secrets were wired early (see top of Run) so config
	// secrets are overlaid before the LLM router / channels are built.

	// ── Channel Registry ─────────────────────────────────────────────────────
	chanReg := channels.NewRegistry(512)
	chanReg.SetLogger(log)
	engine.SetChannelRegistry(chanReg)
	sched.SetChannelRegistry(chanReg)
	channelDefaults := scheduler.DefaultOutputsFromChannelConfig(cfg.Channels)
	engine.SetChannelDefaultOutputs(channelDefaults)
	sched.SetDefaultOutputs(channelDefaults)
	httpAdapter := httpchan.New()
	chanReg.Register(httpAdapter)

	// ── Plugin manifest-v2 contributions (E7) ─────────────────────────────────
	a.wirePluginContributions(ctx, ws, pluginLoader, chanReg, llmRouter, credVault)

	// Start optional channel adapters based on config. Construction is
	// registry-routed (Story E10); the host keeps config-shape handling
	// (single-bot vs multi-bot lists, adapter ids, system-agent guard).
	// registerChannels does the full registration and returns the concrete
	// WhatsApp adapter (needed by the gateway's webhook routes) or nil.
	waAdapter := a.registerChannels(cfg.Channels, chanReg, loader, ws)

	if errs := chanReg.StartAll(ctx); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("channel start error", zap.Error(e))
		}
	}
	stack.push("channel-registry", func() error { _ = chanReg.StopAll(); return nil })

	// Engine ← failure notifier. Run errors produce a real outbound
	// notification (per agent's notify_on_failure block). Wired AFTER
	// chanReg.StartAll so adapters are connected before the first cron tick
	// or HTTP request can trigger a failure path.
	engine.SetFailureNotifier(&failureNotifier{chanReg: chanReg, log: log})

	sched.Start()
	stack.push("scheduler", func() error { sched.Stop(); return nil })

	// PRODUCTION_AUDIT → F2 (2026-05-27): replay any in-flight runs that the
	// previous gateway process didn't finish. Bounded to the last hour;
	// synchronous HTTP + cron triggers are skipped.
	replayIncompleteRuns(actionBackend, chanReg, log)

	// S2.2 — stop the session-eviction sweep goroutine on shutdown so it
	// doesn't leak across restarts.
	stack.push("session-eviction", func() error { engine.StopSessionEviction(); return nil })

	// ── Message Router — bounded worker pool draining the shared inbox ──────
	a.startMessageRouter(ctx, chanReg, loader, engine, personalTenant)

	// ── Auth Engine ───────────────────────────────────────────────────────────
	authEngine, err := a.wireAuth(stack)
	if err != nil {
		return err
	}
	if tenantIdentityLinker != nil {
		authEngine.SetIdentityLinker(tenantIdentityLinker)
	}
	if members, ok := tenantResolver.(tenancy.MemberManager); ok {
		authEngine.SetRefreshAuthorizer(members.CanRefreshUser)
	}

	// ── RBAC Manager ──────────────────────────────────────────────────────────
	rbacManager := a.wireRBAC(ws, stack)

	// ── Engine-attached stores (checkpoint / telemetry / cost) ───────────────
	openedCostStore := a.wireEngineExtras(ctx, ws, engine, llmRouter, stack)

	// ── Gateway Server ────────────────────────────────────────────────────────
	// Construction + every host-side capability (plugin GUI mounts, installer,
	// safety pipeline, registries, voice, workboard/ratelimit/apikey/dlq/history
	// stores, file watcher) is delegated to wireGateway.
	srv := a.wireGateway(gatewayDeps{
		ws:              ws,
		engine:          engine,
		loader:          loader,
		llmRouter:       llmRouter,
		chanReg:         chanReg,
		sched:           sched,
		httpAdapter:     httpAdapter,
		waAdapter:       waAdapter,
		skillLoader:     skillLoader,
		actionBackend:   actionBackend,
		mcpClient:       mcpClient,
		hub:             hub,
		authEngine:      authEngine,
		rbacManager:     rbacManager,
		credVault:       credVault,
		pluginLoader:    pluginLoader,
		openedCostStore: openedCostStore,
		tenantResolver:  tenantResolver,
		tenantPool:      tenantPool,
	}, stack)

	// ── KB ingestion worker ───────────────────────────────────────────────────
	// Document ingestion runs OUT of the HTTP request: uploads are spooled to
	// disk and recorded in a durable job catalog, and this worker drains it —
	// chunking, embedding in batches, reporting progress, retrying transient
	// failures with bounded backoff. On startup it also requeues any job a crash
	// left mid-flight, so a document can't silently go missing.
	if knowledgeSvc != nil {
		ingestWorker := knowledge.NewWorker(knowledgeSvc, knowledge.WorkerOptions{
			MaxDocumentBytes: cfg.Knowledge.MaxDocumentBytes,
		}, log)
		ingestWorker.SetProgressSink(srv.IngestProgressSink())
		srv.SetIngestWorker(ingestWorker)
		ingestWorker.Start(ctx)
		log.Info("knowledge ingestion worker started")
	}

	// Graceful shutdown on SIGINT / SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
		cancel()
	}()

	log.Info("gateway ready",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.Bool("gui", cfg.Server.GUIEnabled),
	)

	return srv.Listen(ctx)
}
