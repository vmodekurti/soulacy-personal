package connectors

import (
	"path/filepath"
	"testing"
)

func TestBuildPlanSuggestsSitesAndKeepsAuthenticationOptional(t *testing.T) {
	plan, err := BuildPlan("Build a shopping connector for eBay and Etsy")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Category != "shopping" {
		t.Fatalf("category=%q", plan.Category)
	}
	selected := map[string]bool{}
	for _, suggestion := range plan.Sites {
		if suggestion.Selected {
			selected[suggestion.Name] = true
		}
		if !suggestion.PublicAccess || suggestion.AuthRequirement != SiteAuthOptional {
			t.Fatalf("site access is not public-first: %+v", suggestion.Site)
		}
		if len(suggestion.PublicCapabilities) == 0 || len(suggestion.AdvancedCapabilities) == 0 {
			t.Fatalf("site capabilities are incomplete: %+v", suggestion.Site)
		}
	}
	if !selected["eBay"] || !selected["Etsy"] {
		t.Fatalf("named sites were not selected: %v", selected)
	}
}

func TestNewCustomSiteRejectsPrivateTargets(t *testing.T) {
	for _, target := range []string{"http://example.com", "https://localhost", "https://127.0.0.1", "https://192.168.1.4"} {
		if _, err := NewCustomSite(target, "custom"); err == nil {
			t.Fatalf("accepted unsafe target %q", target)
		}
	}
}

func TestNormalizeConnectorSanitizesSkillMetadata(t *testing.T) {
	item, err := NormalizeConnector(Connector{
		Name: "Safe connector", Intent: "Find useful public pages", Category: "research\nmalicious: true",
		Sites: []Site{{ID: "../../bad", Name: "Example", BaseURL: "https://example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Category != "custom" {
		t.Fatalf("category=%q, want custom", item.Category)
	}
	if item.Sites[0].ID == "../../bad" || !safeSiteIDPattern.MatchString(item.Sites[0].ID) {
		t.Fatalf("site ID was not normalized: %q", item.Sites[0].ID)
	}
}

func TestConnectorStoreLifecycle(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "connectors.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(t.Context(), Connector{
		WorkspaceID: "personal", OwnerSubject: "admin", Name: "Research sources",
		Intent: "Read selected research websites", Category: "research",
		Sites: []Site{{ID: "hbr", Name: "HBR", BaseURL: "https://hbr.org"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SkillName == "" || created.Sites[0].AuthRequirement != SiteAuthOptional {
		t.Fatalf("created connector=%+v", created)
	}
	updated, err := store.SetSiteConnection(t.Context(), "personal", "admin", created.ID, "hbr", "conn_1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Sites[0].AuthConnectionID != "conn_1" {
		t.Fatalf("connection link=%q", updated.Sites[0].AuthConnectionID)
	}
	items, err := store.List(t.Context(), "personal", "admin")
	if err != nil || len(items) != 1 {
		t.Fatalf("list=%v err=%v", items, err)
	}
	if err := store.Delete(t.Context(), "personal", "admin", created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), "personal", "admin", created.ID); err != ErrConnectorNotFound {
		t.Fatalf("get after delete err=%v", err)
	}
}
