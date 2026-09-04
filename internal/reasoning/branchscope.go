package reasoning

// branchscope.go — a node running INSIDE a parallel branch can only see that
// branch's variables, so a template that reads a sibling branch's output can
// never render.
//
// Each branch of a kind=parallel node runs with its OWN copy of the flow vars
// (runFlowParallel: `bv := copyFlowVars(vars)`) and walks until it reaches the
// node named in join_node. Sibling branches run on separate goroutines against
// separate maps; nothing crosses between them until the join merges them back
// into the parent. So for any node executed within a branch, the readable
// variables are exactly: everything produced before the fan-out, plus what that
// one branch produced itself.
//
// When join_node is empty (or names a node that comes AFTER the real
// convergence point) the branches walk past the place they were meant to stop,
// and a shared merge step runs once PER BRANCH — each copy inside a scope where
// its siblings' outputs do not exist and never will. The graph looks correct on
// the canvas either way; the failure only appears when the thing is run:
//
//	flow: node "fan_out_specialists": branch "fundamentals_analyst":
//	  flow: node "editor": render input: execute template:
//	  executing "" at <.risk_analysis>: map has no entry for key "risk_analysis"
//
// That message describes the symptom at the point of collapse. It names neither
// the cause (the fan-out has no barrier) nor the fix (name one). Worse, it only
// arrives on the first real run — which for a scheduled agent is at 07:30 on a
// weekday, in front of whoever was waiting for the briefing.
//
// The shape is decidable without running anything: walk each branch, collect
// what it can see, and check every template it contains against that. This is
// deliberately placed in CompileFlow rather than in Studio's checks, because
// Studio only guards graphs that pass through Studio. Hand-written YAML, an
// agent saved before this check existed, and an imported template all reach the
// engine directly — and every one of them funnels through CompileFlow.
//
// Only references that are IMPOSSIBLE are refused: the variable must be
// produced somewhere in this flow, and be unreachable from the branch doing the
// reading. A reference to a var no node produces is a different mistake (a
// typo, or an input supplied at run time) and is left to the checks that own it.

