package runtime

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestURLPackageInstallRequest(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"mcp install", "Install the MCP server from https://github.com/acme/server", true},
		{"skill add", "Please add this skill: https://github.com/acme/skill", true},
		{"discussion only", "How do I install an MCP server from https://example.com/docs?", false},
		{"no package type", "Install this app from https://github.com/acme/app", false},
		{"no URL", "Install the MCP server named weather", false},
		{"http rejected", "Install MCP from http://example.com/repo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isURLPackageInstallRequest(tt.text); got != tt.want {
				t.Fatalf("isURLPackageInstallRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatPackageInstallReply(t *testing.T) {
	tests := []struct {
		name   string
		result message.ToolResult
		want   string
	}{
		{
			name:   "success",
			result: message.ToolResult{Name: "package_install", Content: "Installed and registered maverick-mcp."},
			want:   "MCP server installation completed.\n\nInstalled and registered maverick-mcp.",
		},
		{
			name:   "failure exposes actionable cause",
			result: message.ToolResult{Name: "package_install", IsError: true, Content: "error: package_install: Python >=3.12 is required"},
			want:   "MCP server installation failed.\n\nPython >=3.12 is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatPackageInstallReply([]message.ToolResult{tt.result}); got != tt.want {
				t.Fatalf("formatPackageInstallReply() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatPackageInstallReplyBoundsLongErrors(t *testing.T) {
	reply := formatPackageInstallReply([]message.ToolResult{{
		Name: "package_install", IsError: true, Content: "error: " + strings.Repeat("x", 5000),
	}})
	if len(reply) > 4100 || !strings.HasPrefix(reply, "MCP server installation failed.\n\n…") {
		t.Fatalf("long installer error was not bounded: len=%d prefix=%q", len(reply), reply[:40])
	}
}

func TestParseURLPackageInstallRequestPreservesExplicitKind(t *testing.T) {
	tests := []struct {
		text string
		url  string
		kind string
	}{
		{"Install this MCP server: https://github.com/acme/server", "https://github.com/acme/server", "mcp"},
		{"Install this MCP Server - https://github.com/Anmoldureha/flights-skill", "https://github.com/Anmoldureha/flights-skill", "mcp"},
		{"Add skill from <https://github.com/acme/skill>.", "https://github.com/acme/skill", "skill"},
		{"Install this MCP skill https://github.com/acme/hybrid", "https://github.com/acme/hybrid", "auto"},
	}
	for _, tt := range tests {
		got, ok := parseURLPackageInstallRequest(tt.text)
		if !ok {
			t.Fatalf("parseURLPackageInstallRequest(%q) did not match", tt.text)
		}
		if got.SourceURL != tt.url || got.Kind != tt.kind {
			t.Fatalf("parseURLPackageInstallRequest(%q) = %#v, want url=%q kind=%q", tt.text, got, tt.url, tt.kind)
		}
	}
}

func TestToolSchemaExists(t *testing.T) {
	tools := []llm.ToolSchema{{Name: "web_search"}, {Name: "package_install"}}
	if !toolSchemaExists(tools, "package_install") {
		t.Fatal("package_install schema was not found")
	}
	if toolSchemaExists(tools, "shell_exec") {
		t.Fatal("unexpected shell_exec schema match")
	}
}
