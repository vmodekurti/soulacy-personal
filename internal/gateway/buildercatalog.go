// buildercatalog.go — rendering and resolving the tool catalog for the
// conversational builder.
//
// The builder's prompt ends with a promise: deploy will look up each chosen
// tool in this catalog. Deploy never did. It wrote `tools/<name>.py` for every
// tool the conversation picked, whatever kind it was, with an empty parameter
// schema and no such file on disk. A built-in the conversation correctly
// identified arrived as a path to nothing, and because deploy also skipped
// validation — the only place python_file paths are checked — nothing caught
// it. The user got a friendly conversation, a confident summary, and an agent
// whose tools did not exist.
//
// These are methods on the catalog the gateway already builds and memoises,
// rather than a second copy of it, so the prompt and deploy cannot describe
// different worlds.
package gateway

import (
	"fmt"
	"strings"
)

// BuilderPrompt renders the catalog for the builder's system message.
func (c toolCatalogPayload) BuilderPrompt() string {
	var sb strings.Builder
	sb.WriteString("## Available tools\n")
	sb.WriteString("Pick from these EXACT names when populating `tools[]`. ")
	sb.WriteString("Do NOT invent tool names. If the user wants a capability not covered here, list it in `missing` and ask whether to skip it or have them install something.\n\n")

	if len(c.PythonTools) > 0 {
		sb.WriteString("### Python tools\n")
		for _, p := range c.PythonTools {
			fmt.Fprintf(&sb, "- **%s** — %s\n", p.Name, firstLine(p.Description))
		}
		sb.WriteString("\n")
	}
	if len(c.MCPTools) > 0 {
		sb.WriteString("### MCP server tools (use the full namespaced name verbatim)\n")
		for _, t := range c.MCPTools {
			fmt.Fprintf(&sb, "- **%s** — %s\n", t.FullName, firstLine(t.Description))
		}
		sb.WriteString("\n")
	}
	if len(c.Builtins) > 0 {
		sb.WriteString("### Built-in tools (the engine handles these)\n")
		for _, b := range c.Builtins {
			fmt.Fprintf(&sb, "- **%s** — %s\n", b.Name, firstLine(b.Description))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Use the tool's exact name. Deploy resolves every name against this same catalog, so a name that is not listed here cannot be wired to anything.\n")
	return sb.String()
}

// resolvedTools is what a list of chosen tool names actually maps to.
type resolvedTools struct {
	Builtins []string
	MCPTools []string
	Python   []map[string]any
	// Unknown are names that match nothing in the catalog. They are reported
	// rather than guessed at: inventing a path for one is exactly what
	// produced agents whose tools pointed at files that were never written.
	Unknown []string
}

// ResolveToolNames maps chosen names onto the real thing of each kind.
func (c toolCatalogPayload) ResolveToolNames(names []string) resolvedTools {
	var out resolvedTools

	python := make(map[string]pyToolView, len(c.PythonTools))
	for _, p := range c.PythonTools {
		python[p.Name] = p
	}
	mcp := make(map[string]bool, len(c.MCPTools))
	for _, t := range c.MCPTools {
		mcp[t.FullName] = true
	}
	builtin := make(map[string]bool, len(c.Builtins))
	for _, b := range c.Builtins {
		builtin[b.Name] = true
	}

	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		switch {
		case builtin[name]:
			// A built-in belongs in `builtins:`, never in `tools:`. Putting one
			// in tools is what demanded a python_file that cannot exist.
			out.Builtins = append(out.Builtins, name)
		case mcp[name]:
			out.MCPTools = append(out.MCPTools, name)
		default:
			p, ok := python[name]
			if !ok {
				out.Unknown = append(out.Unknown, name)
				continue
			}
			out.Python = append(out.Python, map[string]any{
				"name":        p.Name,
				"description": firstLine(p.Description),
				"python_file": p.Path,
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			})
		}
	}
	return out
}
