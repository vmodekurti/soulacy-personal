package runtime

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// The System agent is the one with shell and file access, so what it can see
// of its own environment matters more here than anywhere else. It could
// already LIST skills, MCP tools and peers; these assert it can now USE them.
func TestSystemAgentCanUseTheCatalogsNotJustListThem(t *testing.T) {
	system := builtinSystemAgent()

	if len(system.Skills) != 1 || system.Skills[0] != "*" {
		t.Fatalf("System should be able to read any installed skill: %v", system.Skills)
	}
	if system.MCPServers == nil || len(*system.MCPServers) != 1 || (*system.MCPServers)[0] != "*" {
		t.Fatalf("System should be able to call any connected MCP tool: %v", system.MCPServers)
	}
	if len(system.Agents) != 1 || system.Agents[0] != "*" {
		t.Fatalf("System should be able to delegate to peers: %v", system.Agents)
	}
	if !system.ParallelPeerCalls {
		t.Fatal("independent delegated work should be able to run in parallel")
	}
}

// Genie's whole method is "the catalogs are live, go look". System now has
// the same reach, so it needs the same instruction or it will keep guessing.
func TestSystemAgentIsToldToReadTheCatalogsRatherThanAssume(t *testing.T) {
	prompt := builtinSystemAgent().SystemPrompt

	for _, tool := range []string{"list_skills", "list_mcp_tools", "list_agents", "read_skill"} {
		if !strings.Contains(prompt, tool) {
			t.Fatalf("the prompt should name %s; a tool it is not told about is a tool it will not use", tool)
		}
	}
	if !strings.Contains(prompt, "live") {
		t.Fatal("the prompt should say the catalogs are live, so a rescan or hot-add is not missed")
	}
	// The failure this fixes: answering "what is installed?" with brew list.
	if !strings.Contains(prompt, "never a shell command") {
		t.Fatalf("asking Soulacy's own catalogs must be preferred over shelling out")
	}
}

// System keeps its confirmation gates: broader reach must not mean quieter.
func TestSystemAgentStillConfirmsEveryDestructiveTool(t *testing.T) {
	system := builtinSystemAgent()
	confirm := map[string]bool{}
	for _, tool := range system.ConfirmTools {
		confirm[tool] = true
	}
	for _, tool := range []string{"shell_exec", "run_script", "write_file", "install_library", "package_install", "mcp_register_remote", "download_file", "http_request"} {
		if !confirm[tool] {
			t.Fatalf("%s must still require confirmation", tool)
		}
	}
}

func TestSystemAgentGetsRepositoryAwareMCPInstallationPlanning(t *testing.T) {
	e := &Engine{}
	system := builtinSystemAgent()
	names := map[string]bool{}
	for _, tool := range e.systemToolsFor(system) {
		names[tool.Name] = true
	}
	if !names["mcp_install_inspect"] || !names["package_install"] || !names["mcp_register_remote"] {
		t.Fatalf("system MCP installation tools = %v", names)
	}

	custom := &agent.Definition{ID: "custom", Capabilities: []string{"system"}}
	for _, tool := range e.systemToolsFor(custom) {
		if tool.Name == "mcp_install_inspect" {
			t.Fatal("repository installation planner should be reserved for the built-in System agent")
		}
	}

	prompt := system.SystemPrompt + mcpInstallationPlanningGuide
	for _, want := range []string{"mcp_install_inspect", "hosted endpoint", "connected device", "companion service", "If no method works"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("System MCP installation guidance missing %q", want)
		}
	}
}

// An engine can legitimately exist without a loader. Every peer path
// dereferences it, so a wildcard `agents:` used to panic mid-run.
func TestPeerResolutionWithoutALoaderReturnsNothingRatherThanPanicking(t *testing.T) {
	e := &Engine{}
	if peers := e.resolveAgentRefs([]string{"*"}, "system"); len(peers) != 0 {
		t.Fatalf("no loader means no peers: %+v", peers)
	}
	if peers := e.resolveAgentRefs([]string{"planner"}, "system"); len(peers) != 0 {
		t.Fatalf("named peers need a loader too: %+v", peers)
	}
	if peers := e.resolveAgentRefs(nil, "system"); peers != nil {
		t.Fatal("no refs, no peers")
	}
}

// Both built-ins should reach the same catalogs; that was the whole ask.
func TestSystemAndGenieHaveTheSameEnvironmentalReach(t *testing.T) {
	system, genie := builtinSystemAgent(), builtinGenieAgent()

	if len(system.Skills) != len(genie.Skills) || system.Skills[0] != genie.Skills[0] {
		t.Fatalf("skills reach differs: system=%v genie=%v", system.Skills, genie.Skills)
	}
	if (*system.MCPServers)[0] != (*genie.MCPServers)[0] {
		t.Fatalf("mcp reach differs: system=%v genie=%v", *system.MCPServers, *genie.MCPServers)
	}
	if system.Agents[0] != genie.Agents[0] {
		t.Fatalf("peer reach differs: system=%v genie=%v", system.Agents, genie.Agents)
	}
	// They are still different agents: System holds the host, Genie does not.
	if !system.SystemTools {
		t.Fatal("System keeps its host access")
	}
	if genie.SystemTools {
		t.Fatal("Genie must not acquire host access from this change")
	}
}

// Genie is the way into the product, so it has to know what the product is.
// Asked "what is Studio for?" it answered about Adobe, Spotify and Visual
// Studio, because nothing told it which Studio it lives inside.
func TestGenieKnowsWhatSoulacyIs(t *testing.T) {
	prompt := builtinGenieAgent().SystemPrompt
	for _, part := range []string{"Studio", "Templates", "Delivery", "Knowledge", "About You", "Providers"} {
		if !strings.Contains(prompt, part) {
			t.Errorf("Genie should know that %q is part of Soulacy", part)
		}
	}
	if !strings.Contains(prompt, "self-hosted") {
		t.Error("Genie should know Soulacy is something the user runs themselves")
	}
	// Knowing the map must not replace reading the live catalogs.
	if !strings.Contains(prompt, "list_skills") {
		t.Error("Genie should still read what is actually installed")
	}
}
