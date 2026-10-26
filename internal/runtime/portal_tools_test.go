package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type fakePortalAccess struct{ calls []string }

func (p *fakePortalAccess) PortalActions(context.Context, string) (any, error) {
	p.calls = append(p.calls, "discover")
	return map[string]any{"actions": []string{"runs.list"}}, nil
}
func (p *fakePortalAccess) PortalAction(_ context.Context, mode, name string, _, _ map[string]any) (any, error) {
	p.calls = append(p.calls, mode+":"+name)
	return map[string]any{"output": "Podcast: https://audio.example/api/podcasts/episodes/episode-123/audio"}, nil
}

func TestPortalAccessStaysWithInteractiveGenie(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = e.buildBuiltins()
	genie := builtinGenieAgent()
	access := &fakePortalAccess{}
	granted := WithPortalAccess(WithPrincipal(context.Background(), Principal{Role: "admin"}), access)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		def  *agent.Definition
		want bool
	}{
		{"signed-in Genie", granted, genie, true},
		{"admin alone is not a portal grant", WithPrincipal(context.Background(), Principal{Role: "admin"}), genie, false},
		{"scheduler has no portal grant", context.Background(), genie, false},
		{"peer cannot name or inherit the tool", granted, &agent.Definition{ID: "peer", Builtins: genie.Builtins}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := toolSchemaNameSet(e.allToolSchemasForContext(tc.ctx, tc.def, "http"))
			if names["portal_update"] != tc.want {
				t.Fatalf("portal tool offered = %v", names["portal_update"])
			}
		})
	}
	peerCtx := applyAgentPrincipalBoundary(granted, &agent.Definition{ID: "peer"})
	if portalAccess(applyAgentPrincipalBoundary(peerCtx, genie)) != nil {
		t.Fatal("peer relayed portal access back to Genie")
	}
	tool := builtinByName(t, e.builtins, "portal_update")
	if _, err := tool.Handler(WithActiveAgent(granted, "peer"), map[string]any{"action": "agents.update"}); err == nil {
		t.Fatal("direct handler invocation bypassed agent boundary")
	}
	if _, err := tool.Handler(WithActiveAgent(context.Background(), GenieAgentID), nil); err == nil {
		t.Fatal("unauthenticated direct invocation succeeded")
	}

	// System gets only the narrow Agensi sign-in bridge, not Genie's general
	// portal administration catalog.
	systemNames := toolSchemaNameSet(e.allToolSchemasForContext(granted, builtinSystemAgent(), "http"))
	if !systemNames["connect_agensi"] || systemNames["portal_update"] {
		t.Fatalf("System portal tools = connect_agensi:%v portal_update:%v", systemNames["connect_agensi"], systemNames["portal_update"])
	}
	connect := builtinByName(t, e.builtins, "connect_agensi")
	if _, err := connect.Handler(WithActiveAgent(granted, SystemAgentID), nil); err != nil {
		t.Fatalf("System could not start Agensi sign-in: %v", err)
	}
	if access.calls[len(access.calls)-1] != "update:skills.agensi_connect" {
		t.Fatalf("Agensi bridge calls = %v", access.calls)
	}
}

func TestPortalToolRunsThroughModelLoop(t *testing.T) {
	def := builtinGenieAgent()
	def.LLM.Provider = "test"
	def.LLM.Model = "fake-model"
	e, provider := newHandleTestEngine(t, def)
	access := &fakePortalAccess{}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "discover", Name: "portal_actions", Arguments: map[string]any{"query": "podcast"}}}},
		{ToolCalls: []message.ToolCall{{ID: "read", Name: "portal_read", Arguments: map[string]any{"action": "runs.list", "params": map[string]any{"agent_id": "podcast"}}}}},
		{Content: "Here is your podcast: https://audio.example/api/podcasts/episodes/episode-123/audio"},
	}
	ctx := WithPortalAccess(WithPrincipal(context.Background(), Principal{Role: "admin"}), access)
	reply, err := e.Handle(ctx, testUserMessage(GenieAgentID, "portal-scenario", "Find my podcast link"))
	if err != nil {
		t.Fatal(err)
	}
	if len(access.calls) != 2 || access.calls[1] != "read:runs.list" {
		t.Fatalf("calls = %v", access.calls)
	}
	if !strings.Contains(reply.Parts[0].Text, "episode-123") {
		t.Fatalf("reply = %#v", reply)
	}
}

func TestPortalPromptMigrationAndConfirmation(t *testing.T) {
	def := builtinGenieAgent()
	def.SystemPrompt += "\nMy custom preference stays."
	hardenGenieDefinition(def)
	hardenGenieDefinition(def)
	if !strings.Contains(def.SystemPrompt, "My custom preference stays.") || strings.Count(def.SystemPrompt, geniePortalInstructions) != 1 {
		t.Fatal("portal prompt migration did not preserve customization or was not idempotent")
	}
	if strings.Contains(def.SystemPrompt, "You cannot change gateway configuration") {
		t.Fatal("obsolete portal restriction survived")
	}
	if !containsExactString(def.ConfirmTools, "portal_confirm") {
		t.Fatal("consequential portal actions missing mandatory confirmation")
	}
	for _, name := range []string{"portal_read", "portal_update", "portal_actions"} {
		if containsExactString(def.ConfirmTools, name) {
			t.Fatal("ordinary portal tools require confirmation")
		}
	}
}

func TestGenieCannotBypassOperationalProtectionThroughHostTools(t *testing.T) {
	ctx := applyAgentPrincipalBoundary(WithPrincipal(context.Background(), Principal{Role: "admin"}), builtinGenieAgent())
	// The restriction survives a peer hop even though its portal access does not.
	ctx = applyAgentPrincipalBoundary(ctx, &agent.Definition{ID: "peer"})
	for _, name := range []string{"shell_exec", "write_file", "http_request", "package_install", "run_script"} {
		if callerAllowsTool(ctx, name) {
			t.Errorf("Genie chain can bypass operational protection with %s", name)
		}
	}
	if !callerAllowsTool(ctx, "web_search") {
		t.Fatal("ordinary research was restricted")
	}
	if !isSideEffectingTool("portal_update") || !isSideEffectingTool("portal_confirm") || isSideEffectingTool("portal_read") {
		t.Fatal("portal dry-run classifications incorrect")
	}
}
