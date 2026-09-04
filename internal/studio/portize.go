// portize.go — lower template handoffs to typed port wires (the redesign's
// "typed ports, not template strings", docs/STUDIO_REDESIGN.md §4).
//
// Every template-class handoff bug (whole-object dumps, doubled paths, dangling
// refs, Go `map[...]` leakage) exists ONLY because step-to-step handoffs are
// hand-authored Go-template strings. The runtime already supports a
// template-free path: typed output/input PORTS wired by edges, resolved by
// reasoning.resolvePortInputs with no templating at all. PortizeHandoffs makes
// that path the DEFAULT by deterministically converting whole-value handoff
// templates in a tool/python node's input — `{"notebook_id":"{{ .nb.id }}"}` —
// into a typed wire: an output port on the producer exposing `id`, an input port
// on the consumer for `notebook_id`, and an edge carrying from_port→to_port. The
// template is removed; the runtime binds the value structurally.
//
// Control flow is never perturbed: the FIRST wire on a pair annotates an existing
// direct control edge (keeping its predicate); any further wires are added as
// data-only edges with `if:"false"` — never traversed for control, but read for
// data by resolvePortInputs (which ignores predicates). Pure except for mutating
// the draft. Idempotent: a node with no whole-value handoff templates is left
// untouched, so it is safe to run on every RepairWiring pass.
package studio

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// wholeValueTemplateRe matches a node-input value that is EXACTLY one template
// referencing a single upstream var (optionally wrapped in toJson/json), with an
// optional dotted field path: `{{ .nb }}`, `{{ .nb.id }}`, `{{ toJson .urls }}`.
// Anchored, so prose-with-a-template ("Summarize {{ .x }}") never matches — only
// a clean whole-value handoff is lowered to a port.
var wholeValueTemplateRe = regexp.MustCompile(
	`^\s*\{\{\s*(?:toJson\s+|json\s+)?\.([A-Za-z_][A-Za-z0-9_]*)((?:\.[A-Za-z_][A-Za-z0-9_]*)*)\s*\}\}\s*$`)

// portWire is one resolved handoff to lower: bind the consumer's argument key
// from the producer's output (whole output when fromField is empty, else the
// dotted field path).
type portWire struct {
	consumerIdx int
	producerID  string
	toKey       string // consumer argument key (becomes the input port + bind key)
	fromField   string // dotted path into producer output; "" = whole output
}

// PortizeHandoffs converts whole-value template handoffs in tool/python node
// inputs into typed port wires. The catalog (optional; pass Catalog{} when
// unavailable) lets it stamp each new input port with the consumer tool
// argument's declared type, so the generated wires carry a real, checkable
// contract. Returns the number of handoffs lowered.
func PortizeHandoffs(draft *Draft, cat Catalog) int {
	if draft == nil || len(draft.Flow.Nodes) == 0 {
		return 0
	}
	nodes := draft.Flow.Nodes

	// tool (normalized) -> arg name -> declared type, for input-port typing.
	argTypes := map[string]map[string]string{}
	for _, srv := range cat.MCP {
		for _, tl := range srv.Tools {
			if name := strings.TrimSpace(tl.Name); name != "" && strings.TrimSpace(tl.Params) != "" {
				argTypes[normalizeName(name)] = paramTypes(tl.Params)
			}
		}
	}

	// producer var -> producing node id, and node id -> index for ordering.
	producer := map[string]string{}
	idx := map[string]int{}
	for i, n := range nodes {
		idx[n.ID] = i
		if v := strings.TrimSpace(n.Output); v != "" {
			producer[v] = n.ID
		}
	}
	anc := ancestors(draft.Flow.Edges, nodes)
	hasEdges := len(draft.Flow.Edges) > 0

	// 1) Collect the wires to lower (deterministic order: node index, then key).
	var wires []portWire
	for ci := range nodes {
		n := &nodes[ci]
		if n.Kind != sdkr.FlowNodeTool && n.Kind != sdkr.FlowNodePython {
			continue
		}
		obj, ok := decodeInputObject(n.Input)
		if !ok {
			continue
		}
		for _, key := range sortedObjectKeys(obj) {
			s, ok := obj[key].(string)
			if !ok {
				continue
			}
			m := wholeValueTemplateRe.FindStringSubmatch(s)
			if m == nil {
				continue
			}
			varName, field := m[1], strings.TrimPrefix(m[2], ".")
			prod, ok := producer[varName]
			if !ok || prod == n.ID {
				continue // not a known upstream handoff, or self-reference
			}
			// Only wire from a step that runs BEFORE this one.
			if hasEdges {
				if a := anc[n.ID]; a == nil || !a[prod] {
					continue
				}
			} else if idx[prod] >= ci {
				continue
			}
			wires = append(wires, portWire{consumerIdx: ci, producerID: prod, toKey: key, fromField: field})
		}
	}
	if len(wires) == 0 {
		return 0
	}

	// 2) Apply each wire: declare ports, wire an edge, drop the template key.
	usedEdge := map[int]bool{} // existing edge indices already annotated this pass
	lowered := 0
	for _, w := range wires {
		consumer := &nodes[w.consumerIdx]

		fromPort := ""
		if w.fromField != "" {
			fromPort = ensureOutputPort(&nodes[idx[w.producerID]], w.fromField)
		}
		// Stamp the consumer input port with the tool argument's declared type
		// (when the schema publishes one), so the wire carries a real contract the
		// design-time type check can enforce.
		typ := ""
		if at := argTypes[normalizeName(consumer.Tool)]; at != nil {
			typ = at[w.toKey]
		}
		ensureInputPort(consumer, w.toKey, typ)

		if wireExists(draft.Flow.Edges, w.producerID, consumer.ID, fromPort, w.toKey) {
			dropInputKey(consumer, w.toKey)
			lowered++
			continue
		}
		wireEdge(&draft.Flow, w.producerID, consumer.ID, fromPort, w.toKey, usedEdge)
		dropInputKey(consumer, w.toKey)
		lowered++
	}
	return lowered
}

