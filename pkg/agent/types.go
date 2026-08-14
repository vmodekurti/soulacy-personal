// Package agent defines the agent definition format (SOUL.yaml) and related types.
// An agent is the atomic unit of intelligence in Soulacy — it binds a system prompt,
// a set of tools, memory access rules, channel bindings, and LLM configuration together
// into a single deployable entity.
package agent

import (
	"time"

	"github.com/soulacy/soulacy/sdk/reasoning"
)

// TriggerKind describes how an agent is activated.
type TriggerKind string

const (
	TriggerChannel  TriggerKind = "channel"  // activated by an inbound channel message
	TriggerCron     TriggerKind = "cron"     // activated on a cron schedule
	TriggerOneShot  TriggerKind = "oneshot"  // activated once at a specific time
	TriggerWebhook  TriggerKind = "webhook"  // activated by an HTTP POST to its endpoint
	TriggerInternal TriggerKind = "internal" // activated programmatically by another agent
)

// MemoryPolicy controls how the agent reads and writes memory.
type MemoryPolicy struct {
	ReadScopes  []string `yaml:"read_scopes"  json:"read_scopes"`
	WriteScopes []string `yaml:"write_scopes" json:"write_scopes"`
	MaxTokens   int      `yaml:"max_tokens"   json:"max_tokens"`
}

// ReasoningConfig configures the multi-step reasoning loop for an agent (CFG-01).
// When Strategy is empty, the agent uses the classic single-call behaviour.
type ReasoningConfig struct {
	// Strategy is "react" or "plan_execute". Empty = loop disabled.
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	// Backend is automatically derived from llm.provider — no need to set this.
	// The reasoning loop uses the same provider as the agent's LLM config so there
	// is exactly one place to configure the model. Supported providers:
	//   ollama    — local Ollama, uses llm.model for Think, qwen2.5:72b for Plan/Reflect
	//   anthropic — Claude API, uses llm.model (default claude-sonnet-4-6)
	//   openai    — OpenAI or any OpenAI-compatible endpoint (Groq, Together, vLLM)
	// Any unsupported provider falls back to Ollama.
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`
	// MaxSteps is the hard ceiling for ReAct iterations (default 8).
	MaxSteps int `yaml:"max_steps,omitempty" json:"max_steps,omitempty"`
	// MaxPlanSteps caps plan decomposition depth for plan_execute (default 6).
	MaxPlanSteps int `yaml:"max_plan_steps,omitempty" json:"max_plan_steps,omitempty"`
	// MaxParallelSteps caps how many planned steps plan_execute runs at once
	// within a dependency level (default 4). Independent steps — three
	// site-scoped searches, say — otherwise execute one at a time for no
	// reason. Set to 1 to force the strictly-serial walk, which is the escape
	// hatch for a custom tool executor that is not concurrency-safe.
	MaxParallelSteps int `yaml:"max_parallel_steps,omitempty" json:"max_parallel_steps,omitempty"`
	// StepTimeout is the per-step context deadline (e.g. "30s").
	StepTimeout string `yaml:"step_timeout,omitempty" json:"step_timeout,omitempty"`
	// TotalTimeout is the whole-task deadline (e.g. "180s").
	TotalTimeout string `yaml:"total_timeout,omitempty" json:"total_timeout,omitempty"`
	// Optional per-phase LLM tuning. Leave empty for Soulacy's reliable defaults.
	Think   ReasoningPhaseConfig `yaml:"think,omitempty" json:"think,omitempty"`
	Plan    ReasoningPhaseConfig `yaml:"plan,omitempty" json:"plan,omitempty"`
	Reflect ReasoningPhaseConfig `yaml:"reflect,omitempty" json:"reflect,omitempty"`
	// Contract is the operator-authored agent contract that Studio's Build step
	// edits: what a successful run achieves, how the agent should behave, how it
	// knows it is done, and the loop policy for the chosen strategy. It lives on
	// the definition so it SURVIVES a save and a SOUL.yaml round-trip — without a
	// home here, everything typed into that panel was discarded.
	Contract *ReasoningContract `yaml:"contract,omitempty" json:"contract,omitempty"`
}

// ReasoningContract is what a reasoning agent is FOR, in the operator's words.
//
// Booleans are pointers throughout. These policies default to TRUE, so a plain
// `bool` with `omitempty` would erase the difference between "the operator
// turned this off" and "the operator never said" — and turning a safety flag
// off would silently revert to on at the next round-trip.
type ReasoningContract struct {
	// Goal is the single sentence describing what a successful run achieves.
	Goal string `yaml:"goal,omitempty" json:"goal,omitempty"`
	// Instructions is the operating guidance handed to the model.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	// CompletionCriteria is the explicit, checkable statement of done. Without
	// it a reasoning loop's only stop condition is its step budget.
	CompletionCriteria string `yaml:"completion_criteria,omitempty" json:"completion_criteria,omitempty"`
	// ToolChoice is "auto" | "required" | "none". Empty = auto.
	ToolChoice string `yaml:"tool_choice,omitempty" json:"tool_choice,omitempty"`
	// RecoveryRetries bounds retries of a failed step. Zero = default.
	RecoveryRetries int `yaml:"recovery_retries,omitempty" json:"recovery_retries,omitempty"`
	// ReAct applies when Strategy == "react".
	ReAct *ReasoningReActPolicy `yaml:"react,omitempty" json:"react,omitempty"`
	// Plan applies when Strategy == "plan_execute".
	Plan *ReasoningPlanPolicy `yaml:"plan,omitempty" json:"plan,omitempty"`
}

// ReasoningReActPolicy bounds the observe→act loop.
type ReasoningReActPolicy struct {
	Objective           string  `yaml:"objective,omitempty" json:"objective,omitempty"`
	ObserveActContract  string  `yaml:"observe_act_contract,omitempty" json:"observe_act_contract,omitempty"`
	StopConditions      string  `yaml:"stop_conditions,omitempty" json:"stop_conditions,omitempty"`
	RecoveryBehavior    string  `yaml:"recovery_behavior,omitempty" json:"recovery_behavior,omitempty"`
	InvalidStepBudget   int     `yaml:"invalid_step_budget,omitempty" json:"invalid_step_budget,omitempty"`
	RepeatedToolLimit   int     `yaml:"repeated_tool_limit,omitempty" json:"repeated_tool_limit,omitempty"`
	ConfidenceThreshold float64 `yaml:"confidence_threshold,omitempty" json:"confidence_threshold,omitempty"`
	PreserveBestResult  *bool   `yaml:"preserve_best_result,omitempty" json:"preserve_best_result,omitempty"`
	FallbackToAuto      *bool   `yaml:"fallback_to_auto,omitempty" json:"fallback_to_auto,omitempty"`
}

// ReasoningPlanPolicy governs the plan→execute split.
type ReasoningPlanPolicy struct {
	Steps                     []ReasoningPlanStep `yaml:"steps,omitempty" json:"steps,omitempty"`
	ReplanAfterFailure        *bool               `yaml:"replan_after_failure,omitempty" json:"replan_after_failure,omitempty"`
	ParallelIndependentSteps  *bool               `yaml:"parallel_independent_steps,omitempty" json:"parallel_independent_steps,omitempty"`
	ApprovalBeforeSideEffects *bool               `yaml:"approval_before_side_effects,omitempty" json:"approval_before_side_effects,omitempty"`
	PlanTimeout               string              `yaml:"plan_timeout,omitempty" json:"plan_timeout,omitempty"`
}

// ReasoningPlanStep is one declared phase of a Plan-Execute run.
type ReasoningPlanStep struct {
	Title          string   `yaml:"title" json:"title"`
	Status         string   `yaml:"status,omitempty" json:"status,omitempty"`
	AllowedTools   []string `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
	ExpectedOutput string   `yaml:"expected_output,omitempty" json:"expected_output,omitempty"`
	Verification   string   `yaml:"verification,omitempty" json:"verification,omitempty"`
	DependsOn      []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}

