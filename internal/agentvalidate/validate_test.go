package agentvalidate

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/pkg/agent"
)

func TestDefinitionFlagsUnavailableModelWithAlternatives(t *testing.T) {
	def := &agent.Definition{
		ID:      "tool-agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "ollama", Model: "missing-model"},
		Tools:   []agent.ToolDef{{Name: "lookup", Inline: "print('ok')"}},
	}
	report := Definition(def, "", Options{
		Config: &config.Config{LLM: config.LLMConfig{
			DefaultProvider: "ollama",
			Providers: map[string]config.ProviderConfig{
				"ollama": {Model: "qwen2.5:72b"},
			},
		}},
		RegisteredProviders: []string{"ollama"},
		ProviderModels: map[string][]string{
			"ollama": {"nomic-embed-text:latest", "llama3.2:3b", "qwen2.5:72b"},
		},
	}, Report{})

	if !report.Valid {
		t.Fatalf("missing model should warn, not fail: %+v", report)
	}
	if report.Warnings == 0 {
		t.Fatalf("expected model warning, got %+v", report)
	}
	if !hasAlternative(report, "qwen2.5:72b") {
		t.Fatalf("expected qwen2.5:72b alternative, got %+v", report.Findings)
	}
}

func TestDefinitionFailsUnavailableModelWhenAuthoritative(t *testing.T) {
	def := &agent.Definition{
		ID:      "tool-agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "ollama", Model: "missing-model"},
	}
	report := Definition(def, "", Options{
		Config: &config.Config{LLM: config.LLMConfig{
			DefaultProvider: "ollama",
			Providers:       map[string]config.ProviderConfig{"ollama": {Model: "qwen2.5:72b"}},
		}},
		RegisteredProviders: []string{"ollama"},
		ProviderModels:      map[string][]string{"ollama": {"llama3.2:3b", "qwen2.5:72b"}},
		AuthoritativeModels: true, // live probe → escalate to hard error
	}, Report{})

	if report.Valid || report.Errors == 0 {
		t.Fatalf("authoritative model list should make a missing model a hard error, got %+v", report)
	}
}

func TestDefinitionFailsUnregisteredProvider(t *testing.T) {
	def := &agent.Definition{
		ID:      "agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "anthropic", Model: "claude-sonnet-4-5"},
	}
	report := Definition(def, "", Options{
		Config: &config.Config{LLM: config.LLMConfig{
			DefaultProvider: "ollama",
			Providers: map[string]config.ProviderConfig{
				"anthropic": {Model: "claude-sonnet-4-5"},
			},
		}},
		RegisteredProviders: []string{"ollama"},
	}, Report{})

	if report.Valid {
		t.Fatalf("expected invalid report for unregistered provider, got %+v", report)
	}
}

func TestDefinitionWarnsWeakToolModel(t *testing.T) {
	def := &agent.Definition{
		ID:      "tool-agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "ollama", Model: "llama3.2:3b"},
		Tools:   []agent.ToolDef{{Name: "lookup", Inline: "print('ok')"}},
	}
	report := Definition(def, "", Options{
		Config: &config.Config{LLM: config.LLMConfig{
			Providers: map[string]config.ProviderConfig{"ollama": {Model: "llama3.2:3b"}},
		}},
		RegisteredProviders: []string{"ollama"},
		ProviderModels:      map[string][]string{"ollama": {"llama3.2:3b", "qwen2.5:72b"}},
	}, Report{})

	if !hasFinding(report, "tool-heavy") {
		t.Fatalf("expected weak tool model warning, got %+v", report.Findings)
	}
}

func TestDefinitionWarnsRiskyBuiltinsWithoutConfirmTools(t *testing.T) {
	builtins := []string{"web_search", "shell_exec"}
	def := &agent.Definition{
		ID:       "risky-agent",
		Trigger:  agent.TriggerChannel,
		LLM:      agent.LLMConfig{Provider: "ollama", Model: "qwen2.5:72b"},
		Builtins: &builtins,
	}
	report := Definition(def, "", Options{}, Report{})
	if !report.Valid {
		t.Fatalf("risky builtins should warn, not fail: %+v", report)
	}
	if !hasFinding(report, "high-risk built-in tool(s)") {
		t.Fatalf("expected risky builtin confirmation warning, got %+v", report.Findings)
	}
}

