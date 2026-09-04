package studio

import (
	"strings"
	"testing"
)

func TestLookupCapabilities_KnownModels(t *testing.T) {
	// Dated build suffixes must resolve to the family entry rather than falling
	// off the registry — otherwise every model release silently becomes unknown.
	c := LookupCapabilities("anthropic", "claude-sonnet-4-6-20260101")
	if !c.Known || !c.NativeTools || c.ArgAccuracy < 85 {
		t.Fatalf("dated build should resolve to the family profile: %+v", c)
	}
	if c.Source != "registry" {
		t.Errorf("source = %q, want registry", c.Source)
	}

	// A more specific entry beats a family one.
	mini := LookupCapabilities("openai", "gpt-4o-mini")
	full := LookupCapabilities("openai", "gpt-4o")
	if mini.ArgAccuracy >= full.ArgAccuracy {
		t.Errorf("gpt-4o-mini must resolve to its own (weaker) profile, not gpt-4o: %+v", mini)
	}

	// A vendor path prefix is decoration, not identity.
	if c := LookupCapabilities("ollama", "meta-llama/llama3.3"); !c.Known {
		t.Errorf("a vendor-prefixed model name should still resolve: %+v", c)
	}
}

func TestUnknownModelsAreConservative(t *testing.T) {
	// The behaviour change that matters most: an unrecognised model used to
	// score "assumed fine", giving the least-understood models the most
	// optimistic treatment.
	c := LookupCapabilities("someprovider", "brand-new-model-9000")
	if c.Known {
		t.Fatal("an unrecognised model must not report as known")
	}
	if c.NativeTools || c.StructuredOutput || c.ParallelCalls {
		t.Errorf("unknown models must assume no capabilities: %+v", c)
	}
	if ok, why := c.SupportsReAct(); ok || why == "" {
		t.Error("an unprofiled model must not clear the ReAct bar, and must say why")
	}
	if c.RecommendedMode() != "auto" {
		t.Errorf("unknown models should use the non-workflow default, got %q", c.RecommendedMode())
	}
	if !strings.Contains(c.Notes, "capability probe") {
		t.Errorf("notes should tell the operator how to resolve it: %q", c.Notes)
	}
	// An empty model name is the same conservative case, not a panic.
	if LookupCapabilities("openai", "").Known {
		t.Error("an empty model must be unknown")
	}
}

func TestReActViability(t *testing.T) {
	// A capable hosted model clears the bar.
	if ok, why := LookupCapabilities("anthropic", "claude-sonnet-4-6").SupportsReAct(); !ok {
		t.Errorf("a frontier model should support ReAct: %s", why)
	}
	// A small local model does not — and the reason must be specific enough to
	// act on, not "unsuitable".
	ok, why := LookupCapabilities("ollama", "llama3.2:1b").SupportsReAct()
	if ok {
		t.Fatal("a small local model must not clear the ReAct bar")
	}
	if !strings.Contains(why, "argument") && !strings.Contains(why, "coherence") {
		t.Errorf("the reason should name the failing dimension: %q", why)
	}
	// A model with no native tool calling fails regardless of other scores.
	if ok, why := LookupCapabilities("ollama", "mistral").SupportsReAct(); ok || !strings.Contains(why, "native tool calling") {
		t.Errorf("no-native-tools must fail ReAct with that reason: %v / %q", ok, why)
	}
}

func TestStrategyWarning(t *testing.T) {
	// Explicit ReAct on a weak model warns (P0-5's fourth bullet).
	w := StrategyWarning("ollama", "phi3", "react")
	if w == "" {
		t.Fatal("explicit ReAct on a weak model must warn")
	}
	if !strings.Contains(w, "Auto") {
		t.Errorf("the warning should offer the safer alternative: %q", w)
	}
	// A sound choice is silent.
	if w := StrategyWarning("anthropic", "claude-sonnet-4-6", "react"); w != "" {
		t.Errorf("a capable model must not warn: %q", w)
	}
	// Plan-Execute has its own bar.
	if w := StrategyWarning("ollama", "gemma", "plan_execute"); w == "" {
		t.Error("plan_execute on a tiny-context model must warn")
	}
	// An unknown strategy name is not this function's business.
	if w := StrategyWarning("anthropic", "claude-sonnet-4-6", "workflow"); w != "" {
		t.Errorf("workflow mode needs no capability warning: %q", w)
	}
}

