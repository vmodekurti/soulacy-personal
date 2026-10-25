package runtime

import (
	"context"

	"github.com/soulacy/soulacy/pkg/agent"
)

// The same policy gates both tool discovery and execution. Hiding a schema
// alone cannot enforce an explicit grant when a model guesses a tool name.
func builtinNameAllowed(def *agent.Definition, name string) bool {
	return def == nil || def.Builtins == nil || containsExactString(*def.Builtins, name) || containsExactString(*def.Builtins, "*") || containsExactString(*def.Builtins, "all")
}

func (e *Engine) builtinAllowed(ctx context.Context, def *agent.Definition, tool BuiltinTool) bool {
	websiteRoute := tool.Gate == "genie" && safeWebsiteRouteTool(tool.Name) && len(e.readyWebsiteConnections(ctx, def)) > 0
	if !builtinNameAllowed(def, tool.Name) && !websiteRoute {
		return false
	}
	switch tool.Gate {
	case "portal":
		return def != nil && def.ID == GenieAgentID && portalAccess(ctx) != nil
	case "agensi":
		return def != nil && (def.ID == GenieAgentID || def.ID == SystemAgentID) && portalAccess(ctx) != nil
	case "genie":
		return websiteRoute || (def != nil && def.Builtins != nil && containsExactString(*def.Builtins, tool.Name))
	case "mobile", "safe_undo", "person":
		return def != nil && def.Builtins != nil && containsExactString(*def.Builtins, tool.Name)
	case "learning":
		return def != nil && def.Learning.Enabled && e.LearningNotebook() != nil
	case "skills":
		return def != nil && len(e.effectiveSkillNames(def)) > 0
	case "knowledge":
		return def != nil && len(def.Knowledge) > 0
	default:
		return true
	}
}