func TestDefinitionDoesNotWarnRiskyBuiltinsWhenConfirmAll(t *testing.T) {
	builtins := []string{"web_search", "shell_exec"}
	def := &agent.Definition{
		ID:           "guarded-agent",
		Trigger:      agent.TriggerChannel,
		LLM:          agent.LLMConfig{Provider: "ollama", Model: "qwen2.5:72b"},
		Builtins:     &builtins,
		ConfirmTools: []string{"all"},
	}
	report := Definition(def, "", Options{}, Report{})
	if hasFinding(report, "high-risk built-in tool(s)") {
		t.Fatalf("confirm_tools: [all] should satisfy risky builtin warning, got %+v", report.Findings)
	}
}

func TestBytesRejectsLegacyTopLevelModelShape(t *testing.T) {
	report := Bytes([]byte(`
id: legacy
name: Legacy
trigger: channel
model: gpt-4o-mini
system_prompt: You are helpful.
channels: [http]
`), "SOUL.yaml", Options{})

	if report.Valid {
		t.Fatalf("expected legacy top-level model shape to be invalid: %+v", report)
	}
	if !hasFinding(report, "field model not found") {
		t.Fatalf("expected unknown model field finding, got %+v", report.Findings)
	}
}

func TestBytesRejectsLegacyTokenBudgetAndSessionShape(t *testing.T) {
	report := Bytes([]byte(`
id: legacy-budget
trigger: channel
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: You are helpful.
channels: [http]
token_budget:
  max_input_tokens: 32000
  max_output_tokens: 1024
session:
  history_turns: 20
`), "SOUL.yaml", Options{})

	if report.Valid {
		t.Fatalf("expected legacy token_budget/session shape to be invalid: %+v", report)
	}
	if !hasFinding(report, "field token_budget not found") {
		t.Fatalf("expected unknown token_budget finding, got %+v", report.Findings)
	}
	if !hasFinding(report, "field session not found") {
		t.Fatalf("expected unknown session finding, got %+v", report.Findings)
	}
}

func TestBytesRejectsListStyleTools(t *testing.T) {
	report := Bytes([]byte(`
id: list-tools
trigger: channel
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: You are helpful.
channels: [http]
tools:
  - web_search
`), "SOUL.yaml", Options{})

	if report.Valid {
		t.Fatalf("expected list-style tools to be invalid: %+v", report)
	}
	if !hasFinding(report, "cannot unmarshal !!str `web_search` into agent.ToolDef") {
		t.Fatalf("expected tool unmarshal finding, got %+v", report.Findings)
	}
}

func TestDefinitionValidatesScheduleOutputPairs(t *testing.T) {
	cases := []struct {
		name      string
		output    *agent.ScheduleOutput
		wantField string
	}{
		{
			name:      "to without channel",
			output:    &agent.ScheduleOutput{To: "123"},
			wantField: "schedule.output.channel",
		},
		{
			name:      "channel without to",
			output:    &agent.ScheduleOutput{Channel: "telegram"},
			wantField: "schedule.output.to",
		},
		{
			name:      "channel and to is valid",
			output:    &agent.ScheduleOutput{Channel: "telegram", To: "123"},
			wantField: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Definition(&agent.Definition{
				ID:      "scheduled",
				Trigger: agent.TriggerCron,
				Schedule: &agent.Schedule{
					Cron:   "0 8 * * *",
					Output: tc.output,
				},
				LLM: agent.LLMConfig{Provider: "openai", Model: "gpt-4o-mini"},
			}, "", Options{}, Report{})

			if tc.wantField == "" {
				if !report.Valid {
					t.Fatalf("expected valid schedule output, got %+v", report)
				}
				return
			}
			if report.Valid {
				t.Fatalf("expected invalid schedule output, got %+v", report)
			}
			if !hasFindingField(report, tc.wantField) {
				t.Fatalf("expected finding for %s, got %+v", tc.wantField, report.Findings)
			}
		})
	}
}

// ─── E4b (Cohort E — Schedule failure handling) — cron pre-validation ────────
//
// A cron-triggered agent used to save cleanly with `schedule.cron: * * *` or
// `* * * * * * *` (wrong field count). The scheduler's `AddFunc` would fail
// at registration time, `handleUpdateAgent` would `s.log.Warn` and return
// 200 OK, and the GUI would just show a blank "Next run" column forever. The
// pre-validation below fails the save with a message that includes the
// parser's own explanation of what's wrong.

