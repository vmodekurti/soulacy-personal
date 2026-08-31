package gateway

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestValidateDemoValueAllowsCuratedStudioContract(t *testing.T) {
	value := map[string]any{
		"provider": "nvidia",
		"model":    "meta/llama-3.3-70b-instruct",
		"tools":    []any{"web_search", map[string]any{"name": "generate_chart"}},
	}
	if reason := validateDemoValue(value, "", normalizedSet([]string{"web_search", "generate_chart"}), normalizedSet([]string{"nvidia"}), normalizedSet([]string{"meta/llama-3.3-70b-instruct"})); reason != "" {
		t.Fatalf("safe demo contract rejected: %s", reason)
	}
}

func TestValidateDemoValueRejectsUncuratedCapabilities(t *testing.T) {
	tools := normalizedSet([]string{"web_search"})
	providers := normalizedSet([]string{"nvidia"})
	models := normalizedSet([]string{"safe-model"})
	tests := []struct {
		name  string
		value any
	}{
		{"provider", map[string]any{"provider": "openai"}},
		{"model", map[string]any{"model": "unbounded-model"}},
		{"tool", map[string]any{"tools": []any{"shell_exec"}}},
		{"structured tool", map[string]any{"capabilities": []any{map[string]any{"tool": "filesystem_write"}}}},
		{"skill", map[string]any{"skills": []any{"private-skill"}}},
		{"mcp", map[string]any{"mcp_servers": []any{"github"}}},
		{"channel", map[string]any{"channels": []any{"telegram"}}},
		{"connection", map[string]any{"connections": map[string]any{"hbr": true}}},
		{"knowledge", map[string]any{"knowledge": []any{"private-kb"}}},
		{"delegated agent", map[string]any{"flow": map[string]any{"nodes": []any{map[string]any{"kind": "agent", "agent": "published-agent"}}}}},
		{"custom code", map[string]any{"flow": map[string]any{"nodes": []any{map[string]any{"kind": "python", "code": "def run(inputs): return inputs"}}}}},
		{"new agent", map[string]any{"new_agents": []any{map[string]any{"name": "peer"}}}},
		{"unattended", map[string]any{"unattended": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if reason := validateDemoValue(test.value, "", tools, providers, models); reason == "" {
				t.Fatal("unsafe demo value was accepted")
			}
		})
	}
}

func TestValidatePublicDemoAgentRequiresExplicitSafeContract(t *testing.T) {
	builtins := []string{"web_search", "generate_chart"}
	def := &agent.Definition{
		ID:       "demo-chart",
		Labels:   map[string]string{"soulacy.public_demo": "true"},
		LLM:      agent.LLMConfig{Provider: "nvidia", Model: "nvidia/demo"},
		Builtins: &builtins,
	}
	tools := normalizedSet([]string{"web_search", "generate_chart"})
	providers := normalizedSet([]string{"nvidia"})
	models := normalizedSet([]string{"nvidia/demo"})
	if reason := validatePublicDemoAgent(def, tools, providers, models); reason != "" {
		t.Fatalf("curated public agent rejected: %s", reason)
	}

	tests := []struct {
		name   string
		mutate func(*agent.Definition)
	}{
		{"missing label", func(d *agent.Definition) { d.Labels = nil }},
		{"unapproved provider", func(d *agent.Definition) { d.LLM.Provider = "openai" }},
		{"implicit tools", func(d *agent.Definition) { d.Builtins = nil }},
		{"unapproved tool", func(d *agent.Definition) { values := []string{"shell_exec"}; d.Builtins = &values }},
		{"mcp server", func(d *agent.Definition) { values := []string{"finance"}; d.MCPServers = &values }},
		{"delegation", func(d *agent.Definition) { d.Agents = []string{"private-agent"} }},
		{"schedule", func(d *agent.Definition) { d.Schedule = &agent.Schedule{Cron: "0 * * * *"} }},
		{"shell", func(d *agent.Definition) { d.AllowShell = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := *def
			test.mutate(&copy)
			if reason := validatePublicDemoAgent(&copy, tools, providers, models); reason == "" {
				t.Fatal("unsafe public-demo agent was accepted")
			}
		})
	}
}
