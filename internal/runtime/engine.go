// engine.go — the agent execution loop.
// The Engine is the heart of Soulacy. It receives a message, assembles the
// full context (system prompt + memory + history + tools), fires the LLM, and
// if the LLM requests tool calls, executes them in a sandboxed Python subprocess
// before re-entering the loop. This continues until the LLM produces a plain
// text response or the max_turns limit is hit.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/audit"
	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/executor"
	"github.com/soulacy/soulacy/internal/injection"
	"github.com/soulacy/soulacy/internal/knowledge"
	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/policy"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
)

// EventSink receives structured events as they happen during agent execution.
// The gateway WebSocket handler implements this to stream events to the GUI.
type EventSink interface {
	Emit(event message.Event)
}

// noopSink discards all events (used when no GUI is connected).
type noopSink struct{}

func (noopSink) Emit(_ message.Event) {}

// SkillLoader is satisfied by *skills.Loader. Defined as an interface here to
// avoid an import cycle (skills → runtime would be circular).
type SkillLoader interface {
	BuildCatalog() string
	Get(name string) *skill.Skill
	All() []*skill.Skill
}

// BuiltinTool is a Go-native tool that runs inside the engine process rather
// than delegating to a Python subprocess. Built-ins are added alongside the
// agent's Python tool definitions when building the LLM tool schema.
type BuiltinTool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     func(ctx context.Context, args map[string]any) (string, error)
	// Gate controls when the tool is offered to the LLM:
	//   "skills" — only when the agent has opted into skills (def.Skills)
	//   "ollama" — only when the agent's LLM provider is Ollama
	//   ""       — always
	Gate string
}

