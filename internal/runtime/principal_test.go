package runtime

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

type principalPluginProvider struct{ tools []PluginTool }

func (p principalPluginProvider) AllTools() []PluginTool { return p.tools }

func TestPrincipalContextIsImmutableAndMessageIndependent(t *testing.T) {
	scopes := []string{"chat"}
	ctx := WithPrincipal(context.Background(), Principal{Subject: "viewer-1", Role: "viewer", Scopes: scopes})
	scopes[0] = "config"
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.Subject != "viewer-1" || p.Role != "viewer" || len(p.Scopes) != 1 || p.Scopes[0] != "chat" {
		t.Fatalf("principal mutated after insertion: %+v", p)
	}
	if callerAllowsTool(ctx, "shell_exec") {
		t.Fatal("viewer reached privileged system tool")
	}
	if !callerAllowsTool(ctx, "read_file") {
		t.Fatal("viewer lost safe read tool")
	}
}

func TestExternalToolsRequireAgentGrantAndCallerPermission(t *testing.T) {
	e := newMinimalEngine(t)
	e.builtins = e.buildBuiltins()
	e.pluginProvider = principalPluginProvider{tools: []PluginTool{{Name: "plugin__weather__forecast"}}}

	def := &agent.Definition{ID: "weather"}
	names := toolSchemaNameSet(e.allToolSchemasForContext(WithPrincipal(context.Background(), Principal{Role: "admin"}), def, "http"))
	if names["plugin__weather__forecast"] || mcpToolAllowed(def, "mcp__weather__forecast") {
		t.Fatal("omitted external grants exposed plugin or MCP tools")
	}
	grants := []string{"plugin__weather__forecast"}
	def.PluginTools = &grants
	names = toolSchemaNameSet(e.allToolSchemasForContext(WithPrincipal(context.Background(), Principal{Role: "admin"}), def, "http"))
	if !names["plugin__weather__forecast"] {
		t.Fatal("admin + explicit agent plugin grant did not expose tool")
	}
	names = toolSchemaNameSet(e.allToolSchemasForContext(WithPrincipal(context.Background(), Principal{Role: "viewer"}), def, "http"))
	if names["plugin__weather__forecast"] {
		t.Fatal("agent grant widened viewer caller permission")
	}
}
