// config.go — helpers for wiring agent SOUL.yaml reasoning config into LoopConfig.
package reasoning

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
)

// ProviderKeys carries API keys for cloud providers. Pass to DefaultBackendFor.
type ProviderKeys struct {
	AnthropicKey string
	OpenAIKey    string
	// NvidiaKey is the NVIDIA NIM / API-catalog key (OpenAI-compatible endpoint).
	NvidiaKey string
	// GroqKey / TogetherKey etc. are just OpenAI-compatible — put them in OpenAIKey
	// and set a custom BaseURL on the returned backend if needed.

	// OllamaBaseURL is the configured Ollama endpoint (from llm.providers.ollama
	// .base_url, env-resolved). It is the fallback when an agent doesn't set its
	// own llm.base_url — without it the reasoning loop's Ollama backend defaults
	// to localhost:11434, which is unreachable from inside a container even when
	// the chat path correctly uses host.docker.internal. Empty = library default.
	OllamaBaseURL string
}

// BackendAvailable reports whether a REAL reasoning backend exists for the
// given (already-resolved) provider. The reasoning strategies (react /
// plan_execute) only have native backends for Anthropic, the OpenAI-compatible
// family, and local Ollama. Every other provider — notably google/gemini — has
// no backend, and DefaultBackendFor silently falls back to Ollama for them. The
// engine calls this BEFORE entering the reasoning loop: when it returns false,
// the agent must use the classic tool loop (which works for every provider)
// rather than being routed to a (likely absent) local Ollama.
//
// Cloud providers require their API key to count as available; Ollama is local
// and always considered available (it's an explicit local choice).
func BackendAvailable(provider string, keys ProviderKeys) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return keys.AnthropicKey != ""
	case "openai", "groq", "together", "openrouter", "mistral", "deepseek", "vllm":
		return keys.OpenAIKey != ""
	case "nvidia":
		return keys.NvidiaKey != ""
	case "ollama":
		return true
	default:
		// google / gemini / grok / any unknown provider: no reasoning backend.
		return false
	}
}

// DefaultBackendFor returns the right LLMBackend for the agent, derived
// entirely from def.LLM.Provider — no extra field in SOUL.yaml needed.
//
// Routing table:
//
//	ollama    → OllamaBackend    (local Ollama, no key required)
//	anthropic → AnthropicBackend (requires keys.AnthropicKey)
//	openai    → OpenAIBackend    (requires keys.OpenAIKey)
//	groq      → OpenAICompatible at api.groq.com/openai/v1
//	together  → OpenAICompatible at api.together.xyz/v1
//	<other>   → OllamaBackend   (safe fallback for unknown providers)
//
// The agent's llm.model is used for Think(). Plan/Reflect default to the
// best available model for the provider (see inline comments below).
func DefaultBackendFor(def *agent.Definition, keys ProviderKeys) LLMBackend {
	provider := strings.ToLower(strings.TrimSpace(def.LLM.Provider))
	model := strings.TrimSpace(def.LLM.Model)
	baseURL := strings.TrimSpace(def.LLM.BaseURL)

	switch provider {
	case "anthropic":
		if keys.AnthropicKey != "" {
			b := NewAnthropicBackend(baseURL, keys.AnthropicKey)
			if model != "" {
				b.ThinkModel = model
				b.PlanModel = model
				b.ReflectModel = model
			}
			return b
		}
		// No key configured — fall back to Ollama with a clear message in logs.
		// The validator will already have warned about the missing key.
		fallthrough

	case "openai":
		if keys.OpenAIKey != "" {
			ep := baseURL
			if ep == "" {
				ep = "https://api.openai.com/v1"
			}
			b := newOpenAICompatibleBackend(ep, keys.OpenAIKey, model)
			if model != "" {
				b.ThinkModel = model
				b.PlanReflectModel = model
			}
			return b
		}
		fallthrough

	case "groq":
		// Groq uses an OpenAI-compatible API. The base URL can be overridden
		// via llm.base_url; otherwise use the standard Groq endpoint.
		ep := baseURL
		if ep == "" {
			ep = "https://api.groq.com/openai/v1"
		}
		b := newOpenAICompatibleBackend(ep, keys.OpenAIKey, model)
		if model != "" {
			b.ThinkModel = model
			b.PlanReflectModel = model
		}
		return b

	case "together":
		ep := baseURL
		if ep == "" {
			ep = "https://api.together.xyz/v1"
		}
		b := newOpenAICompatibleBackend(ep, keys.OpenAIKey, model)
		if model != "" {
			b.ThinkModel = model
			b.PlanReflectModel = model
		}
		return b

	case "nvidia":
		ep := baseURL
		if ep == "" {
			ep = "https://integrate.api.nvidia.com/v1"
		}
		b := newOpenAICompatibleBackend(ep, keys.NvidiaKey, model)
		if model != "" {
			b.ThinkModel = model
			b.PlanReflectModel = model
		}
		return b

	default:
		// ollama + any unknown provider. Fall back to the configured Ollama
		// endpoint when the agent didn't set its own base_url, so the loop
		// reaches the same Ollama the chat path uses (e.g. host.docker.internal)
		// instead of defaulting to an unreachable localhost.
		ob := baseURL
		if ob == "" {
			ob = strings.TrimSpace(keys.OllamaBaseURL)
		}
		b := NewOllamaBackend(ob)
		if model != "" {
			// Use the declared model for Think (hot path).
			// Plan/Reflect keep qwen2.5:72b for better JSON structure unless the
			// declared model is already a large qwen variant or if qwen2.5:72b is not installed.
			b.ThinkModel = model
			if isLargeQwen(model) {
				b.PlanReflectModel = model
			} else if !ollamaHasModel(ob, "qwen2.5:72b") {
				b.PlanReflectModel = model
			}
		}
		return b
	}
}

