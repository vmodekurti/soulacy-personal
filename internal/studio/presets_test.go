package studio

import "testing"

func TestLocalPresetFor(t *testing.T) {
	small := LocalPresetFor("llama3:8b")
	big := LocalPresetFor("llama3:70b")
	if small.RunTimeout != "900s" {
		t.Errorf("small local model should get the most patient timeout, got %q", small.RunTimeout)
	}
	if big.RunTimeout != "600s" {
		t.Errorf("capable local model should get standard local timeout, got %q", big.RunTimeout)
	}
	if small.StepTimeout == "" || big.StepTimeout == "" {
		t.Error("presets should set a step timeout")
	}
}

// Story 9 (Cohort B) — the intent-named preset catalog exposes three named
// intents to the GUI, one flagged as the recommended default. Order and
// defaults must stay stable so the picker doesn't jump around between builds.
func TestListIntentPresets(t *testing.T) {
	presets := ListIntentPresets()
	if len(presets) != 3 {
		t.Fatalf("expected 3 intent presets, got %d", len(presets))
	}
	names := []string{presets[0].Name, presets[1].Name, presets[2].Name}
	want := []string{IntentPresetFastLocal, IntentPresetReliableLocal, IntentPresetCloudQuality}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("preset[%d].Name = %q, want %q", i, names[i], w)
		}
	}
	defaults := 0
	for _, p := range presets {
		if p.Default {
			defaults++
		}
		if p.Preset.MaxTurns <= 0 || p.Preset.StepTimeout == "" || p.Preset.TotalTimeout == "" {
			t.Errorf("preset %q is missing timeout / max_turns fields: %+v", p.Name, p.Preset)
		}
		if p.Label == "" || p.Detail == "" {
			t.Errorf("preset %q needs a Label + Detail for the GUI", p.Name)
		}
	}
	if defaults != 1 {
		t.Errorf("exactly one preset should be flagged Default, got %d", defaults)
	}
}

func TestLookupIntentPreset(t *testing.T) {
	if _, ok := LookupIntentPreset(""); ok {
		t.Error("empty intent should not resolve to a preset")
	}
	if _, ok := LookupIntentPreset("nonexistent"); ok {
		t.Error("unknown intent should not resolve to a preset")
	}
	fast, ok := LookupIntentPreset("Fast_Local")
	if !ok || fast.MaxTurns == 0 {
		t.Errorf("fast_local should resolve case-insensitively: %+v ok=%v", fast, ok)
	}
}
