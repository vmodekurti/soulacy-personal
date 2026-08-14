package app

// wire_gateway.go — gateway server construction + configuration extracted from
// App.Run (Story ARCH-4). Builds the HTTP/GUI server and attaches every
// host-side capability (plugin GUI mounts, installer, safety pipeline, package
// registries, voice, cost/workboard/ratelimit/apikey/dlq/history stores, file
// watcher). Owned resources register their Close on the LIFO shutdown stack.
// Behavior is preserved verbatim from the original inline block.

import (
	"os"
	"time"

	"go.uber.org/zap"

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
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/internal/plugininstall"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/ratelimit"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/scheduler"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/skills"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/internal/voice"
	"github.com/soulacy/soulacy/internal/workboard"
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
	actionBackend   storage.ActionLogBackend
	mcpClient       *mcp.Client
	hub             *gateway.EventHub
	authEngine      *auth.Engine
	rbacManager     *rbac.Manager
	credVault       credentials.Vault
	pluginLoader    *plugins.Loader
	openedCostStore *costs.Store
}

// wireGateway builds the gateway server and attaches every host capability.
// Owned resources push their Close on stack. Returns the configured server,
// ready for Listen().
func (a *App) wireGateway(d gatewayDeps, stack *closerStack) *gateway.Server {
	cfg, cfgPath, log := a.cfg, a.cfgPath, a.log
	ws := d.ws

	// Created BEFORE the watcher so the watcher can wire its OnPyChange hook
	// to the server's tool-catalog cache.
	srv := gateway.New(cfg, cfgPath, d.engine, d.loader, d.llmRouter, d.chanReg, d.sched, d.httpAdapter, d.waAdapter, d.skillLoader, d.actionBackend, d.mcpClient, d.hub, log)
	srv.SetAuth(d.authEngine)
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
				capsEnforcer.SetPluginSet(lp.Caps)
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
	apiKeyPath := ws.DB("apikeys")
	if akStore, akErr := apikeys.NewSQLiteStore(apiKeyPath); akErr != nil {
		log.Warn("api key store unavailable", zap.Error(akErr))
	} else {
		stack.pushClose("apikey-store", akStore)
		srv.SetAPIKeyStore(akStore)
		d.authEngine.SetAPIKeyStore(akStore) // wire into auth middleware (sk_ prefix validation)
		log.Info("api key store ready", zap.String("path", apiKeyPath))
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
	historyPath := ws.DB("history")
	if histStore, histErr := session.NewSQLiteHistoryStore(historyPath, session.WithHistoryRetention(config.RetentionDuration(cfg.Runtime.Retention.ConversationHistory, 30*24*time.Hour))); histErr != nil {
		log.Warn("conversation history store unavailable", zap.Error(histErr))
	} else {
		stack.pushClose("history-store", histStore)
		srv.SetHistoryStore(histStore)
		d.engine.SetHistoryStore(histStore) // wire into engine turn recording
		log.Info("conversation history ready", zap.String("path", historyPath))
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
	return srv
}