func TestProbeOverridesRegistry(t *testing.T) {
	defer ClearProbes()
	// A quantized local build behaves nothing like the reference weights of the
	// same name, so a measured profile must win over the shipped table.
	before := LookupCapabilities("ollama", "llama3.3")
	if !before.Known || before.Source != "registry" {
		t.Fatalf("precondition: %+v", before)
	}
	RecordProbe(Capabilities{
		Provider: "ollama", Model: "llama3.3",
		NativeTools: true, ArgAccuracy: 20, PlanReliability: 20, ContextTokens: 8000,
	})
	after := LookupCapabilities("ollama", "llama3.3")
	if after.Source != "probe" || after.ArgAccuracy != 20 {
		t.Fatalf("probe must override the registry: %+v", after)
	}
	if ok, _ := after.SupportsReAct(); ok {
		t.Error("the measured (poor) profile must now fail the ReAct bar")
	}
	ClearProbes()
	if LookupCapabilities("ollama", "llama3.3").Source != "registry" {
		t.Error("clearing probes must restore the shipped profile")
	}
}

func TestRecommendedMode(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-opus-4": "react",
		"ollama/llama3.3":         "plan_execute",
		"ollama/phi3":             "auto",
		"ollama/gemma2":           "auto",
	}
	for key, want := range cases {
		parts := strings.SplitN(key, "/", 2)
		if got := LookupCapabilities(parts[0], parts[1]).RecommendedMode(); got != want {
			t.Errorf("%s: mode = %q, want %q", key, got, want)
		}
	}
}

func TestAdviseStrategy_CapabilityGating(t *testing.T) {
	planIntent := "First plan the research, decompose it into steps, then execute the plan and write a report"

	// With a capable model, planning advice stands.
	strong := Catalog{Generation: &GenerationProfile{Provider: "anthropic", Model: "claude-sonnet-4-6"}}
	if a := AdviseStrategy(planIntent, strong, "", false); a.Mode != "plan_execute" {
		t.Errorf("a capable model should keep plan_execute, got %q (%s)", a.Mode, a.Reason)
	}

	// With a model that cannot hold a plan together, keep workflow generation off
	// and use Auto with a visible warning.
	weak := Catalog{Generation: &GenerationProfile{Provider: "ollama", Model: "gemma2"}}
	a := AdviseStrategy(planIntent, weak, "", false)
	if a.Mode != "auto" {
		t.Errorf("a weak model should be steered to auto, got %q (%s)", a.Mode, a.Reason)
	}
	if a.CapabilityWarning == "" {
		t.Error("the weaker fallback should explain why planning was not selected")
	}
	if a.Capabilities == nil || a.Capabilities.Model != "gemma2" {
		t.Errorf("advice should carry the profile it reasoned from: %+v", a.Capabilities)
	}

	// An EXPLICIT request is honoured, but warned about — the operator may know
	// something the registry doesn't, yet must not learn the hard way.
	a = AdviseStrategy(planIntent, weak, "plan_execute", false)
	if a.Mode != "plan_execute" {
		t.Errorf("an explicit request must be honoured, got %q", a.Mode)
	}
	if a.CapabilityWarning == "" {
		t.Error("an explicit request beyond the model's ability must warn")
	}
	if a.Confidence != "low" {
		t.Errorf("confidence should drop when warning, got %q", a.Confidence)
	}

	// An unprofiled model uses Auto deliberately rather than silently generating
	// an experimental workflow, and carries a warning.
	unknown := Catalog{Generation: &GenerationProfile{Provider: "acme", Model: "mystery-1"}}
	a = AdviseStrategy("answer questions about our docs", unknown, "", false)
	if a.Mode != "auto" || a.CapabilityWarning == "" {
		t.Errorf("an unprofiled model should use warned Auto: %+v", a)
	}

	// With NO model chosen, capability gating must not fire at all — that is a
	// different state from "a model we don't recognise".
	if a := AdviseStrategy(planIntent, Catalog{}, "", false); a.Mode != "plan_execute" {
		t.Errorf("intent-only advice must not be gated: %q (%s)", a.Mode, a.Reason)
	}
}
