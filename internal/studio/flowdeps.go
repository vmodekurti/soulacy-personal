package studio

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/soulacy/soulacy/internal/reasoning"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// flowVarNameRe is the identifier grammar a node output variable must satisfy
// to be referenceable from Go templates and Python inputs by name.
var flowVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// pythonInputRefRe captures the common inline-Python contract:
// inputs.get("foo"), inputs['foo'], and inputs.get('foo', default). Most keys
// must be supplied by the node input object, a typed input port, or an upstream
// output var available to the node. We keep whether it was a defensive .get()
// because entry/input-normalizer blocks often probe optional inbound aliases.
var pythonInputRefRe = regexp.MustCompile(`inputs\s*(?:(\.\s*get)\s*\(\s*|\[\s*)["']([A-Za-z_][A-Za-z0-9_]*)["']`)

// builtinFlowVars are seeded by the runtime for EVERY flow run (see
// internal/runtime/flow.go: vars{"trigger", "history"}), so a reference to them
// is always satisfied and must NOT be flagged as a missing dependency. The
// entry step legitimately reads the incoming message via {{ .trigger.text }}.
var builtinFlowVars = map[string]bool{
	"trigger": true, // { text: <incoming message>, ... } — the trigger payload
	"history": true, // prior conversation turns
}

var inboundTextAliases = map[string]bool{
	"message": true,
	"text":    true,
	"input":   true,
	"query":   true,
	"prompt":  true,
}

type pythonInputRef struct {
	Key      string
	Optional bool
}

// checkDataFlow validates cross-step data dependencies (Story #3): every flow
// variable a node references in its Input template must be PRODUCED by another
// node, and that producer must run BEFORE the consumer. A reference to a var no
// node outputs is a blocker (dangling); a reference to a var produced only by a
// non-ancestor (so it isn't guaranteed to exist when this node runs) is a
// warning. Pure + deterministic; appended to a PreflightResult by Preflight.
//
// This catches the broken-sequence class the per-node required-arg check can't:
// e.g. an "add sources" node templating {{ .notebook_id }} when the
// notebook-creating node runs AFTER it (or doesn't output notebook_id at all).
func checkDataFlow(draft Draft, add func(sev, kind, node, msg, fix string)) {
	nodes := draft.Flow.Nodes
	if len(nodes) == 0 {
		return
	}

	// producer: output var -> node id that produces it.
	producer := map[string]string{}
	for _, n := range nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}

	// Build ancestor sets via the edges (who can run before whom).
	anc := ancestors(draft.Flow.Edges, nodes)

	for _, n := range nodes {
		checkTemplateVars(n.ID, n.ForEach, producer, anc, nil, add)
		locals := map[string]bool{}
		if strings.TrimSpace(n.ForEach) != "" {
			itemVar := strings.TrimSpace(n.ItemVar)
			if itemVar == "" {
				itemVar = "item"
			}
			locals[itemVar] = true
			locals[itemVar+"_index"] = true
		}
		checkTemplateVars(n.ID, n.Input, producer, anc, locals, add)
		if strings.TrimSpace(n.Kind) == sdkr.FlowNodePython {
			checkPythonInputs(n, draft.Flow.Entry, draft.Flow.Edges, producer, anc, add)
		}
	}

	for i, e := range draft.Flow.Edges {
		edgeID := "edge " + itoa(i+1)
		if !flowTerminal(e.To) {
			checkTemplateVars(edgeID, e.If, producer, anc, nil, add)
		}
		if strings.TrimSpace(e.ToPort) == "" {
			continue
		}
		from := strings.TrimSpace(e.From)
		src, ok := flowNodeByID(nodes, from)
		if !ok {
			continue // CompileFlow reports dangling source nodes.
		}
		if strings.TrimSpace(src.Output) == "" {
			add("block", "dependency", e.To,
				"Typed port wire from \""+from+"\" cannot carry data because that step has no output variable.",
				"Set an output variable on \""+from+"\" or remove the typed port wire.")
		}
	}
}

// checkOutputContracts validates the names nodes publish into the workflow
// variable map. These names are global within one flow run: duplicates clobber
// each other, invalid identifiers cannot be referenced as {{ .var }}, and names
// such as "trigger" or "history" overwrite runtime-provided inputs. Catching
// this at authoring time prevents downstream steps from reading the wrong value
// while the graph still appears to compile.
func checkOutputContracts(draft Draft, add func(sev, kind, node, msg, fix string)) {
	seen := map[string]string{}
	for _, n := range draft.Flow.Nodes {
		out := strings.TrimSpace(n.Output)
		if out == "" {
			continue
		}
		if sdkr.IsStructuralKind(n.Kind) {
			add("warn", "dependency", n.ID,
				"Structural node \""+n.ID+"\" declares output variable \""+out+"\", but trigger/branch/exit nodes do not publish data.",
				"Remove the output variable from this structural node, or move the output to a tool, Python, LLM, or agent step.")
			continue
		}
		if !flowVarNameRe.MatchString(out) {
			add("block", "dependency", n.ID,
				"Output variable \""+out+"\" is not a valid workflow identifier.",
				"Use letters, numbers, and underscores only, and start with a letter or underscore.")
			continue
		}
		if builtinFlowVars[out] {
			add("block", "dependency", n.ID,
				"Output variable \""+out+"\" is reserved by the workflow runtime.",
				"Rename this output variable so it does not replace the built-in \""+out+"\" value.")
			continue
		}
		if first := seen[out]; first != "" && first != n.ID {
			add("block", "dependency", n.ID,
				"Output variable \""+out+"\" is produced by both \""+first+"\" and \""+n.ID+"\".",
				"Give each step a unique output variable name so downstream references are unambiguous.")
			continue
		}
		seen[out] = n.ID
	}
}