// ApplyTuning copies optional SOUL.yaml reasoning phase settings onto built-in
// backends. Unknown/custom backends keep their own defaults.
func ApplyTuning(backend LLMBackend, def *agent.Definition) LLMBackend {
	if backend == nil || def == nil {
		return backend
	}
	think := phaseFromAgent(def.Reasoning.Think)
	plan := phaseFromAgent(def.Reasoning.Plan)
	reflect := phaseFromAgent(def.Reasoning.Reflect)
	switch b := backend.(type) {
	case *RouterBackend:
		b.ThinkParams, b.PlanParams, b.ReflectParams = think, plan, reflect
	case *OpenAIBackend:
		b.ThinkParams, b.PlanParams, b.ReflectParams = think, plan, reflect
	case *OllamaBackend:
		b.ThinkParams, b.PlanParams, b.ReflectParams = think, plan, reflect
	case *AnthropicBackend:
		b.ThinkParams, b.PlanParams, b.ReflectParams = think, plan, reflect
	}
	return backend
}

func phaseFromAgent(in agent.ReasoningPhaseConfig) sdkreasoning.PhaseParams {
	return sdkreasoning.PhaseParams{
		Temperature:    in.Temperature,
		TopP:           in.TopP,
		MaxTokens:      in.MaxTokens,
		ResponseFormat: in.ResponseFormat,
	}
}

// ollamaHasModel queries Ollama's local tags API to verify if targetModel is installed.
func ollamaHasModel(baseURL, targetModel string) bool {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: 200 * time.Millisecond}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false
	}
	for _, m := range payload.Models {
		if m.Name == targetModel || strings.HasPrefix(m.Name, targetModel+":") || strings.Contains(m.Name, targetModel) {
			return true
		}
	}
	return false
}

// isLargeQwen returns true for qwen models likely good enough for Plan/Reflect
// without needing to fall back to qwen2.5:72b.
func isLargeQwen(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "qwen") &&
		(strings.Contains(m, "72b") || strings.Contains(m, "32b") || strings.Contains(m, "14b"))
}

// providersWithoutNativeTools lists provider IDs whose completion API does NOT
// accept a tools/functions parameter. Every provider Soulacy ships today speaks
// native function-calling, so this is empty — it is the single extension point
// for marking a future provider/model that can't, so that an agent left on the
// default ("auto") strategy falls back to the prompt-based ReAct protocol on it
// instead of silently failing to call tools.
var providersWithoutNativeTools = map[string]bool{}

// ProviderSupportsNativeTools reports whether a provider can call tools through
// the native completion API (the reliable path). Used to resolve the default
// ("auto"/unset) execution strategy: native-capable → classic tool loop;
// otherwise → ReAct.
func ProviderSupportsNativeTools(provider string) bool {
	return !providersWithoutNativeTools[strings.ToLower(strings.TrimSpace(provider))]
}