// ReasoningPhaseConfig tunes an internal reasoning LLM phase.
type ReasoningPhaseConfig struct {
	Temperature    float64 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	TopP           float64 `yaml:"top_p,omitempty" json:"top_p,omitempty"`
	MaxTokens      int     `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	ResponseFormat string  `yaml:"response_format,omitempty" json:"response_format,omitempty"`
}

// BrainMemoryConfig controls long-term agent memory behaviour (CFG-02).
type BrainMemoryConfig struct {
	Episodic   EpisodicMemoryConfig   `yaml:"episodic,omitempty"   json:"episodic,omitempty"`
	Semantic   SemanticMemoryConfig   `yaml:"semantic,omitempty"   json:"semantic,omitempty"`
	Procedural ProceduralMemoryConfig `yaml:"procedural,omitempty" json:"procedural,omitempty"`
}

// LearningConfig controls the post-run learning loop. When enabled, successful
// runs create reviewable proposals instead of silently changing memory/rules.
type LearningConfig struct {
	Enabled      bool `yaml:"enabled,omitempty"       json:"enabled,omitempty"`
	AutoPropose  bool `yaml:"auto_propose,omitempty"  json:"auto_propose,omitempty"`
	MinChars     int  `yaml:"min_chars,omitempty"     json:"min_chars,omitempty"`
	MaxProposals int  `yaml:"max_proposals,omitempty" json:"max_proposals,omitempty"`
}

// ToolPolicyConfig gates high-risk tool actions (shell, filesystem, network)
// with allow/prompt/deny decisions. When Enabled is false the agent behaves
// exactly as before. Values for Shell/File/Network are "allow", "prompt", or
// "deny"; empty falls back to a safe default (prompt for shell/file, allow for
// network subject to the domain lists). Enforced in the runtime before any tool
// handler runs, on top of the existing per-path guardrail and confirm gates.
type ToolPolicyConfig struct {
	Enabled bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Shell   string `yaml:"shell,omitempty"   json:"shell,omitempty"`
	File    string `yaml:"file,omitempty"    json:"file,omitempty"`
	Network string `yaml:"network,omitempty" json:"network,omitempty"`

	AllowDomains []string `yaml:"allow_domains,omitempty" json:"allow_domains,omitempty"`
	DenyDomains  []string `yaml:"deny_domains,omitempty"  json:"deny_domains,omitempty"`
	DenyPaths    []string `yaml:"deny_paths,omitempty"    json:"deny_paths,omitempty"`
}

// ExecutionConfig selects the backend that runs this agent's Python tool code.
// Empty Backend uses the server default. "local", "docker", and "ssh" require
// the corresponding backend to be configured at server startup; an unknown or
// unconfigured value falls back to the default with a warning.
type ExecutionConfig struct {
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`
}

// EpisodicMemoryConfig controls episodic task history injection.
type EpisodicMemoryConfig struct {
	Enabled   bool `yaml:"enabled,omitempty"    json:"enabled,omitempty"`
	MaxInject int  `yaml:"max_inject,omitempty" json:"max_inject,omitempty"`
}

// SemanticMemoryConfig controls semantic knowledge chunk injection.
type SemanticMemoryConfig struct {
	Enabled   bool `yaml:"enabled,omitempty"    json:"enabled,omitempty"`
	MaxInject int  `yaml:"max_inject,omitempty" json:"max_inject,omitempty"`
}

// ProceduralMemoryConfig controls procedural rule injection and auto-update.
type ProceduralMemoryConfig struct {
	Enabled    bool `yaml:"enabled,omitempty"     json:"enabled,omitempty"`
	AutoUpdate bool `yaml:"auto_update,omitempty" json:"auto_update,omitempty"`
}

