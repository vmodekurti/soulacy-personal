package runtime

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/soulacy/soulacy/internal/llm"
)

const websiteAccessDirectiveHeading = "## Required Website Access routes"

// applyWebsiteAccessIntent turns matching authenticated sessions into durable
// agent configuration. The builder model still writes the prose, but it cannot
// overlook a ready session or swap it for a generic search or a vendor MCP.
func applyWebsiteAccessIntent(u *BuilderUnderstanding, history []llm.ChatMessage, available []BuilderConnection) {
	if u == nil || len(available) == 0 {
		return
	}
	var userText strings.Builder
	for _, msg := range history {
		if msg.Role == "user" {
			userText.WriteString("\n")
			userText.WriteString(msg.Content)
		}
	}
	request := strings.ToLower(userText.String())

	selected := make([]BuilderConnection, 0, len(available))
	for _, connection := range available {
		if builderConnectionMentioned(request, connection) {
			selected = append(selected, connection)
		}
	}
	if len(selected) == 0 {
		return
	}
	u.Connections = selected

	// Generic network tools and a provider-specific MCP are substitutions for
	// the route the user selected. Keep unrelated tools such as channel.send.
	tools := u.Tools[:0]
	for _, tool := range u.Tools {
		name := strings.ToLower(strings.TrimSpace(tool.Name))
		switch name {
		case "web_search", "fetch_url", "http_request", "browser_open":
			continue
		}
		if strings.HasPrefix(name, "mcp__") && toolMatchesBuilderConnection(tool, selected) {
			continue
		}
		tools = append(tools, tool)
	}
	u.Tools = tools

	for _, connection := range selected {
		if !connection.Ready {
			addBuilderMissing(u, "refresh Website Access for "+connection.Name)
		}
	}
	if strings.TrimSpace(u.SystemPrompt) == "" {
		return
	}

	base := stripWebsiteAccessDirective(u.SystemPrompt)
	var directive strings.Builder
	directive.WriteString(websiteAccessDirectiveHeading)
	directive.WriteString("\nSoulacy found matching Website Access sessions for sites named in the request. Use these authenticated routes:\n")
	for _, connection := range selected {
		fmt.Fprintf(&directive, "- %s: %s\n", connection.Name, strings.Join(connection.Domains, ", "))
	}
	directive.WriteString("Use authenticated_fetch for subscription pages that can be read directly. Use start_website_action, inspect_website_action, act_on_website, and close_website_action for interactive work such as NotebookLM. Soulacy selects the matching saved session by domain and keeps cookies outside model context. Do not substitute web_search, fetch_url, http_request, or an MCP server for these sites. If a selected session is no longer ready, report that the named Website Access connection needs refresh instead of changing routes. Do not report success until every requested artifact and delivery result has been verified.\n")
	u.SystemPrompt = strings.TrimSpace(base) + "\n\n" + directive.String()
}

func builderConnectionMentioned(request string, connection BuilderConnection) bool {
	compactRequest := compactBuilderText(request)
	for _, domain := range connection.Domains {
		label := strings.Split(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "www."), ".")[0]
		if len(label) >= 3 && strings.Contains(compactRequest, compactBuilderText(label)) {
			return true
		}
	}
	name := strings.TrimSpace(connection.Name)
	if i := strings.IndexAny(name, "(:"); i > 0 {
		name = name[:i]
	}
	compactName := compactBuilderText(name)
	return len(compactName) >= 3 && strings.Contains(compactRequest, compactName)
}

func toolMatchesBuilderConnection(tool BuilderTool, selected []BuilderConnection) bool {
	text := compactBuilderText(tool.Name + " " + tool.Description)
	for _, connection := range selected {
		for _, domain := range connection.Domains {
			label := strings.Split(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "www."), ".")[0]
			if len(label) >= 3 && strings.Contains(text, compactBuilderText(label)) {
				return true
			}
		}
		name := strings.TrimSpace(connection.Name)
		if i := strings.IndexAny(name, "(:"); i > 0 {
			name = name[:i]
		}
		if compact := compactBuilderText(name); len(compact) >= 3 && strings.Contains(text, compact) {
			return true
		}
	}
	return false
}

func compactBuilderText(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func addBuilderMissing(u *BuilderUnderstanding, value string) {
	for _, existing := range u.Missing {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			return
		}
	}
	u.Missing = append(u.Missing, value)
}

func stripWebsiteAccessDirective(prompt string) string {
	if i := strings.Index(prompt, websiteAccessDirectiveHeading); i >= 0 {
		return strings.TrimSpace(prompt[:i])
	}
	return strings.TrimSpace(prompt)
}
