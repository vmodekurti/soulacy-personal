package runtime

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestAgensiCreditPurchaseClassification(t *testing.T) {
	tests := []struct {
		name string
		call message.ToolCall
		want bool
	}{
		{"search", message.ToolCall{Name: "mcp__agensi__search_skills", Arguments: map[string]any{"query": "writing"}}, false},
		{"free load", message.ToolCall{Name: "mcp__agensi__get_skill", Arguments: map[string]any{"skill_id": "writer"}}, false},
		{"explicit credit purchase", message.ToolCall{Name: "mcp__agensi__get_skill", Arguments: map[string]any{"skill_id": "writer", "confirm": true}}, true},
		{"string cannot bypass type check", message.ToolCall{Name: "mcp__agensi__get_skill", Arguments: map[string]any{"confirm": "true"}}, false},
		{"another server", message.ToolCall{Name: "mcp__other__get_skill", Arguments: map[string]any{"confirm": true}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agensiCreditPurchase(test.call); got != test.want {
				t.Fatalf("agensiCreditPurchase() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAgensiPurchaseNeverAutoApprovesUnattendedAgent(t *testing.T) {
	e := &Engine{log: zap.NewNop()}
	def := &agent.Definition{ID: "scheduled", Unattended: true}
	call := message.ToolCall{Name: "mcp__agensi__get_skill", Arguments: map[string]any{"skill_id": "paid", "confirm": true}}
	err := e.requiredHumanConfirm(context.Background(), def, call, "Buy this Agensi skill with account credit")
	if err == nil || !strings.Contains(err.Error(), "explicit user approval") {
		t.Fatalf("unattended purchase error = %v", err)
	}
}

func TestBuiltinAgentsHonorAgensiUsageContract(t *testing.T) {
	for _, def := range []struct {
		name   string
		prompt string
	}{
		{"Genie", builtinGenieAgent().SystemPrompt},
		{"System", builtinSystemAgent().SystemPrompt},
	} {
		for _, required := range []string{"call get_skill during this run", "Never save, copy, export, cache, or install", "confirm=true"} {
			if !strings.Contains(def.prompt, required) {
				t.Fatalf("%s prompt does not include %q", def.name, required)
			}
		}
	}
}

func TestAgensiSkillBodyIsTransientAcrossPersistenceSurfaces(t *testing.T) {
	const licensed = "licensed instructions that must never be retained"
	call := message.ToolCall{Name: "mcp__agensi__get_skill"}
	if got := agensiObservableResult(call, licensed, false); got != agensiTransientReceipt {
		t.Fatalf("observable result = %q", got)
	}
	if got := agensiObservableResult(call, licensed, true); got != licensed {
		t.Fatalf("failed result should remain diagnosable, got %q", got)
	}

	result := redactAgensiToolResultForEvent(message.ToolResult{Name: call.Name, Content: licensed})
	if result.Content != agensiTransientReceipt {
		t.Fatalf("event result = %q", result.Content)
	}

	sess := &Session{History: []llm.ChatMessage{
		{Role: "tool", Name: call.Name, Content: licensed},
		{Role: "tool", Name: "mcp__other__get_skill", Content: "ordinary result"},
	}}
	redactAgensiSkillHistory(sess)
	if strings.Contains(sess.History[0].Content, licensed) || sess.History[0].Content != agensiTransientReceipt {
		t.Fatalf("session retained Agensi body: %q", sess.History[0].Content)
	}
	if sess.History[1].Content != "ordinary result" {
		t.Fatalf("ordinary tool result was changed: %q", sess.History[1].Content)
	}

	if got := bestEffortFinal([]llm.ChatMessage{{Role: "tool", Name: call.Name, Content: licensed}}); got != "" {
		t.Fatalf("fallback exposed Agensi body: %q", got)
	}
}