// LLMConfig specifies which model and parameters to use.
type LLMConfig struct {
	Provider         string  `yaml:"provider"           json:"provider"`
	Model            string  `yaml:"model"              json:"model"`
	Temperature      float64 `yaml:"temperature"        json:"temperature"`
	TopP             float64 `yaml:"top_p,omitempty"   json:"top_p,omitempty"`
	MaxTokens        int     `yaml:"max_tokens"         json:"max_tokens"`
	BaseURL          string  `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	ResponseFormat   string  `yaml:"response_format,omitempty"   json:"response_format,omitempty"`
	ReasoningEffort  string  `yaml:"reasoning_effort,omitempty"  json:"reasoning_effort,omitempty"`
	PresencePenalty  float64 `yaml:"presence_penalty,omitempty"  json:"presence_penalty,omitempty"`
	FrequencyPenalty float64 `yaml:"frequency_penalty,omitempty" json:"frequency_penalty,omitempty"`

	// OutputSchema, when set, constrains the final reply to valid JSON matching
	// the supplied JSON Schema. Translated per provider:
	//   • OpenAI  → response_format json_schema (strict)
	//   • Gemini  → generationConfig.responseSchema + responseMimeType
	//   • Anthropic → forced tool_use with the schema as the tool's input_schema
	//   • Ollama  → format: <schema> (Ollama ≥ 0.5)
	// The engine also re-prompts once on failure if the reply doesn't parse.
	OutputSchema map[string]any `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`

	// ToolChoice, when non-empty, constrains how the model picks tools on the
	// FIRST turn only (subsequent turns always use "auto" so the model can
	// synthesize the final answer once tools have produced their results).
	// Accepted values mirror OpenAI / Ollama tool_choice semantics:
	//
	//   ""               (default) — model decides freely on every turn.
	//   "auto"           — equivalent to default.
	//   "none"           — model MUST NOT call any tool on turn 1.
	//   "required"       — model MUST call at least one tool on turn 1.
	//   "<tool_name>"    — model MUST call this specific tool on turn 1.
	//                       Pass the full tool name, e.g. "agent__researcher".
	//
	// Use this when a workflow REQUIRES delegation — e.g. a writer agent that
	// should always consult a researcher peer before composing. Without this,
	// local models often skip the tool call and answer from training data.
	ToolChoice string `yaml:"tool_choice,omitempty" json:"tool_choice,omitempty"`

	// AllowedProviders is an opt-in guard against the "I clicked the wrong
	// model in the GUI dropdown and burned billed credit" failure mode.
	// When set, the engine refuses to start a run whose `Provider` is not
	// on this list, returning a clear actionable error instead of dialling
	// out to a paid API the agent was never meant to use.
	//
	// Semantics:
	//   nil / absent — current behavior; any configured provider is allowed.
	//   [name, …]    — provider MUST be one of these (case-sensitive,
	//                  matched against LLMConfig.Provider). An empty []
	//                  also means "no providers allowed" — bricks the
	//                  agent intentionally, useful for a disabled-state
	//                  shim agent.
	//
	// Recommended for any cron-triggered agent that calls a self-hosted
	// model (ollama / lm-studio): set `allowed_providers: [ollama]` and
	// the agent can never accidentally hit anthropic / openai / gemini,
	// no matter how the GUI dropdown gets fat-fingered.
	AllowedProviders []string `yaml:"allowed_providers,omitempty" json:"allowed_providers,omitempty"`
	// AllowedModels optionally pins this agent to an explicit set of model IDs.
	// It is enforced after Studio/playground overrides are applied.
	AllowedModels []string `yaml:"allowed_models,omitempty" json:"allowed_models,omitempty"`
	// DataClassification is matched against the selected provider's
	// allowed_data_classes policy before any prompt leaves the process.
	DataClassification string `yaml:"data_classification,omitempty" json:"data_classification,omitempty"`
}

// SecurityConfig holds access-control settings for an agent.
// All checks are enforced in Go before the LLM is invoked — they cannot be
// overridden by prompt injection or model behavior.
type SecurityConfig struct {
	// Passphrase, when non-empty, requires every new session to present this
	// exact string before the agent will answer any message. The engine tracks
	// verified sessions in memory; once verified the check is not repeated for
	// the remainder of that session. Comparison is case-sensitive.
	Passphrase string `yaml:"passphrase,omitempty" json:"passphrase,omitempty"`

	// PassphrasePrompt is the message shown to unverified users.
	// Defaults to "🔒 Please provide your access passphrase to continue."
	PassphrasePrompt string `yaml:"passphrase_prompt,omitempty" json:"passphrase_prompt,omitempty"`

	// IntentGate is the S3 (Cohort F) tool-call intent enforcement
	// mode. Empty falls back to "prompt" — the safe default. Values:
	//
	//   - "off"    : bypass the gate (advanced operators only)
	//   - "prompt" : (default) surface a confirmation prompt when
	//                a privileged tool call is not justified by the
	//                user's original goal AND the last untrusted
	//                evidence source carried a High-severity injection
	//                pattern
	//   - "deny"   : hard-deny the same class of call, and prompt on
	//                Medium injection findings under any untrusted
	//                evidence
	//
	// See internal/intent/intent.go for the full decision matrix.
	IntentGate string `yaml:"intent_gate,omitempty" json:"intent_gate,omitempty"`
}

// ToolDef describes a tool the agent can invoke.
type ToolDef struct {
	Name        string         `yaml:"name"                   json:"name"`
	Description string         `yaml:"description"            json:"description"`
	PythonFile  string         `yaml:"python_file,omitempty"  json:"python_file,omitempty"`
	Inline      string         `yaml:"inline,omitempty"       json:"inline,omitempty"`
	Parameters  map[string]any `yaml:"parameters"             json:"parameters,omitempty"`

	// Timeout overrides the engine's global runtime.tool_timeout for this one
	// tool. Use Go duration syntax: "30s", "5m", "30m", "1h". Empty = use the
	// global default. Useful for tools that legitimately block for minutes
	// (e.g. NotebookLM audio generation, large data exports).
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	// Retries re-runs this Python tool after a failure. Default 0 preserves the
	// historical single-attempt behavior. Keep this for idempotent/transient
	// tools such as fetchers, parsers, and flaky API readers; do not use it for
	// tools that perform irreversible side effects.
	Retries int `yaml:"retries,omitempty" json:"retries,omitempty"`

	// RetryBackoff controls the wait before each retry. Empty defaults to 1s.
	// Uses Go duration syntax: "500ms", "2s", "1m".
	RetryBackoff string `yaml:"retry_backoff,omitempty" json:"retry_backoff,omitempty"`
}

// ContextHook is a lifecycle callback inserted before/after context assembly.
type ContextHook struct {
	Event      string `yaml:"event"       json:"event"`
	PythonFile string `yaml:"python_file" json:"python_file"`
	Function   string `yaml:"function"    json:"function"`
}

// Schedule configures cron/one-shot triggers.
type Schedule struct {
	Cron                string          `yaml:"cron,omitempty"                  json:"cron,omitempty"`
	At                  time.Time       `yaml:"at,omitempty"                    json:"at,omitempty"`
	Timeout             string          `yaml:"timeout,omitempty"               json:"timeout,omitempty"`
	RunMissedOnStartup  bool            `yaml:"run_missed_on_startup,omitempty" json:"run_missed_on_startup,omitempty"`
	MissedStartupWindow string          `yaml:"missed_startup_window,omitempty" json:"missed_startup_window,omitempty"`
	Output              *ScheduleOutput `yaml:"output,omitempty"                json:"output,omitempty"`
}

// ScheduleOutput configures where successful scheduled runs should be sent.
type ScheduleOutput struct {
	Channel  string `yaml:"channel,omitempty"  json:"channel,omitempty"`  // channel adapter ID, e.g. telegram or telegram-financial-agent
	To       string `yaml:"to,omitempty"       json:"to,omitempty"`       // destination thread/chat/channel/user ID for the adapter
	BotName  string `yaml:"bot_name,omitempty" json:"bot_name,omitempty"` // display snapshot from channel bot mapping
	Template string `yaml:"template,omitempty" json:"template,omitempty"` // optional text template; {reply} inserts the agent reply
}

// WebhookConfig maps arbitrary inbound JSON payloads into a canonical message.
// Paths are dot-separated, e.g. "issue.title" or "sender.login"; "$." prefixes
// are accepted for users coming from JSONPath-style tooling.
type WebhookConfig struct {
	TextPath      string `yaml:"text_path,omitempty"       json:"text_path,omitempty"`
	UserIDPath    string `yaml:"user_id_path,omitempty"    json:"user_id_path,omitempty"`
	UsernamePath  string `yaml:"username_path,omitempty"   json:"username_path,omitempty"`
	SessionIDPath string `yaml:"session_id_path,omitempty" json:"session_id_path,omitempty"`
	ThreadIDPath  string `yaml:"thread_id_path,omitempty"  json:"thread_id_path,omitempty"`
	IncludeRaw    bool   `yaml:"include_raw,omitempty"     json:"include_raw,omitempty"`
}

// Definition is the parsed representation of a SOUL.yaml file.
// This is the single source of truth for an agent's behaviour.
// BudgetConfig bounds resource consumption for a single agent run
// (Story 1 / S3.1). All limits are per-run unless noted; zero means "no limit
// for that dimension".
//
//	budget:
//	  max_tokens: 200000      # total prompt+completion tokens across the run
//	  max_llm_calls: 40       # hard cap on Router.Complete invocations
type BudgetConfig struct {
	// MaxTokens caps cumulative prompt+completion tokens for the whole run
	// (summed across every turn and every peer-recursion level reached
	// through this run). Checked before each LLM call.
	MaxTokens int `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`

	// MaxLLMCalls caps the number of Router.Complete calls in a single run —
	// a model-agnostic backstop against runaway loops that the token cap
	// might not catch quickly enough (e.g. many tiny calls).
	MaxLLMCalls int `yaml:"max_llm_calls,omitempty" json:"max_llm_calls,omitempty"`
}

type Definition struct {
	// --- Identity ---
	ID          string            `yaml:"id"                json:"id"`
	Name        string            `yaml:"name"              json:"name"`
	Description string            `yaml:"description"       json:"description"`
	Version     string            `yaml:"version"           json:"version"`
	Tags        []string          `yaml:"tags,omitempty"    json:"tags,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"  json:"labels,omitempty"`

	// Kind opts the agent into a non-default execution shape. Empty (or
	// "worker") = the standard LLM loop. "router" = the engine treats this
	// agent as a dispatcher: at message entry, it matches the inbound text
	// against `Routes` and forwards to the first matching peer via the
	// existing agent__<id> peer-call path. No LLM is invoked at the
	// router level. See docs/CHANNEL_DESIGN.md Q2.
	//
	// Backward compat: a SOUL.yaml without `kind:` is a worker — every
	// pre-existing agent keeps its current semantics.
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`

	// Routes is consulted only when Kind == "router". Routes are tried in
	// order; the first match wins. A route with no match clauses (no
	// Regex, no Prefix, no Contains) is the "else" fallback and MUST be
	// last if present. If no route matches and no fallback exists, the
	// router returns an empty reply with an error event.
	Routes []RouterRoute `yaml:"routes,omitempty" json:"routes,omitempty"`

	// Surfaces lists where this agent is meant to APPEAR / be invokable —
	// "chat", "schedule", or a channel name ("telegram", "slack", …). It does
	// not grant capability; it's a UI/interface hint so, e.g., a cron-only agent
	// doesn't clutter the Chat picker. Empty = derive from the trigger/channels
	// (see EffectiveSurfaces). Set by Studio at save time.
	Surfaces []string `yaml:"surfaces,omitempty" json:"surfaces,omitempty"`

	// --- Trigger ---
	Trigger  TriggerKind    `yaml:"trigger"             json:"trigger"`
	Channels []string       `yaml:"channels,omitempty"  json:"channels,omitempty"`
	Schedule *Schedule      `yaml:"schedule,omitempty"  json:"schedule,omitempty"`
	Webhook  *WebhookConfig `yaml:"webhook,omitempty"   json:"webhook,omitempty"`

	// --- Intelligence ---
	SystemPrompt string    `yaml:"system_prompt" json:"system_prompt"`
	LLM          LLMConfig `yaml:"llm"           json:"llm"`

	// --- Persona (opt-in structured agent identity & rules) ---
	//
	// These three blocks let an operator separate WHO the agent is
	// (Identity), HOW it speaks (Personality), and WHAT IT MUST/MUST NOT
	// do (NonNegotiables) from the free-form `system_prompt`. The engine
	// renders them as a structured prefix above the operator's prompt so
	// the LLM sees them with consistent framing across every agent — no
	// more guessing whether "concise" means "tweet-short" or "no fluff".
	//
	// All three are optional. An agent with none of them behaves exactly
	// like a legacy SOUL.yaml: only `system_prompt` is sent. Add only the
	// blocks you need.
	//
	// Why NOT three separate files: an agent is the atomic unit you share
	// and version. Three files = three places to forget to update when
	// forking. Composability for shared personalities/rules belongs in a
	// future `extends:` mechanism, not in physical file separation.
	Identity       *Identity       `yaml:"identity,omitempty"        json:"identity,omitempty"`
	Personality    *Personality    `yaml:"personality,omitempty"     json:"personality,omitempty"`
	NonNegotiables *NonNegotiables `yaml:"non_negotiables,omitempty" json:"non_negotiables,omitempty"`

	// --- Tools ---
	Tools []ToolDef `yaml:"tools,omitempty" json:"tools,omitempty"`

	// --- Skills (opt-in) ---
	// Names of Agent Skills this agent may use. Empty = skills disabled (the
	// skill catalog and read_skill tools are NOT injected, so simple agents
	// don't waste turns on spurious skill lookups). Use ["*"] (or ["all"]) to
	// enable all installed skills, or list specific skill names.
	Skills []string `yaml:"skills,omitempty" json:"skills,omitempty"`

	// --- Knowledge bases (opt-in) ---
	// Names of knowledge bases this agent may search via the built-in
	// `kb_search` tool. Empty = no KB catalog is injected and kb_search is
	// NOT offered, so simple agents stay focused. Each entry must match a
	// KB.Name in the knowledge store; entries that don't resolve at load time
	// are logged but tolerated (the KB may be created later).
	Knowledge []string `yaml:"knowledge,omitempty" json:"knowledge,omitempty"`

	// --- Peer agents (opt-in, multi-agent / agent-as-tool) ---
	// IDs of OTHER agents this agent may invoke as callable tools. The engine
	// dynamically registers one tool per peer named `agent__<id>` whose
	// description is pulled from the target agent's `description` field.
	// When invoked, the peer runs as a fresh session (no shared history with
	// the caller) up to its own max_turns and returns its final reply.
	//
	// Use ["*"] (or ["all"]) to expose every other loaded agent as a tool.
	// Self-references (an agent in its own peer list) are silently skipped.
	// Cycles are bounded by runtime.max_agent_call_depth carried via
	// context.Value; exceeding it returns an error tool result so the parent can
	// recover.
	Agents []string `yaml:"agents,omitempty" json:"agents,omitempty"`

	// ParallelPeerCalls lets an orchestrator fan out multiple peer-agent calls
	// emitted in the same LLM turn. Results are still returned to the model in
	// the original tool-call order, but the peer agents run concurrently under
	// the same nested-chain deadline. Normal tools remain sequential.
	ParallelPeerCalls bool `yaml:"parallel_peer_calls,omitempty" json:"parallel_peer_calls,omitempty"`

	// StructuredPeerResults wraps peer-agent replies in a small JSON envelope
	// when they are returned as tool results. This gives coordinator agents a
	// stable place to read target agent, content, and parsed JSON payloads
	// without changing the SDK ToolResult shape or surprising simple agents.
	StructuredPeerResults bool `yaml:"structured_peer_results,omitempty" json:"structured_peer_results,omitempty"`

	// --- Built-in tool allowlist (opt-in) ---
	// Controls which Go-native built-ins (web_search, kb_search, read_skill, …)
	// are offered to this agent. Three modes:
	//
	//   nil / field absent → default gating applies: built-ins whose Gate
	//                        condition passes are auto-injected. This is the
	//                        backward-compatible behaviour.
	//   []  (empty list)   → NO built-ins are offered. Useful for orchestrator
	//                        agents that should only call their peers — without
	//                        this, an orchestrator with `agents: [web-researcher]`
	//                        ALSO sees the raw `web_search` built-in and may
	//                        bypass the peer-agent abstraction.
	//   [name, …]          → ONLY the named built-ins are offered (still
	//                        subject to their gate — e.g. listing kb_search
	//                        without declaring any knowledge bases is a no-op).
	//                        Use ["*"] or ["all"] as a synonym for the nil mode.
	//
	// IMPORTANT: this field is encoded with `omitempty,!nil` semantics — i.e.
	// a present empty list means "no built-ins" and SERIALIZES as `builtins: []`
	// in YAML so a GUI round-trip preserves the user's intent.
	Builtins *[]string `yaml:"builtins,omitempty" json:"builtins,omitempty"`

	// --- External tool allowlists (default deny) ---
	// MCPServers limits which connected MCP servers this agent can see and call.
	// Nil / absent means no MCP servers are available. Use
	// ["*"] or ["all"] to explicitly allow every connected MCP server.
	MCPServers *[]string `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`

	// MCPTools limits individual MCP tools by full namespaced tool name, e.g.
	// "mcp__rocketmoney__get_transactions". It may be combined with
	// MCPServers; a tool is allowed when either allowlist admits it. Nil means
	// no per-tool restriction unless MCPServers is also set.
	MCPTools *[]string `yaml:"mcp_tools,omitempty" json:"mcp_tools,omitempty"`

	// PluginTools lists the full plugin__<plugin>__<tool> names this agent may
	// invoke. Nil and an empty list both mean none; ["*"] explicitly grants all.
	PluginTools *[]string `yaml:"plugin_tools,omitempty" json:"plugin_tools,omitempty"`

	// SystemTools, when true, opts this agent into the OS-level built-in tool set
	// (shell_exec, run_script, install_library, write_file, download_file, …).
	// ALSO requires runtime.allow_system_tools: true in config.yaml — both must
	// be set. This double opt-in prevents accidental exposure of system access.
	//
	// SEC-3: SystemTools is retained for backward compatibility and is treated
	// as equivalent to declaring the "system" capability (see Capabilities).
	// New SOUL.yaml should prefer `capabilities: [system]`.
	SystemTools bool `yaml:"system_tools,omitempty" json:"system_tools,omitempty"`

	// AllowShell is an explicit, readable opt-in for the OS-level "system"
	// built-ins (Story 6). It is an alias for `capabilities: [system]` /
	// `system_tools: true` and exists so a SOUL.yaml author can grant shell
	// access with an obvious flag name. As with the other gates, it still
	// requires the server-level permit (runtime.allow_system_agents) — both
	// must agree. Default (false) keeps shell_exec and friends OFF.
	AllowShell bool `yaml:"allow_shell,omitempty" json:"allow_shell,omitempty"`

	// Capabilities is the per-agent list of privileged capabilities this agent
	// has been granted (SEC-3). Currently the only recognised value is:
	//
	//   "system" — admits the destructive OS-level built-ins (shell_exec,
	//              run_script, install_library, write_file, download_file).
	//              Still requires runtime.allow_system_tools: true at the
	//              server level — both gates must pass.
	//
	// The legacy `system_tools: true` flag is honoured as an alias for
	// declaring "system" here (see HasCapability). Unknown capabilities are
	// ignored (forward-compatible).
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`

	// Env is the per-agent allowlist of host environment variable NAMES that
	// should be passed through to spawned tool subprocesses (SEC-5). The base
	// allowlist (PATH, HOME, LANG, TMPDIR) is always passed; names listed here
	// are added on top. Secrets the gateway holds (ANTHROPIC_API_KEY, …) are
	// NOT inherited unless explicitly listed. Values are read from the
	// gateway's own environment at spawn time; a name with no value is skipped.
	Env []string `yaml:"env,omitempty" json:"env,omitempty"`

	// ConfirmTools is the list of built-in tool names that require explicit user
	// approval before execution. The engine pauses, emits a "tool_confirm" SSE
	// event, and waits for a POST to /api/v1/chat/confirm before proceeding.
	// Use ["*"] to require confirmation for every built-in tool call.
	// Particularly useful for destructive tools: shell_exec, write_file, http_request.
	ConfirmTools []string `yaml:"confirm_tools,omitempty" json:"confirm_tools,omitempty"`

	// Unattended, when true, lets the agent COMPLETE guardrail-confirmation
	// actions (privileged system/network steps that normally pause for approval)
	// in contexts where no interactive confirmer is present — notably scheduled
	// runs. Without it, such an action is DENIED when there is no GUI to approve
	// it, so a scheduled agent that needs a privileged step fails every run.
	// Default false: the safe behavior (deny when nobody can approve) is
	// preserved; an operator opts into unattended execution explicitly. It does
	// NOT widen what tools are offered (allow_system_tools still gates that) — it
	// only resolves the confirmation gate non-interactively.
	Unattended bool `yaml:"unattended,omitempty" json:"unattended,omitempty"`

	// --- Memory ---
	Memory MemoryPolicy `yaml:"memory" json:"memory"`

	// --- Reasoning loop (CFG-01) ---
	// Controls the multi-step reasoning strategy. When absent the agent uses
	// the classic single-LLM-call behaviour (no loop). Set strategy to "react"
	// or "plan_execute" to enable the reasoning loop for this agent.
	Reasoning ReasoningConfig `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`

	// --- Long-term agent memory (CFG-02) ---
	// Controls episodic, semantic, and procedural memory injection and
	// persistence for this agent. Requires a CompositeStore to be wired into
	// the engine at startup (MEM-02).
	BrainMemory BrainMemoryConfig `yaml:"brain_memory,omitempty" json:"brain_memory,omitempty"`

	// --- Learning loop ---
	// Controls post-run learning proposals. Proposals are reviewable in the GUI
	// and API; accepting one writes it into the relevant memory/rule layer.
	Learning LearningConfig `yaml:"learning,omitempty" json:"learning,omitempty"`

	// --- Tool policy ---
	// Gates high-risk shell/file/network tool actions with allow/prompt/deny
	// decisions before any handler runs. Absent = no policy (unchanged behavior).
	Policy ToolPolicyConfig `yaml:"policy,omitempty" json:"policy,omitempty"`

	// --- Execution backend ---
	// Selects where this agent's Python tool code runs (local/docker/ssh).
	// Absent = server default backend.
	Execution ExecutionConfig `yaml:"execution,omitempty" json:"execution,omitempty"`

	// --- Dry run ---
	// When true, side-effecting tool calls (shell, file writes, network POSTs,
	// MCP/plugin tools) are simulated rather than executed: the agent still plans
	// and reasons, but no real action is taken. Useful for previewing an agent
	// safely. Can also be toggled per-request. Absent = normal execution.
	DryRun bool `yaml:"dry_run,omitempty" json:"dry_run,omitempty"`

	// --- Hooks ---
	Hooks []ContextHook `yaml:"hooks,omitempty" json:"hooks,omitempty"`

	// --- Runtime ---
	MaxTurns    int  `yaml:"max_turns"    json:"max_turns"`
	StreamReply bool `yaml:"stream_reply" json:"stream_reply"`
	Enabled     bool `yaml:"enabled"      json:"enabled"`

	// Budget caps token (and, by extension, cost) consumption for a single
	// run (Story 1 / S3.1). The engine checks cumulative usage against this
	// budget BEFORE every LLM call; when the cap would be exceeded it halts
	// the run with a terminal "budget exceeded" reply rather than letting a
	// runaway loop, prompt injection, or deep peer recursion silently drain
	// API credits. Absent/zero = no per-run cap (server-level ceilings still
	// apply).
	Budget *BudgetConfig `yaml:"budget,omitempty" json:"budget,omitempty"`

	// NotifyOnFailure tells the engine where to post a heads-up when a run
	// errors. Useful for cron-driven agents whose only audience is the
	// scheduler — without this, failures only ever land in the actionlog
	// where nothing alerts a human until the operator notices stale output.
	//
	// Default behavior (field absent): runs triggered by an external
	// channel (telegram/slack/discord/whatsapp) get an automatic error
	// reply on the SAME channel back to the originating user, because the
	// engine has both the channel id and the thread id from the inbound
	// message. Runs triggered by cron or by manual HTTP have no implicit
	// reply target — for those you must set NotifyOnFailure explicitly or
	// the failure is logged silently.
	NotifyOnFailure *NotifyOnFailure `yaml:"notify_on_failure,omitempty" json:"notify_on_failure,omitempty"`

	// Security configures access control for this agent independent of the LLM.
	// When Passphrase is non-empty the engine enforces it in Go before the LLM
	// ever sees the message — no model instruction can bypass this gate.
	Security *SecurityConfig `yaml:"security,omitempty" json:"security,omitempty"`

	// Workflow, when set, declares a multi-step DAG for this agent. The runtime
	// executes steps sequentially, checkpointing state after each step, and can
	// resume on restart. Declared under the `workflow:` key in SOUL.yaml.
	// When Workflow is non-nil, Handle() delegates to WorkflowExecutor instead
	// of the free-form LLM loop.
	Workflow *WorkflowSpec `yaml:"workflow,omitempty" json:"workflow,omitempty"`

	// Outcome is the agent's BUSINESS-OUTCOME CONTRACT (P0-4): what a run must
	// actually achieve, as opposed to merely completing without a tool error.
	//
	// Assertions used to be Studio build-time only — they lived in a test run,
	// judged a draft, and were discarded at save. So an agent that passed every
	// assertion in Studio was, in production, judged by exactly one thing: did a
	// node return an error. A workflow that fetched zero articles, generated no
	// audio, and delivered an empty message was a "successful" run.
	//
	// Persisting the contract here lets the runtime re-evaluate it against real
	// runs, so "delivered the brief" and "returned without crashing" stop being
	// the same result. Optional: an agent with no contract behaves exactly as
	// before.
	Outcome *OutcomeContract `yaml:"outcome,omitempty" json:"outcome,omitempty"`

	// ToolSchemas records the tool contracts this agent was BUILT AGAINST
	// (P0-3). MCP schemas are discovered live, but nothing recorded which
	// version a workflow was generated from — so a server renaming an argument
	// broke the agent at its next scheduled run with an error that looked
	// identical to "this was always wrong". Comparing this snapshot against
	// live schemas detects drift, names the affected node, and marks the agent
	// as needing recertification. Optional and purely additive.
	ToolSchemas *ToolSchemaSnapshot `yaml:"tool_schemas,omitempty" json:"tool_schemas,omitempty"`

	// StudioIntent is the natural-language prompt that generated this agent's
	// workflow in Studio. Persisted so the Studio editor can show the original
	// prompt and let the user edit it and re-generate. Studio-only metadata; the
	// runtime does not use it.
	StudioIntent string `yaml:"studio_intent,omitempty" json:"studio_intent,omitempty"`

	// StudioRefined records that StudioIntent has already been through Studio's
	// full refine pass (and may have been hand-edited). When set, re-opening the
	// workflow in Studio re-generates with a fast LIGHT touch-up instead of a
	// full re-refine. Studio-only metadata; the runtime does not use it.
	StudioRefined bool `yaml:"studio_refined,omitempty" json:"studio_refined,omitempty"`

	// StudioRawIntent is the user's ORIGINAL plain-language prompt, before the
	// refine pass turned it into the detailed StudioIntent spec. Persisted so the
	// Studio prompt editor can show both the original and the refined prompt and
	// let the user edit the original and re-refine. Studio-only metadata.
	StudioRawIntent string `yaml:"studio_raw_intent,omitempty" json:"studio_raw_intent,omitempty"`

	// StudioDeliveryMode preserves Studio's explicit delivery semantics across
	// edit/save cycles. In particular, "reply" is contextual delivery through
	// the invocation route and therefore needs no fixed output channel.
	// Studio-only metadata; the runtime does not use it for routing.
	StudioDeliveryMode string `yaml:"studio_delivery_mode,omitempty" json:"studio_delivery_mode,omitempty"`

	// StudioTriggerMode preserves author-facing trigger choices that share a
	// runtime TriggerKind. "chat" and "manual" both execute as internal runs,
	// but Studio presents different, explicit UX for them.
	StudioTriggerMode string `yaml:"studio_trigger_mode,omitempty" json:"studio_trigger_mode,omitempty"`

	// RunTimeout caps the total wall-clock duration of one full agent run
	// (across all LLM turns and tool calls). Go duration syntax: "5m", "30m",
	// "1h". Empty = use the gateway default (15m). Bump this for agents that
	// call long-running tools — e.g. NotebookLM audio generation can take 10+
	// minutes by itself.
	RunTimeout string `yaml:"run_timeout,omitempty" json:"run_timeout,omitempty"`

	// Populated at load time — excluded from API responses and YAML serialisation.
	SourcePath string    `yaml:"-" json:"-"`
	LoadedAt   time.Time `yaml:"-" json:"-"`
}

// RouterRoute is one rule in a Kind=="router" agent's Routes list. The
// engine matches rules in declaration order; the first match wins.
//
// Match clauses (all optional, evaluated in order: Regex → Prefix →
// Contains). A route with NO match clauses is the "else" fallback and
// must appear last. Matching is case-insensitive for Prefix and
// Contains; Regex is whatever Go's regexp package decides.
//
// Target is the peer agent ID to dispatch to. It MUST appear in the
// router's `agents:` peer list (the existing engine guard at
// runAgentCall enforces this independently — a router cannot manufacture
// a Target outside its allowlist).
//
// See docs/CHANNEL_DESIGN.md Q2 for the rationale.
type RouterRoute struct {
	// Regex, if non-empty, is compiled once at load and matched against
	// the inbound message text. Use `(?i)` inside the pattern for
	// case-insensitive matching; Go's regexp doesn't have a top-level
	// case-insensitive flag.
	Regex string `yaml:"regex,omitempty" json:"regex,omitempty"`

	// Prefix is a case-insensitive starts-with check. Useful for slash
	// commands like "/research" or "!finance".
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// Contains is a case-insensitive any-of substring check. The route
	// matches if the inbound text contains ANY of the listed substrings.
	Contains []string `yaml:"contains,omitempty" json:"contains,omitempty"`

	// Target is the peer agent ID to dispatch to on match. Required.
	Target string `yaml:"target" json:"target"`
}

// NotifyOnFailure is the SOUL.yaml block that configures where a run's
// failure is reported. All fields are optional except Channel + To.
type NotifyOnFailure struct {
	// Channel is the adapter ID (e.g. "telegram", "slack", "discord",
	// "whatsapp", "http"). Must match a channel that is registered and
	// enabled at runtime, otherwise the notification is dropped with a
	// warn log — the original failure is still recorded in the actionlog.
	Channel string `yaml:"channel" json:"channel"`

	// To is the recipient on that channel. Format is adapter-specific:
	//   telegram → chat_id as a numeric string ("8546291328")
	//   slack    → channel id ("C0123ABCD") or user id
	//   discord  → channel id
	//   whatsapp → phone number (E.164)
	//   http     → user_id of the receiving handle
	To string `yaml:"to" json:"to"`

	// IncludeError, when true, appends the engine's error string to the
	// notification body. Defaults to true via the engine's handling code
	// (we generally want the operator to know WHY a job failed).
	IncludeError bool `yaml:"include_error,omitempty" json:"include_error,omitempty"`

	// Template overrides the default notification body. Recognises these
	// substitutions, applied via simple string replace:
	//   {agent_id}   {agent_name}   {timestamp}   {error}   {stage}
	// Default (when Template is empty):
	//   "🚨 Soulacy agent {agent_id} failed at {timestamp}: {error}"
	Template string `yaml:"template,omitempty" json:"template,omitempty"`
}

// Clone returns a deep copy of the Definition. Slice and map fields are
// copied so a hot-reload (which replaces the in-memory pointer) cannot
// mutate the copy held by an in-flight engine.Handle() call.
//
// For fields whose values are read-only after unmarshal (e.g. nested map
// values inside Parameters/OutputSchema), a shallow clone of the map is
// sufficient — only the map header needs to be independent so range/assign
// on one copy doesn't affect the other.
func (d *Definition) Clone() *Definition {
	if d == nil {
		return nil
	}
	cp := *d // copy scalar fields

	// Slices — each gets its own backing array.
	cp.Tags = cloneStrSlice(d.Tags)
	cp.Channels = cloneStrSlice(d.Channels)
	cp.Surfaces = cloneStrSlice(d.Surfaces)
	cp.Skills = cloneStrSlice(d.Skills)
	cp.Knowledge = cloneStrSlice(d.Knowledge)
	cp.Agents = cloneStrSlice(d.Agents)
	cp.ConfirmTools = cloneStrSlice(d.ConfirmTools)
	cp.Capabilities = cloneStrSlice(d.Capabilities)
	cp.Env = cloneStrSlice(d.Env)

	// Tool policy — slices get their own backing arrays.
	cp.Policy.AllowDomains = cloneStrSlice(d.Policy.AllowDomains)
	cp.Policy.DenyDomains = cloneStrSlice(d.Policy.DenyDomains)
	cp.Policy.DenyPaths = cloneStrSlice(d.Policy.DenyPaths)

	// Memory slices.
	cp.Memory = MemoryPolicy{
		MaxTokens:   d.Memory.MaxTokens,
		ReadScopes:  cloneStrSlice(d.Memory.ReadScopes),
		WriteScopes: cloneStrSlice(d.Memory.WriteScopes),
	}

	// LLM — clone slice/map sub-fields.
	cp.LLM = d.LLM
	cp.LLM.AllowedProviders = cloneStrSlice(d.LLM.AllowedProviders)
	cp.LLM.AllowedModels = cloneStrSlice(d.LLM.AllowedModels)
	if d.LLM.OutputSchema != nil {
		cp.LLM.OutputSchema = cloneMapAny(d.LLM.OutputSchema)
	}

	// Labels map.
	if d.Labels != nil {
		cp.Labels = make(map[string]string, len(d.Labels))
		for k, v := range d.Labels {
			cp.Labels[k] = v
		}
	}

	// Builtins — pointer to a slice.
	if d.Builtins != nil {
		cloned := cloneStrSlice(*d.Builtins)
		cp.Builtins = &cloned
	}
	if d.MCPServers != nil {
		cloned := cloneStrSlice(*d.MCPServers)
		cp.MCPServers = &cloned
	}
	if d.MCPTools != nil {
		cloned := cloneStrSlice(*d.MCPTools)
		cp.MCPTools = &cloned
	}
	if d.PluginTools != nil {
		cloned := cloneStrSlice(*d.PluginTools)
		cp.PluginTools = &cloned
	}

	// Tools — each ToolDef's Parameters map gets its own header copy.
	if len(d.Tools) > 0 {
		cp.Tools = make([]ToolDef, len(d.Tools))
		for i, t := range d.Tools {
			cp.Tools[i] = t
			if t.Parameters != nil {
				cp.Tools[i].Parameters = cloneMapAny(t.Parameters)
			}
		}
	}

	// Hooks slice.
	if len(d.Hooks) > 0 {
		cp.Hooks = make([]ContextHook, len(d.Hooks))
		copy(cp.Hooks, d.Hooks)
	}

	// NotifyOnFailure — shallow pointer copy; struct has no sub-slices.
	if d.NotifyOnFailure != nil {
		nof := *d.NotifyOnFailure
		cp.NotifyOnFailure = &nof
	}

	// Outcome contract — deep copy so a clone's assertions can't be mutated
	// through the original (the loader hands clones to concurrent runs).
	if d.Outcome != nil {
		oc := *d.Outcome
		oc.Assertions = append([]OutcomeAssertion(nil), d.Outcome.Assertions...)
		cp.Outcome = &oc
	}

	// Tool-schema snapshot — deep copy, including each record's node list.
	if d.ToolSchemas != nil {
		ts := *d.ToolSchemas
		ts.Tools = make([]ToolSchemaRecord, len(d.ToolSchemas.Tools))
		for i, r := range d.ToolSchemas.Tools {
			r.Nodes = append([]string(nil), r.Nodes...)
			ts.Tools[i] = r
		}
		cp.ToolSchemas = &ts
	}

	// Workflow — shallow pointer copy. Steps/Nodes/Edges elements are
	// read-only after unmarshal so element-shallow copies are safe.
	if d.Workflow != nil {
		wf := *d.Workflow
		wf.Steps = append([]StepSpec(nil), d.Workflow.Steps...)
		wf.Nodes = append([]reasoning.FlowNode(nil), d.Workflow.Nodes...)
		wf.Edges = append([]reasoning.FlowEdge(nil), d.Workflow.Edges...)
		cp.Workflow = &wf
	}

	// Schedule.
	if d.Schedule != nil {
		sched := *d.Schedule
		if d.Schedule.Output != nil {
			out := *d.Schedule.Output
			sched.Output = &out
		}
		cp.Schedule = &sched
	}

	return &cp
}

func cloneStrSlice(s []string) []string {
	if s == nil {
		return nil
	}
	c := make([]string, len(s))
	copy(c, s)
	return c
}

// cloneMapAny produces a one-level-deep map copy (keys + value pointers
// are independent; nested map/slice values share storage). This is
// sufficient because the engine only READS nested schema data — it never
// mutates it in place.
func cloneMapAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// ResolvedRunTimeout returns the agent's declared RunTimeout (parsed as a Go
// duration), falling back to the supplied default when unset or invalid. Used
// by the gateway and scheduler so per-agent caps apply consistently across
// manual triggers, HTTP chat, and cron-driven runs.
func (d *Definition) ResolvedRunTimeout(fallback time.Duration) time.Duration {
	resolved := fallback
	if d == nil || d.RunTimeout == "" {
		resolved = fallback
	} else if t, err := time.ParseDuration(d.RunTimeout); err == nil && t > 0 {
		resolved = t
	}

	if d != nil && d.Reasoning.TotalTimeout != "" {
		if rt, err := time.ParseDuration(d.Reasoning.TotalTimeout); err == nil && rt > resolved {
			return rt
		}
	}
	return resolved
}

// HasCapability reports whether the agent has been granted the named
// capability (SEC-3). The legacy `system_tools: true` flag and the readable
// `allow_shell: true` flag (Story 6) are both honoured as aliases for the
// "system" capability so old and new SOUL.yaml keep working.
func (d *Definition) HasCapability(cap string) bool {
	if d == nil {
		return false
	}
	if cap == "system" && (d.SystemTools || d.AllowShell) {
		return true
	}
	for _, c := range d.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// Status is the live operational state of a loaded agent.
type Status struct {
	AgentID        string     `json:"agent_id"`
	Enabled        bool       `json:"enabled"`
	ActiveSessions int        `json:"active_sessions"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	TotalRuns      int64      `json:"total_runs"`
}

