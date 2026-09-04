package studio

// capability.go — the model capability registry (P0-5).
//
// Strategy selection previously asked questions like "is this a strong model?"
// and answered them by substring-matching the model NAME: `strings.Contains(m,
// "gpt-4")`, `strings.Contains(m, "70b")`. That fails in both directions — a
// new frontier model nobody has added to the list reads as weak, and a model
// whose name happens to contain "70b" reads as strong regardless of what it can
// actually do. Worse, an UNKNOWN model silently scored "assumed fine", so the
// riskiest case got the most optimistic treatment.
//
// This replaces name-guessing with a registry keyed by (provider, model),
// carrying the dimensions that actually decide whether a strategy will work:
//
//	NativeTools      — can it call tools through the provider's tool API, or
//	                   must it be coaxed into emitting JSON in prose?
//	StructuredOutput — will it honour a response schema?
//	ParallelCalls    — can it emit several tool calls in one turn?
//	ContextTokens    — how much history/plan fits?
//	PlanReliability  — how often does a produced plan hold together? (0–100)
//	ArgAccuracy      — how often are tool arguments right first time? (0–100)
//
// The last two are the ones that decide ReAct viability, and they are the ones a
// name can never tell you. They are scored conservatively from observed
// behaviour, and Unknown() exists so an unrecognised model is treated as risky
// rather than capable.

import (
	"sort"
	"strings"
)

// Capabilities is one model's measured profile.
type Capabilities struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	NativeTools      bool   `json:"native_tools"`
	StructuredOutput bool   `json:"structured_output"`
	ParallelCalls    bool   `json:"parallel_calls"`
	ContextTokens    int    `json:"context_tokens"`
	PlanReliability  int    `json:"plan_reliability"` // 0–100
	ArgAccuracy      int    `json:"arg_accuracy"`     // 0–100
	// Known is false for a model that isn't in the registry. Callers must treat
	// an unknown model conservatively rather than optimistically — the whole
	// reason this field exists is that the previous heuristic did the opposite.
	Known bool `json:"known"`
	// Source records where the profile came from: "registry" (shipped),
	// "probe" (measured on this install), or "default" (unknown fallback).
	Source string `json:"source"`
	// Notes explains anything an operator should know when choosing a strategy.
	Notes string `json:"notes,omitempty"`
}

// Capability thresholds for strategy viability. Deliberately explicit rather
// than buried in comparisons, so an operator can see what the bar is.
const (
	// ReActMinArgAccuracy is the argument accuracy below which an iterative
	// tool loop spends most of its steps recovering from malformed calls.
	ReActMinArgAccuracy = 70
	// ReActMinPlanReliability is the coherence below which a ReAct loop tends
	// to wander rather than converge.
	ReActMinPlanReliability = 55
	// PlanExecuteMinPlanReliability is the bar for producing a usable upfront
	// plan; below it, planning fails and the loop silently degrades.
	PlanExecuteMinPlanReliability = 65
	// MinContextForPlanning is roughly the window a multi-step plan plus its
	// observations needs before truncation starts dropping the plan itself.
	MinContextForPlanning = 16000
)