// Engine orchestrates agent execution.
type Engine struct {
	loader      *Loader
	llmRouter   *llm.Router
	memory      memory.Store
	archive     storage.MemoryBackend
	pythonBin   string
	toolTimeout time.Duration
	// adaptiveNodes is the global default for runtime LLM salvage of shape
	// surprises (see FlowNode.Adaptive). Off by default in the zero engine so
	// tests are unaffected; the app wires it from cfg.Runtime.AdaptiveNodes.
	adaptiveNodes bool
	log           *zap.Logger
	sink          EventSink
	sessions      sync.Map // sessionID → *Session

	// flowTraces holds the per-block run traces of recent flow runs (Story S0.3
	// Phase 1 logging), lazily created via ftStore(). In-memory + bounded.
	flowTraceOnce sync.Once
	flowTraces    *flowTraceStore

	// Skills support
	skillLoader      SkillLoader
	builtins         []BuiltinTool
	channelRegistry  *channels.Registry
	channelDefaultMu sync.RWMutex
	channelDefaults  map[string]agent.ScheduleOutput
	queueStore       *agentQueueStore

	// ollamaAPIKey is used by the built-in web_search tool (Ollama Web Search API).
	// Falls back to the OLLAMA_API_KEY env var at call time.
	ollamaAPIKeyMu sync.RWMutex
	ollamaAPIKey   string

	// intentGateDefault is the workspace-level fallback for the S3 tool-call
	// intent gate (Cohort F-Bridge). Consulted from evaluateIntent only when
	// the per-agent agent.Definition.Security.IntentGate is empty. Values
	// match internal/intent.Mode strings (""|"off"|"prompt"|"deny"). Set via
	// SetIntentGateDefault; wired in from internal/app/wire_subsystems.go
	// off Config.Security.IntentGate.
	intentGateDefaultMu sync.RWMutex
	intentGateDefault   string

	// Web search provider and API key
	searchProviderMu sync.RWMutex
	searchProvider   string
	searchAPIKey     string
	// searchTimeout is the operator-level HTTP timeout for the built-in
	// web_search tool (config.yaml search.timeout). Zero = DefaultSearchTimeout.
	// Guarded by searchProviderMu. See searchtimeout.go.
	searchTimeout time.Duration

	// mcpClient routes MCP tool calls to configured external MCP servers.
	// All tools from connected servers are offered to every agent, namespaced
	// as mcp__<server>__<tool>. May be nil if no servers are configured.
	mcpClient *mcp.Client

	// knowledge is the RAG facade — used by the kb_search built-in tool and
	// by buildContext to inject the per-agent KB catalog. nil = RAG disabled.
	knowledge *knowledge.Service

	// Conversational agent builder sessions (sessionID → *builderSession)
	builderSessions sync.Map

	// PRODUCTION_AUDIT → F1 (2026-05-27): when both fields are set,
	// executePythonTool wraps every command in a re-exec of the
	// soulacy binary (selfPath) under __exec-sandbox, applying the
	// rlimits in sandboxLimits before execve'ing python. Zero values
	// disable wrapping (engine falls through to the legacy direct exec).
	selfPath          string
	sandboxLimits     sandbox.Limits
	privilegedRunner  PrivilegedCommandRunner
	privilegedWorkDir string

	// FailureNotifier is wired from main.go to route run failures to a
	// configured channel (see agent.NotifyOnFailure) and/or back to the
	// originating channel. nil = silent (legacy behavior) — failures only
	// land in the actionlog. Set via SetFailureNotifier.
	failureNotifier FailureNotifier

	// allowSystemAgents is the list of agent IDs allowed to access the OS-level
	// built-in tools (shell_exec, run_script, install_library, write_file,
	// download_file). Set from config.Runtime.AllowSystemAgents.
	// An agent must be listed here AND declare the "system" capability.
	allowSystemAgents []string

	// vectorStore, when non-nil, backs the semantic_memory_search built-in.
	// Powered by sqlite-vec in the same archive DB as the long-term memory.
	vectorStore *memory.VectorStore

	// brainStore, when non-nil, enables three-layer long-term memory
	// (episodic / semantic / procedural) for agents that declare brain_memory
	// in their SOUL.yaml. Set via SetBrainMemory after construction.
	brainStore *agentmemory.CompositeStore

	// learningStore, when non-nil, stores reviewable post-run learning
	// proposals for agents that declare learning.enabled in SOUL.yaml.
	learningStore *learning.Store

	// actionLog, when non-nil, backs the session_search built-in so agents can
	// retrieve useful past run context without direct filesystem access.
	actionLog storage.ActionLogBackend

	// pluginProvider, when non-nil, provides plugin-contributed tools.
	// Satisfied by *plugins.Loader via an adapter in main.go.
	pluginProvider PluginToolProvider

	// broker handles pending tool-confirmation requests from the UI.
	// Allocated once in NewEngine; the gateway calls Broker() to resolve decisions.
	broker *ConfirmBroker

	// auditLog records every built-in tool call to an append-only JSONL file.
	// nil = audit logging disabled.
	auditLog *audit.Logger

	// ssrfProtection mirrors config.Runtime.SSRFProtection.
	ssrfProtection bool
	// allowPrivateHosts mirrors config.Runtime.AllowPrivateHosts.
	allowPrivateHosts []string
	// allowedToolDirs mirrors config.Runtime.AllowedToolDirs. When non-empty,
	// any python_file path that does not resolve under one of these prefixes is
	// rejected before the subprocess is forked. Empty = all paths permitted.
	allowedToolDirs []string
	// filesystemRoots is the canonical, symlink-resolved allowlist used by all
	// host filesystem builtins. Empty fails closed.
	filesystemRoots []string

	// pyExecutor is the optional pre-forked Python worker pool. When nil,
	// the engine falls back to the original exec-per-call subprocess path.
	// Set via SetExecutor after construction.
	pyExecutor executor.Backend

	// namedExecutors holds additional execution backends an agent can opt into
	// via its `execution.backend` (e.g. "local", "docker", "ssh"). Registered at
	// startup via SetNamedExecutor. An agent whose chosen backend is not present
	// falls back to pyExecutor.
	namedExecutors map[string]executor.Backend

	// resources is the optional session resource store used for typed media
	// attachments (E1 — Typed Media Attachments).  nil by default; set via
	// SetResourceStore before traffic starts.
	resources *session.ResourceStore

	// checkpoints persists workflow step state across restarts (E5 — Structured
	// Workflow Scaffolding). nil = checkpoint persistence disabled (workflow
	// steps still run but cannot resume after a crash). Set via
	// SetCheckpointStore before traffic starts.
	checkpoints *CheckpointStore

	// tracer is the optional telemetry tracer (Task #32). When nil, no spans
	// are emitted. Set via SetTracer after construction.
	tracer telemetryTracer

	// costStore is the optional per-agent token-cost store (Task #32). When
	// nil, usage is not persisted. Set via SetCostStore after construction.
	costStore agentCostStore

	// historyStore persists every user+assistant turn to a durable conversation
	// log (session history). nil = no persistence (legacy behaviour).
	// Set via SetHistoryStore after construction.
	historyStore session.HistoryStore

	// dlqStore pushes failed Handle() calls to a dead-letter queue so operators
	// can inspect, retry, or purge them via the admin API. nil = no DLQ.
	// Set via SetDLQStore after construction.
	dlqStore deadLetterStore

	// reasoningKeys carries cloud-provider API keys for reasoning loop
	// backends (Story 16). Set via SetReasoningKeys at boot.
	reasoningKeys reasoning.ProviderKeys
	// reasoningBackendFactory, when non-nil, overrides how the reasoning
	// LLM backend is built for an agent (tests / embedders). nil = derive
	// from def.LLM.Provider via reasoning.DefaultBackendFor.
	reasoningBackendFactory func(*agent.Definition) reasoning.LLMBackend

	// reasonerProvider/reasonerModel are the optional global llm.reasoner
	// override: when set, the reasoning loop runs on this provider/model for
	// every agent (planning wants a strong model), regardless of the agent's
	// own chat model. Empty = use the agent's llm.provider/model.
	reasonerProvider string
	reasonerModel    string

	// agentShellEnv holds extra KEY=VALUE entries appended to the environment of
	// shell_exec / run_script subprocesses, so agents can locate the canonical,
	// PERSISTENT install locations (SOULACY_WORKSPACE, SOULACY_CONFIG_FILE,
	// SOULACY_SKILLS_DIR, SOULACY_PLUGINS_DIR, SOULACY_MCP_DIR) instead of
	// guessing and writing to ephemeral paths. nil = inherit the process env
	// unchanged (keeps tests/behaviour identical when unset).
	agentShellEnv []string

	// ── PERF-1: session eviction ────────────────────────────────────────────
	// sessionTTL bounds how long an idle session is retained before the sweep
	// reclaims it. maxSessions caps the live session count. Both are set via
	// SetSessionEviction; zero values fall back to the documented defaults
	// (24h TTL, 10000 sessions). evictStop stops the sweeper goroutine.
	sessionTTL  time.Duration
	maxSessions int
	evictStop   chan struct{}
	evictOnce   sync.Once

	// ── PERF-2: history windowing ───────────────────────────────────────────
	// maxHistoryTurns caps the number of NON-system messages retained in a
	// session's in-memory History. Older turns are trimmed oldest-first; a
	// leading system message (index 0) is always preserved. Set via
	// SetMaxHistoryTurns; <=0 falls back to defaultMaxHistoryTurns.
	maxHistoryTurns int

	// maxTurnsCeiling is a hard server-side cap on any agent's effective
	// max_turns (Story 1 / S3.2). Set via SetMaxTurnsCeiling; <=0 falls back
	// to defaultMaxTurnsCeiling.
	maxTurnsCeiling int

	// maxAgentCallDepth bounds recursive peer-agent delegation chains. Set via
	// SetMaxAgentCallDepth; <=0 falls back to defaultMaxAgentCallDepth.
	maxAgentCallDepth int

	defaultRunBudget    agent.BudgetConfig
	maxRunBudget        agent.BudgetConfig
	runBudgetConfigured bool
	llmTimeout          time.Duration
	stepTimeout         time.Duration
	runTimeout          time.Duration
}

