package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func (e *Engine) websiteAccessRoutingGuide(ctx context.Context, def *agent.Definition) string {
	connections := e.readyWebsiteConnections(ctx, def)
	if len(connections) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("## Ready Website Access\n")
	out.WriteString("Soulacy already has encrypted, agent-approved sessions for the sites below. For account-only work on a matching domain, use start_website_action, inspect_website_action, and act_on_website before trying a browser MCP that may have a separate signed-out profile. Omit connection_id when starting: Soulacy will select the matching granted session. Cookies and tokens remain outside model context.\n")
	for _, connection := range connections {
		fmt.Fprintf(&out, "\n- %s (%s): %s", connection.Name, connection.ID, strings.Join(connection.AllowedDomains, ", "))
	}
	return out.String()
}

func websiteAccessFallbackNudge(results []message.ToolResult, ready bool, genie bool) string {
	for _, result := range results {
		if !strings.HasPrefix(normalizeToolCallName(result.Name), "mcp__") {
			continue
		}
		compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(strings.ToLower(result.Content))
		if !strings.Contains(compact, `"authenticated":false`) {
			continue
		}
		if ready {
			return "The MCP server reported that its own browser profile is not authenticated. Do not retry that MCP route. Use start_website_action for the official site; Soulacy will automatically select this agent's matching ready Website Access session. Continue the task there and report only a real provider blocker."
		}
		if genie {
			return "The MCP server reported that its own browser profile is not authenticated. Do not retry that MCP route. Call prepare_website_access for the provider's official HTTPS URL. If it returns ready, continue immediately with start_website_action. Ask the user to sign in only if it returns needs_sign_in."
		}
	}
	return ""
}
