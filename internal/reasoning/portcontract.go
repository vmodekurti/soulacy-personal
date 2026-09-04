package reasoning

// portcontract.go — P0-2 "Contract-First Workflow Generation": the compile-time
// compatibility check between a producer's output port and a consumer's input
// port.
//
// Before this, typed ports were validated for EXISTENCE only (CompileFlow's
// flowHasPort): a wire naming real ports on both ends compiled, whatever shapes
// those ports declared. So an array output wired into a string input passed
// author-time validation and failed at run time as a stringified Go slice
// ("[map[id:1] map[id:2]]") several nodes downstream, where the error named the
// consumer rather than the wire that was actually wrong.
//
// Three dimensions are checked, each only when BOTH ends declare it — an
// undeclared dimension stays permissive, so existing workflows keep compiling
// and authors can adopt contracts incrementally:
//
//	type         string vs object vs array vs number vs boolean
//	cardinality  one vs many          (the fan-out / aggregation contract)
//	nullability  nullable producer into a non-nullable consumer
//
// The escape hatch is an ADAPTER: a consumer port with Adapter:true accepts any
// producer, because declaring it is the author stating "this node reshapes the
// data." That keeps conversions visible in the graph — the requirement is not
// that conversions are forbidden, it is that they cannot be implicit.

import (
	"fmt"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// portTypeClass normalises the many spellings authors and builder models write
// into the JSON type vocabulary. "" means undeclared/unchecked, and so does any
// spelling we don't recognise — an unknown type label must never fail a build,
// only decline to add a guarantee.
func portTypeClass(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "string", "str", "text":
		return "string"
	case "number", "int", "integer", "float", "double":
		return "number"
	case "bool", "boolean":
		return "boolean"
	case "object", "map", "dict", "json_object":
		return "object"
	case "array", "list", "[]string", "string[]", "number[]", "object[]", "[]object":
		return "array"
	default:
		// "json" / "any" are deliberately unchecked: they mean "shape varies".
		return ""
	}
}

// portIsArrayType reports whether the type spelling itself denotes a collection.
// A port typed "string[]" carries many values even if its Cardinality is unset,
// so the two notations agree instead of contradicting each other.
func portIsArrayType(t string) bool {
	s := strings.ToLower(strings.TrimSpace(t))
	return strings.HasSuffix(s, "[]") || strings.HasPrefix(s, "[]") ||
		s == "array" || s == "list"
}

// portCardinality resolves a port's effective cardinality: the explicit
// Cardinality when set, otherwise inferred from an array-ish type, otherwise
// "" (undeclared → unchecked).
func portCardinality(p sdkr.FlowPort) string {
	switch strings.ToLower(strings.TrimSpace(p.Cardinality)) {
	case sdkr.CardinalityMany:
		return sdkr.CardinalityMany
	case sdkr.CardinalityOne:
		return sdkr.CardinalityOne
	}
	if portIsArrayType(p.Type) {
		return sdkr.CardinalityMany
	}
	return ""
}

// PortIncompatibility describes exactly why one wire is refused: which
// dimension disagreed, what each end declared, and the concrete remedy. It is
// returned rather than a bare error so Studio can render a targeted repair
// action instead of a sentence the user has to parse.
type PortIncompatibility struct {
	Dimension string // "type" | "cardinality" | "nullability"
	From      string // producer node id
	FromPort  string
	To        string // consumer node id
	ToPort    string
	Producer  string // what the producer declares on this dimension
	Consumer  string // what the consumer declares
	Fix       string // the concrete remedy
}

func (p PortIncompatibility) Error() string {
	return fmt.Sprintf("flow: %s %q→%s %q: %s mismatch — producer is %s, consumer expects %s. %s",
		p.From, p.FromPort, p.To, p.ToPort, p.Dimension, p.Producer, p.Consumer, p.Fix)
}