const (
	defaultSessionTTL        = 24 * time.Hour
	defaultMaxSessions       = 10000
	defaultMaxHistoryTurns   = 100
	defaultMaxTurnsCeiling   = 50
	defaultMaxAgentCallDepth = 5
	defaultRunBudgetTokens   = 100000
	defaultRunBudgetCalls    = 20
	defaultMaxBudgetTokens   = 1000000
	defaultMaxBudgetCalls    = 100
	defaultLLMTimeout        = 3 * time.Minute
	defaultStepTimeout       = 4 * time.Minute
	defaultRunTimeout        = 15 * time.Minute
)

// PluginToolProvider is satisfied by *plugins.Loader. Defined locally to
// avoid an import cycle (plugins → engine would be circular).
type PluginToolProvider interface {
	AllTools() []PluginTool
}

// PluginTool is a callable tool contributed by a Soulacy plugin.
type PluginTool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     string // "python:<path>::<function>"
}

// streamCallbackKey is the context key for the per-request token callback.
type streamCallbackKey struct{}

// sessionIDKey carries the current session ID so nested helpers (confirm,
// audit) can tag their records without threading it through every signature.
type inboundMsgKey struct{}

// WithStreamCallback returns a context that delivers streaming tokens to cb.
// The engine's Handle() checks for this callback and sets Stream: true on the
// CompletionRequest when it's present (and the agent has stream_reply: true).
func WithStreamCallback(ctx context.Context, cb func(string)) context.Context {
	return context.WithValue(ctx, streamCallbackKey{}, cb)
}

// streamCallback extracts the token callback from ctx, or returns nil.
func streamCallback(ctx context.Context) func(string) {
	if cb, ok := ctx.Value(streamCallbackKey{}).(func(string)); ok {
		return cb
	}
	return nil
}

// FailureNotifier is invoked by the engine after a run errors. The engine
// passes the agent definition (so the notifier can read def.NotifyOnFailure
// + def.Name etc.), the original inbound message (so the notifier can
// reply on the same channel when no explicit NotifyOnFailure is set), and
// the rendered error string the LLM/operator should see.
//
// Notifiers must not block — the engine calls this synchronously from the
// run's deferred outcome handler. Implementations should use chanReg.Send
// (which already non-blocks) plus a short timeout for any network work.
type FailureNotifier interface {
	NotifyFailure(ctx context.Context, def *agent.Definition, inbound message.Message, errMsg string)
}

// SetFailureNotifier wires the run-failure callback. Safe to call zero or
// one time before traffic starts.
func (e *Engine) SetFailureNotifier(fn FailureNotifier) {
	e.failureNotifier = fn
}

// SetSandbox installs Python-tool sandboxing. selfPath should be the
// absolute path to the running soulacy binary (os.Executable()). Limits
// should come from cfg.Runtime.Sandbox via sandbox.Limits{Enabled:…, …}.
// Safe to call zero or one time; a second call replaces the previous
// settings. Goroutine-safe to call before tools start running.
func (e *Engine) SetSandbox(selfPath string, limits sandbox.Limits) {
	e.selfPath = selfPath
	e.sandboxLimits = limits
}