import (
	"fmt"
	"regexp"
	"strings"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// templateVarRe matches a flow-variable reference inside a Go-template
// expression: the identifier immediately after a dot, e.g. `.articles` in
// `{{ toJson .articles }}`. Only the leading identifier of a chain matters —
// `.report.title` is a reference to `report`.
var templateVarRe = regexp.MustCompile(`(?:{{|\s)\.([A-Za-z_][A-Za-z0-9_]*)`)

// pythonInputRe matches the inline-Python read of a flow var:
// inputs.get("foo"), inputs['foo'].
var pythonInputRe = regexp.MustCompile(`inputs\s*(?:\.\s*get\s*\(\s*|\[\s*)["']([A-Za-z_][A-Za-z0-9_]*)["']`)

// TemplateVars returns the distinct flow-var identifiers a Go template reads,
// in order of first appearance. Exported so the one grammar for "what does this
// template depend on" is shared rather than re-derived per package.
func TemplateVars(input string) []string {
	if !strings.Contains(input, "{{") {
		return nil
	}
	return distinctSubmatches(templateVarRe, input)
}

// PythonInputVars returns the distinct flow-var keys inline Python reads off
// its `inputs` payload.
func PythonInputVars(code string) []string {
	if !strings.Contains(code, "inputs") {
		return nil
	}
	return distinctSubmatches(pythonInputRe, code)
}

func distinctSubmatches(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		v := m[len(m)-1]
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// flowBuiltinVars are seeded by the runtime on every flow run, so a reference to
// one is always satisfiable and is never a scope error.
var flowBuiltinVars = map[string]bool{
	"trigger": true,
	"history": true,
	"failure": true, // bound for the escalation node
	"item":    true, // default for_each item binding
}

// validateBranchScope refuses a flow in which a node inside a parallel branch
// reads a variable that only a sibling branch produces.
func validateBranchScope(spec sdkr.FlowSpec, nodes map[string]sdkr.FlowNode, out map[string][]int) error {
	// producer: var -> the node that writes it. A var written by more than one
	// node is recorded once per writer, because reachability of ANY writer is
	// enough to make the read legal.
	producers := map[string][]string{}
	for _, n := range nodes {
		if v := strings.TrimSpace(n.Output); v != "" {
			producers[v] = append(producers[v], n.ID)
		}
	}
	if len(producers) == 0 {
		return nil
	}

	// Deterministic order: a flow with two mis-scoped branches must report the
	// same one every time, or the same graph yields different errors run to run.
	for _, decl := range spec.Nodes {
		p, ok := nodes[decl.ID]
		if !ok || p.Kind != sdkr.FlowNodeParallel {
			continue
		}
		if err := checkParallelScope(spec, nodes, out, p, producers); err != nil {
			return err
		}
	}
	return nil
}

func checkParallelScope(
	spec sdkr.FlowSpec,
	nodes map[string]sdkr.FlowNode,
	out map[string][]int,
	p sdkr.FlowNode,
	producers map[string][]string,
) error {
	starts := branchEntries(spec, out, p.ID)
	if len(starts) < 2 {
		return nil
	}

	// Everything upstream of the fan-out is copied into every branch, so its
	// outputs are readable everywhere below. Permissive on purpose: whether an
	// upstream node actually ran before this one is an ordering question that
	// belongs to the data-flow check, not to branch scoping.
	shared := map[string]bool{}
	for _, n := range nodes {
		if n.ID == p.ID || flowReaches(spec, out, n.ID, p.ID) {
			if v := strings.TrimSpace(n.Output); v != "" {
				shared[v] = true
			}
		}
	}

	walks := make([]map[string]bool, len(starts))
	for i, s := range starts {
		walks[i] = branchWalk(spec, out, s, p.ID, p.JoinNode)
	}

	for i, s := range starts {
		visible := map[string]bool{}
		for id := range walks[i] {
			if v := strings.TrimSpace(nodes[id].Output); v != "" {
				visible[v] = true
			}
		}
		// Stable iteration over the branch's nodes.
		for _, decl := range spec.Nodes {
			if !walks[i][decl.ID] {
				continue
			}
			n := nodes[decl.ID]
			for _, v := range nodeReadVars(n) {
				if flowBuiltinVars[v] || v == n.ItemVar || shared[v] || visible[v] {
					continue
				}
				owners := producers[v]
				if len(owners) == 0 {
					continue // not produced anywhere: a dangling reference, not a scope error
				}
				sibling := siblingOwner(owners, walks, i)
				if sibling == "" {
					continue
				}
				return scopeError(p, s, n.ID, v, sibling, walks, starts, nodes)
			}
		}
	}
	return nil
}

// siblingOwner returns the id of a node that writes the var and lives in a
// DIFFERENT branch of the same fan-out — the case where the read is provably
// impossible rather than merely unproven.
func siblingOwner(owners []string, walks []map[string]bool, self int) string {
	for _, o := range owners {
		for j, w := range walks {
			if j != self && w[o] {
				return o
			}
		}
	}
	return ""
}

// branchEntries lists the distinct nodes a parallel node forks to, in edge
// order.
func branchEntries(spec sdkr.FlowSpec, out map[string][]int, id string) []string {
	seen := map[string]bool{}
	var res []string
	for _, ei := range out[id] {
		to := spec.Edges[ei].To
		if flowEdgeTerminal(to) || to == id || seen[to] {
			continue
		}
		seen[to] = true
		res = append(res, to)
	}
	return res
}

// branchWalk is the set of nodes one branch executes: forward from its entry,
// never re-entering the fan-out, stopping AT the barrier without including it
// (the barrier runs once, in the parent scope, after the join).
func branchWalk(spec sdkr.FlowSpec, out map[string][]int, entry, fanOut, barrier string) map[string]bool {
	res := map[string]bool{}
	if entry == barrier || entry == fanOut {
		return res
	}
	res[entry] = true
	queue := []string{entry}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, ei := range out[cur] {
			to := spec.Edges[ei].To
			if flowEdgeTerminal(to) || to == fanOut || to == barrier || res[to] {
				continue
			}
			res[to] = true
			queue = append(queue, to)
		}
	}
	return res
}

// nodeReadVars is every flow var a node reads, across the fields that can hold
// one.
func nodeReadVars(n sdkr.FlowNode) []string {
	var res []string
	seen := map[string]bool{}
	add := func(vs []string) {
		for _, v := range vs {
			if !seen[v] {
				seen[v] = true
				res = append(res, v)
			}
		}
	}
	add(TemplateVars(n.Input))
	add(TemplateVars(n.ForEach))
	if n.Kind == sdkr.FlowNodePython {
		add(PythonInputVars(n.Code))
	}
	return res
}

// scopeError says what is wrong, why it cannot work, and what to change. The
// barrier is named when the shape implies one, because "set join_node: editor"
// is a fix the reader can apply, and "your branches are mis-scoped" is not.
func scopeError(
	p sdkr.FlowNode,
	branchEntry, reader, v, writer string,
	walks []map[string]bool,
	starts []string,
	nodes map[string]sdkr.FlowNode,
) error {
	// A node present in EVERY branch is the place they were meant to converge.
	inAll := true
	for _, w := range walks {
		if !w[reader] {
			inAll = false
			break
		}
	}
	if inAll && len(starts) > 1 {
		return fmt.Errorf(
			"flow: node %q runs inside every branch of parallel node %q and reads %q, which is written by %q in a different branch — "+
				"set join_node: %s on %q so it runs once, after the branches, with all of their outputs",
			reader, p.ID, v, writer, reader, p.ID)
	}
	if p.JoinNode != "" {
		return fmt.Errorf(
			"flow: node %q runs inside branch %q of parallel node %q (whose barrier is %q) and reads %q, written by %q in a sibling branch — "+
				"move the barrier back to %q, or give this branch its own source for %q",
			reader, branchEntry, p.ID, p.JoinNode, v, writer, reader, v)
	}
	return fmt.Errorf(
		"flow: node %q runs inside branch %q of parallel node %q and reads %q, written by %q in a sibling branch — "+
			"branches cannot see each other's variables; name the barrier with join_node on %q, or give this branch its own source for %q",
		reader, branchEntry, p.ID, v, writer, p.ID, v)
}
