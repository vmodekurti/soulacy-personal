package gateway

import (
	"testing"

	"github.com/soulacy/soulacy/internal/workspacesettings"
)

func stringPtr(value string) *string { return &value }

func TestApplyModelSelectionClearsModelWhenProviderChanges(t *testing.T) {
	selected := workspacesettings.ProviderModel{Provider: "nvidia", Model: "old-model"}
	applyModelSelection(&selected, &modelPatch{Provider: stringPtr(" OpenAI ")})
	if selected.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", selected.Provider)
	}
	if selected.Model != "" {
		t.Fatalf("model = %q, want cleared model after provider switch", selected.Model)
	}
}

func TestApplyModelSelectionCanSwitchProviderAndModelAtomically(t *testing.T) {
	selected := workspacesettings.ProviderModel{Provider: "nvidia", Model: "old-model"}
	applyModelSelection(&selected, &modelPatch{
		Provider: stringPtr("OpenAI"),
		Model:    stringPtr(" gpt-4.1-mini "),
	})
	if selected.Provider != "openai" || selected.Model != "gpt-4.1-mini" {
		t.Fatalf("selection = %#v", selected)
	}
}
