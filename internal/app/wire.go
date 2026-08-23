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
	"github.com/soulacy/soulacy/internal/approvals"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/channels"
	httpchan "github.com/soulacy/soulacy/internal/channels/http"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/entitlements"
	"github.com/soulacy/soulacy/internal/events"
	"github.com/soulacy/soulacy/internal/gateway"
	"github.com/soulacy/soulacy/internal/hooks"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/mcpstore"
	"github.com/soulacy/soulacy/internal/ownership"
	"github.com/soulacy/soulacy/internal/releasegate"
	"github.com/soulacy/soulacy/internal/runs"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/schedules"
	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workspacelayout"
	"github.com/soulacy/soulacy/internal/workspacepolicy"
	"github.com/soulacy/soulacy/internal/workspacesettings"
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
	// MU-037 criterion 6: the release gate's own state is validated at boot and
	// its open items are LOGGED rather than fatal.
	//
	// Fatal would be wrong, and the distinction matters. An unclassified store
	// (below) is a hole somebody has not looked at. A recorded release-gate gap
	// is a hole somebody HAS looked at and written down — the Qdrant tests
	// needing a live instance, the load figures coming from a measurement
	// rather than a test. Refusing to boot on those would teach whoever is
	// blocked to delete the record instead of the gap, and the record is the
	// only reason anybody knows.
	if err := releasegate.Validate(); err != nil {
		return fmt.Errorf("release gate inventory: %w", err)
	}
	reportScaleReadiness(log, cfg)
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		for _, blocker := range releasegate.Blockers() {
			log.Warn("multi-tenant release gate: recorded gap", zap.String("gap", blocker))
		}
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
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		migration, migrateErr := workspacelayout.Migrate(ws.Root)
		if migrateErr != nil {
			return fmt.Errorf("migrate named workspaces into isolated roots: %w", migrateErr)
		}
		if len(migration.Moves) > 0 {
			log.Info("migrated legacy workspace filesystem layout",
				zap.Int("subtrees", len(migration.Moves)),
				zap.String("workspace_root", filepath.Join(ws.Root, wsroot.WorkspaceDir)))
		}
	}
	log.Info("workspace", zap.String("root", ws.Root), zap.Bool("legacy", ws.Legacy))
	var personalTenant *tenancy.PersonalTenant
	var tenantResolver tenancy.Resolver
	var workspaceLifecycle tenancy.WorkspaceLifecycle
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
			// A personal installation's only workspace is the installation
			// itself. The lifecycle is wired so the routes exist and REFUSE
			// with a remedy, rather than 503ing as if the feature were
			// misconfigured — the answer there is "remove the data directory",
			// and only a wired lifecycle can say so.
			workspaceLifecycle = tenancy.NewPersonalWorkspaceLifecycle(tenant)
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
	credVault, err := a.wireCredentialVault(parent, ws, stack)
	if err != nil {
		return err
	}
	a.wireSecrets(credVault)

	// ── Agent brain memory (episodic / semantic / procedural) ────────────────
	brainStores := a.wireBrainMemory(ws, stack)
	learningStore := a.wireLearning(ws)

	// ── Memory ───────────────────────────────────────────────────────────────
	fileStore, archive, err := a.wireMemory(ws, stack)
	if err != nil {
		return err
	}

	// ── Storage backend (action log + memory archive) ─────────────────────
	actionBackend, memBackend, err := a.wireStorageBackend(parent, ws, archive, stack)
	if err != nil {
		return err
	}
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		// THE WAIVER DOES NOT WAIVE THIS, and saying so here is the fix.
		//
		// `unsafe_multi_user_prerequisites` removes the CONFIG check that a
		// team deployment names a Postgres DSN. It cannot remove the RUNTIME
		// need for one: the tenancy catalog — workspaces, memberships, roles,
		// invitations, the deletion lifecycle — exists only in Postgres. So a
		// team install with the waiver and no DSN used to pass validation,
		// start, reach this line, and die at the ping below with a connection
		// error that reads like a network problem.
		//
		// There is no such thing as a SQLite-backed team deployment, and the
		// acknowledgement implied there was. Refused here, by name, with the
		// two real options.
		if strings.TrimSpace(cfg.Storage.PostgresDSN) == "" {
			return fmt.Errorf(
				"%s mode requires PostgreSQL for the workspace and membership catalog, and "+
					"storage.postgres_dsn is empty. The %q acknowledgement waives the configuration "+
					"check, not the dependency — there is no SQLite-backed multi-user deployment. "+
					"Set storage.postgres_dsn, or run in personal mode",
				cfg.DeploymentMode(), config.UnsafeDeploymentPrerequisitesAcknowledgement)
		}
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
		store.ConfigureProviderEncryptionSecret(cfg.Server.APIKey)
		tenantResolver = store
		workspaceLifecycle = store
		tenantIdentityLinker = store
		log.Info("multi-user tenancy catalog ready", zap.String("mode", cfg.DeploymentMode()))
	}

	// ── Plugin database migrations (Story E16) ───────────────────────────────
	a.applyCompiledPluginMigrations(ws)

	// ── LLM Router ───────────────────────────────────────────────────────────
	llmRouter := a.wireLLMRouter()
	ollamaCfg := cfg.LLM.Providers["ollama"]

	// ── Loaders (agent / plugin / skill) + python pre-flight ─────────────────
	loader, pluginLoader, pluginStores, skillLoader, skillStores := a.wireLoaders(ws, credVault)

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
			Transport:  sc.Transport,
			Command:    sc.Command,
			Args:       sc.Args,
			Env:        sc.Env,
			InheritEnv: sc.InheritEnv,
			InheritAll: sc.InheritAll,
			URL:        sc.URL,
			Headers:    sc.Headers,
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

	// ── Queue Backend ─────────────────────────────────────────────────────────
	// Execution workers use this boundary too, so it must exist before the
	// executor is selected.
	queueBackend, err := a.wireQueue(stack)
	if err != nil {
		return err
	}

	// ── Python Executor Backend ───────────────────────────────────────────────
	// "process" (default): one python3 subprocess per call, simple + compatible.
	// "pool": N pre-forked persistent workers, eliminates interpreter cold-start.
	pyExecutor := a.wirePythonExecutor(stack, credVault, queueBackend)
	// Named backends agents can opt into via `execution.backend` (local/docker/ssh).
	namedExecutors := a.wireNamedExecutors(credVault, queueBackend)

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
		skillStores:    skillStores,
		mcpClient:      mcpClient,
		mcpServers:     mcpServers,
		credVault:      credVault,
		stack:          stack,
		knowledgeSvc:   knowledgeSvc,
		vectorStore:    vectorStore,
		pluginProvider: pluginProvider,
		pluginStores:   pluginStores,
		pyExecutor:     pyExecutor,
		namedExecutors: namedExecutors,
		queueBackend:   queueBackend,
		brainStores:    brainStores,
		learningStore:  learningStore,
		ollamaAPIKey:   ollamaAPIKey,
		searchProvider: searchProvider,
		searchAPIKey:   searchAPIKey,
		toolTimeout:    toolTimeout,
		workspaceRoot:  ws.Root,
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
	// ── Durable, workspace-scoped schedules (MU-023) ────────────────────────
	// The claim is what makes an occurrence fire once across instances. Team
	// and Scale refuse to start without it: two gateways sharing an in-memory
	// cron table fire every schedule twice, and "run two for availability" and
	// "send the customer one email" are then incompatible.
	var scheduleStore *schedules.Store
	if store, serr := schedules.Open(ws.DB("schedules")); serr != nil {
		if config.IsMultiUserMode(cfg.DeploymentMode()) {
			return fmt.Errorf("durable schedules are required outside personal mode: %w", serr)
		}
		log.Warn("durable schedules unavailable; scheduling stays single-process", zap.Error(serr))
	} else {
		scheduleStore = store
		stack.pushClose("schedules", store)
		// The instance id must differ between processes, or two of them are
		// indistinguishable to the claim. The hostname plus this process's PID
		// is the strongest identity available without asking an operator to
		// configure one, and it is stable for the life of the process — which
		// is exactly the lifetime a lease is about.
		sched.SetScheduleStore(store, schedulerInstanceID())
		log.Info("durable schedules enabled", zap.String("instance", schedulerInstanceID()))
	}
	sched.SetStatePath(filepath.Join(cfg.Memory.Dir, "scheduler-state.json"))
	sched.SetEventSink(hub) // record scheduled-delivery outcomes in Activity
	// Readiness gate (ST-16): a Studio-deployed agent may only fire on a
	// schedule once its deployment carries passing certification. The store is
	// re-read on every tick, so re-certifying unblocks the schedule without a
	// restart; agents with no deployment record are unaffected.
	deploymentStore := studio.NewDeploymentStore(studio.DeploymentsDir(ws.Root))
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		deploymentStore.SetWorkspaceLayoutRoot(ws.Root)
	}
	sched.SetReadinessGate(deploymentReadinessGate(deploymentStore))
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

	// ── Durable run records (MU-020) ────────────────────────────────────────
	// Opened before the worker pool because both the pool and the API need the
	// same handle: a run submitted through the API is executed by the pool,
	// and its record is how the two agree on what happened.
	var runStore *runs.Store
	var recovered []runs.RecoveryOutcome
	if store, rerr := runs.Open(ws.DB("runs")); rerr != nil {
		log.Warn("durable runs unavailable", zap.Error(rerr))
	} else {
		runStore = store
		stack.pushClose("runs", runStore)
		// MU-021 criterion 6. The sweep no longer just counts: a run that
		// never acted is re-queued, a run that already called out to the
		// world is failed with the tool named so a human decides, and a run
		// that has burnt its attempts stops being handed to workers. See
		// internal/runs/recovery.go for why each direction is wrong alone.
		engine.SetSideEffectRecorder(runSideEffectRecorder{store: runStore})
		if outcomes, perr := runStore.RecoverPending(ctx); perr != nil {
			log.Error("durable run recovery sweep failed", zap.Error(perr))
		} else if len(outcomes) > 0 {
			counts := map[string]int{}
			for _, outcome := range outcomes {
				counts[outcome.Action]++
				if outcome.Action == runs.RecoveryFailedSideEffects {
					// Named individually, not just counted: this is the case
					// where somebody has to check whether the effect landed,
					// and a bare number tells them nothing about where to look.
					log.Warn("durable run needs review after worker loss",
						zap.String("run_id", outcome.Run.ID),
						zap.String("workspace_id", outcome.Run.WorkspaceID),
						zap.String("agent_id", outcome.Run.AgentID),
						zap.String("reason", outcome.Reason))
				}
			}
			log.Info("durable runs recovered from a previous process",
				zap.Int("requeued", counts[runs.RecoveryRequeued]),
				zap.Int("needs_review", counts[runs.RecoveryFailedSideEffects]),
				zap.Int("attempts_exhausted", counts[runs.RecoveryFailedExhausted]),
				zap.Int("skipped", counts[runs.RecoverySkipped]))
			recovered = outcomes
		}
	}

	// ── Durable approval records (MU-022) ───────────────────────────────────
	var approvalStore *approvals.Store
	if store, aerr := approvals.Open(ws.DB("approvals")); aerr != nil {
		if config.IsMultiUserMode(cfg.DeploymentMode()) {
			// In Team/Scale the in-memory fallback is not a lesser option, it
			// is the wrong one: without a workspace on the record, listing and
			// deciding fall back to a tenant-free map. Refuse rather than
			// serve every workspace's paused calls to every admin.
			return fmt.Errorf("durable approvals are required outside personal mode: %w", aerr)
		}
		log.Warn("durable approvals unavailable; approvals will not survive a restart", zap.Error(aerr))
	} else {
		approvalStore = store
		stack.pushClose("approvals", store)
		engine.Broker().SetStore(store, log)
		// A restart severs every channel a paused run was waiting on, so a
		// record still saying "pending" describes a question nobody is
		// listening for the answer to. Closing them is what stops the
		// approvals page from offering decisions that would release nothing.
		//
		// The runs those approvals were blocking are read FIRST, because
		// closing the approval is what strands them and afterwards there is
		// nothing left to say which runs those were. The run sweep above
		// deliberately skips `paused` runs as somebody's chosen state — a
		// premise that stops holding the instant the approval is invalidated,
		// leaving a run nobody and nothing can move. See
		// runs.Store.ResolveOrphanedPause.
		blocked, berr := store.PendingRunRefs(ctx)
		if berr != nil {
			log.Warn("runs blocked on stale approvals could not be listed", zap.Error(berr))
		}
		if n, ierr := store.InvalidateAllPending(ctx, approvals.ReasonWorkspaceRestarting); ierr != nil {
			log.Warn("stale approvals could not be closed", zap.Error(ierr))
		} else if n > 0 {
			log.Info("approvals left unanswered by a previous process were closed", zap.Int("closed", n))
		}
		if runStore != nil {
			recovered = append(recovered, a.resolveOrphanedPauses(ctx, runStore, blocked)...)
		}
	}

	// ── Message Router — bounded worker pool draining the shared inbox ──────
	a.startMessageRouter(ctx, chanReg, loader, engine, personalTenant, runStore)

	// Re-queued runs need a message, not just a record. Setting the record
	// back to `queued` without re-enqueueing would leave it queued forever —
	// a state that reads like "waiting its turn" and means "abandoned". Done
	// after the router starts so the messages have somewhere to land.
	for _, outcome := range recovered {
		if outcome.Action != runs.RecoveryRequeued {
			continue
		}
		if !chanReg.Enqueue(runs.InboundMessage(outcome.Run)) {
			// The inbox is bounded and may be full. The record stays queued
			// and the next restart's sweep will try again, spending one more
			// of the run's attempts rather than silently dropping it.
			log.Warn("recovered run could not be re-queued; it remains pending",
				zap.String("run_id", outcome.Run.ID),
				zap.String("workspace_id", outcome.Run.WorkspaceID))
		}
	}

	// ── Auth Engine ───────────────────────────────────────────────────────────
	authEngine, err := a.wireAuth(stack)
	if err != nil {
		return err
	}
	if tenantIdentityLinker != nil {
		authEngine.SetIdentityLinker(tenantIdentityLinker)
	}
	if providers, ok := tenantResolver.(tenancy.WorkspaceIdentityStore); ok {
		authEngine.SetWorkspaceOIDCAvailabilityResolver(func(ctx context.Context, workspaceID string) bool {
			login, err := providers.WorkspaceLoginConfig(ctx, workspaceID)
			if err != nil {
				return false
			}
			organizationStatus := strings.ToLower(strings.TrimSpace(login.OrganizationStatus))
			workspaceStatus := strings.ToLower(strings.TrimSpace(login.WorkspaceStatus))
			identityStatus := strings.ToLower(strings.TrimSpace(login.IdentityStatus))
			return (organizationStatus == "" || organizationStatus == tenancy.WorkspaceActive) &&
				(workspaceStatus == "" || workspaceStatus == tenancy.WorkspaceActive) &&
				identityStatus == "active"
		})
		authEngine.SetWorkspaceOIDCProviderResolver(func(ctx context.Context, workspaceID string) (auth.WorkspaceOIDCProvider, bool) {
			provider, err := providers.ResolveWorkspaceIdentityProvider(ctx, workspaceID)
			if err != nil {
				return auth.WorkspaceOIDCProvider{}, false
			}
			return auth.WorkspaceOIDCProvider{WorkspaceID: provider.WorkspaceID, ProviderType: provider.ProviderType, Issuer: provider.Issuer, ClientID: provider.ClientID, ClientSecret: provider.ClientSecret, Audience: provider.Audience, Scopes: provider.Scopes}, true
		})
	}
	if members, ok := tenantResolver.(tenancy.MemberManager); ok {
		authEngine.SetRefreshAuthorizer(members.CanRefreshUser)
		// Without this, a signed-in member receives a token with no workspace,
		// which authenticates fine and then acts with the personal workspace's
		// authority — valid credential, wrong tenant.
		authEngine.SetTokenIdentityResolver(func(ctx context.Context, subject string) (auth.TokenIdentity, bool) {
			membership, ok := members.PrimaryMembership(ctx, subject)
			if !ok {
				return auth.TokenIdentity{}, false
			}
			return auth.TokenIdentity{
				Subject:        subject,
				Role:           membership.Role,
				OrganizationID: membership.OrganizationID,
				WorkspaceID:    membership.WorkspaceID,
				MembershipID:   membership.ID,
			}, true
		})
		authEngine.SetWorkspaceTokenIdentityResolver(func(ctx context.Context, subject, workspaceID string) (auth.TokenIdentity, bool) {
			membership, err := tenantResolver.ResolveMembership(ctx, subject, workspaceID)
			if err != nil {
				return auth.TokenIdentity{}, false
			}
			return auth.TokenIdentity{Subject: subject, Role: membership.Role, OrganizationID: membership.OrganizationID, WorkspaceID: membership.WorkspaceID, MembershipID: membership.MembershipID}, true
		})
		authEngine.SetWorkspaceInvitationAccepter(func(ctx context.Context, token, subject, workspaceID string) (auth.TokenIdentity, bool) {
			membership, err := members.AcceptInvitation(ctx, tenancy.Mutation{ActorSubject: subject, RequestID: "oidc-invitation", At: time.Now().UTC()}, token, subject)
			if err != nil || membership.WorkspaceID != workspaceID {
				return auth.TokenIdentity{}, false
			}
			return auth.TokenIdentity{Subject: subject, Role: membership.Role, OrganizationID: membership.OrganizationID, WorkspaceID: membership.WorkspaceID, MembershipID: membership.ID}, true
		})
	}

	// ── RBAC Manager ──────────────────────────────────────────────────────────
	rbacManager := a.wireRBAC(ws, stack)

	// ── Engine-attached stores (checkpoint / telemetry / cost) ───────────────
	openedCostStore := a.wireEngineExtras(ctx, ws, engine, llmRouter, stack)

	// ── Per-workspace limits (MU-030 criterion 1) ─────────────────────────────
	// The store holds what each workspace has set on ITSELF; the effective
	// ceiling is that value tightened against the operator's YAML, never
	// replacing it. Opened for every deployment, including Personal — a
	// personal install with no entries composes to exactly the flat config it
	// has always had (invariant 7).
	var workspacePolicies *workspacepolicy.Store
	if store, err := workspacepolicy.NewStore(ws.DB("workspace-policies")); err != nil {
		log.Warn("per-workspace limits unavailable; the deployment-wide config remains the only ceiling", zap.Error(err))
	} else {
		workspacePolicies = store
		stack.pushClose("workspace-policies", store)
	}
	var workspaceSettings *workspacesettings.Store
	if store, err := workspacesettings.NewStore(ws.DB("workspace-settings")); err != nil {
		log.Warn("workspace settings unavailable", zap.Error(err))
	} else {
		workspaceSettings = store
		stack.pushClose("workspace-settings", store)
	}

	// The servers a WORKSPACE defined for itself, as opposed to the operator's
	// template in config.yaml. Opened in every mode, including Personal, for
	// the same reason the policy store is: a personal install with no rows
	// behaves exactly as it always has.
	//
	// Unavailable means "no workspace has servers of its own", never "fall
	// back to the shared ones" — a store that cannot be read costs a tenant a
	// tool, where the alternative hands them somebody else's.
	var workspaceMCPServers *mcpstore.Store
	if store, err := mcpstore.Open(ws.DB("workspace-mcp-servers")); err != nil {
		log.Warn("workspace-defined MCP servers unavailable; only the operator's configured servers will run",
			zap.Error(err))
	} else {
		workspaceMCPServers = store
		stack.pushClose("workspace-mcp-servers", store)
	}

	// ── Gateway Server ────────────────────────────────────────────────────────
	// Construction + every host-side capability (plugin GUI mounts, installer,
	// safety pipeline, registries, voice, workboard/ratelimit/apikey/dlq/history
	// stores, file watcher) is delegated to wireGateway.
	srv := a.wireGateway(gatewayDeps{
		ws:                  ws,
		engine:              engine,
		loader:              loader,
		llmRouter:           llmRouter,
		chanReg:             chanReg,
		sched:               sched,
		httpAdapter:         httpAdapter,
		waAdapter:           waAdapter,
		skillLoader:         skillLoader,
		skillStores:         skillStores,
		runStore:            runStore,
		actionBackend:       actionBackend,
		mcpClient:           mcpClient,
		hub:                 hub,
		authEngine:          authEngine,
		rbacManager:         rbacManager,
		credVault:           credVault,
		pluginLoader:        pluginLoader,
		pluginStores:        pluginStores,
		queueBackend:        queueBackend,
		openedCostStore:     openedCostStore,
		approvalStore:       approvalStore,
		scheduleStore:       scheduleStore,
		tenantResolver:      tenantResolver,
		workspaceLifecycle:  workspaceLifecycle,
		workspacePolicies:   workspacePolicies,
		workspaceSettings:   workspaceSettings,
		workspaceMCPServers: workspaceMCPServers,
		costGovernor:        a.costGovernor,
		tenantPool:          tenantPool,
	}, stack)
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		if tenantPool == nil {
			return fmt.Errorf("entitlements require the tenancy PostgreSQL pool")
		}
		entitlementStore, entitlementErr := entitlements.OpenPostgres(parent, tenantPool)
		if entitlementErr != nil {
			return fmt.Errorf("entitlement store: %w", entitlementErr)
		}
		srv.SetEntitlements(entitlements.New(entitlementStore), entitlementStore)
		if strings.EqualFold(cfg.Billing.Provider, "stripe") && strings.TrimSpace(cfg.Billing.StripeWebhookSecret) == "" {
			return fmt.Errorf("billing.stripe_webhook_secret is required when billing.provider=stripe")
		}
	}

	// ── Workspace deletion sweep ──────────────────────────────────────────────
	// The thing that makes a requested deletion actually happen. Without it the
	// product has a deletion API that records an intention and never acts on
	// it: the workspace sits at `deleting`, refusing writes, data intact,
	// forever — with the customer having been told it would be gone on a date,
	// and nothing but a database query able to reveal otherwise.
	//
	// Started here rather than in gateway.New, so a directly constructed or
	// embedded gateway — which is what every test builds — never acquires a
	// goroutine that deletes workspaces.
	srv.StartWorkspacePurgeSweep(ctx)

	// Compose the stored per-workspace limits onto the operator's YAML at
	// startup, not only when one is edited. Without this a deployment that
	// restarts loses every workspace's self-imposed ceiling until somebody
	// happens to save one — a limit that quietly stops applying is worse than
	// one that was never set, because nobody is watching for its absence.
	srv.ReloadWorkspaceQuotaPolicy(ctx)

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
