package runtime

import "github.com/soulacy/soulacy/pkg/agent"

// The same policy gates both tool discovery and execution. Hiding a schema
// alone cannot enforce an explicit grant when a model guesses a tool name.
func builtinNameAllowed(def *agent.Definition, name string) bool {
	return def == nil || def.Builtins == nil || containsExactString(*def.Builtins, name) || containsExactString(*def.Builtins, "*") || containsExactString(*def.Builtins, "all")
}

// builtinAllowed reports whether def may see and call tool. websiteRoute is
// true for one of the safe browser-route tools when the agent holds a ready,
// explicitly granted Website Access connection: that grant stands in for
// naming the tool in `builtins:`.
func (e *Engine) builtinAllowed(def *agent.Definition, tool BuiltinTool, websiteRoute bool) bool {
	if websiteRoute {
		return true
	}
	if !builtinNameAllowed(def, tool.Name) {
		return false
	}
	switch tool.Gate {
	case "mobile", "safe_undo", "person", "genie":
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
