package runtime

import (
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
)

type urlPackageInstallRequest struct {
	SourceURL string
	Kind      string
}

// isURLPackageInstallRequest recognizes only an explicit operator request to
// install a Skill or MCP server from an HTTPS URL. Keeping this narrow avoids
// forcing a mutating tool for ordinary discussions about installation.
func isURLPackageInstallRequest(text string) bool {
	_, ok := parseURLPackageInstallRequest(text)
	return ok
}

func parseURLPackageInstallRequest(text string) (urlPackageInstallRequest, bool) {
	low := strings.ToLower(text)
	if !strings.Contains(low, "https://") {
		return urlPackageInstallRequest{}, false
	}

	var sourceURL string
	for _, field := range strings.Fields(text) {
		candidate := strings.Trim(field, "<>()[]{}\"'`,.;:!?")
		if strings.HasPrefix(strings.ToLower(candidate), "https://") {
			sourceURL = candidate
			break
		}
	}
	if sourceURL == "" {
		return urlPackageInstallRequest{}, false
	}

	// Package names frequently contain misleading type words (for example an
	// MCP repository named "flights-skill"). Determine the requested kind only
	// from the operator's prose, never from the URL itself.
	intentLow := strings.Replace(low, strings.ToLower(sourceURL), "", 1)
	hasMCP := strings.Contains(intentLow, "mcp")
	hasSkill := strings.Contains(intentLow, "skill")
	if !hasMCP && !hasSkill {
		return urlPackageInstallRequest{}, false
	}
	for _, informational := range []string{"how do i", "how can i", "how to ", "explain how", "show me how"} {
		if strings.Contains(intentLow, informational) {
			return urlPackageInstallRequest{}, false
		}
	}
	explicitAction := false
	for _, verb := range []string{"install", "add", "set up", "setup"} {
		if strings.Contains(intentLow, verb) {
			explicitAction = true
			break
		}
	}
	if !explicitAction {
		return urlPackageInstallRequest{}, false
	}

	kind := "auto"
	if hasMCP && !hasSkill {
		kind = "mcp"
	} else if hasSkill && !hasMCP {
		kind = "skill"
	}
	return urlPackageInstallRequest{SourceURL: sourceURL, Kind: kind}, true
}

func toolSchemaExists(tools []llm.ToolSchema, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
