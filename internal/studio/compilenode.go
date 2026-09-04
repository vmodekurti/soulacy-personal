package studio

import (
	"context"
	"fmt"
	"strings"

	reasoning "github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// compilenode.go — Phase C: per-component compilation. The heart of "no code."
// Each node carries an Intent (plain language); CompileNode turns THAT ONE node's
// intent into concrete config — a tool + args, an mcp__ call, a read_skill, a
// peer agent, or an inline python block. Scoping the LLM to a single node (with
// the upstream OUTPUT SHAPES it can wire from) is far more reliable than
// whole-graph generation, and reuses all the draft-parsing hardening
// (object-input coercion, kind normalization, python capability classification).

// UpstreamVar describes one flow variable available to the node being compiled:
// its name and, when known, a JSON SAMPLE/shape of the producer's output so the
// model wires by real field names (and via typed ports) instead of guessing.
type UpstreamVar struct {
	Name  string `json:"name"`
	Shape string `json:"shape,omitempty"`
}

// CompileNodeRequest is the input to CompileNode.
type CompileNodeRequest struct {
	Intent   string        `json:"intent"`
	NodeID   string        `json:"nodeId,omitempty"`   // keep the canvas node's id when recompiling
	Kind     string        `json:"kind,omitempty"`     // optional hint: tool|agent|python|"" (let the model choose)
	Upstream []UpstreamVar `json:"upstream,omitempty"` // available upstream outputs (name + shape)
	Catalog  Catalog       `json:"catalog,omitempty"`
}

// CompileNode compiles a single node from its plain-language Intent. It wraps the
// model's answer as a one-node draft so it rides ParseDraft's coercion, then
// normalizes the kind, classifies python capabilities, and validates the node
// compiles on its own. The returned node carries its Intent (parity) and the
// requested id.
func CompileNode(ctx context.Context, llm LLM, req CompileNodeRequest) (sdkr.FlowNode, error) {
	if strings.TrimSpace(req.Intent) == "" {
		return sdkr.FlowNode{}, fmt.Errorf("studio: node intent is required")
	}
	if llm == nil {
		return sdkr.FlowNode{}, fmt.Errorf("studio: no LLM configured")
	}

	prompt := BuildNodePrompt(req)
	raw, err := llm.Complete(ctx, prompt)
	if err != nil {
		return sdkr.FlowNode{}, fmt.Errorf("studio: compile node: %w", err)
	}

	// Reuse the whole draft-parsing hardening by treating the answer as a 1-node
	// draft (ParseDraft coerces object-typed "input", strips fences, etc.).
	draft, err := ParseDraft(raw)
	if err != nil {
		return sdkr.FlowNode{}, fmt.Errorf("studio: compile node: %w", err)
	}
	if len(draft.Flow.Nodes) == 0 {
		return sdkr.FlowNode{}, fmt.Errorf("studio: compile node: model returned no node")
	}
	normalizeFlow(&draft)          // settle Kind, unwrap any {"code":…} envelope
	classifyFlowNodes(&draft.Flow) // set Requires on a python node

	node := draft.Flow.Nodes[0]
	if id := strings.TrimSpace(req.NodeID); id != "" {
		node.ID = id
	} else if strings.TrimSpace(node.ID) == "" {
		node.ID = "step"
	}
	node.Intent = req.Intent // parity: the node remembers its prompt

	// The node must compile on its own (right kind/fields). Edges are out of scope
	// here — this is a single-node contract check.
	if _, verr := reasoning.CompileFlow(sdkr.FlowSpec{Nodes: []sdkr.FlowNode{node}, Entry: node.ID}); verr != nil {
		return sdkr.FlowNode{}, fmt.Errorf("studio: compiled node is invalid: %w", verr)
	}
	return node, nil
}

// BuildNodePrompt builds the scoped, single-node instruction. It pins the node
// JSON shape, grounds the model in the catalog (tools/mcp/skills/agents) and the
// upstream output shapes, and forbids inventing tool names.
func BuildNodePrompt(req CompileNodeRequest) string {
	var sb strings.Builder
	sb.WriteString("You are the Soulacy Studio per-step compiler. Turn ONE workflow step description into ONE flow node.\n\n")
	sb.WriteString("Output RULES:\n")
	sb.WriteString("- Respond with ONLY a single JSON object of this shape, no prose, no code fences:\n")
	sb.WriteString(`{"flow":{"nodes":[{"id":"step","kind":"tool|agent|python","tool":"<tool name if kind=tool>","agent":"<agent id if kind=agent>","code":"<python def run(inputs): … if kind=python>","input":"<stringified JSON args or task prompt>","output":"<result var name>"}]}}`)
	sb.WriteString("\n\n")
	sb.WriteString("- Choose kind: a TOOL/MCP call when one in the catalog covers this operation; an AGENT when reasoning/summarizing is needed; PYTHON only for PURE DATA GLUE no tool covers (parsing, reshaping, deduping, formatting, computation). NEVER use a python node to call a tool/MCP or to shell out to a CLI (subprocess) that wraps one — that operation MUST be a tool/MCP node. Do NOT invent tool names.\n")
	sb.WriteString("- A tool node's \"input\" is a STRINGIFIED JSON object of the tool's real arguments. A python node's \"code\" is a complete def run(inputs): returning the result. An agent node's \"input\" is the full task prompt.\n")
	sb.WriteString("- Give the node a meaningful \"output\" var name.\n")
	if k := strings.TrimSpace(req.Kind); k != "" {
		sb.WriteString("- Preferred kind for this step: ")
		sb.WriteString(k)
		sb.WriteString(" (use it unless clearly wrong).\n")
	}

	// Ground in the upstream output shapes so the node wires by real field names.
	if len(req.Upstream) > 0 {
		sb.WriteString("\nUpstream outputs available to this step (reference with {{ .name }} or a typed port):\n")
		for _, u := range req.Upstream {
			name := strings.TrimSpace(u.Name)
			if name == "" {
				continue
			}
			sb.WriteString("- ")
			sb.WriteString(name)
			if s := strings.TrimSpace(u.Shape); s != "" {
				if len(s) > 300 {
					s = s[:300] + "…"
				}
				sb.WriteString(" — sample: ")
				sb.WriteString(s)
			}
			sb.WriteString("\n")
		}
	}

	// Reuse the existing catalog grounding blocks (tools/mcp/skills/agents).
	cat := FilterCatalogForIntent(req.Intent, req.Catalog)
	if len(cat.Tools) > 0 {
		sb.WriteString("\nAvailable tools: ")
		sb.WriteString(strings.Join(cat.Tools, ", "))
		sb.WriteString("\n")
	}
	if len(cat.Agents) > 0 {
		sb.WriteString("Available agents: ")
		sb.WriteString(strings.Join(cat.Agents, ", "))
		sb.WriteString("\n")
	}
	if len(cat.Skills) > 0 {
		sb.WriteString("Available skills (use read_skill with the EXACT name): ")
		names := make([]string, 0, len(cat.Skills))
		for _, sk := range cat.Skills {
			if n := strings.TrimSpace(sk.Name); n != "" {
				names = append(names, n)
			}
		}
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}
	if len(cat.MCP) > 0 {
		sb.WriteString("Available MCP servers and tools (use the EXACT tool name):\n")
		for _, srv := range cat.MCP {
			for _, tl := range srv.Tools {
				if tn := strings.TrimSpace(tl.Name); tn != "" {
					sb.WriteString("  • ")
					sb.WriteString(tn)
					if p := strings.TrimSpace(tl.Params); p != "" {
						sb.WriteString("(")
						sb.WriteString(p)
						sb.WriteString(")")
					}
					sb.WriteString("\n")
				}
			}
		}
	}

	sb.WriteString("\nStep to compile:\n")
	sb.WriteString(req.Intent)
	sb.WriteString("\n")
	return sb.String()
}