// ─── Persona: identity, personality, non-negotiables ─────────────────────────
//
// All three structs are pointer-attached on Definition so the engine can
// tell "operator didn't write this block" (nil) from "operator wrote it
// empty" (non-nil with zero fields). nil = skip entirely; non-nil =
// render the block header even if every field is empty, so an operator
// who explicitly clears Identity sees that in the prompt prefix.

// Identity describes WHO the agent is. Used at the top of the rendered
// system prefix so the LLM has a clear self-concept on turn 1. Fields
// are intentionally short and concrete; long backstories belong in
// system_prompt, not here.
type Identity struct {
	// Role is the agent's professional or functional title.
	// Examples: "senior research analyst", "code reviewer", "triage nurse".
	Role string `yaml:"role,omitempty" json:"role,omitempty"`

	// Expertise lists domains/topics the agent knows well. Rendered as a
	// bullet list. Keep entries short noun phrases, not sentences.
	// Examples: ["macroeconomics", "monetary policy"].
	Expertise []string `yaml:"expertise,omitempty" json:"expertise,omitempty"`

	// Audience names who the agent is talking to. Helps the model pitch
	// vocabulary and depth. Examples: "institutional investors",
	// "first-time programmers", "internal eng team".
	Audience string `yaml:"audience,omitempty" json:"audience,omitempty"`

	// Backstory is optional free text — only fill it if a backstory
	// materially changes behavior. Most agents shouldn't have one.
	Backstory string `yaml:"backstory,omitempty" json:"backstory,omitempty"`
}