func TestDefinitionRejectsInvalidCronExpression(t *testing.T) {
	cases := []struct {
		name string
		cron string
	}{
		{name: "too_few_fields", cron: "* * *"},
		{name: "too_many_fields", cron: "* * * * * * *"},
		{name: "gibberish", cron: "hello world"},
		{name: "bad_range", cron: "60 * * * *"}, // minute > 59
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Definition(&agent.Definition{
				ID:      "scheduled",
				Trigger: agent.TriggerCron,
				Schedule: &agent.Schedule{
					Cron:   tc.cron,
					Output: &agent.ScheduleOutput{Channel: "telegram", To: "123"},
				},
				LLM: agent.LLMConfig{Provider: "openai", Model: "gpt-4o-mini"},
			}, "", Options{}, Report{})

			if report.Valid {
				t.Fatalf("expected invalid cron %q to fail validation, got valid report: %+v", tc.cron, report.Findings)
			}
			if !hasFindingField(report, "schedule.cron") {
				t.Fatalf("expected finding on schedule.cron field, got %+v", report.Findings)
			}
		})
	}
}

func TestDefinitionAcceptsValidCronExpression(t *testing.T) {
	cases := []struct {
		name string
		cron string
	}{
		{name: "standard_five_field", cron: "0 9 * * 1-5"},
		{name: "wildcards", cron: "*/15 * * * *"},
		{name: "descriptor_daily", cron: "@daily"},
		{name: "descriptor_hourly", cron: "@hourly"},
		{name: "six_field_with_seconds", cron: "0 */30 * * * *"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := Definition(&agent.Definition{
				ID:      "scheduled",
				Trigger: agent.TriggerCron,
				Schedule: &agent.Schedule{
					Cron:   tc.cron,
					Output: &agent.ScheduleOutput{Channel: "telegram", To: "123"},
				},
				LLM: agent.LLMConfig{Provider: "openai", Model: "gpt-4o-mini"},
			}, "", Options{}, Report{})

			if !report.Valid {
				t.Fatalf("expected valid cron %q to pass, got errors: %+v", tc.cron, report.Findings)
			}
			// The schedule.cron field-specific finding must be absent for valid input.
			for _, f := range report.Findings {
				if f.Field == "schedule.cron" && f.Severity == Error {
					t.Fatalf("expected no schedule.cron error for %q, got %+v", tc.cron, f)
				}
			}
		})
	}
}

func hasAlternative(report Report, value string) bool {
	for _, finding := range report.Findings {
		for _, alt := range finding.Alternatives {
			if alt == value {
				return true
			}
		}
	}
	return false
}

func hasFindingField(report Report, field string) bool {
	for _, finding := range report.Findings {
		if finding.Field == field {
			return true
		}
	}
	return false
}

func hasFinding(report Report, needle string) bool {
	for _, finding := range report.Findings {
		if strings.Contains(finding.Message, needle) || strings.Contains(finding.Suggestion, needle) {
			return true
		}
	}
	return false
}

// ─── validateLocalPath ────────────────────────────────────────────────────────

func TestValidateLocalPathAbsolutePathIsSkipped(t *testing.T) {
	// Absolute paths are not checked — function returns immediately.
	report := &Report{}
	validateLocalPath(report, "tools[0].python_file", "/some/soul.yaml", "/absolute/path/tool.py")
	if report.Warnings != 0 || report.Errors != 0 {
		t.Fatalf("absolute path should produce no findings, got %+v", report.Findings)
	}
}

func TestValidateLocalPathEmptySoulPathIsSkipped(t *testing.T) {
	// Empty soulPath means no base dir to resolve against — return early.
	report := &Report{}
	validateLocalPath(report, "tools[0].python_file", "", "relative/tool.py")
	if report.Warnings != 0 || report.Errors != 0 {
		t.Fatalf("empty soulPath should produce no findings, got %+v", report.Findings)
	}
}

func TestValidateLocalPathRelativePathNotFound(t *testing.T) {
	// A relative path that doesn't exist should produce a warning.
	report := &Report{}
	validateLocalPath(report, "tools[0].python_file", "/nonexistent/soul.yaml", "no_such_file.py")
	if report.Warnings == 0 {
		t.Fatalf("missing relative path should produce a warning, got %+v", report.Findings)
	}
	if !hasFindingField(*report, "tools[0].python_file") {
		t.Fatalf("warning should reference tools[0].python_file field, got %+v", report.Findings)
	}
}