// Session tracks per-conversation state.
type Session struct {
	ID        string
	AgentID   string
	History   []llm.ChatMessage
	CreatedAt time.Time
	mu        sync.Mutex

	// cachedPrefix is the rendered system_prompt+catalogs for this session.
	// Primed by Handle() once per inbound user message and reused across
	// every turn of the agent loop. Cleared between Handle() calls so a
	// hot-reloaded def picks up its new catalogs on the next inbound
	// message (vs. the next process restart).
	cachedPrefix string

	// PassphraseVerified tracks whether the user has provided the correct
	// passphrase for agents that have security.passphrase set. Enforced in
	// Go before the LLM is invoked — cannot be bypassed by prompt injection.
	PassphraseVerified bool

	// lastAccess is the wall-clock time of the most recent inbound message
	// for this session. The eviction sweep (PERF-1) compares this against
	// the configured TTL to decide whether an idle session can be reclaimed.
	// Guarded by mu.
	lastAccess time.Time

	// inUse counts the number of in-flight Handle() calls touching this
	// session. The eviction sweep NEVER drops a session with inUse > 0 — a
	// session that is mid-conversation is always retained. Guarded by mu.
	inUse int

	// injectionMax is the highest injection severity observed on any
	// tool result during this session (S2, Cohort F). The S3 intent
	// gate reads this via injectionState() when deciding whether to
	// allow / prompt / deny a privileged tool call — a High finding
	// on the last evidence source flips the gate from "log & allow"
	// to "require confirmation" unless policy already denies. Guarded
	// by mu.
	injectionMax injection.Severity
	// injectionLastSource is the tool name that produced the most
	// recent high-severity finding. Empty when no High finding has
	// been recorded. Guarded by mu.
	injectionLastSource string
	// lastEvidenceUntrusted flips true whenever a tool result carried
	// trust=untrusted, and back to false on the next trusted result.
	// Consumed by the S3 intent gate to decide whether a followup
	// privileged tool call is plausibly steered by untrusted content.
	// Guarded by mu.
	lastEvidenceUntrusted bool
	// userGoal is the most recent user-role text on this session,
	// annotation-stripped, for the S3 gate's goal-matching heuristic.
	// Set by Handle on inbound. Guarded by mu.
	userGoal string
}

// NewEngine creates a new agent execution engine.
// skillLoader, knowledgeSvc, vectorStore, and pluginProvider may be nil — if
// so, those capabilities are silently disabled.
func NewEngine(
	loader *Loader,
	router *llm.Router,
	mem memory.Store,
	archive storage.MemoryBackend,
	pythonBin string,
	toolTimeout time.Duration,
	log *zap.Logger,
	sink EventSink,
	skillLoader SkillLoader,
	ollamaAPIKey string,
	mcpClient *mcp.Client,
	knowledgeSvc *knowledge.Service,
	allowSystemAgents []string,
	vectorStore *memory.VectorStore,
	pluginProvider PluginToolProvider,
) *Engine {
	if sink == nil {
		sink = noopSink{}
	}
	e := &Engine{
		loader:            loader,
		llmRouter:         router,
		memory:            mem,
		archive:           archive,
		pythonBin:         pythonBin,
		toolTimeout:       toolTimeout,
		log:               log,
		sink:              sink,
		skillLoader:       skillLoader,
		ollamaAPIKey:      ollamaAPIKey,
		mcpClient:         mcpClient,
		knowledge:         knowledgeSvc,
		allowSystemAgents: allowSystemAgents,
		vectorStore:       vectorStore,
		pluginProvider:    pluginProvider,
		queueStore:        newAgentQueueStore(),
		privilegedRunner:  denyPrivilegedRunner{},
	}
	e.broker = newConfirmBroker()
	e.builtins = e.buildBuiltins()
	return e
}

// Broker returns the ConfirmBroker so the gateway can resolve pending
// tool-confirmation requests when the user approves or denies via the API.
func (e *Engine) Broker() *ConfirmBroker { return e.broker }

// SetAuditLog installs an audit logger. Safe to call before traffic starts.
func (e *Engine) SetAuditLog(l *audit.Logger) { e.auditLog = l }

// SetOllamaAPIKey refreshes the hosted Ollama API key used by web_search.
func (e *Engine) SetOllamaAPIKey(key string) {
	e.ollamaAPIKeyMu.Lock()
	defer e.ollamaAPIKeyMu.Unlock()
	e.ollamaAPIKey = strings.TrimSpace(key)
}

// SetIntentGateDefault installs the workspace-scoped default intent-gate mode
// (Cohort F-Bridge). Consulted by evaluateIntent only when the per-agent
// SecurityConfig.IntentGate is empty; when both are empty the intent package
// treats it as ModePrompt. Empty input clears the workspace default.
func (e *Engine) SetIntentGateDefault(mode string) {
	e.intentGateDefaultMu.Lock()
	defer e.intentGateDefaultMu.Unlock()
	e.intentGateDefault = strings.TrimSpace(mode)
}

// getIntentGateDefault returns the current workspace default mode string
// (empty when nothing is configured). Concurrency-safe.
func (e *Engine) getIntentGateDefault() string {
	e.intentGateDefaultMu.RLock()
	defer e.intentGateDefaultMu.RUnlock()
	return e.intentGateDefault
}

// ResolveIntentGate returns the effective intent-gate mode for an agent
// definition, preferring the per-agent value and falling back to the
// workspace default. Returns "" only when both are empty (intent.Evaluate
// then treats it as ModePrompt). Exposed so out-of-runtime consumers
// (Doctor / Studio preflight) can share the exact same resolution.
func (e *Engine) ResolveIntentGate(def *agent.Definition) string {
	perAgent := ""
	if def != nil && def.Security != nil {
		perAgent = strings.TrimSpace(def.Security.IntentGate)
	}
	if perAgent != "" {
		return perAgent
	}
	return e.getIntentGateDefault()
}

func (e *Engine) getOllamaAPIKey() string {
	e.ollamaAPIKeyMu.RLock()
	defer e.ollamaAPIKeyMu.RUnlock()
	return e.ollamaAPIKey
}

