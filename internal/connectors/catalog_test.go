package connectors

import (
	"strings"
	"testing"

	builtinskills "github.com/soulacy/soulacy/internal/skills/builtin"
)

func TestCatalogDefinitionsAreSafeAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Catalog() {
		if d.ID == "" || d.Name == "" || d.Provider == "" || d.Category == "" {
			t.Fatalf("connector has missing identity fields: %+v", d)
		}
		if seen[d.ID] {
			t.Fatalf("duplicate connector id %q", d.ID)
		}
		seen[d.ID] = true
		if !strings.HasPrefix(d.DocsURL, "https://") {
			t.Errorf("%s docs URL must use HTTPS: %q", d.ID, d.DocsURL)
		}
		if len(d.Capabilities) == 0 || len(d.SetupSteps) == 0 {
			t.Errorf("%s needs capabilities and setup steps", d.ID)
		}
		if len(d.RecommendedSkills) == 0 {
			t.Errorf("%s needs at least one recommended skill", d.ID)
		}
		for _, c := range d.Capabilities {
			if c.Effect != EffectRead && c.Effect != EffectWrite {
				t.Errorf("%s capability %s has unknown effect %q", d.ID, c.ID, c.Effect)
			}
		}
		for _, credential := range d.Credentials {
			if credential.Name == "" || strings.Contains(strings.ToLower(credential.Name), "value") {
				t.Errorf("%s has an unsafe credential descriptor: %+v", d.ID, credential)
			}
		}
	}
}

func TestFilterMatchesCategoryProviderAndCapabilities(t *testing.T) {
	if got := Filter("", "events"); len(got) != 2 {
		t.Fatalf("events count = %d, want 2", len(got))
	}
	got := Filter("barcode", "")
	if len(got) != 1 || got[0].ID != "open-food-facts" {
		t.Fatalf("barcode search = %+v", got)
	}
	got = Filter("ticketmaster", "shopping")
	if len(got) != 0 {
		t.Fatalf("combined filters should return none: %+v", got)
	}
}

func TestCatalogReturnsIndependentSlice(t *testing.T) {
	first := Catalog()
	first[0].Name = "changed"
	if Catalog()[0].Name == "changed" {
		t.Fatal("Catalog returned its backing slice")
	}
}

func TestRecommendedSkillsExistInBuiltinCatalog(t *testing.T) {
	available := map[string]bool{}
	for _, name := range builtinskills.Names() {
		available[name] = true
	}
	for _, connector := range Catalog() {
		for _, name := range connector.RecommendedSkills {
			if !available[name] {
				t.Errorf("%s recommends unknown built-in skill %q", connector.ID, name)
			}
		}
	}
}
