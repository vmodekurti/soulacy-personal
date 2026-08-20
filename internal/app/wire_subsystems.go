package app

// wire_subsystems.go — per-subsystem constructors extracted from App.Run
// (Story ARCH-4). Each helper builds ONE subsystem, registers any owned
// resource's Close on the LIFO shutdown stack, and returns the component (plus
// an error for the fatal-on-failure subsystems). Construction order and
// behavior are preserved verbatim from the original monolithic Run.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/executor/cloud"
	executorcommand "github.com/soulacy/soulacy/internal/executor/command"
	executordocker "github.com/soulacy/soulacy/internal/executor/docker"
	"github.com/soulacy/soulacy/internal/executor/pool"
	"github.com/soulacy/soulacy/internal/executor/process"
	executorssh "github.com/soulacy/soulacy/internal/executor/ssh"
	"github.com/soulacy/soulacy/internal/extstorage"
	"github.com/soulacy/soulacy/internal/gateway"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/pluginmigrate"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/quota"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/runs"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/internal/skills"
	"github.com/soulacy/soulacy/internal/storage"
	storagepg "github.com/soulacy/soulacy/internal/storage/postgres"
	storagesqlite "github.com/soulacy/soulacy/internal/storage/sqlite"
	"github.com/soulacy/soulacy/internal/telemetry"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/vector"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/queue"
	"github.com/soulacy/soulacy/sdk/registry"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

// wireBrainMemory builds the agent-brain CompositeStore (episodic/semantic/
// procedural). A missing/uncreatable dir disables long-term memory (warn, not
// fatal) and returns a nil store.
func (a *App) wireBrainMemory(ws config.Paths, stack *closerStack) *agentmemory.Stores {
	log := a.log
	brainMemDir := os.Getenv("SOULACY_MEMORY_DIR")
	if brainMemDir == "" {
		brainMemDir = ws.Memory
	}
	if err := os.MkdirAll(brainMemDir, 0755); err != nil {
		log.Warn("brain memory dir create failed — long-term memory disabled",
			zap.String("dir", brainMemDir), zap.Error(err))
		return nil
	}
	brainStores := agentmemory.NewStores(brainMemDir)
	stack.push("brain-memory", func() error { return brainStores.Close() }) // releases every workspace's E23 rulebook db
	log.Info("agent brain memory enabled", zap.String("dir", brainMemDir))
	return brainStores
}

func (a *App) wireLearning(ws config.Paths) *learning.Stores {
	stores, err := learning.NewStores(ws.DB("learning"))
	if err != nil {
		a.log.Warn("learning proposal store unavailable", zap.Error(err))
		return nil
	}
	a.log.Info("learning proposal store ready", zap.String("path", ws.DB("learning")))
	return stores
}

// wireMemory builds the file store and the SQLite archive. Both failures are
// fatal. The archive's Close is registered on the stack.
func (a *App) wireMemory(stack *closerStack) (*memory.FileStore, *memory.SQLiteArchive, error) {
	cfg := a.cfg
	fileStore, err := memory.NewFileStore(cfg.Memory.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("file store: %w", err)
	}
	archive, err := memory.NewSQLiteArchive(cfg.Memory.SQLitePath)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite archive: %w", err)
	}
	stack.pushClose("sqlite-archive", archive)
	return fileStore, archive, nil
}

// wireStorageBackend selects the action-log + memory archive backend
// (sqlite/postgres/external). Backend init failures are fatal. Owned resources
// register their Close on the stack.
func (a *App) wireStorageBackend(parent context.Context, ws config.Paths, archive *memory.SQLiteArchive, stack *closerStack) (storage.ActionLogBackend, storage.MemoryBackend, error) {
	cfg, log := a.cfg, a.log
	var (
		actionBackend storage.ActionLogBackend
		memBackend    storage.MemoryBackend
	)
	switch cfg.Storage.Backend {
	case "postgres":
		pgLogDir := cfg.Storage.PostgresLogDir
		if pgLogDir == "" {
			pgLogDir = ws.Logs
		}
		pgAL, pgMem, _, pgErr := storagepg.Open(cfg.Storage.PostgresDSN, pgLogDir, log)
		if pgErr != nil {
			return nil, nil, fmt.Errorf("postgres storage backend: %w", pgErr)
		}
		// pgAL.Close() flushes the async queue and closes the shared pgx pool.
		stack.pushClose("postgres-storage", pgAL)
		actionBackend = pgAL
		memBackend = pgMem
		log.Info("storage backend: postgres", zap.String("log_dir", pgLogDir))
	case "external":
		// Action log falls back to SQLite
		logsDir := ws.Logs
		actionsDB := ws.DB("actions")
		sqAL, sqErr := actionlog.New(logsDir, actionsDB, log, actionlog.WithRetention(config.RetentionDuration(cfg.Runtime.Retention.ActionEvents, 90*24*time.Hour)))
		if sqErr != nil {
			return nil, nil, fmt.Errorf("sqlite action log: %w", sqErr)
		}
		stack.pushClose("sqlite-action-log", sqAL)
		actionBackend = storagesqlite.NewActionLog(sqAL)

		// Storage Backend is external sidecar
		if cfg.Storage.Command == "" {
			return nil, nil, fmt.Errorf("external storage backend: missing command configuration")
		}
		scratchRoot := "/tmp/soulacy-shared/"
		if err := os.MkdirAll(scratchRoot, 0755); err != nil {
			return nil, nil, fmt.Errorf("external storage backend: create scratch root: %w", err)
		}

		cc := extstorage.ClientConfig{
			Name:        "storage-external",
			Command:     cfg.Storage.Command,
			Args:        cfg.Storage.Args,
			ScratchRoot: scratchRoot,
			Log:         log,
		}
		extMem, err := extstorage.NewStorageBackend(parent, cc)
		if err != nil {
			return nil, nil, fmt.Errorf("external storage backend: %w", err)
		}
		stack.pushClose("external-storage", extMem)
		memBackend = extMem
		log.Info("storage backend: external sidecar", zap.String("command", cfg.Storage.Command))
	default: // "sqlite" or empty
		logsDir := ws.Logs
		actionsDB := ws.DB("actions")
		sqAL, sqErr := actionlog.New(logsDir, actionsDB, log, actionlog.WithRetention(config.RetentionDuration(cfg.Runtime.Retention.ActionEvents, 90*24*time.Hour)))
		if sqErr != nil {
			return nil, nil, fmt.Errorf("sqlite action log: %w", sqErr)
		}
		stack.pushClose("sqlite-action-log", sqAL)
		actionBackend = storagesqlite.NewActionLog(sqAL)
		memBackend = storagesqlite.NewMemoryArchive(archive)
		log.Info("storage backend: sqlite", zap.String("dir", logsDir))
	}
	return actionBackend, memBackend, nil
}

// applyCompiledPluginMigrations applies schema steps registered by compiled-in
// plugins from init(). Best-effort: failures warn and skip, never abort.
func (a *App) applyCompiledPluginMigrations(ws config.Paths) {
	log := a.log
	if pending := sdkstorage.RegisteredMigrations(); len(pending) > 0 {
		pmPath := ws.DB("plugins")
		if pm, pmErr := pluginmigrate.Open(pmPath); pmErr != nil {
			log.Warn("plugin database unavailable; plugin migrations skipped", zap.Error(pmErr))
		} else {
			defer pm.Close()
			applied, merrs := pm.Apply(pending)
			for _, e := range merrs {
				log.Warn("plugin migration refused or failed", zap.Error(e))
			}
			if applied > 0 {
				log.Info("plugin migrations applied",
					zap.Int("count", applied), zap.String("path", pmPath))
			}
		}
	}
}

// applyManifestPluginMigrations applies schema declared in installed plugins'
// plugin.yaml. Best-effort: failures warn and skip a plugin's chain only.
func (a *App) applyManifestPluginMigrations(ws config.Paths, pluginLoader *plugins.Loader) {
	log := a.log
	if pending := plugins.ManifestMigrations(pluginLoader.All()); len(pending) > 0 {
		pmPath := ws.DB("plugins")
		if pm, pmErr := pluginmigrate.Open(pmPath); pmErr != nil {
			log.Warn("plugin database unavailable; manifest migrations skipped", zap.Error(pmErr))
		} else {
			applied, merrs := pm.Apply(pending)
			for _, e := range merrs {
				log.Warn("manifest migration refused or failed", zap.Error(e))
			}
			if applied > 0 {
				log.Info("manifest-declared plugin migrations applied",
					zap.Int("count", applied), zap.String("path", pmPath))
			}
			_ = pm.Close()
		}
	}
}

