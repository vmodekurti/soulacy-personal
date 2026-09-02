package gateway

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestShouldUseWorkspaceChatLLM_AgentAssignmentWins(t *testing.T) {
	tests := []struct {
		name string
		def  *agent.Definition
		want bool
	}{
		{name: "fully assigned", def: &agent.Definition{LLM: agent.LLMConfig{Provider: "google", Model: "gemini-2.5-pro"}}},
		{name: "provider assigned", def: &agent.Definition{LLM: agent.LLMConfig{Provider: "google"}}},
		{name: "model assigned", def: &agent.Definition{LLM: agent.LLMConfig{Model: "gemini-2.5-pro"}}},
		{name: "unassigned", def: &agent.Definition{}, want: true},
		{name: "definition unavailable", def: nil, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseWorkspaceChatLLM(test.def, "", ""); got != test.want {
				t.Fatalf("shouldUseWorkspaceChatLLM() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestShouldUseWorkspaceChatLLM_ExplicitRunOverrideWins(t *testing.T) {
	unassigned := &agent.Definition{}
	if shouldUseWorkspaceChatLLM(unassigned, "openai", "") {
		t.Fatal("explicit provider must win over the workspace Chat fallback")
	}
	if shouldUseWorkspaceChatLLM(unassigned, "", "gpt-5") {
		t.Fatal("explicit model must win over the workspace Chat fallback")
	}
}