func TestValidateLocalPathTraversalNotFound(t *testing.T) {
	// Path traversal sequences are relative and checked for existence.
	report := &Report{}
	validateLocalPath(report, "tools[0].python_file", "/some/dir/soul.yaml", "../../secret.py")
	// The joined path won't exist, so we expect a warning.
	if report.Warnings == 0 {
		t.Fatalf("path traversal to non-existent file should produce a warning, got %+v", report.Findings)
	}
}

// ─── validateMCPList ─────────────────────────────────────────────────────────

func TestValidateMCPListNilIsOK(t *testing.T) {
	report := &Report{}
	validateMCPList(report, "mcp_servers", nil, false)
	if report.Errors != 0 || report.Warnings != 0 {
		t.Fatalf("nil list should produce no findings, got %+v", report.Findings)
	}
}

func TestValidateMCPListValidServerEntry(t *testing.T) {
	values := []string{"my-server"}
	report := &Report{}
	validateMCPList(report, "mcp_servers", &values, false)
	if report.Errors != 0 {
		t.Fatalf("valid server entry should produce no errors, got %+v", report.Findings)
	}
}

func TestValidateMCPListWildcardAndAllAreOK(t *testing.T) {
	for _, v := range []string{"*", "all", "ALL", "All"} {
		values := []string{v}
		report := &Report{}
		validateMCPList(report, "mcp_servers", &values, false)
		if report.Errors != 0 {
			t.Fatalf("entry %q should produce no errors, got %+v", v, report.Findings)
		}
	}
}

func TestValidateMCPListEmptyEntryIsError(t *testing.T) {
	values := []string{""}
	report := &Report{}
	validateMCPList(report, "mcp_servers", &values, false)
	if report.Errors == 0 {
		t.Fatalf("empty entry should produce an error, got %+v", report.Findings)
	}
}

func TestValidateMCPListWhitespaceEntryIsError(t *testing.T) {
	values := []string{"  "}
	report := &Report{}
	validateMCPList(report, "mcp_servers", &values, false)
	// Trimmed to "" → treated as empty.
	if report.Errors == 0 {
		t.Fatalf("whitespace-only entry should produce an error, got %+v", report.Findings)
	}
}

func TestValidateMCPListEntryWithInternalWhitespaceIsError(t *testing.T) {
	values := []string{"my server"}
	report := &Report{}
	validateMCPList(report, "mcp_servers", &values, false)
	if report.Errors == 0 {
		t.Fatalf("entry with internal whitespace should produce an error, got %+v", report.Findings)
	}
}

func TestValidateMCPListFullToolMissingPrefixIsError(t *testing.T) {
	// fullTool=true requires "mcp__" prefix.
	values := []string{"server__tool"}
	report := &Report{}
	validateMCPList(report, "mcp_tools", &values, true)
	if report.Errors == 0 {
		t.Fatalf("tool entry without mcp__ prefix should produce an error, got %+v", report.Findings)
	}
}

func TestValidateMCPListFullToolMissingSegmentsIsError(t *testing.T) {
	// fullTool=true requires at least mcp__server__tool (two "__" separators).
	values := []string{"mcp__onlyone"}
	report := &Report{}
	validateMCPList(report, "mcp_tools", &values, true)
	if report.Errors == 0 {
		t.Fatalf("tool entry without server+tool segments should produce an error, got %+v", report.Findings)
	}
}

func TestValidateMCPListValidFullToolEntry(t *testing.T) {
	values := []string{"mcp__myserver__mytool"}
	report := &Report{}
	validateMCPList(report, "mcp_tools", &values, true)
	if report.Errors != 0 {
		t.Fatalf("valid full tool entry should produce no errors, got %+v", report.Findings)
	}
}

// ─── validateReasoningFit ─────────────────────────────────────────────────────

func baseReasoningDef() *agent.Definition {
	return &agent.Definition{
		ID:      "reasoning-agent",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "openai", Model: "gpt-4o"},
		Tools:   []agent.ToolDef{{Name: "search", Inline: "print('ok')"}},
		Reasoning: agent.ReasoningConfig{
			Strategy:     "react",
			StepTimeout:  "30s",
			TotalTimeout: "180s",
		},
	}
}