// checkPortCompatibility validates one wired edge. ok=true means the wire is
// permitted (compatible, or one end left the dimension undeclared, or the
// consumer is an adapter). Otherwise the returned incompatibility explains the
// refusal.
//
// An ADAPTER consumer short-circuits every dimension: the author has declared
// this node exists to reshape, so refusing its input would forbid the very
// escape hatch the contract rules require.
func checkPortCompatibility(fromID string, from sdkr.FlowPort, toID string, to sdkr.FlowPort) (PortIncompatibility, bool) {
	if to.Adapter {
		return PortIncompatibility{}, true
	}

	// ── Cardinality ────────────────────────────────────────────────────────
	// Checked before type: "many strings into one string" is a cardinality
	// problem, and reporting it as a type problem would send the author looking
	// at the wrong thing.
	fromCard, toCard := portCardinality(from), portCardinality(to)
	if fromCard != "" && toCard != "" && fromCard != toCard {
		fix := "insert an aggregating adapter node (or set the consumer's cardinality to many)"
		if fromCard == sdkr.CardinalityOne {
			fix = "insert an adapter that wraps the single value into a list (or set the consumer's cardinality to one)"
		}
		return PortIncompatibility{
			Dimension: "cardinality", From: fromID, FromPort: from.Name, To: toID, ToPort: to.Name,
			Producer: fromCard, Consumer: toCard,
			Fix: fix + ". A for_each node on the consumer also resolves many→one, by running the body once per item.",
		}, false
	}

	// ── Type ───────────────────────────────────────────────────────────────
	fromType, toType := portTypeClass(from.Type), portTypeClass(to.Type)
	if fromType != "" && toType != "" && fromType != toType {
		// number→string is the one widening every JSON encoder performs
		// identically and no author has ever been surprised by. Everything else
		// (object→array, string→object, array→string) is a real reshape.
		if !(fromType == "number" && toType == "string") {
			return PortIncompatibility{
				Dimension: "type", From: fromID, FromPort: from.Name, To: toID, ToPort: to.Name,
				Producer: fromType, Consumer: toType,
				Fix: fmt.Sprintf("route this wire through an adapter node that converts %s to %s, or correct one of the declared port types",
					fromType, toType),
			}, false
		}
	}

	// ── Nullability ────────────────────────────────────────────────────────
	// Only meaningful when the consumer has actually declared a type or is
	// required; a wholly undeclared port makes no promise to violate.
	if from.Nullable && !to.Nullable && (to.Required || toType != "") {
		return PortIncompatibility{
			Dimension: "nullability", From: fromID, FromPort: from.Name, To: toID, ToPort: to.Name,
			Producer: "nullable", Consumer: "non-nullable",
			Fix: "insert an adapter that supplies a default when the value is absent, or mark the consumer port nullable",
		}, false
	}

	return PortIncompatibility{}, true
}

// checkRequiredInputs verifies every port declared Required on a node is
// actually fed: by a wired incoming edge, by the node's static Input template,
// or — for a for_each body — by the loop item itself.
//
// This is the contract counterpart to the runtime's forgiving behaviour: at run
// time an unwired port binds a zero value on purpose (a slightly-off wire should
// degrade, not abort mid-run). Declaring a port Required moves that decision to
// author time, where it is cheap to fix.
func checkRequiredInputs(spec sdkr.FlowSpec, node sdkr.FlowNode) error {
	if len(node.Inputs) == 0 {
		return nil
	}
	wired := map[string]bool{}
	for _, e := range spec.Edges {
		if e.To == node.ID && e.ToPort != "" {
			wired[e.ToPort] = true
		}
	}
	for _, p := range node.Inputs {
		if !p.Required || wired[p.Name] {
			continue
		}
		// A static Input template that mentions the port's key satisfies it —
		// constants and hand-authored args are legitimate sources.
		key := p.Name
		if strings.TrimSpace(p.Field) != "" {
			key = p.Field
		}
		if key != "" && strings.Contains(node.Input, key) {
			continue
		}
		// Inside a for_each body the loop item is an implicit input.
		if node.ForEach != "" && node.ItemVar != "" && strings.Contains(node.Input, node.ItemVar) {
			continue
		}
		return fmt.Errorf("flow: node %q requires input port %q but nothing supplies it — wire an upstream output into it, or provide it in the node's input", node.ID, p.Name)
	}
	return nil
}

// validatePortContracts runs the full P0-2 check over a compiled node set:
// required inputs are satisfied, and every WIRED edge connects compatible
// ports. Edges without both FromPort and ToPort are untyped handoffs and are
// left alone — they carry no declared contract to check.
func validatePortContracts(spec sdkr.FlowSpec, nodes map[string]sdkr.FlowNode) error {
	for _, n := range spec.Nodes {
		compiled, ok := nodes[n.ID]
		if !ok {
			continue
		}
		if err := checkRequiredInputs(spec, compiled); err != nil {
			return err
		}
	}
	for i, e := range spec.Edges {
		if e.FromPort == "" || e.ToPort == "" || flowEdgeTerminal(e.To) {
			continue
		}
		fromNode, ok := nodes[e.From]
		if !ok {
			continue
		}
		toNode, ok := nodes[e.To]
		if !ok {
			continue
		}
		fromPort := findPort(fromNode.Outputs, e.FromPort)
		toPort := findPort(toNode.Inputs, e.ToPort)
		if fromPort == nil || toPort == nil {
			continue // existence is already enforced by CompileFlow
		}
		// A for_each consumer receives ONE item per invocation, so a "many"
		// producer feeding it is the intended shape, not a mismatch. Model that
		// by treating the consumer as many-valued for this check.
		effectiveTo := *toPort
		if toNode.ForEach != "" && portCardinality(effectiveTo) == sdkr.CardinalityOne {
			effectiveTo.Cardinality = sdkr.CardinalityMany
		}
		if bad, ok := checkPortCompatibility(e.From, *fromPort, e.To, effectiveTo); !ok {
			return fmt.Errorf("edge %d: %w", i, bad)
		}
	}
	return nil
}