// SetSearchConfig configures the search provider and key for the built-in web_search tool.
func (e *Engine) SetSearchConfig(provider, apiKey string) {
	e.searchProviderMu.Lock()
	defer e.searchProviderMu.Unlock()
	e.searchProvider = strings.TrimSpace(provider)
	e.searchAPIKey = strings.TrimSpace(apiKey)
}

func (e *Engine) getSearchConfig() (string, string) {
	e.searchProviderMu.RLock()
	defer e.searchProviderMu.RUnlock()
	return e.searchProvider, e.searchAPIKey
}

// SetChannelRegistry wires the outbound channel registry used by channel.send.
func (e *Engine) SetChannelRegistry(reg *channels.Registry) {
	e.channelRegistry = reg
}

// SetChannelDefaultOutputs wires shared outbound destinations used by
// channel.send when an agent omits "to" or targets a logical base channel.
func (e *Engine) SetChannelDefaultOutputs(outputs map[string]agent.ScheduleOutput) {
	e.channelDefaultMu.Lock()
	defer e.channelDefaultMu.Unlock()
	e.channelDefaults = make(map[string]agent.ScheduleOutput, len(outputs))
	for k, v := range outputs {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		e.channelDefaults[k] = v
	}
}

// SetSSRF configures SSRF protection for the HTTP-fetching built-in tools.
func (e *Engine) SetSSRF(enabled bool, allowedHosts []string) {
	e.ssrfProtection = enabled
	e.allowPrivateHosts = allowedHosts
}

// SetAllowedToolDirs installs the python_file path allowlist. When dirs is
// non-empty, executePythonTool rejects any python_file that does not resolve
// under one of the listed directory prefixes.
func (e *Engine) SetAllowedToolDirs(dirs []string) {
	e.allowedToolDirs = dirs
}

// SetBrainMemory wires the three-layer agent memory store (MEM-03).
// When non-nil, agents with brain_memory.episodic.enabled=true will have their
// task history injected before each run (RL-10) and persisted after (RL-09).
func (e *Engine) SetBrainMemory(store *agentmemory.CompositeStore) {
	e.brainStore = store
}

// BrainStore returns the CompositeStore, or nil if not configured.
func (e *Engine) BrainStore() *agentmemory.CompositeStore { return e.brainStore }

// SetLearningStore wires the reviewable post-run learning proposal store.
func (e *Engine) SetLearningStore(store *learning.Store) { e.learningStore = store }

// SetAdaptiveNodes sets the global default for runtime LLM salvage of flow nodes
// that hit an unexpected data shape (FlowNode.Adaptive forces it per-node too).
func (e *Engine) SetAdaptiveNodes(on bool) { e.adaptiveNodes = on }

// LearningStore returns the proposal store, or nil when learning is disabled.
func (e *Engine) LearningStore() *learning.Store { return e.learningStore }

// SetActionLogBackend wires recent run history for safe session_search.
func (e *Engine) SetActionLogBackend(store storage.ActionLogBackend) {
	e.actionLog = store
	e.builtins = e.buildBuiltins()
}

// SetExecutor installs an executor.Backend for Python tool dispatch.
// When set to a pool backend, Python tool cold-start latency is eliminated by
// reusing pre-forked interpreter processes. When nil (or never called), the
// engine uses the original per-call subprocess path.
// Safe to call once at startup before any traffic.
func (e *Engine) SetExecutor(ex executor.Backend) { e.pyExecutor = ex }

// SetNamedExecutor registers an execution backend under a name that agents can
// select via `execution.backend` in SOUL.yaml. Call once per backend at startup.
func (e *Engine) SetNamedExecutor(name string, ex executor.Backend) {
	if name == "" || ex == nil {
		return
	}
	if e.namedExecutors == nil {
		e.namedExecutors = map[string]executor.Backend{}
	}
	e.namedExecutors[strings.ToLower(strings.TrimSpace(name))] = ex
}

// selectedNamedBackend returns a registered execution backend ONLY when the
// agent explicitly selected one by name (and it maps to a non-"local" registered
// backend). It returns nil for the default/unset case and for "local"/"process"
// so those keep the local sandboxed subprocess path. This is the routing hook
// for per-agent docker/ssh tool execution.
func (e *Engine) selectedNamedBackend(def *agent.Definition) executor.Backend {
	if def == nil {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(def.Execution.Backend))
	if name == "" || name == "local" || name == "process" {
		return nil
	}
	if ex, ok := e.namedExecutors[name]; ok && ex != nil {
		return ex
	}
	return nil
}

// progressPublisher is satisfied by *gateway.EventHub (and any test double).
// Defined locally to avoid importing gateway from runtime.
type progressPublisher interface {
	PublishProgress(ev message.ProgressEvent)
}

// progressExecutor is the subset of executor.Backend that supports an
// OnProgress callback. Implemented by both process.Executor and pool.Pool.
type progressExecutor interface {
	SetOnProgress(fn func(message.ProgressEvent))
}

// SetProgressHub wires progress events from the active executor through to hub.
// When called, if the configured executor supports SetOnProgress, each parsed
// progress event from tool stdout will be forwarded to hub.PublishProgress.
// Safe to call after SetExecutor, before traffic starts.
func (e *Engine) SetProgressHub(hub progressPublisher) {
	pe, ok := e.pyExecutor.(progressExecutor)
	if !ok || hub == nil {
		return
	}
	pe.SetOnProgress(func(ev message.ProgressEvent) {
		hub.PublishProgress(ev)
	})
}