// wireLoaders builds the agent, plugin, and skill loaders, runs the Python
// pre-flight ($PATH resolution of runtime.python_bin), and applies
// manifest-declared plugin migrations. Returns the three loaders in
// construction order.
func (a *App) wireLoaders(ws config.Paths, credVault credentials.Vault) (*runtime.Loader, *plugins.Loader, *plugins.Stores, *skills.Loader, *skills.Stores) {
	cfg, log := a.cfg, a.log

	// ── Agent Loader ─────────────────────────────────────────────────────────
	loader := runtime.NewLoader(cfg.AgentDirs)
	loader.SetLogger(log)
	// Decided here, applied BEFORE LoadAll, because LoadAll is where a
	// SOUL.yaml claiming the reserved ID would otherwise be promoted to a
	// system-tools agent.
	//
	// The loader takes a boolean, not the mode: internal/runtime knows nothing
	// about deployment modes and is better for it, so the policy lives in
	// config.Config.PlatformAgentsEnabled and the decision is made once, here.
	platformAgents := cfg.PlatformAgentsEnabled()
	loader.SetPlatformAgentsEnabled(platformAgents)
	switch {
	case platformAgents && config.IsMultiUserMode(cfg.DeploymentMode()):
		log.Warn("the built-in System agent is ENABLED in a multi-user deployment by explicit "+
			"acknowledgement; it can run shell commands, write files and edit this deployment's "+
			"configuration, and those actions belong to no workspace",
			zap.String("acknowledgement", config.UnsafeTenantSystemAgentAcknowledgement))
	case !platformAgents:
		// Named as a withdrawal rather than logged as a fact, because a Team
		// install upgrading into this loses a feature its users were using,
		// and the first thing they will do is look in the log.
		log.Info("the built-in System agent is not available in this deployment mode: its tools "+
			"act on the host and the deployment config, which cannot be scoped to a workspace. "+
			"Install MCP servers and skills from the GUI or the `sy` CLI on the host instead",
			zap.String("mode", cfg.DeploymentMode()),
			zap.String("restore_with_acknowledgement", config.UnsafeTenantSystemAgentAcknowledgement))
	}
	if errs := loader.LoadAll(); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("agent load error", zap.Error(e))
		}
	}
	log.Info("agents loaded", zap.Int("count", len(loader.All())))
	// SEC-3: report which agents have the privileged "system" capability so an
	// operator can audit OS-level access at a glance. System tools (shell_exec,
	// run_script, write_file, …) require BOTH this server permit and a per-agent
	// `capabilities: [system]` declaration (system_tools: true is a legacy alias).
	{
		var systemAgents []string
		for _, def := range loader.All() {
			if def.HasCapability("system") {
				systemAgents = append(systemAgents, def.ID)
			}
		}
		if len(a.cfg.Runtime.AllowSystemAgents) == 0 {
			log.Info("system tools disabled server-wide (runtime.allow_system_agents is empty); "+
				"destructive OS-level built-ins will not be offered to any agent",
				zap.Int("agents_requesting_system", len(systemAgents)),
				zap.Strings("agents_requesting_system_ids", systemAgents))
		} else if len(systemAgents) == 0 {
			log.Info("system tools permitted but no agent declares the 'system' capability")
		} else {
			log.Warn("system tools ENABLED for agents (capabilities: [system])",
				zap.Strings("agents_requesting_system_ids", systemAgents),
				zap.Strings("server_allowlist", a.cfg.Runtime.AllowSystemAgents))
		}
	}

	// ── Python binary pre-flight ─────────────────────────────────────────────
	// Resolve runtime.python_bin via $PATH at startup and rewrite the
	// config field to the absolute path. Catches the launchd/Finder tiny-PATH
	// problem and config typos at boot rather than on the first cron fire.
	// Missing python is a warn, not fatal — built-in-only deployments are fine.
	if cfg.Runtime.PythonBin != "" {
		if resolved, perr := exec.LookPath(cfg.Runtime.PythonBin); perr == nil {
			if resolved != cfg.Runtime.PythonBin {
				log.Info("python_bin resolved to absolute path",
					zap.String("configured", cfg.Runtime.PythonBin),
					zap.String("resolved", resolved))
			}
			cfg.Runtime.PythonBin = resolved
		} else {
			log.Warn("python_bin not found on $PATH — python tools will fail until this is fixed",
				zap.String("python_bin", cfg.Runtime.PythonBin),
				zap.String("PATH", os.Getenv("PATH")),
				zap.String("hint", "set runtime.python_bin to an absolute path (e.g. /opt/homebrew/bin/python3) in config.yaml"),
			)
		}
	}

	// ── Plugin Loader ─────────────────────────────────────────────────────────
	// Scans plugin_dirs for plugin.yaml manifests; loads Python tool libraries
	// and (manifest_schema 2, E7) sidecar channels, providers, skills, GUI mounts.
	// Platform scan list, layered exactly like skills: the operator's
	// configured directories stay read-only templates every workspace sees,
	// and each workspace's own directory is scanned LAST so it can shadow a
	// platform plugin by name without modifying the shared copy.
	pluginStores := plugins.NewStores(cfg.PluginDirs, ws.Plugins, log)
	// BEFORE SetSettings and before the first For, so no workspace's loader is
	// ever built holding the operator's credential.
	//
	// A plugin's DECLARED credentials were already per-workspace; its
	// plugins_config settings were one shared map. Settings are meant to be
	// configuration, but nothing stopped an author putting an API key there,
	// and when they did every tenant ran on the operator's key. See
	// internal/plugins/tenantsettings.go.
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		pluginStores.RequireTenantSettings(vaultPluginSettings(credVault))
		log.Info("credential-looking plugins_config values are per-workspace; a value a workspace " +
			"has not supplied is withheld rather than inherited from the operator")
	}
	// plugins_config is attached through the registry rather than only by
	// plugins.Wire, so a loader rebuilt after an invalidation keeps its
	// settings. See Stores.SetSettings.
	pluginStores.SetSettings(cfg.PluginsConfig)
	pluginLoader := pluginStores.For(wsroot.PersonalWorkspaceID)
	if pluginLoader.Count() > 0 {
		log.Info("plugins loaded", zap.Int("count", pluginLoader.Count()))
	}

	// Manifest-declared plugin migrations (Story 17): installed plugins
	// declare schema in plugin.yaml; the loader already validated every
	// step (namespace + statement rules), so apply through the same E16
	// runner — dedicated plugins.db, transactional, checksummed,
	// applied-once. A failing step skips that plugin's chain only.
	a.applyManifestPluginMigrations(ws, pluginLoader)

	// ── Skill Loader ─────────────────────────────────────────────────────────
	// Scans ~/.soulacy/skills/, ~/.agents/skills/, ./.agents/skills/, etc.
	// Extra skill dirs come from config.skill_dirs and manifest-v2 plugins (E7).
	workDir, _ := os.Getwd()
	skillDirs := append([]string{}, cfg.SkillDirs...)
	for _, lp := range pluginLoader.All() {
		skillDirs = append(skillDirs, lp.SkillDirs()...)
	}
	// Platform scan list: the operator's directories and the cross-client
	// conventions. Every workspace sees these as read-only templates; each also
	// gets its own directory, scanned last so it can shadow a platform skill by
	// name without modifying the platform copy (MU-017 criterion 1).
	platformSkillDirs := skills.PlatformDirs(workDir, skillDirs)
	skillStores := skills.NewStores(platformSkillDirs, ws.Skills, log)
	skillLoader := skills.NewWithDirs(skillStores.ScanDirs(wsroot.PersonalWorkspaceID), log)
	if errs := skillLoader.Scan(); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("skill load warning", zap.Error(e))
		}
	}
	if skillLoader.Count() > 0 {
		log.Info("agent skills loaded", zap.Int("count", skillLoader.Count()))
	}

	return loader, pluginLoader, pluginStores, skillLoader, skillStores
}

// wireLLMRouter builds the LLM router, registers the unconditional Ollama
// provider, and resolves every other configured provider (dedicated factory or
// generic OpenAI-compatible adapter).
func (a *App) wireLLMRouter() *llm.Router {
	cfg, log := a.cfg, a.log
	llmRouter := llm.NewRouter(cfg.LLM.DefaultProvider)

	// Ollama registers unconditionally (zero-config local default).
	ollamaCfg := cfg.LLM.Providers["ollama"]
	if p, ok, perr := registry.NewProvider("ollama", providerCfgMap(ollamaCfg)); ok && perr == nil {
		llmRouter.Register(p)
	} else {
		log.Warn("ollama provider init failed", zap.Error(perr))
	}

	// Every other configured provider resolves by its config id. Known names
	// hit their dedicated factory; anything else with base_url + api_key gets
	// the generic OpenAI-compatible adapter (OpenRouter / Together / Groq /
	// vLLM under a custom id, no code changes).
	for id, pcfg := range cfg.LLM.Providers {
		if id == "ollama" || pcfg.APIKey == "" {
			continue
		}
		m := providerCfgMap(pcfg)
		m["id"] = id
		p, ok, perr := registry.NewProvider(id, m)
		if !ok {
			if pcfg.BaseURL == "" {
				log.Warn("llm provider skipped: unknown id and no base_url for the generic adapter",
					zap.String("id", id))
				continue
			}
			p, _, perr = registry.NewProvider("openai", m)
		}
		if perr != nil {
			log.Warn("llm provider init failed", zap.String("id", id), zap.Error(perr))
			continue
		}
		llmRouter.Register(p)
	}
	log.Info("llm providers registered",
		zap.Strings("ids", llmRouter.ProviderIDs()),
		zap.String("default", llmRouter.DefaultProvider()),
	)
	return llmRouter
}

// wireKnowledge builds the optional RAG service (SQLite + sqlite-vec +
// provider-backed embeddings). Disabled silently when DBPath is empty; an
// unavailable store warns and returns nil.
func (a *App) wireKnowledge(ollamaBaseURL string, llmRouter *llm.Router, stack *closerStack) *knowledge.Service {
	cfg, log := a.cfg, a.log
	if cfg.Knowledge.DBPath == "" {
		return nil
	}
	kbStore, kerr := knowledge.Open(cfg.Knowledge.DBPath)
	if kerr != nil {
		log.Warn("knowledge store unavailable (RAG disabled)", zap.Error(kerr))
		return nil
	}
	stack.pushClose("knowledge-store", kbStore)
	embedders := llm.NewEmbedderRegistry()
	if cfg.LLM.Providers == nil {
		cfg.LLM.Providers = map[string]config.ProviderConfig{}
	}
	if _, ok := cfg.LLM.Providers["ollama"]; !ok {
		cfg.LLM.Providers["ollama"] = config.ProviderConfig{BaseURL: ollamaBaseURL}
	}
	for id, pc := range cfg.LLM.Providers {
		if emb := embedderForProvider(id, pc, ollamaBaseURL); emb != nil {
			emb = llm.NewGovernedEmbedder(emb, llmRouter)
			embedders.Register(emb)
		}
	}
	knowledgeSvc := knowledge.NewService(kbStore, embedders)
	log.Info("knowledge store ready",
		zap.String("path", cfg.Knowledge.DBPath),
		zap.String("default_embedding_model", cfg.Knowledge.EmbeddingModel),
		zap.Strings("embedders", embedders.IDs()),
	)
	return knowledgeSvc
}

