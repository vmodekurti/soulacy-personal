// Package agentvalidate checks SOUL.yaml definitions before deployment.
package agentvalidate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/policy"
	"github.com/soulacy/soulacy/pkg/agent"
	"gopkg.in/yaml.v3"
)

// cronParser matches internal/scheduler.cronParser so a Save-time validation
// verdict is identical to the scheduler's registration-time verdict. If the
// two ever drift, an operator saves an agent successfully and then finds it
// silently never fires — exactly the E4 failure mode the pre-validation is
// designed to close. Keep the flag set in sync with scheduler.cronParser.
var cronParser = cron.NewParser(
	cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// validateCronExpression returns an error when expr won't parse under the
// scheduler's rules. Callers should treat an empty expression as an earlier
// validation concern; this helper only speaks to syntactically-invalid text.
func validateCronExpression(expr string) error {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return fmt.Errorf("cron expression is empty")
	}
	if _, err := cronParser.Parse(trimmed); err != nil {
		return fmt.Errorf("%q is not a valid cron expression: %v (expected 5 fields like `0 9 * * 1-5` or an @descriptor)", trimmed, err)
	}
	return nil
}

type Severity string

const (
	Warn  Severity = "warn"
	Error Severity = "error"
)

type Finding struct {
	Severity     Severity `json:"severity"`
	Field        string   `json:"field"`
	Message      string   `json:"message"`
	Suggestion   string   `json:"suggestion,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
}

type Report struct {
	Path     string    `json:"path,omitempty"`
	AgentID  string    `json:"agent_id,omitempty"`
	Valid    bool      `json:"valid"`
	Warnings int       `json:"warnings"`
	Errors   int       `json:"errors"`
	Findings []Finding `json:"findings"`
}

type Options struct {
	Config              *config.Config
	RegisteredProviders []string
	ProviderModels      map[string][]string
	ConfiguredOnly      bool

	// AuthoritativeModels indicates the ProviderModels map was obtained by a
	// live probe of each provider (not a stale/baked-in list). When true, a
	// model that is absent from its provider's list is escalated from a Warn
	// to a hard Error — the run is guaranteed to 404 at call time, so the
	// agent should fail to load rather than serve users a broken model
	// (Story 2 / S1.x).
	AuthoritativeModels bool
}

func File(path string, opts Options) (Report, error) {
	report := Report{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	return Bytes(data, path, opts), nil
}

func Bytes(data []byte, path string, opts Options) Report {
	report := Report{Path: path}
	strictDec := yaml.NewDecoder(bytes.NewReader(data))
	strictDec.KnownFields(true)
	var strict agent.Definition
	if err := strictDec.Decode(&strict); err != nil {
		report.add(Error, "SOUL.yaml", "unknown or malformed field: "+err.Error(), "", nil)
	}

	var def agent.Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		report.add(Error, "SOUL.yaml", "YAML parse failed: "+err.Error(), "", nil)
		report.Valid = false
		return report
	}
	return Definition(&def, path, opts, report)
}

func Definition(def *agent.Definition, path string, opts Options, report Report) Report {
	if def == nil {
		report.add(Error, "agent", "definition is nil", "", nil)
		report.Valid = false
		return report
	}
	report.AgentID = def.ID
	validateDefinitionShape(&report, def, path)
	validateLLMFit(&report, def, opts)
	validateReasoningFit(&report, def, opts)
	validateToolSafety(&report, def)
	if report.Findings == nil {
		report.Findings = []Finding{}
	}
	report.Valid = report.Errors == 0
	return report
}

func validateDefinitionShape(report *Report, def *agent.Definition, path string) {
	if strings.TrimSpace(def.ID) == "" {
		report.add(Error, "id", "required", "", nil)
	} else if strings.ContainsAny(def.ID, "/\\ \t\n\r") || def.ID == "." || def.ID == ".." {
		// Error, not Warn. report.Valid is `Errors == 0`, so a Warn here let the
		// package importer's `if !inspected.Validation.Valid` check pass on an ID
		// like "../../../root/.ssh" — which then became a directory name. The
		// loader now refuses these too; this is the earlier, clearer stop, and it
		// names the field the operator has to change.
		report.add(Error, "id", "must not contain whitespace or path separators: the ID becomes a folder name and part of every tool name for this agent", "", nil)
	}
	if def.Trigger == "" {
		report.add(Warn, "trigger", "not set; runtime defaults may not match the intended activation mode", "", nil)
	}
	switch def.Trigger {
	case "", agent.TriggerChannel, agent.TriggerCron, agent.TriggerOneShot, agent.TriggerWebhook, agent.TriggerInternal:
	default:
		report.add(Error, "trigger", fmt.Sprintf("unsupported trigger %q", def.Trigger), "", nil)
	}
	if def.Trigger == agent.TriggerChannel && len(def.Channels) == 0 {
		report.add(Warn, "channels", "channel-triggered agents normally declare at least one channel", "", nil)
	}
	if (def.Trigger == agent.TriggerCron || def.Trigger == agent.TriggerOneShot) && def.Schedule == nil {
		report.add(Error, "schedule", "required for cron and oneshot triggers", "", nil)
	}
	if def.Schedule != nil {
		if def.Trigger == agent.TriggerCron && strings.TrimSpace(def.Schedule.Cron) == "" {
			report.add(Error, "schedule.cron", "required for cron trigger", "", nil)
		}
		// E4b (Cohort E — Schedule failure handling): validate the cron string
		// at save time using the same parser the scheduler uses at registration
		// time. Previously an invalid expression only surfaced as a
		// `s.log.Warn("scheduler re-registration failed", ...)` line in the
		// gateway logs — the save succeeded, the GUI showed the agent as
		// enabled, and the "Next run" column silently stayed blank forever.
		// Now the save fails cleanly with the parser's own explanation of
		// what's wrong ("expected exactly 5 fields, found 3").
		if def.Trigger == agent.TriggerCron && strings.TrimSpace(def.Schedule.Cron) != "" {
			if err := validateCronExpression(def.Schedule.Cron); err != nil {
				report.add(Error, "schedule.cron", err.Error(),
					"See docs/getting-started/first-agent.md for cron examples.", nil)
			}
		}
		if def.Trigger == agent.TriggerOneShot && def.Schedule.At.IsZero() {
			report.add(Error, "schedule.at", "required for oneshot trigger", "", nil)
		}
		validateDuration(report, "schedule.timeout", def.Schedule.Timeout)
		if def.Schedule.Output != nil {
			channel := strings.TrimSpace(def.Schedule.Output.Channel)
			to := strings.TrimSpace(def.Schedule.Output.To)
			if channel == "" && to != "" {
				report.add(Error, "schedule.output.channel", "required when schedule.output.to is set", "", nil)
			}
			if channel != "" && to == "" {
				report.add(Error, "schedule.output.to", "required when schedule.output.channel is set", "", nil)
			}
		}
	}

	// Delivery-gap warnings for cron agents. A cron agent with no complete
	// schedule.output produces a reply that only lands in the logs unless a
	// default outbound channel is configured — a very common "results went
	// nowhere" trap, especially when the system prompt asks to deliver results.
	if def.Trigger == agent.TriggerCron {
		hasOutput := def.Schedule != nil && def.Schedule.Output != nil &&
			strings.TrimSpace(def.Schedule.Output.Channel) != "" &&
			strings.TrimSpace(def.Schedule.Output.To) != ""
		if !hasOutput {
			if PromptImpliesDelivery(def.SystemPrompt) {
				report.add(Warn, "schedule.output",
					"the system prompt asks to deliver results, but schedule.output is not set — the reply will not be sent to any channel (set schedule.output.channel and schedule.output.to, or configure a default outbound channel)",
					"", nil)
			} else {
				report.add(Warn, "schedule.output",
					"cron agent has no delivery target — results are only logged unless schedule.output or a default outbound channel is configured",
					"", nil)
			}
		}
	}
	validateDuration(report, "run_timeout", def.RunTimeout)
	if def.LLM.MaxTokens < 0 {
		report.add(Error, "llm.max_tokens", "must be zero or positive", "", nil)
	}
	if def.LLM.Temperature < 0 {
		report.add(Error, "llm.temperature", "must be zero or positive", "", nil)
	}
	if def.Memory.MaxTokens < 0 {
		report.add(Error, "memory.max_tokens", "must be zero or positive", "", nil)
	}
	for i, tool := range def.Tools {
		prefix := fmt.Sprintf("tools[%d]", i)
		if strings.TrimSpace(tool.Name) == "" {
			report.add(Error, prefix+".name", "required", "", nil)
		}
		if strings.TrimSpace(tool.PythonFile) == "" && strings.TrimSpace(tool.Inline) == "" {
			report.add(Warn, prefix, "tool has neither python_file nor inline implementation", "", nil)
		}
		if strings.TrimSpace(tool.PythonFile) != "" {
			validateLocalPath(report, prefix+".python_file", path, tool.PythonFile)
		}
		validateDuration(report, prefix+".timeout", tool.Timeout)
	}
	validateMCPList(report, "mcp_servers", def.MCPServers, false)
	validateMCPList(report, "mcp_tools", def.MCPTools, true)
}

func validateToolSafety(report *Report, def *agent.Definition) {
	if def == nil {
		return
	}
	risky := riskyExplicitBuiltins(def)
	if len(risky) == 0 {
		return
	}
	if confirmsAll(def.ConfirmTools) {
		return
	}
	unconfirmed := make([]string, 0, len(risky))
	for _, tool := range risky {
		if !contains(def.ConfirmTools, tool) {
			unconfirmed = append(unconfirmed, tool)
		}
	}
	if len(unconfirmed) == 0 {
		return
	}
	sort.Strings(unconfirmed)
	report.add(Warn, "confirm_tools",
		fmt.Sprintf("high-risk built-in tool(s) can run without an explicit confirmation gate: %s", strings.Join(unconfirmed, ", ")),
		"add these tools to confirm_tools, use confirm_tools: [all], or enable unattended only for trusted scheduled automation",
		append([]string{"all"}, unconfirmed...),
	)
}

func riskyExplicitBuiltins(def *agent.Definition) []string {
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" || strings.EqualFold(name, "all") || seen[name] {
			return
		}
		if policy.RiskTierOf(name).HighRisk() || policy.RiskTierOf(name) == policy.RiskNetwork {
			seen[name] = true
		}
	}
	if def.Builtins != nil {
		for _, name := range *def.Builtins {
			add(name)
		}
	}
	for _, name := range def.ConfirmTools {
		if name == "*" || strings.EqualFold(name, "all") {
			return nil
		}
	}
	if def.HasCapability("system") {
		for _, name := range []string{"shell_exec", "run_script", "install_library", "write_file", "download_file"} {
			add(name)
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func confirmsAll(values []string) bool {
	for _, value := range values {
		if value == "*" || strings.EqualFold(value, "all") {
			return true
		}
	}
	return false
}

func validateLLMFit(report *Report, def *agent.Definition, opts Options) {
	provider := strings.TrimSpace(def.LLM.Provider)
	if provider == "" && opts.Config != nil {
		provider = opts.Config.LLM.DefaultProvider
	}
	if provider == "" {
		report.add(Warn, "llm.provider", "no provider set and no default provider was available", "set llm.provider or llm.default_provider", providerAlternatives(opts))
		return
	}

	if len(def.LLM.AllowedProviders) > 0 && !contains(def.LLM.AllowedProviders, provider) {
		report.add(Error, "llm.allowed_providers", fmt.Sprintf("active provider %q is blocked by allowed_providers", provider), "add the provider to allowed_providers or select an allowed provider", def.LLM.AllowedProviders)
	}
	if opts.Config != nil {
		if _, ok := opts.Config.LLM.Providers[provider]; !ok {
			report.add(Warn, "llm.provider", fmt.Sprintf("provider %q is not configured in config.yaml", provider), "configure credentials/base_url for this provider or choose a configured provider", providerAlternatives(opts))
		}
	}
	if len(opts.RegisteredProviders) > 0 && !contains(opts.RegisteredProviders, provider) {
		report.add(Error, "llm.provider", fmt.Sprintf("provider %q is not registered in the running gateway", provider), "restart the gateway after editing config, or choose a registered provider", opts.RegisteredProviders)
	}

	model := strings.TrimSpace(def.LLM.Model)
	if model == "" && opts.Config != nil {
		if pc, ok := opts.Config.LLM.Providers[provider]; ok {
			model = strings.TrimSpace(pc.Model)
		}
	}
	if model == "" {
		report.add(Warn, "llm.model", "no model set; runtime will rely on provider defaults", "set llm.model explicitly for reproducible agent behavior", modelAlternatives(opts, provider))
		return
	}
	if models := modelsFor(opts, provider); len(models) > 0 && !contains(models, model) {
		sev := Warn
		suggestion := "choose one of the currently available models"
		if opts.AuthoritativeModels {
			// The model list is from a live probe; the model genuinely isn't
			// there. Fail the agent rather than let every run 404.
			sev = Error
			suggestion = fmt.Sprintf("the model is not available on provider %q — choose a listed model (for Ollama: run `ollama pull %s`)", provider, model)
		}
		report.add(sev, "llm.model", fmt.Sprintf("model %q was not found for provider %q", model, provider), suggestion, bestModelAlternatives(models, def))
	}

	if likelyNeedsToolUse(def) && weakToolModel(provider, model) {
		report.add(Warn, "llm.model", fmt.Sprintf("model %q may be unreliable for tool-heavy agentic flows", model), "prefer a stronger tool-calling model for agents with tools, MCP, peer agents, knowledge search, or forced tool_choice", bestModelAlternatives(modelsFor(opts, provider), def))
	}
	if def.LLM.OutputSchema != nil && weakStructuredOutputModel(provider, model) {
		report.add(Warn, "llm.model", fmt.Sprintf("model %q may be unreliable for strict structured output", model), "prefer a model with stronger JSON/schema adherence", bestModelAlternatives(modelsFor(opts, provider), def))
	}
}

func validateDuration(report *Report, field, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	if _, err := time.ParseDuration(value); err != nil {
		report.add(Error, field, "invalid Go duration: "+err.Error(), "", nil)
	}
}

func validateLocalPath(report *Report, field, soulPath, value string) {
	if filepath.IsAbs(value) || soulPath == "" {
		return
	}
	candidate := filepath.Join(filepath.Dir(soulPath), value)
	if _, err := os.Stat(candidate); err != nil {
		report.add(Warn, field, "relative path not found from SOUL.yaml directory: "+value, "", nil)
	}
}

func validateMCPList(report *Report, field string, values *[]string, fullTool bool) {
	if values == nil {
		return
	}
	for i, raw := range *values {
		value := strings.TrimSpace(raw)
		item := fmt.Sprintf("%s[%d]", field, i)
		if value == "" {
			report.add(Error, item, "empty entries are not allowed", "", nil)
			continue
		}
		if value == "*" || strings.EqualFold(value, "all") {
			continue
		}
		if strings.ContainsAny(value, " \t\n\r") {
			report.add(Error, item, "must not contain whitespace", "", nil)
		}
		if fullTool && !strings.HasPrefix(value, "mcp__") {
			report.add(Error, item, "MCP tool entries must use full tool names like mcp__server__tool", "", nil)
		}
		if fullTool && strings.Count(value, "__") < 2 {
			report.add(Error, item, "MCP tool entries must include server and tool segments", "", nil)
		}
	}
}

func (r *Report) add(severity Severity, field, message, suggestion string, alternatives []string) {
	r.Findings = append(r.Findings, Finding{
		Severity: severity, Field: field, Message: message,
		Suggestion: suggestion, Alternatives: alternatives,
	})
	switch severity {
	case Error:
		r.Errors++
	case Warn:
		r.Warnings++
	}
}

func providerAlternatives(opts Options) []string {
	if len(opts.RegisteredProviders) > 0 {
		return sortedCopy(opts.RegisteredProviders)
	}
	if opts.Config == nil {
		return nil
	}
	values := make([]string, 0, len(opts.Config.LLM.Providers))
	for id := range opts.Config.LLM.Providers {
		values = append(values, id)
	}
	return sortedCopy(values)
}

func modelsFor(opts Options, provider string) []string {
	if opts.ProviderModels != nil {
		if models := opts.ProviderModels[provider]; len(models) > 0 {
			return sortedCopy(models)
		}
	}
	return nil
}

func modelAlternatives(opts Options, provider string) []string {
	if models := modelsFor(opts, provider); len(models) > 0 {
		return models
	}
	if opts.Config != nil {
		if pc, ok := opts.Config.LLM.Providers[provider]; ok && pc.Model != "" {
			return []string{pc.Model}
		}
	}
	return nil
}

func bestModelAlternatives(models []string, def *agent.Definition) []string {
	if len(models) == 0 {
		return nil
	}
	type scored struct {
		name  string
		score int
	}
	scoredModels := make([]scored, 0, len(models))
	for _, model := range models {
		score := modelScore(model, likelyNeedsToolUse(def), def.LLM.OutputSchema != nil)
		scoredModels = append(scoredModels, scored{name: model, score: score})
	}
	sort.SliceStable(scoredModels, func(i, j int) bool {
		if scoredModels[i].score == scoredModels[j].score {
			return scoredModels[i].name < scoredModels[j].name
		}
		return scoredModels[i].score > scoredModels[j].score
	})
	limit := 3
	if len(scoredModels) < limit {
		limit = len(scoredModels)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, scoredModels[i].name)
	}
	return out
}

func modelScore(model string, needsTools, needsJSON bool) int {
	m := strings.ToLower(model)
	score := 0
	for _, marker := range []string{"70b", "72b", "opus", "sonnet", "gpt-4", "4o", "gemini-2", "qwen3", "qwen2.5"} {
		if strings.Contains(m, marker) {
			score += 3
		}
	}
	for _, marker := range []string{"mini", "haiku", "flash", "32b", "14b"} {
		if strings.Contains(m, marker) {
			score += 1
		}
	}
	for _, marker := range []string{"embed", "embedding", "nomic", "bge"} {
		if strings.Contains(m, marker) {
			score -= 20
		}
	}
	if needsTools {
		for _, marker := range []string{"qwen", "gpt-4", "4o", "sonnet", "opus", "gemini"} {
			if strings.Contains(m, marker) {
				score += 2
			}
		}
	}
	if needsJSON && strings.Contains(m, "json") {
		score += 1
	}
	return score
}

func likelyNeedsToolUse(def *agent.Definition) bool {
	if def == nil {
		return false
	}
	return len(def.Tools) > 0 || len(def.Agents) > 0 || len(def.Knowledge) > 0 ||
		(def.Builtins != nil && len(*def.Builtins) > 0) ||
		def.MCPServers != nil || def.MCPTools != nil ||
		strings.TrimSpace(def.LLM.ToolChoice) == "required" ||
		strings.HasPrefix(strings.TrimSpace(def.LLM.ToolChoice), "agent__")
}

func weakToolModel(provider, model string) bool {
	m := strings.ToLower(model)
	if strings.Contains(m, "embed") || strings.Contains(m, "nomic") {
		return true
	}
	if provider == "ollama" {
		for _, weak := range []string{"llama3:8b", "llama3.1:8b", "llama3.2:1b", "llama3.2:3b", "mistral:7b", "gemma:2b", "gemma2:2b"} {
			if strings.Contains(m, weak) {
				return true
			}
		}
	}
	return false
}

func weakStructuredOutputModel(provider, model string) bool {
	return weakToolModel(provider, model)
}

// validateReasoningFit checks that the agent's LLM configuration is suitable
// for the reasoning loop strategy it declares. These checks complement the
// general LLM fit checks above with reasoning-loop-specific concerns:
//
//   - JSON output reliability: Think/Plan/Reflect require structured JSON.
//     Small, embedding, or weak models produce unparseable prose.
//   - Context window: the step history grows each turn; models with small
//     windows will lose history and produce incoherent output.
//   - Rate limits: Groq's free tier (6000 TPM) is easily exhausted by an
//     8-step ReAct loop (~800 tokens × 8 = 6400 tokens, plus Plan + Reflect).
//   - Consistency: brain_memory enabled but no reasoning strategy is likely a
//     misconfiguration — the agent will accumulate memory but never use the
//     reasoning loop to benefit from it.
func validateReasoningFit(report *Report, def *agent.Definition, opts Options) {
	strategy := strings.TrimSpace(def.Reasoning.Strategy)
	hasBrainMemory := def.BrainMemory.Episodic.Enabled ||
		def.BrainMemory.Semantic.Enabled ||
		def.BrainMemory.Procedural.Enabled

	// No reasoning loop declared — only check for the brain_memory/no-strategy inconsistency.
	if strategy == "" {
		if hasBrainMemory {
			report.add(Warn, "brain_memory",
				"brain_memory layers are enabled but reasoning.strategy is not set",
				"set reasoning.strategy to 'react' or 'plan_execute' so the loop injects and persists memory",
				[]string{"react", "plan_execute"},
			)
		}
		return
	}

	// strategy is set — validate all reasoning-loop-specific concerns.
	provider := strings.ToLower(strings.TrimSpace(def.LLM.Provider))
	model := strings.ToLower(strings.TrimSpace(def.LLM.Model))

	// ── 1. Embedding / non-chat models cannot generate structured JSON ────────
	if isEmbeddingModel(model) {
		report.add(Error, "llm.model",
			fmt.Sprintf("model %q is an embedding model and cannot generate structured JSON for the reasoning loop", def.LLM.Model),
			"choose a chat/instruction model; reasoning requires Think(), Plan(), and Reflect() JSON output",
			reasoningModelSuggestions(provider),
		)
		return // further checks are meaningless
	}

	// ── 2. Known weak JSON models ─────────────────────────────────────────────
	if weakJSONModel(provider, model) {
		report.add(Warn, "llm.model",
			fmt.Sprintf("model %q has unreliable structured JSON output, which will cause Think()/Plan()/Reflect() parse failures", def.LLM.Model),
			"use a model with strong instruction-following and JSON mode support",
			reasoningModelSuggestions(provider),
		)
	}

	// ── 3. Small context window ───────────────────────────────────────────────
	// ReAct grows the step history each turn. With 8 steps × ~600 tokens each,
	// the prompt can reach 5000+ tokens before the final Reflect call.
	if smallContextModel(provider, model) {
		maxSteps := def.Reasoning.MaxSteps
		if maxSteps <= 0 {
			maxSteps = 8
		}
		report.add(Warn, "reasoning.max_steps",
			fmt.Sprintf("model %q has a small context window (~4096 tokens); %d steps may overflow it mid-run", def.LLM.Model, maxSteps),
			"reduce max_steps to ≤ 4, or switch to a model with a larger context window",
			nil,
		)
	}

	// ── 4. Groq rate limit warning ────────────────────────────────────────────
	// Groq free tier: 6000 TPM. Each ReAct step ≈ 600–900 tokens output + input.
	// 8 steps + Plan + Reflect ≈ 8000–12000 tokens total — exceeds free tier.
	if provider == "groq" {
		maxSteps := def.Reasoning.MaxSteps
		if maxSteps <= 0 {
			maxSteps = 8
		}
		if maxSteps > 4 {
			report.add(Warn, "reasoning.max_steps",
				fmt.Sprintf("Groq free tier is 6000 TPM; %d ReAct steps (~%d tokens) will likely hit the rate limit mid-run", maxSteps, maxSteps*800),
				"reduce reasoning.max_steps to ≤ 4, or use a paid Groq plan",
				nil,
			)
		}
	}

	// ── 5. plan_execute with small max_plan_steps ─────────────────────────────
	if strategy == "plan_execute" {
		maxPlan := def.Reasoning.MaxPlanSteps
		if maxPlan <= 0 {
			maxPlan = 6
		}
		if maxPlan > 10 {
			report.add(Warn, "reasoning.max_plan_steps",
				fmt.Sprintf("max_plan_steps=%d is very high; large plans produce long prompts and often exceed context windows", maxPlan),
				"keep max_plan_steps ≤ 8 for reliable execution",
				nil,
			)
		}
	}

	// ── 6. Timeouts not set ───────────────────────────────────────────────────
	if strings.TrimSpace(def.Reasoning.StepTimeout) == "" {
		report.add(Warn, "reasoning.step_timeout",
			"step_timeout not set; defaults to 30s per step which may be too short for slow providers",
			"set reasoning.step_timeout explicitly, e.g. '45s' for Groq, '120s' for large local models",
			nil,
		)
	}
	if strings.TrimSpace(def.Reasoning.TotalTimeout) == "" {
		report.add(Warn, "reasoning.total_timeout",
			"total_timeout not set; defaults to 180s which may be insufficient for plan_execute with many steps",
			"set reasoning.total_timeout, e.g. '300s' for plan_execute or complex research tasks",
			nil,
		)
	}

	// ── 7. No tools declared ──────────────────────────────────────────────────
	// A ReAct loop without any tools will spin: Think always returns IsDone=false
	// because there's nothing to act on, exhausting MaxSteps immediately.
	if strategy == "react" && len(def.Tools) == 0 &&
		def.MCPServers == nil && def.MCPTools == nil &&
		(def.Builtins == nil || len(*def.Builtins) == 0) {
		report.add(Warn, "reasoning.strategy",
			"ReAct strategy declared but no tools are configured; the loop has nothing to act on and will exhaust MaxSteps immediately",
			"add at least one tool (web_search, memory_read, a Python tool, or an MCP server) for the ReAct loop to be useful",
			nil,
		)
	}
}

// ─── Reasoning-specific model checks ─────────────────────────────────────────

// weakJSONModel returns true for models known to produce unreliable JSON even
// when format:"json" / response_format is set.
func weakJSONModel(provider, model string) bool {
	m := strings.ToLower(model)
	// Embedding models handled separately by isEmbeddingModel().
	// These are small or poorly instruction-tuned chat models.
	weak := []string{
		"llama3.2:1b", "llama3.2:3b", // tiny llamas
		"gemma:2b", "gemma2:2b", // very small gemmas
		"phi3:mini",   // 3.8B, weak JSON
		"mistral:7b",  // 7B mistral, unreliable JSON
		"neural-chat", // Intel neural chat
		"stablelm",    // StableLM
		"tinyllama",   // obvious
	}
	for _, w := range weak {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// smallContextModel returns true for models with context windows too small
// to reliably hold a full ReAct step history (< ~8k tokens).
func smallContextModel(provider, model string) bool {
	m := strings.ToLower(model)
	small := []string{
		"llama3.2:1b", "llama3.2:3b",
		"gemma:2b", "gemma2:2b",
		"phi3:mini",
		"mistral:7b", // base mistral has 8k but older quantisations are 4k
		"tinyllama",
	}
	for _, s := range small {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// isEmbeddingModel returns true for models that generate vectors, not text.
func isEmbeddingModel(model string) bool {
	m := strings.ToLower(model)
	for _, marker := range []string{"embed", "nomic", "bge", "e5-", "minilm", "sentence"} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// reasoningModelSuggestions returns provider-appropriate model suggestions
// for the reasoning loop.
func reasoningModelSuggestions(provider string) []string {
	switch provider {
	case "anthropic":
		return []string{"claude-sonnet-4-6", "claude-haiku-4-5-20251001"}
	case "openai":
		return []string{"gpt-4o", "gpt-4o-mini"}
	case "groq":
		return []string{"llama-3.3-70b-versatile", "mixtral-8x7b-32768"}
	default: // ollama
		return []string{"qwen2.5:72b", "qwen2.5:32b", "gemma4:latest", "llama3.3:70b"}
	}
}

// deliveryPromptRe matches system-prompt language that implies the agent is
// expected to send its result to a communication channel. Used to warn when a
// cron agent describes delivery in prose but has no schedule.output wired, so
// the result silently goes nowhere.
var deliveryPromptRe = regexp.MustCompile(`(?i)\b(send|deliver|post|notify|message|email|dm|report (?:to|it)|share (?:it|the))\b[^.\n]{0,40}\b(telegram|slack|discord|whatsapp|channel|chat|inbox|email|me|team|user|group|recipient)\b`)

// simpleChannelRe catches a bare channel name mentioned as a destination even
// without a verb nearby (e.g. "Output: Telegram").
var simpleChannelRe = regexp.MustCompile(`(?i)\b(to|on|via|through|output(?:s)?|deliver(?:y|ed)?)\b[^.\n]{0,20}\b(telegram|slack|discord|whatsapp)\b`)

// PromptImpliesDelivery reports whether a system prompt appears to ask the agent
// to deliver its output to a channel. Intentionally conservative — it only
// drives a warning, never a hard error.
func PromptImpliesDelivery(prompt string) bool {
	p := strings.TrimSpace(prompt)
	if p == "" {
		return false
	}
	return deliveryPromptRe.MatchString(p) || simpleChannelRe.MatchString(p)
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
