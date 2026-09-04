// glue.go — auto-generated glue tools (Architect pillar #3).
//
// When a draft references a tool that NOTHING in the environment provides (not a
// builtin, not a connected MCP tool, not an installed Python tool), the step is a
// hole that will fail at run time. For steps that are plainly local data work —
// parse, filter, format, extract, transform, dedupe, rank — Studio doesn't leave
// the hole: it converts the step into a Custom Python node and has the framework
// model author a complete `def run(inputs)` that does the job, wired to the same
// upstream outputs. Steps that imply an external service (send, post, fetch a
// remote API) are left as suggestions instead of fabricating a network call.
//
// This runs as an explicit pre-loop pass (not inside the repair loop) so the
// transcript clearly attributes "wrote glue code for step X".
package studio

import (
	"context"
	"strings"
)

// transformVerbs are capability words that denote LOCAL, deterministic data work
// a Python node can implement safely without any external service.
var transformVerbs = []string{
	"parse", "filter", "format", "extract", "transform", "dedupe", "deduplicate",
	"rank", "sort", "merge", "combine", "convert", "compute", "calculate", "count",
	"build", "render", "clean", "normalize", "select", "pick", "map", "reshape",
	"join", "split", "group", "aggregate", "summarise table", "tabulate", "slice",
}

// externalVerbs denote steps that need a real service/network — we must NOT
// fabricate code for these; they need a real tool/MCP/channel instead.
var externalVerbs = []string{
	"send", "post", "email", "publish", "notify", "message", "tweet", "upload",
	"download", "fetch", "http", "request", "call api", "search the web", "scrape",
	"create notebook", "telegram", "slack", "discord",
}

// knownToolName reports whether a tool name is satisfiable in the environment:
// an MCP tool (mcp__ prefix; connectivity is Preflight's job), or a builtin /
// installed Python tool present in the catalog's tool list.
func knownToolName(cat Catalog, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true // empty is a different (field) problem
	}
	if strings.HasPrefix(name, "mcp__") {
		return true
	}
	for _, t := range cat.Tools {
		if strings.EqualFold(strings.TrimSpace(t), name) {
			return true
		}
	}
	return false
}

// looksLikeTransform decides whether a missing-tool step is local data work we
// can implement in Python. It checks the description first, then the tool name.
func looksLikeTransform(desc, tool string) bool {
	hay := strings.ToLower(desc + " " + strings.ReplaceAll(tool, "_", " "))
	for _, v := range externalVerbs {
		if strings.Contains(hay, v) {
			return false
		}
	}
	for _, v := range transformVerbs {
		if strings.Contains(hay, v) {
			return true
		}
	}
	return false
}

// EnsureCapabilities fills capability holes by writing glue code. For each tool
// node whose tool is unsatisfiable AND looks like local data work, it rewrites
// the node as a Custom Python node and authors its body via GenerateNodeCode,
// preserving the node id, output var, and edges. It returns the number of nodes
// converted and a note per conversion. Best-effort: a codegen failure leaves the
// node unchanged (Preflight will still surface it).
func EnsureCapabilities(ctx context.Context, llm LLM, draft *Draft, cat Catalog) (int, []string) {
	if draft == nil || llm == nil || draft.IsAgent() {
		return 0, nil
	}
	var converted int
	var notes []string
	for i := range draft.Flow.Nodes {
		n := &draft.Flow.Nodes[i]
		if strings.TrimSpace(n.Tool) == "" || knownToolName(cat, n.Tool) {
			continue
		}
		if !looksLikeTransform(n.Description, n.Tool) {
			continue // external capability — needs a real tool, not fabricated code
		}
		desc := strings.TrimSpace(n.Description)
		if desc == "" {
			desc = "implement what the step \"" + n.Tool + "\" was meant to do, using its upstream inputs"
		}
		code, err := GenerateNodeCode(ctx, llm, CodegenRequest{
			NodeID:      n.ID,
			Description: desc,
			Workflow:    *draft,
		})
		if err != nil || strings.TrimSpace(code) == "" {
			continue
		}
		missing := n.Tool
		n.Kind = "python"
		n.Tool = ""
		n.Code = code
		converted++
		notes = append(notes, "wrote glue code for step \""+n.ID+"\" (no tool provided \""+missing+"\")")
	}
	return converted, notes
}