func embedderForProvider(id string, pc config.ProviderConfig, ollamaBaseURL string) llm.Embedder {
	id = strings.TrimSpace(id)
	baseURL := strings.TrimSpace(pc.BaseURL)
	switch id {
	case "ollama":
		if baseURL == "" {
			baseURL = ollamaBaseURL
		}
		return llm.NewOllamaEmbedder(baseURL)
	case "google", "gemini":
		if pc.APIKey == "" {
			return nil
		}
		return llm.NewGoogleCompatibleEmbedder(id, baseURL, pc.APIKey)
	case "openai":
		if pc.APIKey == "" {
			return nil
		}
		return llm.NewOpenAIEmbedder(baseURL, pc.APIKey)
	case "openroute", "openrouter", "ollama_cloud", "nvidia", "together", "groq", "mistral", "deepseek":
		if pc.APIKey == "" {
			return nil
		}
		return llm.NewOpenAICompatibleEmbedder(id, baseURL, pc.APIKey)
	default:
		if pc.APIKey != "" && strings.Contains(baseURL, "/v1") {
			return llm.NewOpenAICompatibleEmbedder(id, baseURL, pc.APIKey)
		}
		return nil
	}
}

// wireVector builds the optional vector-memory tier. Returns the sqlite-vec
// *memory.VectorStore (consumed directly by the engine) and the new
// vector.Backend interface (held for future memory tools). Disabled when no
// backend key is set.
func (a *App) wireVector(archive *memory.SQLiteArchive, llmRouter *llm.Router) (*memory.VectorStore, vector.Backend) {
	cfg, log := a.cfg, a.log
	ollamaCfg := cfg.LLM.Providers["ollama"]

	vectorBackendKey := cfg.Vector.Backend
	if vectorBackendKey == "" {
		vectorBackendKey = cfg.Memory.VectorDB // backwards-compat
	}
	if vectorBackendKey == "" {
		// sqlite-vec is embedded in the monolithic binary and is the secure,
		// zero-service default. Operators can still select qdrant/external.
		vectorBackendKey = "sqlite-vec"
	}

	var vectorStore *memory.VectorStore // kept for engine (sqlite-vec path only)
	var vecBackend vector.Backend       // new interface (used by future memory tools)

	embedModel := cfg.Knowledge.EmbeddingModel
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}
	rawEmbedder := embedderForProvider(cfg.Knowledge.EmbeddingProvider, cfg.LLM.Providers[cfg.Knowledge.EmbeddingProvider], ollamaCfg.BaseURL)
	if rawEmbedder == nil {
		rawEmbedder = llm.NewOllamaEmbedder(ollamaCfg.BaseURL)
	}
	rawEmbedder = llm.NewGovernedEmbedder(rawEmbedder, llmRouter)
	memEmbedder := &llmEmbedAdapter{inner: rawEmbedder, model: embedModel}

	dims := cfg.Vector.Dims
	if dims <= 0 {
		dims = cfg.Memory.VectorDims
	}
	if dims <= 0 {
		dims = 768
	}

	// Resolved through the SDK factory registry (Story E10). The sqlite-vec
	// path keeps building *memory.VectorStore host-side — the engine consumes
	// the store directly — and hands it to the factory under the "store" key.
	switch vectorBackendKey {
	case "qdrant":
		// DOC-2: the Qdrant vector backend has no automated tests and no
		// known production users. Warn loudly so operators know they are
		// on an unvetted code path.
		log.Warn("qdrant vector backend is EXPERIMENTAL and untested — no automated tests, no known production users; prefer sqlite-vec or an external sidecar")
		qURL := cfg.Vector.URL
		if qURL == "" {
			qURL = "http://localhost:6333"
		}
		qCol := cfg.Vector.Collection
		if qCol == "" {
			qCol = "soulacy_memory"
		}
		qb, _, qerr := registry.NewVector("qdrant", map[string]any{
			"base_url":   qURL,
			"collection": qCol,
			"api_key":    cfg.Vector.APIKey,
			"dims":       dims,
			"embedder":   memory.Embedder(memEmbedder),
		})
		if qerr != nil {
			log.Warn("qdrant vector backend unavailable", zap.Error(qerr))
		} else {
			vecBackend = qb
			log.Info("vector memory enabled (qdrant)",
				zap.String("url", qURL),
				zap.String("collection", qCol),
				zap.Int("dims", dims),
			)
		}
	case "external": // storage sidecar over stdio (E24)
		scratchRoot := "/tmp/soulacy-shared/"
		if err := os.MkdirAll(scratchRoot, 0755); err != nil {
			log.Warn("external vector scratch root create failed", zap.Error(err))
		}
		eb, _, eerr := registry.NewVector("external", map[string]any{
			"id":           "vector-external",
			"command":      cfg.Vector.Command,
			"args":         cfg.Vector.Args,
			"scratch_root": scratchRoot,
			"logger":       log,
		})
		if eerr != nil {
			log.Warn("external vector sidecar unavailable", zap.Error(eerr))
		} else {
			vecBackend = eb
			log.Info("vector memory enabled (external sidecar)",
				zap.String("command", cfg.Vector.Command))
		}
	default: // "sqlite-vec" or any legacy non-empty value
		vs, verr := memory.NewVectorStore(archive.DB(), memEmbedder, dims)
		if verr != nil {
			log.Warn("vector memory disabled (sqlite-vec not loaded or schema error)", zap.Error(verr))
		} else {
			vectorStore = vs
			svb, _, sverr := registry.NewVector("sqlite-vec", map[string]any{"store": vs})
			if sverr != nil {
				log.Warn("sqlite-vec backend init failed", zap.Error(sverr))
			} else {
				vecBackend = svb
				log.Info("vector memory enabled (sqlite-vec)",
					zap.Int("dims", dims),
					zap.String("embedding_provider", cfg.Knowledge.EmbeddingProvider),
				)
			}
		}
	}
	return vectorStore, vecBackend
}

// wirePythonExecutor selects the Python executor backend (process-per-call or
// pre-forked pool). A failed pool degrades to the process executor.
func (a *App) wirePythonExecutor(stack *closerStack, vault credentials.Vault) executor.Backend {
	cfg, log := a.cfg, a.log
	switch cfg.Executor.Backend {
	case "pool":
		workers := cfg.Executor.Workers
		if workers <= 0 {
			workers = 4
		}
		pb, perr := pool.New(cfg.Runtime.PythonBin, workers)
		if perr != nil {
			log.Warn("python worker pool failed to start, falling back to process executor",
				zap.Error(perr), zap.Int("workers", workers))
			return process.New(cfg.Runtime.PythonBin)
		}
		stack.push("python-worker-pool", func() error { pb.Close(); return nil })
		log.Info("python executor: pre-forked pool",
			zap.Int("workers", workers),
			zap.String("python_bin", cfg.Runtime.PythonBin),
		)
		return pb
	case "docker":
		return a.buildDockerExecutor()
	case "ssh":
		return a.buildSSHExecutor(vault)
	default: // "process" or empty
		log.Info("python executor: process-per-call",
			zap.String("python_bin", cfg.Runtime.PythonBin))
		return process.New(cfg.Runtime.PythonBin)
	}
}

// buildDockerExecutor constructs the docker backend from config, honoring the
// explicit volume allowlist (executor.docker_volumes).
func (a *App) buildDockerExecutor() executor.Backend {
	cfg, log := a.cfg, a.log
	image := cfg.Executor.DockerImage
	if image == "" {
		image = "python:3.12-slim"
	}
	network := cfg.Executor.DockerNetwork
	if network == "" {
		network = "none"
	}
	log.Info("python executor: docker",
		zap.String("image", image),
		zap.String("network", network),
		zap.Int("volumes", len(cfg.Executor.DockerVolumes)),
		zap.String("python_bin", cfg.Runtime.PythonBin))
	return executordocker.NewWithVolumes(image, cfg.Runtime.PythonBin, network, cfg.Executor.DockerVolumes)
}

// buildSSHExecutor constructs the ssh backend. When executor.ssh_identity_credential
// is set it resolves the private key from the encrypted vault and materializes
// it into a 0600 temp file, keeping the key out of config and the environment.
func (a *App) buildSSHExecutor(vault credentials.Vault) executor.Backend {
	cfg, log := a.cfg, a.log
	pythonBin := cfg.Executor.SSHPythonBin
	if pythonBin == "" {
		pythonBin = "python3"
	}
	identity := cfg.Executor.SSHIdentity
	if cred := strings.TrimSpace(cfg.Executor.SSHIdentityCredential); cred != "" && vault != nil {
		mgr := secrets.New(vault)
		if key, ok := mgr.Get(context.Background(), cred); ok && strings.TrimSpace(key) != "" {
			if path, err := writeTempIdentity(key); err == nil {
				identity = path
				log.Info("ssh executor: identity resolved from vault", zap.String("credential", cred))
			} else {
				log.Warn("ssh executor: could not materialize vault identity; falling back to ssh_identity",
					zap.Error(err))
			}
		} else {
			log.Warn("ssh executor: identity credential not found in vault; falling back to ssh_identity",
				zap.String("credential", cred))
		}
	}
	log.Info("python executor: ssh",
		zap.String("host", cfg.Executor.SSHHost),
		zap.String("user", cfg.Executor.SSHUser),
		zap.String("python_bin", pythonBin))
	return executorssh.New(cfg.Executor.SSHHost, cfg.Executor.SSHUser, pythonBin, identity)
}

