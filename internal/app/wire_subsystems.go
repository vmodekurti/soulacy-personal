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
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/internal/skills"
	"github.com/soulacy/soulacy/internal/storage"
	storagepg "github.com/soulacy/soulacy/internal/storage/postgres"
	storagesqlite "github.com/soulacy/soulacy/internal/storage/sqlite"
	"github.com/soulacy/soulacy/internal/telemetry"
	"github.com/soulacy/soulacy/internal/vector"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/queue"
	"github.com/soulacy/soulacy/sdk/registry"
	sdkstorage "github.com/soulacy/soulacy/sdk/storage"
)

// wireBrainMemory builds the agent-brain CompositeStore (episodic/semantic/
// procedural). A missing/uncreatable dir disables long-term memory (warn, not
// fatal) and returns a nil store.
func (a *App) wireBrainMemory(ws config.Paths, stack *closerStack) *agentmemory.CompositeStore {
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
	brainStore := agentmemory.NewCompositeStore(brainMemDir, nil)
	stack.push("brain-memory", func() error { brainStore.Close(); return nil }) // releases the E23 rulebook db
	log.Info("agent brain memory enabled", zap.String("dir", brainMemDir))
	return brainStore
}

func (a *App) wireLearning(ws config.Paths) *learning.Store {
	store, err := learning.NewStore(ws.DB("learning"))
	if err != nil {
		a.log.Warn("learning proposal store unavailable", zap.Error(err))
		return nil
	}
	a.log.Info("learning proposal store ready", zap.String("path", ws.DB("learning")))
	return store
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
func (a *App) wireLoaders(ws config.Paths) (*runtime.Loader, *plugins.Loader, *skills.Loader) {
	cfg, log := a.cfg, a.log

	// ── Agent Loader ─────────────────────────────────────────────────────────
	loader := runtime.NewLoader(cfg.AgentDirs)
	loader.SetLogger(log)
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
	pluginLoader := plugins.New(cfg.PluginDirs, log)
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
	skillLoader := skills.New(workDir, skillDirs, log)
	if errs := skillLoader.Scan(); len(errs) > 0 {
		for _, e := range errs {
			log.Warn("skill load warning", zap.Error(e))
		}
	}
	if skillLoader.Count() > 0 {
		log.Info("agent skills loaded", zap.Int("count", skillLoader.Count()))
	}

	return loader, pluginLoader, skillLoader
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
		return nil, nil
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
		// production users. Warn loudly so operators know they are on an
		// unvetted code path.
		log.Warn("nats queue backend is EXPERIMENTAL and untested — no automated tests, no known production users; the default in-memory queue is the supported path")
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
		Mode:          cfg.Auth.Mode,
		JWTSecret:     cfg.Auth.JWTSecret,
		JWTAccessTTL:  accessTTL,
		JWTRefreshTTL: refreshTTL,
		OIDCIssuer:    cfg.Auth.OIDCIssuer,
		OIDCAudience:  cfg.Auth.OIDCAudience,
		OIDCClientID:  cfg.Auth.OIDCClientID,
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
		log.Warn("RBAC SQLite store unavailable, falling back to static policy only",
			zap.String("path", rbacDBPath), zap.Error(rerr))
		rbacStore = rbac.NoopStore{}
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

	// ── Telemetry (OTEL) ─────────────────────────────────────────────────────
	telCfg := telemetry.Config{
		Enabled:      cfg.Telemetry.Enabled,
		Exporter:     cfg.Telemetry.Exporter,
		OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
		ServiceName:  cfg.Telemetry.ServiceName,
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
		reservationTTL, err := time.ParseDuration(cfg.Costs.ReservationTTL)
		if err != nil || reservationTTL <= 0 {
			reservationTTL = 15 * time.Minute
		}
		circuitCooldown, err := time.ParseDuration(cfg.Costs.CircuitCooldown)
		if err != nil || circuitCooldown <= 0 {
			circuitCooldown = 30 * time.Second
		}
		providerPolicies := make(map[string]costs.ProviderPolicy, len(cfg.LLM.Providers))
		for id, providerCfg := range cfg.LLM.Providers {
			providerPolicies[id] = costs.ProviderPolicy{AllowedDataClasses: providerCfg.AllowedDataClasses,
				CacheAllowedDataClasses: providerCfg.CacheAllowedDataClasses,
				MaxTokensPerMinute:      providerCfg.MaxTokensPerMinute, Region: providerCfg.Region,
				Retention: providerCfg.Retention, PromptCaching: providerCfg.PromptCaching}
		}
		llmRouter.SetController(costs.NewGovernor(costsStore, prices, costs.GovernanceConfig{
			DailyBudgetUSD:           cfg.Costs.DailyBudgetUSD,
			MonthlyBudgetUSD:         cfg.Costs.MonthlyBudgetUSD,
			PerUserDailyBudgetUSD:    cfg.Costs.PerUserDailyBudgetUSD,
			PerAgentDailyBudgetUSD:   cfg.Costs.PerAgentDailyBudgetUSD,
			EnforcementMode:          cfg.Costs.EnforcementMode,
			UnknownPricing:           cfg.Costs.UnknownPricing,
			DefaultMaxOutput:         cfg.Costs.DefaultMaxOutputTokens,
			MaxOutputCeiling:         cfg.Costs.MaxOutputTokensCeiling,
			ConfirmationThresholdUSD: cfg.Costs.ConfirmationThresholdUSD,
			ReservationTTL:           reservationTTL,
			PerUserTokensDay:         cfg.RateLimit.PerUserTokensDay,
			PerAgentTokensDay:        cfg.RateLimit.PerAgentTokensDay,
			AllowedProviders:         cfg.LLM.AllowedProviders,
			AllowedModels:            cfg.LLM.AllowedModels,
			AllowedRegions:           cfg.LLM.AllowedRegions,
			MaxConcurrentPerProvider: cfg.Costs.MaxConcurrentPerProvider,
			CircuitFailureThreshold:  cfg.Costs.CircuitFailureThreshold,
			CircuitCooldown:          circuitCooldown,
			ProviderPolicies:         providerPolicies,
		}))
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
	loader         *runtime.Loader
	llmRouter      *llm.Router
	fileStore      *memory.FileStore
	actionBackend  storage.ActionLogBackend
	memBackend     storage.MemoryBackend
	hub            *gateway.EventHub
	skillLoader    *skills.Loader
	mcpClient      *mcp.Client
	knowledgeSvc   *knowledge.Service
	vectorStore    *memory.VectorStore
	pluginProvider runtime.PluginToolProvider
	pyExecutor     executor.Backend
	namedExecutors map[string]executor.Backend
	brainStore     *agentmemory.CompositeStore
	learningStore  *learning.Store
	ollamaAPIKey   string
	searchProvider string
	searchAPIKey   string
	toolTimeout    time.Duration
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
	if !sbx.Enabled || strings.EqualFold(strings.TrimSpace(sbx.Mode), "unsandboxed") {
		engine.SetPrivilegedCommandRunner(runtime.HostPrivilegedRunner{})
		if roots := engine.FilesystemRoots(); len(roots) > 0 {
			engine.SetPrivilegedWorkDir(roots[0])
		}
		log.Error("UNSAFE privileged-tool mode active: commands run as the gateway user without isolation",
			zap.String("mode", "unsandboxed"))
	} else if roots := engine.FilesystemRoots(); len(roots) > 0 {
		sandboxWorkDir := filepath.Join(roots[0], "data", "sandbox")
		if err := os.MkdirAll(sandboxWorkDir, 0o700); err != nil {
			log.Error("privileged tools disabled: cannot create isolated workspace", zap.Error(err))
		} else {
			_ = os.Chmod(sandboxWorkDir, 0o700)
			engine.SetPrivilegedWorkDir(sandboxWorkDir)
			engine.SetPrivilegedCommandRunner(runtime.DockerPrivilegedRunner{Workspace: sandboxWorkDir, Image: sbx.Image, Limits: limits, PIDs: sbx.PIDs})
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

	// MEM-03: pass the brain memory store into the engine.
	if d.brainStore != nil {
		engine.SetBrainMemory(d.brainStore)
	}
	if d.learningStore != nil {
		engine.SetLearningStore(d.learningStore)
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
// chanReg.Send(). Concurrency is bounded by runtime.max_concurrent_sessions
// (default 100); per-run timeout uses each agent's declared run_timeout.
// (PRODUCTION_AUDIT → CRITICAL/Concurrency)
func (a *App) startMessageRouter(ctx context.Context, chanReg *channels.Registry, loader *runtime.Loader, engine *runtime.Engine) {
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
				def := loader.Get(msg.AgentID)
				timeout := 5 * time.Minute
				if def != nil {
					timeout = def.ResolvedRunTimeout(timeout)
				}
				mCtx, mCancel := context.WithTimeout(ctx, timeout)
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
						SessionID: msg.SessionID,
						AgentID:   msg.AgentID,
						Channel:   msg.Channel,
						ThreadID:  msg.ThreadID,
						UserID:    msg.UserID,
						Role:      message.RoleAssistant,
						Parts:     message.Text("⚠ Sorry — I couldn't complete that. (" + concise(err) + ") Check the agent's LLM provider is reachable; see the gateway Logs."),
						CreatedAt: time.Now().UTC(),
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
