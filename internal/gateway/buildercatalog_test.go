package gateway

import (
	"strings"
	"testing"
)

func sampleCatalog() toolCatalogPayload {
	return toolCatalogPayload{
		PythonTools: []pyToolView{
			{Name: "fetch_prices", Path: "/ws/tools/fetch_prices.py", Description: "Fetch prices.\nSecond line."},
		},
		MCPTools: []mcpToolView{
			{FullName: "mcp__gcal__list_events", Name: "list_events", Server: "gcal", Description: "List calendar events"},
		},
		Builtins: []builtinToolView{
			{Name: "web_search", Description: "Search the web"},
		},
	}
}

// The defect: every chosen tool became tools/<name>.py with an empty schema,
// whatever kind it was, and no such file was ever written.
func TestEachToolKindIsWiredToItsOwnField(t *testing.T) {
	res := sampleCatalog().ResolveToolNames([]string{"web_search", "mcp__gcal__list_events", "fetch_prices"})

	if len(res.Builtins) != 1 || res.Builtins[0] != "web_search" {
		t.Errorf("a built-in belongs in builtins, got %v", res.Builtins)
	}
	if len(res.MCPTools) != 1 || res.MCPTools[0] != "mcp__gcal__list_events" {
		t.Errorf("an MCP tool belongs in mcp_tools, got %v", res.MCPTools)
	}
	if len(res.Python) != 1 {
		t.Fatalf("want one python tool, got %v", res.Python)
	}
	// The real path from the catalog, not a fabricated one.
	if got := res.Python[0]["python_file"]; got != "/ws/tools/fetch_prices.py" {
		t.Errorf("python_file = %v, want the catalog's real path", got)
	}
	if strings.Contains(res.Python[0]["python_file"].(string), "tools/fetch_prices.py") &&
		res.Python[0]["python_file"] != "/ws/tools/fetch_prices.py" {
		t.Error("a relative guess is what produced agents whose tools pointed at nothing")
	}
	if len(res.Unknown) != 0 {
		t.Errorf("nothing should be unknown here, got %v", res.Unknown)
	}
}

// A built-in must never land in tools[], because that demands a python_file
// which cannot exist for it.
func TestBuiltinsNeverBecomePythonTools(t *testing.T) {
	res := sampleCatalog().ResolveToolNames([]string{"web_search"})
	if len(res.Python) != 0 {
		t.Fatalf("a built-in must not become a python tool: %v", res.Python)
	}
}

// An invented name is reported, never guessed at. Guessing is the whole bug.
func TestUnknownToolNamesAreReportedNotInvented(t *testing.T) {
	res := sampleCatalog().ResolveToolNames([]string{"send_owl_mail", "web_search"})
	if len(res.Unknown) != 1 || res.Unknown[0] != "send_owl_mail" {
		t.Fatalf("unknown = %v, want the invented name", res.Unknown)
	}
	if len(res.Python) != 0 {
		t.Errorf("an unknown name must not become a python tool: %v", res.Python)
	}
	if len(res.Builtins) != 1 {
		t.Errorf("the real tool alongside it should still resolve: %v", res.Builtins)
	}
}

func TestResolveIgnoresBlanksAndDuplicates(t *testing.T) {
	res := sampleCatalog().ResolveToolNames([]string{"web_search", " web_search ", "", "   "})
	if len(res.Builtins) != 1 {
		t.Fatalf("want one built-in after de-duplication, got %v", res.Builtins)
	}
}

// The prompt and deploy must describe the same world, which is the reason
// they now share one catalog.
func TestPromptListsExactlyWhatResolveAccepts(t *testing.T) {
	cat := sampleCatalog()
	prompt := cat.BuilderPrompt()

	for _, name := range []string{"fetch_prices", "mcp__gcal__list_events", "web_search"} {
		if !strings.Contains(prompt, name) {
			t.Errorf("prompt should offer %q", name)
		}
		if res := cat.ResolveToolNames([]string{name}); len(res.Unknown) != 0 {
			t.Errorf("prompt offers %q but resolve rejects it", name)
		}
	}
	// The old prompt promised deploy would look up python_file paths. It must
	// not promise anything it does not do.
	if strings.Contains(prompt, "python_file") {
		t.Error("the prompt should not ask the model for file paths; deploy resolves them")
	}
}

func TestEmptyCatalogResolvesEverythingAsUnknown(t *testing.T) {
	var empty toolCatalogPayload
	res := empty.ResolveToolNames([]string{"anything"})
	if len(res.Unknown) != 1 {
		t.Fatalf("with no tools installed, every name is unknown: %+v", res)
	}
}