// writeTempIdentity writes an SSH private key to a 0600-mode temp file and
// returns its path. Caller-owned; cleaned up on process exit.
func writeTempIdentity(key string) (string, error) {
	f, err := os.CreateTemp("", "soulacy-ssh-*.key")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	if !strings.HasSuffix(key, "\n") {
		key += "\n"
	}
	if _, err := f.WriteString(key); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// wireNamedExecutors builds the set of execution backends agents can select via
// `execution.backend` in SOUL.yaml. "local" is always available; "docker" and
// "ssh" are registered when their config is present (or when they are the server
// default), so an agent can opt into container/remote execution even when the
// global default is local, and vice versa.
func (a *App) wireNamedExecutors(vault credentials.Vault) map[string]executor.Backend {
	cfg := a.cfg
	out := map[string]executor.Backend{
		"local": process.New(cfg.Runtime.PythonBin),
	}
	if cfg.Executor.Backend == "docker" || strings.TrimSpace(cfg.Executor.DockerImage) != "" {
		out["docker"] = a.buildDockerExecutor()
	}
	if cfg.Executor.Backend == "ssh" || strings.TrimSpace(cfg.Executor.SSHHost) != "" {
		out["ssh"] = a.buildSSHExecutor(vault)
	}
	if preset := strings.ToLower(strings.TrimSpace(cfg.Executor.CloudPreset)); preset != "" {
		if runner, ok := cloud.Preset(preset, cfg.Executor.CloudTarget, cfg.Executor.CloudCLI); ok {
			pythonBin := cfg.Executor.SSHPythonBin
			if pythonBin == "" {
				pythonBin = "python3"
			}
			out[preset] = executorcommand.New(preset, runner, pythonBin)
			a.log.Info("cloud execution preset registered",
				zap.String("preset", preset), zap.String("target", cfg.Executor.CloudTarget))
		} else {
			a.log.Warn("unknown cloud execution preset; ignoring", zap.String("preset", preset))
		}
	}
	return out
}

// wireQueue resolves the queue backend through the SDK factory registry.
// Unknown backend or init error is fatal. The backend's Close registers on the
// stack.
func (a *App) wireQueue(stack *closerStack) (queue.Backend, error) {
	cfg, log := a.cfg, a.log
	queueName := cfg.Queue.Backend
	if queueName == "" {
		queueName = "memory"
	}
	if queueName == "nats" {
		// DOC-2: the NATS queue backend has no automated tests and no known
		// production users.
		//
		// WHY TWO MESSAGES. The old single warning told every NATS operator to
		// "use the default in-memory queue instead", which is advice scale mode
		// forbids — its own validation REQUIRES nats or external. An operator
		// following the warning would be told by the next boot to undo it. The
		// remedy depends entirely on why they are here, so the message does
		// too: a personal or team install genuinely can go back to the default;
		// a scale install cannot, and needs to know the requirement it
		// satisfied is itself unvetted rather than being sent in a circle.
		if cfg.DeploymentMode() == config.DeploymentModeScale {
			log.Warn("nats queue backend is EXPERIMENTAL and untested — no automated tests, no known " +
				"production users. Scale mode REQUIRES a distributed queue, so this is not a setting to " +
				"revert: treat the queue as an unvetted dependency, exercise failover before relying on " +
				"it, or supply your own with queue.backend \"external\"")
		} else {
			log.Warn("nats queue backend is EXPERIMENTAL and untested — no automated tests, no known " +
				"production users. Nothing in this deployment mode requires it; the default in-memory " +
				"queue is the supported path")
		}
	}
	queueBackend, qok, qerr := registry.NewQueue(queueName, map[string]any{
		"url":            cfg.Queue.NATSUrl,
		"stream":         cfg.Queue.NATSStream,
		"subject_prefix": cfg.Queue.NATSSubjectPrefix,
		"ack_wait":       cfg.Queue.NATSAckWait,
		"max_deliver":    cfg.Queue.NATSMaxDeliver,
		// external storage sidecar keys (backend: "external", E24)
		"id":           "queue-external",
		"command":      cfg.Queue.Command,
		"args":         cfg.Queue.Args,
		"scratch_root": "/tmp/soulacy-shared/",
		"logger":       log,
	})
	if !qok {
		return nil, fmt.Errorf("unknown queue backend %q (registered: %v)", queueName, registry.Queues())
	}
	if qerr != nil {
		return nil, fmt.Errorf("%s queue backend: %w", queueName, qerr)
	}
	stack.pushClose("queue-backend", queueBackend)
	log.Info("queue backend ready", zap.String("backend", queueName))
	return queueBackend, nil
}

// wireCredentialVault builds the credential vault. Created before the channel
// registry so plugin sidecar channels (E6/E7) can resolve their delegated
// credentials at spawn. A KMS or store failure disables the vault (warn, not
// fatal) and returns nil. The vault's Close registers on stack.
func (a *App) wireCredentialVault(ws config.Paths, stack *closerStack) credentials.Vault {
	log := a.log
	vaultPath := ws.CredentialsDB()
	// Persist the master secret next to the vault so credentials survive
	// restarts even without a hardware machine id (e.g. in containers).
	localKMS, kmsErr := credentials.NewLocalKMSWithStore(filepath.Dir(vaultPath))
	if kmsErr != nil {
		log.Warn("credential vault KMS init failed, vault disabled", zap.Error(kmsErr))
		return nil
	}
	cv, cvErr := credentials.NewSQLiteVault(vaultPath, localKMS)
	if cvErr != nil {
		log.Warn("credential vault unavailable", zap.String("path", vaultPath), zap.Error(cvErr))
		return nil
	}
	stack.pushClose("credential-vault", cv)
	log.Info("credential vault ready", zap.String("path", vaultPath))
	return cv
}

// wireSecrets (SEC-8) migrates any plaintext secrets in config.yaml into the
// encrypted vault on first run, then overlays vault-stored secrets onto the
// in-memory config (vault wins). Runs BEFORE the LLM router and channel
// adapters are built so they read vault values. No-op when the vault is nil.
func (a *App) wireSecrets(vault credentials.Vault) {
	if vault == nil {
		return
	}
	mgr := secrets.New(vault)
	// Retained so the gateway can re-overlay after a config reload. Migrate
	// blanks vault-backed values in memory as well as on disk, expecting
	// Overlay to restore them — and every reload calls config.Load, which
	// re-reads the blanked file. Running Overlay only here meant the first
	// config write of a process's life emptied every provider key and channel
	// token in the in-memory config.
	a.secretsManager = mgr
	ctx := context.Background()
	if a.cfgPath != "" {
		if n, err := mgr.Migrate(ctx, a.cfg, a.cfgPath); err != nil {
			a.log.Warn("secret migration failed", zap.Error(err))
		} else if n > 0 {
			a.log.Info("migrated plaintext secrets into encrypted vault",
				zap.Int("count", n), zap.String("config", a.cfgPath))
		}
	}
	if n := mgr.Overlay(ctx, a.cfg); n > 0 {
		a.log.Info("applied secrets from vault", zap.Int("count", n))
	}
}

// wirePluginContributions applies plugin manifest-v2 contributions (E7):
// sidecar channels become supervised external adapters (started by StartAll),
// OpenAI-compatible providers join the LLM router. Best-effort: a broken
// contribution logs a warning, never aborts.
func (a *App) wirePluginContributions(ctx context.Context, ws config.Paths, pluginLoader *plugins.Loader, chanReg *channels.Registry, llmRouter *llm.Router, credVault credentials.Vault) {
	cfg, log := a.cfg, a.log
	if pluginLoader.Count() == 0 {
		return
	}
	wireSandboxSelf := ""
	wireSandboxLimits := sandbox.Limits{}
	if sbx := cfg.Runtime.Sandbox; sbx.Enabled {
		if selfPath, e := os.Executable(); e == nil {
			wireSandboxSelf = selfPath
			wireSandboxLimits = sandbox.Limits{
				Enabled:    true,
				CPUSeconds: sbx.CPUSeconds,
				MemoryMB:   sbx.MemoryMB,
				OpenFiles:  sbx.OpenFiles,
				FileSizeMB: sbx.FileSizeMB,
			}
		}
	}
	for _, werr := range plugins.Wire(ctx, pluginLoader, plugins.WireDeps{
		Channels:      chanReg,
		LLM:           llmRouter,
		Vault:         credVault,
		Log:           log,
		SandboxSelf:   wireSandboxSelf,
		SandboxLimits: wireSandboxLimits,
		PluginsConfig: cfg.PluginsConfig,                 // Story E17
		ScratchRoot:   filepath.Join(ws.Data, "scratch"), // Story E24
	}) {
		log.Warn("plugin contribution skipped", zap.Error(werr))
	}
}

// wireAuth builds the auth engine. Default "apikey" mode checks the static API
// key on every request; auth.mode=jwt enables short-lived token issuance +
// optional OIDC. Init failure is fatal. The engine's Close registers on stack.
func (a *App) wireAuth(stack *closerStack) (*auth.Engine, error) {
	cfg, log := a.cfg, a.log
	accessTTL, _ := time.ParseDuration(cfg.Auth.JWTAccessTTL)
	refreshTTL, _ := time.ParseDuration(cfg.Auth.JWTRefreshTTL)
	authEngine, authErr := auth.New(auth.Config{
		Mode:             cfg.Auth.Mode,
		JWTSecret:        cfg.Auth.JWTSecret,
		JWTAccessTTL:     accessTTL,
		JWTRefreshTTL:    refreshTTL,
		OIDCIssuer:       cfg.Auth.OIDCIssuer,
		OIDCAudience:     cfg.Auth.OIDCAudience,
		OIDCClientID:     cfg.Auth.OIDCClientID,
		OIDCClientSecret: cfg.Auth.OIDCClientSecret,
		OIDCRedirectURL:  cfg.Auth.OIDCRedirectURL,
		OIDCScopes:       cfg.Auth.OIDCScopes,
	}, cfg.Server.APIKey, log)
	if authErr != nil {
		return nil, fmt.Errorf("auth engine: %w", authErr)
	}
	stack.push("auth-engine", func() error { authEngine.Close(); return nil })
	log.Info("auth engine ready", zap.String("mode", authEngine.Mode()))
	return authEngine, nil
}

// wireRBAC builds the RBAC manager. Per-agent grants persist in SQLite; the
// static default policy lives in the rbac package. NoopStore fallback =
// static-policy-only degradation. The store's Close registers on stack.
func (a *App) wireRBAC(ws config.Paths, stack *closerStack) *rbac.Manager {
	log := a.log
	rbacDBPath := ws.DB("rbac")
	var rbacStore rbac.Store
	if rs, rerr := rbac.NewSQLiteStore(rbacDBPath); rerr != nil {
		if config.IsMultiUserMode(a.cfg.DeploymentMode()) {
			log.Error("RBAC store unavailable; multi-user authorization will fail closed",
				zap.String("path", rbacDBPath), zap.Error(rerr))
			rbacStore = rbac.ErrorStore{Err: rerr}
		} else {
			log.Warn("RBAC SQLite store unavailable, falling back to static policy only",
				zap.String("path", rbacDBPath), zap.Error(rerr))
			rbacStore = rbac.NoopStore{}
		}
	} else {
		stack.pushClose("rbac-store", rs)
		rbacStore = rs
		log.Info("RBAC store ready", zap.String("path", rbacDBPath))
	}
	return rbac.NewManager(rbacStore, log)
}

// wireEngineExtras attaches the workflow-checkpoint store, telemetry tracer,
// and cost store to the engine. Each owned resource registers its Close on the
// stack. Returns the opened cost store (or nil) for the gateway's /costs routes.
func (a *App) wireEngineExtras(ctx context.Context, ws config.Paths, engine *runtime.Engine, llmRouter *llm.Router, stack *closerStack) *costs.Store {
	cfg, log := a.cfg, a.log

	// ── Workflow Checkpoint Store (E5) ────────────────────────────────────────
	checkpointPath := ws.DB("checkpoints")
	if cs, cserr := runtime.NewCheckpointStore(checkpointPath); cserr != nil {
		log.Warn("workflow checkpoint store unavailable", zap.Error(cserr))
	} else {
		stack.pushClose("checkpoint-store", cs)
		engine.SetCheckpointStore(cs)
		log.Info("workflow checkpoints ready", zap.String("path", checkpointPath))
	}

	// package_install's exemption from allow_system_agents is a single-user
	// convenience: it gives an operator a safe install path without enabling
	// shell_exec. In multi-user it is withdrawn, because the installer runs
	// outside the sandbox and writes the deployment-wide config, and the
	// System agent is only present there under an acknowledgement that
	// restores a chat agent rather than the right to rewrite the deployment.
	//
	// The tool is not removed; it goes back behind runtime.allow_system_agents,
	// so an operator who wants it says so a second time.
	if config.IsMultiUserMode(cfg.DeploymentMode()) {
		engine.SetManagedInstallExempt(false)
	}

	// ── Telemetry (OTEL) ─────────────────────────────────────────────────────
	telCfg := telemetry.Config{
		Enabled:        cfg.Telemetry.Enabled,
		Exporter:       cfg.Telemetry.Exporter,
		OTLPEndpoint:   cfg.Telemetry.OTLPEndpoint,
		ServiceName:    cfg.Telemetry.ServiceName,
		ServiceVersion: config.Version,
	}
	if telCfg.ServiceName == "" {
		telCfg.ServiceName = "soulacy"
	}
	telProvider, telErr := telemetry.New(ctx, telCfg)
	if telErr != nil {
		log.Warn("telemetry init failed, tracing disabled", zap.Error(telErr))
	} else {
		stack.push("telemetry", func() error { return telProvider.Shutdown(context.Background()) })
		engine.SetTracer(&engineTracerAdapter{t: telProvider.Tracer})
		log.Info("telemetry ready", zap.String("exporter", telCfg.Exporter))
	}

	// ── Cost Store ────────────────────────────────────────────────────────────
	costsPath := ws.DB("costs")
	var openedCostStore *costs.Store
	if costsStore, cserr := costs.NewStore(costsPath); cserr != nil {
		log.Warn("cost store unavailable", zap.Error(cserr))
	} else {
		stack.pushClose("cost-store", costsStore)
		openedCostStore = costsStore
		prices := costPriceTableFromConfig(cfg.Costs.Pricing)
		// One builder, shared with the reload path — see costs.GovernanceFrom.
		governor := costs.NewGovernor(costsStore, prices, costs.GovernanceFrom(cfg))
		llmRouter.SetController(governor)
		// Retained so the gateway can reinstall a recomposed policy when a
		// workspace changes its own limits (MU-030 criterion 1).
		a.costGovernor = governor
		if policy := quotaPolicyFrom(cfg.Costs.Quotas); policy != nil {
			governor.SetQuotaPolicy(policy)
			log.Info("multi-level quota policy active",
				zap.Int("organizations", len(cfg.Costs.Quotas.Organizations)),
				zap.Int("workspaces", len(cfg.Costs.Quotas.Workspaces)),
				zap.Int("agents", len(cfg.Costs.Quotas.Agents)))
		}
		log.Info("central LLM cost governance ready", zap.String("path", costsPath),
			zap.String("mode", cfg.Costs.EnforcementMode), zap.Int("pricing_entries", len(prices)))
		if cfg.Costs.Reconciliation.Enabled {
			interval, err := time.ParseDuration(cfg.Costs.Reconciliation.Interval)
			if err != nil || interval <= 0 {
				interval = 24 * time.Hour
			}
			importers := make([]costs.BillingImporter, 0, len(cfg.Costs.Reconciliation.Providers))
			for id, providerCfg := range cfg.Costs.Reconciliation.Providers {
				if !strings.EqualFold(strings.TrimSpace(providerCfg.Type), "openai") {
					log.Warn("unsupported cost reconciliation provider type", zap.String("provider", id), zap.String("type", providerCfg.Type))
					continue
				}
				keyEnv := strings.TrimSpace(providerCfg.APIKeyEnv)
				if keyEnv == "" {
					keyEnv = "OPENAI_ADMIN_KEY"
				}
				importer, err := costs.NewOpenAICostImporter(id, providerCfg.BaseURL, os.Getenv(keyEnv), providerCfg.Organization, nil)
				if err != nil {
					log.Warn("cost reconciliation importer disabled", zap.String("provider", id), zap.String("api_key_env", keyEnv), zap.Error(err))
					continue
				}
				importers = append(importers, importer)
			}
			if len(importers) > 0 {
				reconciler := costs.NewReconciler(costsStore, importers, costs.ReconcilerConfig{
					Interval: interval, VarianceAlertThreshold: cfg.Costs.Reconciliation.VarianceAlertThreshold,
				}, log)
				reconciler.Start(ctx)
				stack.pushClose("cost-reconciler", reconciler)
				log.Info("scheduled provider cost reconciliation ready", zap.Int("providers", len(importers)), zap.Duration("interval", interval))
			}
		}
	}
	return openedCostStore
}

// sweepScratch removes stale per-run scratch dirs left by a crashed previous
// run; live ones are recreated by their owners.
func (a *App) sweepScratch(ws config.Paths) {
	if err := os.RemoveAll(filepath.Join(ws.Data, "scratch")); err != nil {
		a.log.Warn("scratch sweep failed", zap.Error(err))
	}
}

// engineDeps bundles the already-constructed subsystems the engine needs. It
// keeps wireEngine's signature readable given the large dependency set.
type engineDeps struct {
	loader        *runtime.Loader
	llmRouter     *llm.Router
	fileStore     *memory.FileStore
	actionBackend storage.ActionLogBackend
	memBackend    storage.MemoryBackend
	hub           *gateway.EventHub
	skillLoader   *skills.Loader
	skillStores   *skills.Stores
	mcpClient     *mcp.Client
	// mcpServers is the operator's configured template, kept alongside the
	// client because the pool instantiates it per workspace rather than
	// reusing the client's already-started servers.
	mcpServers map[string]mcp.ServerConfig
	// credVault resolves each workspace's OWN MCP credentials. Carried here
	// rather than looked up later because the pool must be told about it
	// before any workspace asks for its first client.
	credVault      credentials.Vault
	knowledgeSvc   *knowledge.Service
	vectorStore    *memory.VectorStore
	pluginProvider runtime.PluginToolProvider
	pluginStores   *plugins.Stores
	pyExecutor     executor.Backend
	namedExecutors map[string]executor.Backend
	brainStores    *agentmemory.Stores
	learningStore  *learning.Stores
	ollamaAPIKey   string
	searchProvider string
	searchAPIKey   string
	toolTimeout    time.Duration
	// stack registers the MCP pool for shutdown; it owns processes the
	// boot-time client does not.
	stack *closerStack
}

// wireEngine constructs the runtime engine and applies all host-side
// configuration (executor, reasoning keys, sandbox, brain memory, audit log,
// tool-dir allowlist, SSRF protection). Behavior is preserved verbatim.
func (a *App) wireEngine(d engineDeps) *runtime.Engine {
	cfg, log := a.cfg, a.log

	engine := runtime.NewEngine(
		d.loader, d.llmRouter, d.fileStore, d.memBackend,
		cfg.Runtime.PythonBin, d.toolTimeout, log, d.hub, d.skillLoader, d.ollamaAPIKey, d.mcpClient, d.knowledgeSvc,
		cfg.Runtime.AllowSystemAgents, d.vectorStore, d.pluginProvider,
	)
	filesystemRoots := append([]string(nil), cfg.Runtime.FilesystemRoots...)
	if len(filesystemRoots) == 0 {
		if ws, err := config.ResolveWorkspace(); err == nil {
			filesystemRoots = []string{ws.Root}
		} else {
			log.Error("filesystem tools disabled: workspace root cannot be resolved", zap.Error(err))
		}
	}
	if err := engine.SetFilesystemRoots(filesystemRoots); err != nil {
		log.Error("filesystem tools disabled: invalid filesystem roots", zap.Error(err))
	} else {
		log.Info("filesystem tool confinement active", zap.Strings("roots", engine.FilesystemRoots()))
	}
	for _, def := range d.loader.All() {
		if d.mcpClient != nil && len(d.mcpClient.AllTools()) > 0 && def.MCPServers == nil && def.MCPTools == nil {
			log.Warn("agent migration required: omitted MCP grants now mean none",
				zap.String("agent", def.ID), zap.String("remediation", "set mcp_servers or mcp_tools explicitly"))
		}
		if d.pluginProvider != nil && len(d.pluginProvider.AllTools()) > 0 && def.PluginTools == nil {
			log.Warn("agent migration required: omitted plugin_tools now means none",
				zap.String("agent", def.ID), zap.String("remediation", "set plugin_tools explicitly"))
		}
		if def.Budget != nil && (def.Budget.MaxTokens == 0 || def.Budget.MaxLLMCalls == 0) {
			log.Warn("agent explicitly disables a run-budget dimension",
				zap.String("agent", def.ID), zap.Int("max_tokens", def.Budget.MaxTokens), zap.Int("max_llm_calls", def.Budget.MaxLLMCalls))
		}
	}
	engine.SetSearchConfig(d.searchProvider, d.searchAPIKey)
	// web_search HTTP ceiling. Unset (or unparseable) keeps the historical 30s.
	// An unparseable value warns rather than silently applying the default, so
	// an operator who wrote `timeout: 90 seconds` learns why it had no effect.
	if raw := strings.TrimSpace(cfg.Search.Timeout); raw != "" {
		if d, ok := runtime.ParseSearchTimeout(raw); ok {
			engine.SetSearchTimeout(d)
			log.Info("web_search timeout configured", zap.Duration("timeout", d))
		} else {
			log.Warn("search.timeout is not a valid duration — using the default",
				zap.String("value", raw), zap.Duration("default", runtime.DefaultSearchTimeout))
		}
	}
	// F-Bridge — install the workspace-scoped default intent-gate mode. The
	// runtime resolver in Engine.evaluateIntent prefers per-agent
	// security.intent_gate, falling back to this workspace default when the
	// per-agent value is empty. Empty here + empty per-agent = intent.Evaluate
	// treats it as ModePrompt (see internal/intent/intent.go).
	engine.SetIntentGateDefault(cfg.Security.IntentGate)
	engine.SetActionLogBackend(d.actionBackend)
	engine.SetExecutor(d.pyExecutor)
	for name, be := range d.namedExecutors {
		engine.SetNamedExecutor(name, be)
	}

	// Story 16 — reasoning loop backends: cloud-provider keys come from the
	// same llm.providers config the router uses (env var fallback matches the
	// providers' own behaviour).
	engine.SetReasoningKeys(reasoning.ProviderKeys{
		AnthropicKey: providerKeyFor(cfg, "anthropic", "ANTHROPIC_API_KEY"),
		OpenAIKey:    providerKeyFor(cfg, "openai", "OPENAI_API_KEY"),
		NvidiaKey:    providerKeyFor(cfg, "nvidia", "NVIDIA_API_KEY"),
		// Fall back the reasoning loop's Ollama backend to the SAME endpoint the
		// chat path uses (env-resolved llm.providers.ollama.base_url), so ReAct
		// reaches Ollama instead of an unreachable localhost inside a container.
		OllamaBaseURL: cfg.LLM.Providers["ollama"].BaseURL,
	})
	// Optional global llm.reasoner override: run the reasoning loop on a strong
	// model regardless of the agent's chat model.
	engine.SetReasonerOverride(cfg.LLM.Reasoner.Provider, cfg.LLM.Reasoner.Model)

	// Canonical install-path hints for agent shell tools (shell_exec / run_script),
	// so agents install skills/plugins/MCP servers/packages into the PERSISTENT
	// workspace volume and register them in the REAL config.yaml — instead of
	// guessing and writing to ephemeral paths (e.g. $HOME, the CWD) that vanish
	// on restart. Surfaced to the system agent's prompt too (buildSystemPrefix).
	if ws, werr := config.ResolveWorkspace(); werr == nil {
		engine.SetAgentShellEnv([]string{
			"SOULACY_WORKSPACE=" + ws.Root,
			"SOULACY_CONFIG_FILE=" + ws.ConfigFile,
			"SOULACY_AGENTS_DIR=" + ws.Agents,
			"SOULACY_SKILLS_DIR=" + ws.Skills,
			"SOULACY_PLUGINS_DIR=" + ws.Plugins,
			"SOULACY_MCP_DIR=" + filepath.Join(ws.Root, "mcp-servers"),
		})
	}

	// Privileged builtins use a mandatory, fail-closed isolation backend. The
	// legacy rlimit wrapper remains for ordinary agent Python tools; it is not
	// treated as a security boundary.
	sbx := cfg.Runtime.Sandbox
	limits := sandbox.Limits{Enabled: true, CPUSeconds: sbx.CPUSeconds, MemoryMB: sbx.MemoryMB, OpenFiles: sbx.OpenFiles, FileSizeMB: sbx.FileSizeMB}
	isolation, isolationReason := privilegedIsolationFor(sbx.Enabled, sbx.Mode, cfg.DeploymentMode())
	if isolation == IsolationRefused {
		// MU-021 criterion 1. SetPrivilegedCommandRunner(nil) installs the
		// deny runner, so every privileged builtin refuses rather than
		// silently running somewhere the operator did not intend.
		engine.SetPrivilegedCommandRunner(nil)
		log.Error("privileged tools disabled: unsandboxed execution is refused outside personal mode",
			zap.String("deployment_mode", cfg.DeploymentMode()),
			zap.String("reason", isolationReason),
			zap.String("remediation", "set runtime.sandbox.mode=docker, or run in personal mode"))
	} else if isolation == IsolationHost {
		engine.SetPrivilegedCommandRunner(runtime.HostPrivilegedRunner{})
		if roots := engine.FilesystemRoots(); len(roots) > 0 {
			engine.SetPrivilegedWorkDir(roots[0])
		}
		log.Error("UNSAFE privileged-tool mode active",
			zap.String("mode", "unsandboxed"), zap.String("reason", isolationReason))
	} else if roots := engine.FilesystemRoots(); len(roots) > 0 {
		sandboxWorkDir := filepath.Join(roots[0], "data", "sandbox")
		if err := os.MkdirAll(sandboxWorkDir, 0o700); err != nil {
			log.Error("privileged tools disabled: cannot create isolated workspace", zap.Error(err))
		} else {
			_ = os.Chmod(sandboxWorkDir, 0o700)
			engine.SetPrivilegedWorkDir(sandboxWorkDir)
			// MU-021: Root is the outer bound, Workspace the personal default.
			// The engine narrows the mount to the running workspace's own tree
			// (see workspaceScratchDir); roots[0] is what makes that legal
			// without letting any caller name an arbitrary host path.
			engine.SetPrivilegedCommandRunner(runtime.DockerPrivilegedRunner{Workspace: sandboxWorkDir, Root: roots[0], Image: sbx.Image, Limits: limits, PIDs: sbx.PIDs})
			log.Info("privileged tool isolation enabled", zap.String("mode", "docker"), zap.String("image", sbx.Image), zap.String("network", "none"), zap.String("workspace", sandboxWorkDir))
		}
	} else {
		log.Error("privileged tools disabled: isolation has no workspace root")
	}

	if sbx.Enabled {
		if selfPath, e := os.Executable(); e == nil {
			engine.SetSandbox(selfPath, limits)
			log.Info("python resource limits enabled",
				zap.Int("cpu_seconds", sbx.CPUSeconds),
				zap.Int("memory_mb", sbx.MemoryMB),
				zap.Int("open_files", sbx.OpenFiles),
				zap.Int("file_size_mb", sbx.FileSizeMB),
			)
		} else {
			log.Warn("python resource wrapper unavailable", zap.Error(e))
		}
	}

	// Per-workspace MCP servers (MU-017 criterion 5). Installed AFTER
	// SetFilesystemRoots and SetSandbox, because the pool asks the engine
	// where each workspace's tree is and what limits to apply — installing it
	// earlier would give every workspace the pre-configuration answer, which
	// for the roots is "none" and therefore no servers at all.
	//
	// The engine is both the pool's confinement source and its consumer. That
	// cycle is real and is resolved by ordering rather than by an interface
	// dance: the pool holds the engine, the engine holds the pool, and the
	// pool only calls back lazily on a workspace's first MCP use — long after
	// this function returns.
	if d.mcpClient != nil {
		pool := mcp.NewPool(mcp.Config{Servers: d.mcpServers}, engine, log)
		// BEFORE any workspace can ask for a client, and before the engine
		// holds the pool. The operator's config carries the operator's tokens;
		// in a multi-user deployment those must not travel into a tenant's
		// subprocess, because a tenant running as the operator sees whatever
		// the operator's token sees — including other tenants' data. See
		// internal/mcp/tenantcreds.go.
		//
		// Personal is untouched: one tenant, whose credentials genuinely are
		// the operator's.
		if config.IsMultiUserMode(cfg.DeploymentMode()) {
			pool.RequireTenantCredentials(vaultMCPCredentials(d.credVault))
			log.Info("mcp servers use each workspace's own credentials; a server whose secrets a " +
				"workspace has not supplied is not started for it")
		}
		engine.SetMCPPool(pool)
		a.mcpPool = pool
		// Registered for shutdown separately from mcp-client: the pool owns a
		// different set of processes (one per active workspace), and closing
		// only the boot-time client would leave every workspace's servers
		// running after the gateway exits — the zombie problem MU-017
		// criterion 7 fixed for RemoveServer, reappearing at shutdown.
		if d.stack != nil {
			d.stack.pushClose("mcp-pool", pool)
		}
		log.Info("mcp servers are per-workspace", zap.Int("configured", len(d.mcpServers)))
	}

	// MEM-03: pass the brain memory store into the engine.
	// Per-workspace skill catalogs. A skill is executable instruction text an
	// agent follows, so a shared catalog changes what another tenant's agents
	// do rather than merely exposing metadata.
	// Per-workspace plugin contributions. A plugin contributes tools an agent
	// can CALL, so a shared provider does not merely expose another tenant's
	// inventory — it runs their code on this tenant's behalf.
	//
	// The adapter is rebuilt per workspace rather than cached because
	// Stores.For already caches the loader; wrapping it is a struct literal.
	// An empty loader returns a provider with no tools rather than nil, so
	// "this workspace has none" and "no resolver is installed" stay distinct —
	// conflating them is what would send the second case to the shared
	// provider.
	if d.pluginStores != nil {
		engine.SetPluginProviders(func(workspaceID string) runtime.PluginToolProvider {
			loader := d.pluginStores.For(workspaceID)
			if loader == nil {
				return nil
			}
			return &pluginToolAdapter{loader: loader}
		})
	}
	if d.skillStores != nil {
		engine.SetSkillLoaders(func(workspaceID string) runtime.SkillLoader {
			if loader := d.skillStores.For(workspaceID); loader != nil {
				return loader
			}
			return nil
		})
	}
	if d.brainStores != nil {
		engine.SetBrainMemory(d.brainStores)
	}
	if d.learningStore != nil {
		engine.SetLearningStores(d.learningStore)
	}

	// Runtime adaptive-node salvage: on by default (keep flows running through
	// shape surprises); operators opt out with runtime.adaptive_nodes: false.
	engine.SetAdaptiveNodes(cfg.Runtime.AdaptiveNodes == nil || *cfg.Runtime.AdaptiveNodes)

	engine.SetAuditLog(audit.NewWithRetention(cfg.Runtime.AuditDir, config.RetentionDuration(cfg.Runtime.Retention.AuditLogs, 30*24*time.Hour)))
	if cfg.Runtime.AuditDir != "" {
		log.Info("audit logging enabled", zap.String("dir", cfg.Runtime.AuditDir))
	}

	// python_file path allowlist — prevents crafted SOUL.yaml from executing
	// arbitrary host files. Skipped (all paths allowed) when list is empty.
	engine.SetAllowedToolDirs(cfg.Runtime.AllowedToolDirs)
	if len(cfg.Runtime.AllowedToolDirs) > 0 {
		log.Info("python_file allowlist active",
			zap.Strings("allowed_tool_dirs", cfg.Runtime.AllowedToolDirs))
	}

	// SSRF protection for HTTP-fetching built-in tools.
	engine.SetSSRF(cfg.Runtime.SSRFProtection, cfg.Runtime.AllowPrivateHosts)
	if cfg.Runtime.SSRFProtection {
		log.Info("SSRF protection enabled",
			zap.Strings("allow_private_hosts", cfg.Runtime.AllowPrivateHosts))
	}

	// PERF-1: session eviction (TTL + max-count). Sessions accumulate forever
	// without this — the sweeper reclaims idle/excess in-memory sessions.
	sessionTTL := 24 * time.Hour
	if cfg.Runtime.SessionTTL != "" {
		if d, perr := time.ParseDuration(cfg.Runtime.SessionTTL); perr == nil && d > 0 {
			sessionTTL = d
		} else if perr != nil {
			log.Warn("invalid runtime.session_ttl, using default 24h",
				zap.String("value", cfg.Runtime.SessionTTL), zap.Error(perr))
		}
	}
	engine.SetSessionEviction(sessionTTL, cfg.Runtime.MaxSessions)
	engine.StartSessionEviction(0) // 0 → derive interval from TTL
	log.Info("session eviction enabled",
		zap.Duration("ttl", sessionTTL),
		zap.Int("max_sessions", cfg.Runtime.MaxSessions))

	// PERF-2: history windowing — cap per-session in-memory History length.
	engine.SetMaxHistoryTurns(cfg.Runtime.MaxHistoryTurns)

	// S3.2: hard ceiling on any agent's effective max_turns.
	engine.SetMaxTurnsCeiling(cfg.Runtime.MaxTurnsCeiling)
	parseTimeout := func(raw string) time.Duration { d, _ := time.ParseDuration(raw); return d }
	engine.SetTimeoutHierarchy(
		parseTimeout(cfg.Runtime.Timeouts.Tool), parseTimeout(cfg.Runtime.Timeouts.LLM),
		parseTimeout(cfg.Runtime.Timeouts.Step), parseTimeout(cfg.Runtime.Timeouts.Run),
	)
	engine.SetRunBudgets(
		agent.BudgetConfig{MaxTokens: cfg.Runtime.DefaultBudget.MaxTokens, MaxLLMCalls: cfg.Runtime.DefaultBudget.MaxLLMCalls},
		agent.BudgetConfig{MaxTokens: cfg.Runtime.MaxBudget.MaxTokens, MaxLLMCalls: cfg.Runtime.MaxBudget.MaxLLMCalls},
	)
	if cfg.Runtime.DefaultBudget.MaxTokens == 0 || cfg.Runtime.DefaultBudget.MaxLLMCalls == 0 {
		log.Warn("runtime default run budget contains an unlimited dimension",
			zap.Int("max_tokens", cfg.Runtime.DefaultBudget.MaxTokens), zap.Int("max_llm_calls", cfg.Runtime.DefaultBudget.MaxLLMCalls))
	}
	if cfg.Runtime.MaxBudget.MaxTokens == 0 || cfg.Runtime.MaxBudget.MaxLLMCalls == 0 {
		log.Warn("runtime maximum run budget contains an unlimited ceiling",
			zap.Int("max_tokens", cfg.Runtime.MaxBudget.MaxTokens), zap.Int("max_llm_calls", cfg.Runtime.MaxBudget.MaxLLMCalls))
	}

	// Bound recursive peer-agent delegation chains while allowing deeper
	// coordinator hierarchies to opt in from config.
	engine.SetMaxAgentCallDepth(cfg.Runtime.MaxAgentCallDepth)

	return engine
}

// startMessageRouter launches the bounded worker pool draining the shared
// inbox. HTTP channel messages are handled synchronously elsewhere; all other
// channel messages flow through the engine and are replied to via
// executeDurableRun claims, runs and records one durable run.
//
// Every step goes through the run record rather than through worker-local
// state, so a second process picking the same message up cannot double-execute
// it and a cancellation issued mid-flight is not overwritten by the outcome.
func (a *App) executeDurableRun(ctx context.Context, engine *runtime.Engine, loader *runtime.Loader,
	runStore *runs.Store, msg message.Message, runID string) {
	log := a.log
	if runStore == nil {
		log.Error("a durable run arrived but no run store is configured", zap.String("run_id", runID))
		return
	}
	run, ok := beginRun(ctx, runStore, msg.WorkspaceID, runID, a.workerID(), log)
	if !ok {
		return
	}

	timeout := 5 * time.Minute
	if def := loader.Get(msg.AgentID); def != nil {
		timeout = def.ResolvedRunTimeout(timeout)
	}
	// The run gets a DRAIN context, not a child of the app context. A child
	// would be cancelled the instant SIGTERM arrives, so an executing agent
	// would be cut off mid-tool-call while the HTTP layer was still politely
	// finishing its requests. See rundrain.go.
	drainCtx, draining, stopDrain := runDrainContext(ctx)
	defer stopDrain()
	runCtx, cancel := context.WithTimeout(drainCtx, timeout)
	defer cancel()
	runCtx = runtime.WithPrincipal(runCtx, runPrincipal(run, msg.ID))
	// The run's identity travels with the context so the engine can mark the
	// first outside-visible call it makes (MU-021 criterion 6). Without this
	// the marker is never set and the recovery sweep reads every lost run as
	// retry-safe — the exact double-execution the criterion forbids.
	runCtx = withRunID(runCtx, run.WorkspaceID, run.ID)

	// MU-027 criterion 5. The record is the cancellation signal, because the
	// request may arrive at a different gateway process than the one running
	// the work — an in-memory channel would only reach a worker in the same
	// process. Stopped before the outcome is recorded, or the poller outlives
	// the run.
	stopWatching := watchForCancellation(runCtx, cancel, runStore, run, log)

	// MU-034 criterion 1: hold the claim for as long as the work takes. Only
	// now, with the run context built — a lease renewed past the run's own
	// deadline would keep a finished run looking held.
	releaseLease := holdRunLease(runCtx, runStore, run, a.workerID(), cancel, log)

	metrics.WorkerPoolActiveRuns.Inc()
	reply, err := engine.Handle(runCtx, msg)
	metrics.WorkerPoolActiveRuns.Dec()
	stopWatching()
	// Released BEFORE the outcome is recorded. finishRun's terminal
	// transition clears the expiry anyway, but a run that ends by timeout may
	// never reach a terminal state, and leaving that one held would delay
	// recovery of a genuinely crashed worker by a full lease period.
	releaseLease()

	// context.WithoutCancel: the outcome must be recorded even when the run
	// timed out. Writing it through the cancelled context would leave the
	// record stuck in "running" forever, which is the one state a reader
	// cannot distinguish from "still working".
	outcomeCtx := context.WithoutCancel(ctx)

	// A run interrupted by SHUTDOWN is left exactly as a crashed one: still
	// `running`, with no holder. Writing `failed` here — which is what
	// finishRun would do with a cancelled context — makes it terminal, and the
	// recovery sweep skips terminal runs by design. So a clean shutdown
	// destroyed work that a crash would have recovered.
	//
	// Doing nothing is the fix. The lease was released above, so the next
	// boot's sweep sees an unheld running run and applies the real policy:
	// re-queue what never touched the outside world, fail what did with the
	// tool named. That decision belongs to recovery, which knows whether a
	// retry is safe; this function does not.
	if draining() && err != nil {
		log.Info("durable run interrupted by shutdown; left for recovery to decide",
			zap.String("run_id", run.ID), zap.String("workspace_id", run.WorkspaceID))
	} else if !finishCancelled(outcomeCtx, runStore, run, log) {
		// A run that stopped because it was asked to did not fail, and
		// recording it as failed would put a cancellation in whatever
		// dashboard counts failures.
		finishRun(outcomeCtx, runStore, run, replyText(reply), err, log)
	}
	// MU-027 criterion 6: record the decomposition from the finished record
	// rather than from timers held in this function. The record is the one
	// place queue latency exists at all — this worker never saw the
	// submission — and re-reading it keeps the metric and the API reporting
	// the same numbers.
	if finished, ferr := runStore.Get(outcomeCtx, run.WorkspaceID, run.ID); ferr == nil {
		metrics.ObserveRunLatency(
			finished.QueueLatency(),
			time.Duration(finished.ExternalMicros)*time.Microsecond,
			finished.ProcessingLatency(),
		)
	}

	// MU-021 criteria 2 and 5: the run's scratch directory goes away when the
	// run does, whatever the outcome. A failed or timed-out run is exactly as
	// likely to have written a decrypted secret to disk as a successful one —
	// more likely, if it died holding one — so cleanup cannot be conditional
	// on success, and it cannot run through the cancelled context either.
	if scratchErr := engine.RemoveRunScratch(outcomeCtx, run.WorkspaceID, run.ID); scratchErr != nil {
		log.Warn("run scratch space could not be removed",
			zap.String("run_id", run.ID), zap.String("workspace_id", run.WorkspaceID), zap.Error(scratchErr))
	}
}

// chanReg.Send(). Concurrency is bounded by runtime.max_concurrent_sessions
// (default 100); per-run timeout uses each agent's declared run_timeout.
// (PRODUCTION_AUDIT → CRITICAL/Concurrency)
func (a *App) startMessageRouter(ctx context.Context, chanReg *channels.Registry, loader *runtime.Loader, engine *runtime.Engine, personalTenant *tenancy.PersonalTenant, runStore *runs.Store) {
	cfg, log := a.cfg, a.log
	workerCount := cfg.Runtime.MaxConcurrentSessions
	if workerCount <= 0 {
		workerCount = 100
	}
	inbox := chanReg.Inbox()
	for w := 0; w < workerCount; w++ {
		go func() {
			for {
				// S2.2 — exit promptly on shutdown. Previously workers ranged
				// over the inbox channel, which is never closed, so they hung
				// forever on SIGTERM and the process couldn't drain/exit
				// cleanly. Selecting on ctx.Done() lets each idle worker return
				// the moment the app context is cancelled.
				var msg message.Message
				select {
				case <-ctx.Done():
					return
				case m, ok := <-inbox:
					if !ok {
						return
					}
					msg = m
				}
				if msg.Channel == "http" {
					continue // synchronous path
				}
				// A durable run carries its own record. It executes here like
				// any other message, but its outcome is stored rather than
				// sent: the caller has already gone, and comes back for the
				// result by run id.
				if runID := RunIDOf(msg); runID != "" {
					a.executeDurableRun(ctx, engine, loader, runStore, msg, runID)
					continue
				}
				if config.IsMultiUserMode(cfg.DeploymentMode()) && personalTenant == nil {
					log.Error("channel message blocked: verified workspace routing is unavailable",
						zap.String("channel", msg.Channel), zap.String("agent", msg.AgentID))
					continue
				}
				def := loader.Get(msg.AgentID)
				timeout := 5 * time.Minute
				if def != nil {
					timeout = def.ResolvedRunTimeout(timeout)
				}
				mCtx, mCancel := context.WithTimeout(ctx, timeout)
				if personalTenant != nil {
					subject := strings.TrimSpace(msg.UserID)
					if subject == "" {
						subject = "channel:" + strings.TrimSpace(msg.Channel)
					}
					mCtx = runtime.WithPrincipal(mCtx, runtime.Principal{
						Subject: subject, OrganizationID: personalTenant.OrganizationID,
						WorkspaceID: personalTenant.WorkspaceID, MembershipID: personalTenant.MembershipID,
						Role: "admin", CredentialID: "channel:" + strings.TrimSpace(msg.Channel),
						RequestID: msg.ID, Kind: "channel-user",
					})
				}
				// Worker-pool saturation gauge. (PRODUCTION_AUDIT → MED/Observability)
				metrics.WorkerPoolActiveRuns.Inc()
				reply, err := engine.Handle(mCtx, msg)
				metrics.WorkerPoolActiveRuns.Dec()
				if err != nil {
					log.Error("engine error", zap.String("agent", msg.AgentID), zap.Error(err))
					// Don't leave the user staring at silence: send a short
					// error back to the originating chat so a failed run
					// (e.g. LLM unreachable / model not pulled) is visible.
					errReply := message.Message{
						WorkspaceID: msg.WorkspaceID,
						SessionID:   msg.SessionID,
						AgentID:     msg.AgentID,
						Channel:     msg.Channel,
						ThreadID:    msg.ThreadID,
						UserID:      msg.UserID,
						Role:        message.RoleAssistant,
						Parts:       message.Text("⚠ Sorry — I couldn't complete that. (" + concise(err) + ") Check the agent's LLM provider is reachable; see the gateway Logs."),
						CreatedAt:   time.Now().UTC(),
					}
					if serr := chanReg.Send(mCtx, errReply); serr != nil {
						log.Error("channel send error (error-reply)",
							zap.String("channel", msg.Channel), zap.Error(serr))
					}
					mCancel()
					continue
				}
				if err := chanReg.Send(mCtx, reply); err != nil {
					log.Error("channel send error",
						zap.String("channel", msg.Channel), zap.Error(err))
				}
				mCancel()
			}
		}()
	}
}

// schedulerInstanceID identifies this gateway process to the schedule claim
// (MU-023).
//
// Two instances sharing an identity are indistinguishable to the claim: each
// looks like the other re-entering, so a lease steal cannot be told from a
// retry and exactly-once quietly becomes at-least-once. Hostname plus PID is
// the strongest identity available without asking an operator to configure
// one — distinct across containers, distinct across processes on one host, and
// stable for the life of the process, which is the lifetime a lease is about.
func schedulerInstanceID() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown-host"
	}
	return host + ":" + strconv.Itoa(os.Getpid())
}

// quotaPolicyFrom converts the configured multi-level limits into a resolved
// policy, or nil when nothing is configured (MU-024).
func quotaPolicyFrom(cfg config.QuotaConfig) *quota.Policy {
	convert := func(in map[string]config.QuotaLimit) costs.LevelLimits {
		if len(in) == 0 {
			return nil
		}
		out := make(costs.LevelLimits, len(in))
		for id, limit := range in {
			out[id] = costs.DollarsToLimit(limit.DailyUSD, limit.MonthlyUSD, limit.DailyTokens, limit.Concurrency)
		}
		return out
	}
	return costs.BuildPolicy(
		costs.DollarsToLimit(cfg.Deployment.DailyUSD, cfg.Deployment.MonthlyUSD, cfg.Deployment.DailyTokens, cfg.Deployment.Concurrency),
		convert(cfg.Organizations), convert(cfg.Workspaces),
		convert(cfg.Principals), convert(cfg.Agents), convert(cfg.Models),
	)
}