func TestValidateReasoningFitNoStrategyNoBrainMemoryIsOK(t *testing.T) {
	def := &agent.Definition{
		ID:      "simple",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "openai", Model: "gpt-4o"},
	}
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if report.Errors != 0 || report.Warnings != 0 {
		t.Fatalf("no strategy + no brain memory should produce no findings, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitNoStrategyWithBrainMemoryWarns(t *testing.T) {
	def := &agent.Definition{
		ID:      "brain",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "openai", Model: "gpt-4o"},
		BrainMemory: agent.BrainMemoryConfig{
			Episodic: agent.EpisodicMemoryConfig{Enabled: true},
		},
	}
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if report.Warnings == 0 {
		t.Fatalf("brain_memory without strategy should warn, got %+v", report.Findings)
	}
	if !hasFindingField(*report, "brain_memory") {
		t.Fatalf("warning should be on brain_memory field, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitEmbeddingModelIsError(t *testing.T) {
	def := baseReasoningDef()
	def.LLM.Model = "nomic-embed-text:latest"
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if report.Errors == 0 {
		t.Fatalf("embedding model with reasoning strategy should produce an error, got %+v", report.Findings)
	}
	if !hasFinding(*report, "embedding model") {
		t.Fatalf("error message should mention embedding model, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitWeakJSONModelWarns(t *testing.T) {
	def := baseReasoningDef()
	def.LLM.Provider = "ollama"
	def.LLM.Model = "tinyllama"
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if !hasFinding(*report, "unreliable structured JSON") {
		t.Fatalf("weak JSON model should warn about unreliable structured JSON, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitSmallContextModelWarns(t *testing.T) {
	def := baseReasoningDef()
	def.LLM.Provider = "ollama"
	def.LLM.Model = "phi3:mini"
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if !hasFinding(*report, "small context window") {
		t.Fatalf("small context model should warn about small context window, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitGroqHighMaxStepsWarns(t *testing.T) {
	def := baseReasoningDef()
	def.LLM.Provider = "groq"
	def.LLM.Model = "llama-3.3-70b-versatile"
	def.Reasoning.MaxSteps = 8
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if !hasFinding(*report, "rate limit") {
		t.Fatalf("Groq with >4 max_steps should warn about rate limit, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitGroqLowMaxStepsOK(t *testing.T) {
	def := baseReasoningDef()
	def.LLM.Provider = "groq"
	def.LLM.Model = "llama-3.3-70b-versatile"
	def.Reasoning.MaxSteps = 4
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	for _, f := range report.Findings {
		if strings.Contains(f.Message, "rate limit") {
			t.Fatalf("Groq with <=4 max_steps should not warn about rate limit, got %+v", report.Findings)
		}
	}
}

func TestValidateReasoningFitPlanExecuteHighPlanStepsWarns(t *testing.T) {
	def := baseReasoningDef()
	def.Reasoning.Strategy = "plan_execute"
	def.Reasoning.MaxPlanSteps = 15
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if !hasFindingField(*report, "reasoning.max_plan_steps") {
		t.Fatalf("plan_execute with max_plan_steps>10 should warn, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitMissingTimeoutsWarn(t *testing.T) {
	def := baseReasoningDef()
	def.Reasoning.StepTimeout = ""
	def.Reasoning.TotalTimeout = ""
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if !hasFindingField(*report, "reasoning.step_timeout") {
		t.Fatalf("missing step_timeout should warn, got %+v", report.Findings)
	}
	if !hasFindingField(*report, "reasoning.total_timeout") {
		t.Fatalf("missing total_timeout should warn, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitReactWithNoToolsWarns(t *testing.T) {
	def := &agent.Definition{
		ID:      "no-tools",
		Trigger: agent.TriggerChannel,
		LLM:     agent.LLMConfig{Provider: "openai", Model: "gpt-4o"},
		Reasoning: agent.ReasoningConfig{
			Strategy:     "react",
			StepTimeout:  "30s",
			TotalTimeout: "180s",
		},
	}
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	if !hasFinding(*report, "nothing to act on") {
		t.Fatalf("react without tools should warn about nothing to act on, got %+v", report.Findings)
	}
}

func TestValidateReasoningFitReactWithToolsOK(t *testing.T) {
	def := baseReasoningDef() // has Tools
	report := &Report{}
	validateReasoningFit(report, def, Options{})
	for _, f := range report.Findings {
		if strings.Contains(f.Message, "nothing to act on") {
			t.Fatalf("react with tools should not warn about nothing to act on, got %+v", report.Findings)
		}
	}
}

// ─── weakStructuredOutputModel ────────────────────────────────────────────────

func TestWeakStructuredOutputModel(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     bool
	}{
		// Embedding models are weak (only "embed" and "nomic" prefixes are checked).
		{"ollama", "nomic-embed-text:latest", true},
		{"ollama", "bge-large", false}, // "bge" alone is not in the weak list
		// Small/weak chat models are weak.
		{"ollama", "llama3.2:1b", true},
		{"ollama", "llama3.2:3b", true},
		{"ollama", "gemma:2b", true},
		{"ollama", "gemma2:2b", true},
		{"ollama", "mistral:7b", true},
		// Strong models are not weak.
		{"openai", "gpt-4o", false},
		{"anthropic", "claude-sonnet-4-6", false},
		{"ollama", "qwen2.5:72b", false},
		{"groq", "llama-3.3-70b-versatile", false},
	}
	for _, tc := range cases {
		got := weakStructuredOutputModel(tc.provider, tc.model)
		if got != tc.want {
			t.Errorf("weakStructuredOutputModel(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// ─── weakJSONModel ────────────────────────────────────────────────────────────

func TestWeakJSONModel(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     bool
	}{
		// Explicitly listed weak models.
		{"ollama", "llama3.2:1b", true},
		{"ollama", "llama3.2:3b", true},
		{"ollama", "gemma:2b", true},
		{"ollama", "gemma2:2b", true},
		{"ollama", "phi3:mini", true},
		{"ollama", "mistral:7b", true},
		{"ollama", "neural-chat", true},
		{"ollama", "stablelm", true},
		{"ollama", "tinyllama", true},
		// Substring matching (model names can have tags).
		{"ollama", "tinyllama:latest", true},
		{"ollama", "phi3:mini-4k", true},
		// Strong models are not weak.
		{"openai", "gpt-4o", false},
		{"anthropic", "claude-sonnet-4-6", false},
		{"ollama", "qwen2.5:72b", false},
		{"ollama", "llama3.3:70b", false},
	}
	for _, tc := range cases {
		got := weakJSONModel(tc.provider, tc.model)
		if got != tc.want {
			t.Errorf("weakJSONModel(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// ─── smallContextModel ────────────────────────────────────────────────────────

func TestSmallContextModel(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     bool
	}{
		{"ollama", "llama3.2:1b", true},
		{"ollama", "llama3.2:3b", true},
		{"ollama", "gemma:2b", true},
		{"ollama", "gemma2:2b", true},
		{"ollama", "phi3:mini", true},
		{"ollama", "mistral:7b", true},
		{"ollama", "tinyllama", true},
		// Substring matching.
		{"ollama", "tinyllama:latest", true},
		// Large/capable models are not small context.
		{"openai", "gpt-4o", false},
		{"anthropic", "claude-sonnet-4-6", false},
		{"ollama", "qwen2.5:72b", false},
		{"ollama", "llama3.3:70b", false},
		{"groq", "mixtral-8x7b-32768", false},
	}
	for _, tc := range cases {
		got := smallContextModel(tc.provider, tc.model)
		if got != tc.want {
			t.Errorf("smallContextModel(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// ─── isEmbeddingModel ─────────────────────────────────────────────────────────

func TestIsEmbeddingModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// Positive: each marker substring.
		{"nomic-embed-text:latest", true},
		{"mxbai-embed-large", true},
		{"bge-large-en", true},
		{"e5-small-v2", true},
		{"all-minilm", true},
		{"sentence-transformers/all-MiniLM-L6-v2", true},
		// Positive: uppercase normalised.
		{"NOMIC-EMBED", true},
		{"BGE-M3", true},
		// Negative: regular chat/instruction models.
		{"gpt-4o", false},
		{"claude-sonnet-4-6", false},
		{"qwen2.5:72b", false},
		{"llama3.3:70b", false},
		{"gemma4:latest", false},
		// Edge: model name that contains "sentence" but is a chat model.
		{"sentence-level-classifier", true}, // still matches "sentence" marker
	}
	for _, tc := range cases {
		got := isEmbeddingModel(tc.model)
		if got != tc.want {
			t.Errorf("isEmbeddingModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}