// SetResourceStore wires the session resource store used for typed media
// attachments (E1 — Typed Media Attachments).  Safe to call once at startup
// before any traffic.
func (e *Engine) SetResourceStore(s session.ResourceStore) { e.resources = &s }

// SetCheckpointStore wires the workflow checkpoint store (E5 — Structured
// Workflow Scaffolding). Safe to call once at startup before any traffic.
func (e *Engine) SetCheckpointStore(s *CheckpointStore) { e.checkpoints = s }

// ---------------------------------------------------------------------------
// Task #32 — telemetry + cost tracking interfaces and setters
// ---------------------------------------------------------------------------

// telemetryTracer is a minimal tracing interface satisfied by any tracer
// whose Start method takes a context, a span name, and optional string
// key/value pairs (as flat ...string variadics: key, value, key, value …).
// Defined locally so the runtime package does not import the telemetry
// package and create a cycle.
type telemetryTracer interface {
	// Start begins a span named spanName. kv is an optional flat list of
	// string key/value attribute pairs (key0, val0, key1, val1, …).
	// Returns the child context and a span whose End() must be deferred.
	Start(ctx context.Context, spanName string, kv ...string) (context.Context, interface{ End() })
}

// agentCostStore is a minimal interface satisfied by *costs.Store via
// engineCostStoreAdapter (defined in main.go). Defined locally so the runtime
// package does not import the costs package.
type agentCostStore interface {
	Record(ctx context.Context, agentID, sessionID, provider, model string,
		promptTokens, compTokens, totalTokens int, costUSD float64) error
}

// deadLetterStore is a minimal interface satisfied by *dlq.SQLiteStore via
// engineDLQAdapter (defined in main.go). Defined locally to avoid importing
// the dlq package and creating a cycle.
type deadLetterStore interface {
	// PushFailed records a failed message delivery in the dead-letter queue.
	PushFailed(ctx context.Context, queue string, payload []byte, errMsg string) error
}

// SetTracer installs a telemetry tracer. Safe to call once at startup.
func (e *Engine) SetTracer(t telemetryTracer) { e.tracer = t }

// SetCostStore installs a cost store. Safe to call once at startup.
func (e *Engine) SetCostStore(s agentCostStore) { e.costStore = s }

// SetHistoryStore installs a conversation history store. Safe to call once at startup.
func (e *Engine) SetHistoryStore(s session.HistoryStore) { e.historyStore = s }

// SeedSessionHistory initialises the in-memory history of (agentID,
// sessionID) from persisted conversation entries — used when forking a chat
// session (Story 8) so the branch's copied turns become real LLM context on
// the next Handle. A no-op when the in-memory session already has history:
// live conversations are never clobbered.
func (e *Engine) SeedSessionHistory(agentID, sessionID string, entries []session.ConversationEntry) {
	if len(entries) == 0 {
		return
	}
	sess := e.getOrCreateSession(sessionID, agentID)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.History) > 0 {
		return
	}
	history := make([]llm.ChatMessage, 0, len(entries))
	for _, en := range entries {
		role := en.Role
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		history = append(history, llm.ChatMessage{Role: role, Content: en.Content})
	}
	sess.History = history
}

// SetDLQStore installs a dead-letter queue store. Safe to call once at startup.
func (e *Engine) SetDLQStore(s deadLetterStore) { e.dlqStore = s }

// recordUsage writes a token-usage record to the cost store, if one is wired.
// Silently no-ops when costStore is nil or any argument is zero.
func (e *Engine) recordUsage(ctx context.Context, agentID, sessionID, provider, model string, promptTokens, compTokens int) {
	if e.costStore == nil || (promptTokens == 0 && compTokens == 0) {
		return
	}
	total := promptTokens + compTokens
	if err := e.costStore.Record(ctx, agentID, sessionID, provider, model,
		promptTokens, compTokens, total, 0); err != nil {
		e.log.Warn("cost store record failed", zap.Error(err))
	}
}