func checkTemplateVars(nodeID, input string, producer map[string]string, anc map[string]map[string]bool, locals map[string]bool, add func(sev, kind, node, msg, fix string)) {
	for _, v := range referencedVars(input) {
		if builtinFlowVars[v] || locals[v] {
			continue // always seeded by the runtime — not a step dependency
		}
		prod, ok := producer[v]
		if !ok {
			fix := "Add a step that outputs \"" + v + "\" before this one, or fix the variable name."
			if strings.Contains(input, "range ") || v == "url" || v == "id" {
				fix += " If you are trying to extract a list of fields from an array of objects, use {{ pluck \"" + v + "\" .upstream_var | toJson }} instead of loops."
			}
			add("block", "dependency", nodeID,
				"Step references {{ ."+v+" }} but no earlier step produces \""+v+"\".",
				fix)
			continue
		}
		if prod == nodeID {
			continue // self-reference (unusual but not a missing dep)
		}
		if a := anc[nodeID]; a != nil && !a[prod] {
			add("warn", "dependency", nodeID,
				"Step uses {{ ."+v+" }} from \""+prod+"\", which is not guaranteed to run before it.",
				"Wire an edge so \""+prod+"\" runs before \""+nodeID+"\".")
		}
	}
}

func checkPythonInputs(n sdkr.FlowNode, entry string, edges []sdkr.FlowEdge, producer map[string]string, anc map[string]map[string]bool, add func(sev, kind, node, msg, fix string)) {
	for _, ref := range pythonInputRefs(n.Code) {
		key := ref.Key
		if builtinFlowVars[key] {
			continue
		}
		if isInboundAliasProbe(n, entry, ref) {
			continue
		}
		if inputObjectHasKey(n.Input, key) || nodeHasIncomingPort(n, edges, key) {
			continue
		}
		prod, ok := producer[key]
		if ok && prod != n.ID {
			if a := anc[n.ID]; a != nil && a[prod] {
				continue
			}
			add("warn", "dependency", n.ID,
				"Python code reads inputs[\""+key+"\"] from \""+prod+"\", which is not guaranteed to run before it.",
				"Wire an edge so \""+prod+"\" runs before \""+n.ID+"\", or pass it through a typed input port.")
			continue
		}
		add("block", "dependency", n.ID,
			"Python code reads inputs[\""+key+"\"] but no input, typed port, or earlier step provides \""+key+"\".",
			"Add a JSON input key, wire a typed input port, or add an earlier step that outputs \""+key+"\".")
	}
}

func isInboundAliasProbe(n sdkr.FlowNode, entry string, ref pythonInputRef) bool {
	if !ref.Optional || !inboundTextAliases[ref.Key] {
		return false
	}
	if strings.TrimSpace(n.ID) == "" {
		return false
	}
	e := strings.TrimSpace(entry)
	if e == "" {
		return false
	}
	if strings.TrimSpace(n.ID) != e {
		return false
	}
	return strings.Contains(n.Code, `inputs.get("trigger"`) ||
		strings.Contains(n.Code, `inputs.get('trigger'`) ||
		strings.Contains(n.Code, `inputs["trigger"]`) ||
		strings.Contains(n.Code, `inputs['trigger']`)
}

// referencedVars extracts the distinct flow-var identifiers referenced in a
// node's input template. Returns nil for non-template inputs.
//
// Delegates to the engine's grammar rather than keeping a second copy of it.
// Studio's data-flow check and the engine's branch-scope check must agree on
// what a template reads, or one of them polices a dependency the other cannot
// see — and a rule that disagrees with the thing it is guarding is worse than
// no rule, because it reads as coverage.
func referencedVars(input string) []string { return reasoning.TemplateVars(input) }

func pythonInputRefs(code string) []pythonInputRef {
	if !strings.Contains(code, "inputs") {
		return nil
	}
	seen := map[string]bool{}
	var out []pythonInputRef
	for _, m := range pythonInputRefRe.FindAllStringSubmatch(code, -1) {
		k := m[2]
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, pythonInputRef{Key: k, Optional: strings.Contains(m[1], ".")})
	}
	return out
}

func inputObjectHasKey(input, key string) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(input), &m); err == nil {
		_, ok := m[key]
		return ok
	}
	return regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:`).MatchString(input)
}

func nodeHasIncomingPort(n sdkr.FlowNode, edges []sdkr.FlowEdge, key string) bool {
	for _, p := range n.Inputs {
		bind := p.Name
		if p.Field != "" {
			bind = p.Field
		}
		if bind != key {
			continue
		}
		for _, e := range edges {
			if e.To == n.ID && e.ToPort == p.Name {
				return true
			}
		}
	}
	return false
}

func flowNodeByID(nodes []sdkr.FlowNode, id string) (sdkr.FlowNode, bool) {
	for _, n := range nodes {
		if n.ID == id {
			return n, true
		}
	}
	return sdkr.FlowNode{}, false
}

// ancestors returns, for each node id, the set of node ids that can run before
// it (transitive predecessors over the directed edges). Robust to cycles.
func ancestors(edges []sdkr.FlowEdge, nodes []sdkr.FlowNode) map[string]map[string]bool {
	preds := map[string][]string{}
	for _, e := range edges {
		from := strings.TrimSpace(e.From)
		to := strings.TrimSpace(e.To)
		if from == "" || to == "" {
			continue
		}
		preds[to] = append(preds[to], from)
	}
	out := map[string]map[string]bool{}
	for _, n := range nodes {
		set := map[string]bool{}
		var walk func(id string)
		walk = func(id string) {
			for _, p := range preds[id] {
				if !set[p] {
					set[p] = true
					walk(p)
				}
			}
		}
		walk(n.ID)
		out[n.ID] = set
	}
	return out
}