// LoopConfigFromDefinition is the SINGLE source of truth for choosing an agent's
// execution mode. It returns (config, true) when the agent should run through
// the multi-step reasoning Loop, and (zero, false) when it should use the
// classic native-tool-calling loop instead.
//
// Resolution rules:
//   - explicit strategy ("react" / "plan_execute" / a registered custom name) → that loop
//   - unset or "auto":
//     · agents holding the "system" capability → ReAct (system tools need it)
//     · model supports native tool-calling → classic loop (more reliable; the
//     common, recommended default)
//     · model has NO native tool-calling → ReAct (prompt-based fallback)
//
// supportsNativeTools is supplied by the engine, which knows the agent's
// effective provider. Every creation surface (Studio, Agent Builder, Agents
// screen, CLI, templates, raw YAML) just leaves the strategy unset/"auto" — the
// decision lives here, so behaviour is identical no matter how the agent was made.
func LoopConfigFromDefinition(def *agent.Definition, systemPrompt string, supportsNativeTools bool) (LoopConfig, bool) {
	rc := def.Reasoning
	strat := strings.ToLower(strings.TrimSpace(rc.Strategy))
	system := def.HasCapability("system")

	if strat == "" || strat == StrategyAuto {
		// Default: the classic native-tool-calling loop handles every agent —
		// including system-capability agents — reliably. Only fall back to the
		// prompt-based ReAct protocol when the model can't do native tool calls.
		// (The system→ReAct force below applies only to EXPLICIT strategies, to
		// keep plan_execute from breaking on highly-parameterized system tools.)
		if supportsNativeTools {
			return LoopConfig{}, false // classic loop
		}
		strat = StrategyReAct
	}

	cfg := LoopConfig{
		Strategy:      LoopStrategy(strat),
		SystemPrompt:  systemPrompt,
		ThinkParams:   phaseFromAgent(rc.Think),
		PlanParams:    phaseFromAgent(rc.Plan),
		ReflectParams: phaseFromAgent(rc.Reflect),
	}

	// SEC-3: System tools (shell_exec, write_file, etc.) are highly parameterized
	// and are fundamentally incompatible with plan_execute's single "task" argument.
	// Force the react strategy to prevent the agent from breaking itself.
	if system {
		cfg.Strategy = LoopStrategy(StrategyReAct)
	}

	cfg.MaxSteps = rc.MaxSteps
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = defaultMaxSteps(def, cfg.Strategy)
	}

	cfg.MaxPlanSteps = rc.MaxPlanSteps
	if cfg.MaxPlanSteps <= 0 {
		cfg.MaxPlanSteps = defaultMaxPlanSteps(def, cfg.Strategy)
	}

	cfg.MaxParallelSteps = rc.MaxParallelSteps
	if cfg.MaxParallelSteps <= 0 {
		cfg.MaxParallelSteps = DefaultMaxParallelSteps
	}

	if rc.StepTimeout != "" {
		if d, err := time.ParseDuration(rc.StepTimeout); err == nil && d > 0 {
			cfg.StepTimeout = d
		}
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = 30 * time.Second
	}

	if rc.TotalTimeout != "" {
		if d, err := time.ParseDuration(rc.TotalTimeout); err == nil && d > 0 {
			cfg.TotalTimeout = d
		}
	}
	if cfg.TotalTimeout <= 0 {
		cfg.TotalTimeout = 180 * time.Second
	}

	// Collect tool names from the agent's tool definitions (RL-08).
	for _, t := range def.Tools {
		cfg.ToolNames = append(cfg.ToolNames, t.Name)
	}

	// Story E25: the "flow" strategy needs the agent's graph. Attached for
	// any strategy — only "flow" reads it; nil when the workflow declares
	// no nodes (the strategy then degrades gracefully).
	cfg.Flow = def.Workflow.FlowSpec()

	return cfg, true
}

func defaultMaxSteps(def *agent.Definition, strategy LoopStrategy) int {
	if strategy == StrategyPlanExecute {
		if complexReasoningDefinition(def) {
			return 24
		}
		return 16
	}
	if complexReasoningDefinition(def) {
		return 18
	}
	return 8
}

func defaultMaxPlanSteps(def *agent.Definition, strategy LoopStrategy) int {
	if strategy == StrategyPlanExecute {
		if complexReasoningDefinition(def) {
			return 12
		}
		return 8
	}
	return 6
}

func complexReasoningDefinition(def *agent.Definition) bool {
	if def == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		def.StudioIntent,
		def.StudioRawIntent,
		def.SystemPrompt,
		strings.Join(toolNamesFromDefinition(def), " "),
	}, " "))
	if strings.Contains(text, "notebooklm") || strings.Contains(text, "notebook lm") ||
		strings.Contains(text, "podcast") || strings.Contains(text, "audio overview") {
		return true
	}
	hits := 0
	for _, word := range []string{
		"search", "find", "fetch", "read", "rank", "filter", "summarize",
		"create", "generate", "poll", "wait", "store", "ingest", "send", "deliver",
	} {
		if strings.Contains(text, word) {
			hits++
		}
	}
	if hits >= 4 {
		return true
	}
	mcpCount := 0
	for _, name := range toolNamesFromDefinition(def) {
		if strings.HasPrefix(strings.TrimSpace(name), "mcp__") {
			mcpCount++
		}
	}
	return len(toolNamesFromDefinition(def)) >= 8 || mcpCount >= 4
}

func toolNamesFromDefinition(def *agent.Definition) []string {
	if def == nil {
		return nil
	}
	var names []string
	for _, t := range def.Tools {
		if strings.TrimSpace(t.Name) != "" {
			names = append(names, t.Name)
		}
	}
	if def.Builtins != nil {
		names = append(names, *def.Builtins...)
	}
	if def.MCPTools != nil {
		names = append(names, *def.MCPTools...)
	}
	return names
}