// RunTool invokes a named tool with raw JSON arguments and returns raw JSON output.
// Used by WorkflowExecutor to call individual tools without a full LLM round-trip.
func (e *Engine) RunTool(ctx context.Context, toolName string, argsJSON string) (json.RawMessage, error) {
	// Look up the tool by name in the engine's built-in registry.
	for _, bt := range e.builtins {
		if bt.Name == toolName {
			// Decode argsJSON into map[string]any for the handler.
			args := map[string]any{}
			if argsJSON != "" {
				if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
					return nil, fmt.Errorf("RunTool: decode args for %q: %w", toolName, err)
				}
			}
			result, err := bt.Handler(ctx, args)
			if err != nil {
				return nil, err
			}
			// Wrap plain string results as a JSON string so callers always
			// receive valid json.RawMessage.
			raw, merr := json.Marshal(result)
			if merr != nil {
				return nil, fmt.Errorf("RunTool: marshal result for %q: %w", toolName, merr)
			}
			return json.RawMessage(raw), nil
		}
	}

	// MCP tools (mcp__<server>__<tool>) are not in the builtin registry — route
	// them to the MCP client, mirroring the agent-loop runTool path. Without this,
	// callers that use the PUBLIC RunTool (notably the Studio "Build until it works"
	// verifier, via studioRealRunner) would get "tool not found" for every MCP tool
	// and could never verify an MCP-based flow. Honors the per-node timeout override
	// carried on ctx and returns the same actionable deadline message.
	if e.mcpClient != nil && strings.HasPrefix(toolName, mcp.FullNamePrefix) {
		args := map[string]any{}
		if strings.TrimSpace(argsJSON) != "" {
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return nil, fmt.Errorf("RunTool: decode args for %q: %w", toolName, err)
			}
		}
		to := e.effectiveToolTimeout(ctx)
		tctx, cancel := context.WithTimeout(ctx, to)
		defer cancel()
		out, callErr := e.mcpClient.Call(tctx, toolName, args)
		if callErr != nil {
			return nil, toolTimeoutError(toolName, to, ctx.Err(), callErr)
		}
		trimmed := strings.TrimSpace(out)
		if json.Valid([]byte(trimmed)) {
			return json.RawMessage(trimmed), nil
		}
		wrapped, merr := json.Marshal(out)
		if merr != nil {
			return nil, fmt.Errorf("RunTool: marshal MCP result for %q: %w", toolName, merr)
		}
		return json.RawMessage(wrapped), nil
	}

	return nil, fmt.Errorf("tool %q not found", toolName)
}

// RunInlinePython executes a Studio "Custom Python" node's inline code in the
// sandboxed Python executor (process-per-call; env filtered to the allowlist).
// argsJSON is delivered on stdin and passed to the node's run(inputs) function;
// the printed value is captured and returned as JSON (a non-JSON string is
// wrapped as a JSON string so downstream flow vars stay well-typed). Returns an
// error when no Python executor is configured.
//
// SECURITY: this runs LLM/user-authored code. The flow runner must only reach
// here for a python node whose per-case consent has been granted (see the
// consent model in internal/studio/plan.go and docs/STUDIO_PYTHON_TOOLS.md §13);
// RunInlinePython itself is the mechanism, not the gate.
func (e *Engine) RunInlinePython(ctx context.Context, code string, argsJSON []byte) (json.RawMessage, error) {
	if e.pyExecutor == nil {
		return nil, fmt.Errorf("RunInlinePython: no python executor configured")
	}
	if len(argsJSON) == 0 {
		argsJSON = []byte("{}")
	}
	// Respect an explicit per-node timeout override when the flow node set one
	// (e.g. a long data-transform block). Only applied when present, so inline
	// python with no declared timeout keeps its prior behavior (bounded only by
	// the run context).
	if d, ok := toolTimeoutOverride(ctx); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	out, err := e.pyExecutor.Run(ctx, "", "run", code, argsJSON)
	if err != nil {
		return nil, fmt.Errorf("RunInlinePython: %w", err)
	}
	trimmed := []byte(strings.TrimSpace(out))
	if json.Valid(trimmed) {
		return json.RawMessage(trimmed), nil
	}
	wrapped, merr := json.Marshal(out)
	if merr != nil {
		return nil, fmt.Errorf("RunInlinePython: encode output: %w", merr)
	}
	return json.RawMessage(wrapped), nil
}