// Personality describes HOW the agent speaks. Voice and tone choices
// that change wording but not factual content. Keep entries short
// adjective/phrase form — the LLM responds better to "concise, dry" than
// to a paragraph essay on style.
type Personality struct {
	// Tone characterises emotional/professional register.
	// Examples: "concise, slightly dry", "warm and encouraging".
	Tone string `yaml:"tone,omitempty" json:"tone,omitempty"`

	// Voice describes structural style — "first-person", "third-person
	// observations", "no 'I think'", "direct, declarative sentences".
	Voice string `yaml:"voice,omitempty" json:"voice,omitempty"`

	// Avoid is a list of things to never do in output. Soft preference,
	// not enforced — these become "avoid X" guidance in the prompt.
	// For things the agent MUST NEVER do, use NonNegotiables.MustNot.
	// Examples: ["exclamation marks", "hedging like 'perhaps'", "emojis"].
	Avoid []string `yaml:"avoid,omitempty" json:"avoid,omitempty"`

	// Prefer is the inverse: things to lean into.
	// Examples: ["concrete numbers", "named sources", "active voice"].
	Prefer []string `yaml:"prefer,omitempty" json:"prefer,omitempty"`
}

// NonNegotiables are HARD rules the agent must follow regardless of the
// user's request. Rendered with explicit "HARD RULES" framing so the
// LLM treats them differently from prose guidance. The engine wraps
// them deterministically — every agent's prompt has the same wording
// around the rules so cross-agent behavior is predictable.
//
// Phase 1 (this release): prompt-level enforcement only. The engine
// puts these in a structured block above the operator's prompt and
// repeats the strongest must_not items in a "FINAL REMINDERS" footer
// when ToolChoice is involved. Pre-LLM and post-LLM validation hooks
// are deferred to a follow-up — wiring takes more thought than the
// schema change.
type NonNegotiables struct {
	// Must is a list of things the agent must ALWAYS do.
	// Examples: ["cite every numeric claim with [n]", "respond in the
	// same language as the most recent user message"].
	Must []string `yaml:"must,omitempty" json:"must,omitempty"`

	// MustNot is a list of things the agent must NEVER do.
	// Examples: ["reveal any environment variable", "give legal or
	// medical advice", "claim to be human if asked directly"].
	MustNot []string `yaml:"must_not,omitempty" json:"must_not,omitempty"`

	// OutputConstraints are mechanical bounds on the final reply.
	// MaxLength and MinLength are word counts; Format is one of
	// "markdown", "plain", "json", "code". Engine puts them under
	// "Output constraints" framing — and they remain prompt-level
	// guidance until the post-LLM validator lands.
	OutputConstraints *OutputConstraints `yaml:"output_constraints,omitempty" json:"output_constraints,omitempty"`
}

// OutputConstraints carries mechanical, machine-checkable bounds on the
// agent's reply. Word counts (not character/token counts) because
// they're what operators and end-users actually think in.
type OutputConstraints struct {
	MaxLength int    `yaml:"max_length,omitempty" json:"max_length,omitempty"`
	MinLength int    `yaml:"min_length,omitempty" json:"min_length,omitempty"`
	Format    string `yaml:"format,omitempty"     json:"format,omitempty"`
}
