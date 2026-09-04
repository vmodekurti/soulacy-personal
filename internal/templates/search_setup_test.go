package templates

import "testing"

// The shipped research templates use web_search and must therefore surface a
// "search" setup item plus a search-provider required secret, so users aren't
// surprised by a runtime "no API key" error.
func TestWebSearchTemplatesFlagSearchProvider(t *testing.T) {
	cat := New("")
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	searchUsers := map[string]bool{
		"web-researcher": true, "research-brief": true, "stock-screener": true,
		"flight-deal-finder": true, "market-monitor": true,
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !searchUsers[e.Name] {
			continue
		}
		seen[e.Name] = true
		hasSetup := false
		for _, s := range e.Setup {
			if s.Key == "search" {
				hasSetup = true
			}
		}
		if !hasSetup {
			t.Fatalf("template %q uses web_search but has no 'search' setup item", e.Name)
		}
		hasSecret := false
		for _, sec := range e.RequiredSecrets {
			if sec.Key == "search.api_key" {
				hasSecret = true
			}
		}
		if !hasSecret {
			t.Fatalf("template %q uses web_search but has no search.api_key required secret", e.Name)
		}
	}
	for name := range searchUsers {
		if !seen[name] {
			t.Fatalf("expected web_search template %q to be present", name)
		}
	}
}

// A non-search template must NOT get flagged with the search requirement.
func TestNonSearchTemplateHasNoSearchItem(t *testing.T) {
	cat := New("")
	entries, _ := cat.List()
	for _, e := range entries {
		if e.Name != "meeting-minutes" {
			continue
		}
		for _, s := range e.Setup {
			if s.Key == "search" {
				t.Fatalf("meeting-minutes should not require web search")
			}
		}
	}
}