// maybeConfirm checks whether call.Name is in def.ConfirmTools and, if so,
// emits a tool_confirm SSE event and blocks until the user approves or denies.
// Returns nil if confirmation is not required or if the tool is approved.
// Returns an error (wrapping "denied") if the user rejected the call.
func (e *Engine) maybeConfirm(ctx context.Context, def *agent.Definition, call message.ToolCall) error {
	if len(def.ConfirmTools) == 0 {
		return nil
	}
	required := false
	for _, t := range def.ConfirmTools {
		if t == "*" || t == "all" || t == call.Name {
			required = true
			break
		}
	}
	if !required {
		return nil
	}

	sender, ok := confirmSenderFrom(ctx)
	if !ok {
		// No confirm channel in this context (e.g. a scheduled or non-streaming
		// run). Unattended is the explicit operator opt-in for hands-off
		// execution; otherwise fail closed so confirm_tools means the same thing
		// across GUI chat, HTTP, cron, and channel-triggered runs.
		if def != nil && def.Unattended {
			e.log.Warn("tool confirmation required, no confirm channel — auto-approved (unattended agent)",
				zap.String("agent", def.ID), zap.String("tool", call.Name))
			e.logAudit(ctx, def, call, "auto-approved (unattended)", time.Now(), false, nil)
			return nil
		}
		e.log.Warn("tool confirmation required but no confirm channel in context — denying",
			zap.String("tool", call.Name))
		return fmt.Errorf("tool %q requires confirmation, but no GUI confirmation channel is available", call.Name)
	}

	callID := uuid.New().String()
	resultCh := sender(ConfirmRequest{
		CallID: callID,
		Tool:   call.Name,
		Args:   call.Arguments,
	})
	// Resolve deletes the pending entry; a run that ends without an answer must
	// clean up after itself, or the approval sits in the broker forever.
	defer e.Broker().Forget(callID)

	select {
	case approved := <-resultCh:
		if !approved {
			e.logAudit(ctx, def, call, "", time.Now(), true, nil)
			return fmt.Errorf("tool %q was denied by the user", call.Name)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) dynamicConfirm(ctx context.Context, def *agent.Definition, call message.ToolCall, reason string) error {
	sender, ok := confirmSenderFrom(ctx)
	if !ok {
		// No confirm channel in this context (e.g. a scheduled run). An agent
		// explicitly marked Unattended opts into auto-approving these guardrail
		// confirmations so it can COMPLETE without a human present; otherwise we
		// DENY for safety. The auto-approval is recorded in the audit log.
		if def != nil && def.Unattended {
			e.log.Warn("guardrail required confirmation, no confirm channel — auto-approved (unattended agent)",
				zap.String("agent", def.ID), zap.String("tool", call.Name), zap.String("reason", reason))
			e.logAudit(ctx, def, call, "auto-approved (unattended)", time.Now(), false, nil)
			return nil
		}
		e.log.Warn("guardrail required confirmation but no confirm channel in context — denying",
			zap.String("tool", call.Name))
		return fmt.Errorf("guardrail required confirmation, but no GUI available to confirm: %s", reason)
	}

	callID := uuid.New().String()
	resultCh := sender(ConfirmRequest{
		CallID: callID,
		Tool:   call.Name,
		Args:   call.Arguments,
		Reason: reason,
	})
	defer e.Broker().Forget(callID)

	select {
	case approved := <-resultCh:
		if !approved {
			e.logAudit(ctx, def, call, "", time.Now(), true, nil)
			return fmt.Errorf("tool %q was denied by the user (guardrail flag: %s)", call.Name, reason)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isSideEffectingTool(name string) bool {
	if toolSecurityClasses[name].SideEffecting {
		return true
	}
	return strings.HasPrefix(name, "mcp__") || strings.HasPrefix(name, "plugin__")
}

// dryRunResult returns a human- and model-readable simulated result describing
// what the tool would have done, so the reasoning loop can continue.
func dryRunResult(call message.ToolCall) string {
	argsJSON, _ := json.Marshal(call.Arguments)
	return fmt.Sprintf("[DRY RUN] Skipped %q — no action taken. Would have executed with args: %s", call.Name, string(argsJSON))
}

// policyConfigFor adapts the agent's declarative policy block into the pure
// policy.Config the evaluator consumes.
func policyConfigFor(def *agent.Definition) policy.Config {
	return policy.Config{
		Enabled:      def.Policy.Enabled,
		Shell:        def.Policy.Shell,
		File:         def.Policy.File,
		Network:      def.Policy.Network,
		AllowDomains: def.Policy.AllowDomains,
		DenyDomains:  def.Policy.DenyDomains,
		DenyPaths:    def.Policy.DenyPaths,
	}
}

// logAudit records a tool call to the audit logger (no-op when auditLog is nil).
func (e *Engine) logAudit(ctx context.Context, def *agent.Definition, call message.ToolCall, result string, start time.Time, denied bool, err error) {
	if e.auditLog == nil {
		return
	}
	sessionID := ""
	if msg, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
		sessionID = msg.SessionID
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	e.auditLog.Log(audit.Entry{
		Timestamp:  start.UTC(),
		SessionID:  sessionID,
		AgentID:    def.ID,
		Tool:       call.Name,
		Args:       call.Arguments,
		ResultLen:  len(result),
		DurationMS: time.Since(start).Milliseconds(),
		Denied:     denied,
		Error:      errStr,
	})
}

// Knowledge returns the engine's RAG service (may be nil).
func (e *Engine) Knowledge() *knowledge.Service { return e.knowledge }

// Builtins returns a copy of the Go-native built-in tools (web_search,
// read_skill, …) plus the system tools (shell_exec, run_script, …).
// Used by the gateway's /tool-catalog endpoint to advertise what's available
// to the Builder and the Agents Edit UI.
// System tools are listed in the catalog regardless of channel; they are only
// actually offered at runtime when the three-way guard in allToolSchemas passes.
func (e *Engine) Builtins() []BuiltinTool {
	out := make([]BuiltinTool, len(e.builtins))
	copy(out, e.builtins)
	// SAFE (read-only) OS-level built-ins are always advertised so the GUI
	// Builder can display them — they are available to every http-channel
	// agent regardless of capability (SEC-3).
	out = append(out, e.safeSystemTools()...)
	// The constrained URL package installer is always advertised for the
	// built-in System agent. Unlike arbitrary system tools, it remains usable
	// when allow_system_agents is empty because it has a fixed command shape
	// and obtains explicit approval for every installation.
	if len(e.allowSystemAgents) == 0 {
		for _, b := range e.buildSystemTools() {
			if b.Name == "package_install" {
				out = append(out, b)
				break
			}
		}
	}
	// The privileged SYSTEM partition (shell_exec, run_script, …) is only
	// advertised when the server permits system tools at all. Whether a GIVEN
	// agent may actually call them additionally requires the "system"
	// capability — enforced per-agent in systemToolsFor at dispatch time.
	if len(e.allowSystemAgents) > 0 {
		all := e.buildSystemTools()
		for _, b := range all {
			if isPrivilegedSystemTool(b.Name) {
				out = append(out, b)
			}
		}
	}
	return out
}

// buildBuiltins constructs the set of Go-native built-in tools available to
// every agent. Currently includes:
//   - read_skill: load the full instructions for an Agent Skill by name
//   - read_skill_file: read a resource file (script/reference/asset) from a skill