// decodeInputObject decodes a node Input that is a JSON object, or reports ok=false.
func decodeInputObject(input string) (map[string]any, bool) {
	s := strings.TrimSpace(input)
	if s == "" || s[0] != '{' {
		return nil, false
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, false
	}
	return m, true
}

func sortedObjectKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ensureOutputPort guarantees the producer declares an output port exposing the
// given dotted field, returning the port name to wire. Reuses an existing port
// with the same Field; otherwise adds one named after the field (kept unique).
func ensureOutputPort(producer *sdkr.FlowNode, field string) string {
	for _, p := range producer.Outputs {
		if p.Field == field || (p.Field == "" && p.Name == field) {
			return p.Name
		}
	}
	base := portNameFromField(field)
	name := base
	for k := 2; outputPortExists(producer.Outputs, name); k++ {
		name = base + "_" + itoa(k)
	}
	producer.Outputs = append(producer.Outputs, sdkr.FlowPort{Name: name, Field: field})
	return name
}

// ensureInputPort guarantees the consumer declares an input port named key (the
// argument the wired value binds to), carrying typ when known. Backfills the type
// on an existing untyped port; never overwrites an explicit one.
func ensureInputPort(consumer *sdkr.FlowNode, key, typ string) {
	for i := range consumer.Inputs {
		if consumer.Inputs[i].Name == key {
			if consumer.Inputs[i].Type == "" && typ != "" {
				consumer.Inputs[i].Type = typ
			}
			return
		}
	}
	consumer.Inputs = append(consumer.Inputs, sdkr.FlowPort{Name: key, Type: typ})
}

func outputPortExists(ports []sdkr.FlowPort, name string) bool {
	for _, p := range ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

// portNameFromField turns a dotted field path into a flat, identifier-safe port
// name: "notebook.id" -> "notebook_id".
func portNameFromField(field string) string {
	name := strings.ReplaceAll(field, ".", "_")
	name = varSanitizeRe.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = "out"
	}
	return name
}

// wireExists reports whether an edge already carries exactly this from/to wire,
// so re-running is idempotent.
func wireExists(edges []sdkr.FlowEdge, from, to, fromPort, toPort string) bool {
	for _, e := range edges {
		if e.From == from && e.To == to && e.FromPort == fromPort && e.ToPort == toPort {
			return true
		}
	}
	return false
}

// wireEdge attaches a from_port→to_port wire between producer and consumer. It
// first tries to annotate an existing, unused, un-annotated DIRECT control edge
// (preserving its predicate); failing that it appends a DATA-ONLY edge with
// if:"false" — read for data by resolvePortInputs, never traversed for control.
func wireEdge(f *Flow, from, to, fromPort, toPort string, usedEdge map[int]bool) {
	for i := range f.Edges {
		e := &f.Edges[i]
		if e.From == from && e.To == to && e.FromPort == "" && e.ToPort == "" && !usedEdge[i] {
			e.FromPort = fromPort
			e.ToPort = toPort
			usedEdge[i] = true
			return
		}
	}
	f.Edges = append(f.Edges, sdkr.FlowEdge{
		From: from, To: to, FromPort: fromPort, ToPort: toPort, If: "false",
	})
}

// dropInputKey removes one key from a node's JSON-object Input, clearing the
// Input entirely when only that key remained (the value now arrives via a port).
func dropInputKey(n *sdkr.FlowNode, key string) {
	m, ok := decodeInputObject(n.Input)
	if !ok {
		return
	}
	delete(m, key)
	if len(m) == 0 {
		n.Input = ""
		return
	}
	if b, err := json.Marshal(m); err == nil {
		n.Input = string(b)
	}
}