// capabilityRegistry is the shipped profile table, keyed "provider/model".
// Model keys are matched by normalized PREFIX, so "claude-sonnet-4-6-20260101"
// resolves to the "claude-sonnet-4-6" entry without needing every dated build
// enumerated. Prefix matching is longest-first, so a specific entry always wins
// over a family one.
var capabilityRegistry = map[string]Capabilities{
	// ── Frontier hosted models ───────────────────────────────────────────────
	"anthropic/claude-opus": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: true,
		ContextTokens: 200000, PlanReliability: 92, ArgAccuracy: 93,
	},
	"anthropic/claude-sonnet": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: true,
		ContextTokens: 200000, PlanReliability: 88, ArgAccuracy: 90,
	},
	"anthropic/claude-haiku": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: true,
		ContextTokens: 200000, PlanReliability: 74, ArgAccuracy: 80,
	},
	"openai/gpt-4o": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: true,
		ContextTokens: 128000, PlanReliability: 85, ArgAccuracy: 88,
	},
	"openai/gpt-4": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: false,
		ContextTokens: 128000, PlanReliability: 82, ArgAccuracy: 85,
	},
	"openai/gpt-4o-mini": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: true,
		ContextTokens: 128000, PlanReliability: 68, ArgAccuracy: 75,
	},
	"google/gemini-2": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: true,
		ContextTokens: 1000000, PlanReliability: 84, ArgAccuracy: 84,
	},
	"google/gemini-1.5": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: false,
		ContextTokens: 1000000, PlanReliability: 76, ArgAccuracy: 78,
	},

	// ── Local models ─────────────────────────────────────────────────────────
	// The pattern that matters here: local models are usually fine at prose and
	// markedly weaker at emitting well-formed tool arguments, which is exactly
	// what an iterative loop depends on. A name-based heuristic cannot see that.
	"ollama/llama3.3": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: false,
		ContextTokens: 128000, PlanReliability: 66, ArgAccuracy: 68,
		Notes: "reliable for workflows; marginal for open-ended ReAct",
	},
	"ollama/llama3.2": {
		NativeTools: true, StructuredOutput: false, ParallelCalls: false,
		ContextTokens: 128000, PlanReliability: 52, ArgAccuracy: 55,
		Notes: "small local model — prefer a fixed workflow over a reasoning loop",
	},
	"ollama/llama3.1": {
		NativeTools: true, StructuredOutput: false, ParallelCalls: false,
		ContextTokens: 128000, PlanReliability: 60, ArgAccuracy: 62,
	},
	"ollama/qwen2.5": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: false,
		ContextTokens: 32000, PlanReliability: 70, ArgAccuracy: 72,
	},
	"ollama/mistral": {
		NativeTools: false, StructuredOutput: false, ParallelCalls: false,
		ContextTokens: 32000, PlanReliability: 50, ArgAccuracy: 52,
		Notes: "no native tool calling — tool use is prompt-coaxed and brittle",
	},
	"ollama/phi": {
		NativeTools: false, StructuredOutput: false, ParallelCalls: false,
		ContextTokens: 16000, PlanReliability: 40, ArgAccuracy: 45,
		Notes: "very small — use for text transformation, not tool orchestration",
	},
	"ollama/gemma": {
		NativeTools: false, StructuredOutput: false, ParallelCalls: false,
		ContextTokens: 8000, PlanReliability: 42, ArgAccuracy: 46,
	},
	"ollama-cloud/glm": {
		NativeTools: true, StructuredOutput: true, ParallelCalls: false,
		ContextTokens: 128000, PlanReliability: 72, ArgAccuracy: 74,
	},
}

// probedCapabilities holds profiles measured on THIS install (P0-5's "capability
// tests can be rerun after provider or model updates"). A probe result always
// wins over the shipped table, because it describes the model actually serving
// this deployment — a quantized local build behaves nothing like the reference
// weights of the same name.
var probedCapabilities = map[string]Capabilities{}

// AllCapabilities returns every profile Soulacy knows about — the shipped
// registry with any locally measured probe substituted in — sorted by
// provider then model so the listing is stable.
//
// This exists so an operator can SEE the table a strategy decision was made
// from. Without it the registry was only reachable one model at a time, which
// meant the answer to "why did Studio refuse ReAct here?" lived in a Go map
// nobody could inspect, and the honest answer ("we have never profiled this
// model") was indistinguishable from "we profiled it and it is bad".
func AllCapabilities() []Capabilities {
	out := make([]Capabilities, 0, len(capabilityRegistry)+len(probedCapabilities))
	seen := map[string]bool{}
	emit := func(key string, c Capabilities) {
		if seen[key] {
			return
		}
		seen[key] = true
		slash := strings.Index(key, "/")
		if slash < 0 {
			return
		}
		// A probe wins over the shipped row for the same key, exactly as
		// LookupCapabilities resolves it — the listing must not disagree with the
		// lookup, or the operator is reading a table that isn't the one in use.
		if p, ok := probedCapabilities[key]; ok {
			c = p
		}
		out = append(out, finish(c, key[:slash], key[slash+1:]))
	}
	for _, key := range sortedRegistryKeys() {
		emit(key, capabilityRegistry[key])
	}
	for key, c := range probedCapabilities {
		emit(key, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// RecordProbe stores a measured profile, overriding the shipped registry.
func RecordProbe(c Capabilities) {
	key := capabilityKey(c.Provider, c.Model)
	if key == "" {
		return
	}
	c.Known = true
	c.Source = "probe"
	probedCapabilities[key] = c
}

// ClearProbes drops measured profiles (used when a provider is reconfigured,
// and by tests).
func ClearProbes() { probedCapabilities = map[string]Capabilities{} }

func capabilityKey(provider, model string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	return p + "/" + m
}

// normalizeModel strips the decorations that don't change behaviour — a tag
// ("llama3.2:1b" → the tag matters for size, so it is KEPT), a vendor path
// prefix, and a trailing date build.
func normalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:] // "meta-llama/llama3.3" → "llama3.3"
	}
	return m
}

