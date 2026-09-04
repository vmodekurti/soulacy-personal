package studio

import "strings"

// ModelPreset holds runtime defaults tuned for a model class (Stories #23/#24).
// Local models are slower, so their agents need more generous timeouts than the
// engine's cloud-oriented defaults — otherwise a perfectly good run is killed
// mid-thought. Empty string fields mean "leave the engine default".
type ModelPreset struct {
	MaxTurns     int    `json:"max_turns"`
	RunTimeout   string `json:"run_timeout"`   // whole-run wall clock
	StepTimeout  string `json:"step_timeout"`  // per reasoning step
	TotalTimeout string `json:"total_timeout"` // whole reasoning task
}

// LocalPresetFor returns timeout/turn defaults appropriate for running an agent
// on the given LOCAL model. Pure + deterministic. Smaller models get the most
// generous timeouts (they're slowest token-for-token); capable local models get
// a moderate bump over cloud defaults. Returns the zero ModelPreset for a model
// we don't want to special-case (caller then keeps engine defaults).
func LocalPresetFor(model string) ModelPreset {
	if isSmallModel(model) && !isStrongModel(model) {
		// Small local model: be patient.
		return ModelPreset{MaxTurns: 10, RunTimeout: "900s", StepTimeout: "180s", TotalTimeout: "900s"}
	}
	// Any other local model: a solid, generous-but-bounded default.
	return ModelPreset{MaxTurns: 15, RunTimeout: "600s", StepTimeout: "120s", TotalTimeout: "600s"}
}

// LocalPresetName is a short, human label for the preset a model maps to, for
// display in the UI.
func LocalPresetName(model string) string {
	if strings.TrimSpace(model) == "" {
		return "local default"
	}
	if isSmallModel(model) && !isStrongModel(model) {
		return "compact local (patient timeouts)"
	}
	return "local default"
}

// Intent-named presets — Story 9 (Cohort B). The existing preset system
// classifies by model (compact / patient / etc.), which reads well to engineers
// but not to operators. These give the GUI three intent names — "fast local",
// "reliable local", "cloud quality" — that are thin wrappers over the model
// defaults so operators can express what they want without knowing the
// underlying model tier. Empty intent falls back to LocalPresetFor(model).
const (
	IntentPresetFastLocal     = "fast_local"
	IntentPresetReliableLocal = "reliable_local"
	IntentPresetCloudQuality  = "cloud_quality"
)

// IntentPreset is the catalog record surfaced to the GUI.
type IntentPreset struct {
	Name    string      `json:"name"`
	Label   string      `json:"label"`
	Detail  string      `json:"detail"`
	Preset  ModelPreset `json:"preset"`
	Default bool        `json:"default,omitempty"`
}

// ListIntentPresets returns the three named intent presets in a stable order,
// with the "reliable_local" entry flagged as the recommended default so the
// GUI can pre-select it out of the box.
func ListIntentPresets() []IntentPreset {
	return []IntentPreset{
		{
			Name:   IntentPresetFastLocal,
			Label:  "Fast local",
			Detail: "Optimised for latency on a local model. Fewer turns, shorter timeouts — the agent stops sooner rather than wandering.",
			Preset: ModelPreset{MaxTurns: 6, RunTimeout: "300s", StepTimeout: "60s", TotalTimeout: "300s"},
		},
		{
			Name:    IntentPresetReliableLocal,
			Label:   "Reliable local",
			Detail:  "Balanced for local models — same generous defaults Studio uses today, so tools have time to finish and repair loops can converge.",
			Preset:  ModelPreset{MaxTurns: 15, RunTimeout: "600s", StepTimeout: "120s", TotalTimeout: "600s"},
			Default: true,
		},
		{
			Name:   IntentPresetCloudQuality,
			Label:  "Cloud quality",
			Detail: "Longer step budget for cloud models that can plan multi-turn work. Set your provider first — this preset does not switch models.",
			Preset: ModelPreset{MaxTurns: 20, RunTimeout: "600s", StepTimeout: "180s", TotalTimeout: "1200s"},
		},
	}
}

// LookupIntentPreset returns the preset for a named intent. Unknown / empty
// intent returns the zero preset (caller falls back to LocalPresetFor).
func LookupIntentPreset(intent string) (ModelPreset, bool) {
	intent = strings.ToLower(strings.TrimSpace(intent))
	for _, p := range ListIntentPresets() {
		if p.Name == intent {
			return p.Preset, true
		}
	}
	return ModelPreset{}, false
}
