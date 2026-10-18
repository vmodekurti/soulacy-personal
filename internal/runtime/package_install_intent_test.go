package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcpinstall"
	"github.com/soulacy/soulacy/internal/taskcontract"
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
			result: message.ToolResult{Name: "package_install", Content: verifiedManagedActionResult("Installed and registered maverick-mcp.")},
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

func TestVerifiedManagedActionResultSatisfiesTaskContract(t *testing.T) {
	now := time.Now().UTC()
	c := taskcontract.New("run", message.Message{
		ID: "message", AgentID: "system", SessionID: "session", Channel: "http",
		Role: message.RoleUser, Parts: message.Text("Install this MCP server"), CreatedAt: now,
	}, now)
	c.ObserveTool(message.ToolCall{Name: "package_install"}, verifiedManagedActionResult("Installed and registered."), false)
	got := c.Complete(nil, true, false, now.Add(time.Second))
	if got.Outcome != taskcontract.OutcomeVerified {
		t.Fatalf("package install outcome = %q, want %q", got.Outcome, taskcontract.OutcomeVerified)
	}
}

func TestPackageInstallFailureDetail(t *testing.T) {
	results := []message.ToolResult{{
		Name: "package_install", IsError: true,
		Content: "error: package_install: installer failed: tsc: not found",
	}}
	if got := packageInstallFailureDetail(results); got != "installer failed: tsc: not found" {
		t.Fatalf("failure detail = %q", got)
	}
}

func TestDeterministicMCPInstallFollowup(t *testing.T) {
	request := urlPackageInstallRequest{SourceURL: "https://github.com/acme/compatible-mcp", Kind: "mcp"}
	rememberMCPAdvice(request.SourceURL, mcpinstall.Recommendation{Source: request.SourceURL, CanInstallHere: true})
	results := []message.ToolResult{{Name: "mcp_install_inspect", Content: "Install in the gateway"}}

	call, final := deterministicMCPInstallFollowup(t.Context(), request, results)
	if final != "" || call == nil || call.Name != "package_install" {
		t.Fatalf("followup call = %#v, final = %q", call, final)
	}
	if call.Arguments["source_url"] != request.SourceURL || call.Arguments["kind"] != "mcp" {
		t.Fatalf("followup arguments = %#v", call.Arguments)
	}
}

func TestDeterministicMCPInstallFollowupReturnsTypedRefusal(t *testing.T) {
	request := urlPackageInstallRequest{SourceURL: "https://github.com/acme/companion-mcp", Kind: "mcp"}
	rememberMCPAdvice(request.SourceURL, mcpinstall.Recommendation{
		Source: request.SourceURL, CanInstallHere: false,
		Title: "Run as a companion service", Summary: "requires a separate browser host",
	})
	results := []message.ToolResult{{Name: "mcp_install_inspect", Content: "Use a companion service"}}

	call, final := deterministicMCPInstallFollowup(t.Context(), request, results)
	if call != nil || !strings.Contains(final, "was not attempted") || !strings.Contains(final, "companion service") {
		t.Fatalf("followup call = %#v, final = %q", call, final)
	}
}

func TestDeterministicMCPInstallFollowupStopsWhenInspectionFails(t *testing.T) {
	request := urlPackageInstallRequest{SourceURL: "https://github.com/acme/broken-mcp", Kind: "mcp"}
	results := []message.ToolResult{{Name: "mcp_install_inspect", IsError: true, Content: "repository unavailable"}}
	call, final := deterministicMCPInstallFollowup(t.Context(), request, results)
	if call != nil || final != "" {
		t.Fatalf("followup call = %#v, final = %q", call, final)
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

func TestFormatPackageInstallReplyFindsInstallerResultInMixedToolBatch(t *testing.T) {
	results := []message.ToolResult{
		{Name: "env_get", Content: "HOME=/workspace"},
		{Name: "package_install", IsError: true, Content: "error: package_install: Python >=3.12 is required"},
	}
	if !hasPackageInstallResult(results) {
		t.Fatal("package_install result was not detected")
	}
	want := "MCP server installation failed.\n\nPython >=3.12 is required"
	if got := formatPackageInstallReply(results); got != want {
		t.Fatalf("formatPackageInstallReply() = %q, want %q", got, want)
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