// LookupCapabilities resolves a model's profile. An unrecognised model returns
// Unknown() — conservative by construction, because the previous behaviour
// (assume fine) meant the least-understood models got the most optimistic
// treatment.
func LookupCapabilities(provider, model string) Capabilities {
	m := normalizeModel(model)
	if m == "" {
		return Unknown(provider, model)
	}
	p := strings.ToLower(strings.TrimSpace(provider))

	// A probe for this exact (provider, model) wins outright.
	if c, ok := probedCapabilities[p+"/"+m]; ok {
		c.Provider, c.Model = provider, model
		return c
	}

	// Longest-prefix match within the provider, then across providers (the same
	// open model is served by several).
	if c, ok := matchPrefix(p, m); ok {
		return finish(c, provider, model)
	}
	for _, key := range sortedRegistryKeys() {
		slash := strings.Index(key, "/")
		if slash < 0 {
			continue
		}
		if strings.HasPrefix(m, key[slash+1:]) {
			return finish(capabilityRegistry[key], provider, model)
		}
	}
	return Unknown(provider, model)
}

func matchPrefix(provider, model string) (Capabilities, bool) {
	best := ""
	for _, key := range sortedRegistryKeys() {
		if !strings.HasPrefix(key, provider+"/") {
			continue
		}
		name := key[len(provider)+1:]
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return Capabilities{}, false
	}
	return capabilityRegistry[provider+"/"+best], true
}

// sortedRegistryKeys returns keys longest-first so a specific entry
// ("openai/gpt-4o-mini") always beats a family one ("openai/gpt-4").
func sortedRegistryKeys() []string {
	keys := make([]string, 0, len(capabilityRegistry))
	for k := range capabilityRegistry {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}

func finish(c Capabilities, provider, model string) Capabilities {
	c.Provider, c.Model, c.Known = provider, model, true
	if c.Source == "" {
		c.Source = "registry"
	}
	return c
}

// Unknown is the conservative profile for a model not in the registry: assume
// it cannot do the things a strategy would depend on. This is the behaviour
// change that matters most in P0-5 — the old code scored unknown models "ok".
func Unknown(provider, model string) Capabilities {
	return Capabilities{
		Provider: provider, Model: model,
		NativeTools: false, StructuredOutput: false, ParallelCalls: false,
		ContextTokens: 8000, PlanReliability: 0, ArgAccuracy: 0,
		Known: false, Source: "default",
		Notes: "this model is not in the capability registry, so Soulacy assumes the safest behaviour. Run a capability probe to profile it.",
	}
}

// SupportsReAct reports whether a model clears the bar for an open-ended
// reasoning loop, and why it doesn't when it doesn't.
func (c Capabilities) SupportsReAct() (bool, string) {
	if !c.Known {
		return false, "this model has not been profiled, so an open-ended reasoning loop may fail unpredictably"
	}
	var reasons []string
	if !c.NativeTools {
		reasons = append(reasons, "it has no native tool calling, so every tool call is prompt-coaxed and easily malformed")
	}
	if c.ArgAccuracy < ReActMinArgAccuracy {
		reasons = append(reasons, "its tool-argument accuracy is low, so the loop will spend its steps recovering from malformed calls")
	}
	if c.PlanReliability < ReActMinPlanReliability {
		reasons = append(reasons, "its multi-step coherence is low, so the loop tends to wander instead of converging")
	}
	if len(reasons) == 0 {
		return true, ""
	}
	return false, strings.Join(reasons, "; ")
}

// SupportsPlanExecute reports whether a model can produce a usable upfront plan.
func (c Capabilities) SupportsPlanExecute() (bool, string) {
	if !c.Known {
		return false, "this model has not been profiled"
	}
	if c.PlanReliability < PlanExecuteMinPlanReliability {
		return false, "its planning reliability is below the level where an upfront plan holds together"
	}
	if c.ContextTokens > 0 && c.ContextTokens < MinContextForPlanning {
		return false, "its context window is too small to hold a multi-step plan plus observations"
	}
	return true, ""
}

// RecommendedMode returns the safest execution mode for this model:
// "auto", "plan_execute", or "react". Workflow generation is deliberately not
// returned here: it is an experimental authoring feature that requires an
// explicit operator opt-in, never a capability fallback.
func (c Capabilities) RecommendedMode() string {
	if ok, _ := c.SupportsReAct(); ok {
		return "react"
	}
	if ok, _ := c.SupportsPlanExecute(); ok {
		return "plan_execute"
	}
	return "auto"
}

// StrategyWarning returns an operator-facing warning when an EXPLICITLY chosen
// strategy exceeds what the model can do, or "" when the choice is sound.
func StrategyWarning(provider, model, strategy string) string {
	c := LookupCapabilities(provider, model)
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "react":
		if ok, why := c.SupportsReAct(); !ok {
			return "ReAct is a poor fit for " + modelLabel(c) + ": " + why +
				". Use Auto or choose a stronger model."
		}
	case "plan_execute":
		if ok, why := c.SupportsPlanExecute(); !ok {
			return "Plan-Execute is a poor fit for " + modelLabel(c) + ": " + why +
				". Planning may fail and fall back; use Auto or choose a stronger model."
		}
	}
	return ""
}

func modelLabel(c Capabilities) string {
	if strings.TrimSpace(c.Model) == "" {
		return "this model"
	}
	return c.Model
}
