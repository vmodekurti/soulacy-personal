package studio

// joinnode.go — a fan-out that reconverges must name where it reconverges.
//
// A parallel node's branches each walk until they reach the node named in
// `join_node`, and stop there without running it. The barrier then runs ONCE,
// after every branch, and sees all of their outputs. With `join_node` empty each
// branch instead walks to the end of the graph — so a node the branches share is
// executed once PER BRANCH, and each copy sees only its own branch's variables.
//
// Found by dry-running a generated three-specialist workflow that every static
// check had passed as VALID, 0 blockers, 0 warnings:
//
//	studio: test run: flow: node "fan_out_specialists": branch "fundamentals_analyst":
//	  flow: node "editor": render input: execute template:
//	  executing "" at <.risk_analysis>: map has no entry for key "risk_analysis"
//
// Read the path: the editor ran INSIDE the fundamentals branch, where the risk
// branch's output does not exist and never will. The graph was otherwise
// perfect — `join: all` declared, three analysts, all three edges converging on
// the editor. It named the policy and omitted the place, and the omission is
// invisible until the thing is run.
//
// The barrier is inferable when every branch converges on one node, so infer it
// rather than asking the user to know this. What cannot be inferred is reported
// instead of guessed.

import sdkr "github.com/soulacy/soulacy/sdk/reasoning"

// ConvergenceOf returns the single node every branch of `p` reaches, or "" when
// the branches do not all converge on exactly one node.
//
// "" is the honest answer for the shapes where a barrier is not implied: a
// fan-out whose branches genuinely end separately (three notifications, no
// summary), or one whose branches meet at more than one place — where picking
// one would be a guess about which the author meant.
func ConvergenceOf(flow Flow, p sdkr.FlowNode) string {
	starts := branchStarts(flow, p.ID)
	if len(starts) < 2 {
		return "" // not a fan-out in any meaningful sense
	}

	// The nodes each branch can reach, walking forward and never re-entering the
	// fan-out itself.
	reach := make([]map[string]bool, 0, len(starts))
	for _, s := range starts {
		reach = append(reach, reachableFrom(flow, s, p.ID))
	}

	// Candidates: reachable from EVERY branch, and not a branch start itself —
	// a branch start is inside one branch, so it cannot be the barrier for all.
	startSet := map[string]bool{}
	for _, s := range starts {
		startSet[s] = true
	}
	var common []string
	for id := range reach[0] {
		if startSet[id] {
			continue
		}
		inAll := true
		for _, r := range reach[1:] {
			if !r[id] {
				inAll = false
				break
			}
		}
		if inAll {
			common = append(common, id)
		}
	}
	if len(common) == 0 {
		return ""
	}

	// With several shared nodes the barrier is the EARLIEST — the one that
	// reaches all the others. Anything after it is downstream of the join, not
	// the join. If no single candidate dominates the rest, the shape is
	// ambiguous and this returns "" rather than choosing.
	for _, c := range common {
		r := reachableFrom(flow, c, p.ID)
		dominates := true
		for _, other := range common {
			if other == c {
				continue
			}
			if !r[other] {
				dominates = false
				break
			}
		}
		if dominates {
			return c
		}
	}
	return ""
}

// branchStarts lists the distinct nodes a parallel node forks to.
func branchStarts(flow Flow, id string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range flow.Edges {
		if e.From != id || e.To == "" || e.To == id {
			continue
		}
		if seen[e.To] {
			continue
		}
		seen[e.To] = true
		out = append(out, e.To)
	}
	return out
}

// reachableFrom walks forward from `start`, never passing back through `avoid`.
func reachableFrom(flow Flow, start, avoid string) map[string]bool {
	out := map[string]bool{}
	next := map[string][]string{}
	for _, e := range flow.Edges {
		if e.From == "" || e.To == "" {
			continue
		}
		next[e.From] = append(next[e.From], e.To)
	}
	var walk func(string)
	walk = func(id string) {
		for _, to := range next[id] {
			if to == avoid || out[to] {
				continue
			}
			out[to] = true
			walk(to)
		}
	}
	out[start] = true
	walk(start)
	delete(out, start) // a branch's own start is not something it "reaches"
	return out
}

// InferJoinNodes fills in `join_node` for every parallel node whose branches
// converge on one place and that has not named it. Returns how many it set.
//
// Only ever ADDS: an explicit join_node is the author's, and this must not
// second-guess it. A shape with no single convergence point is left alone for
// assessJoinBarrier to report, because a guessed barrier runs the wrong node
// once instead of the right node three times — a quieter failure than the one
// being fixed.
func InferJoinNodes(flow *Flow) int {
	if flow == nil {
		return 0
	}
	n := 0
	for i := range flow.Nodes {
		node := &flow.Nodes[i]
		if node.Kind != sdkr.FlowNodeParallel || node.JoinNode != "" {
			continue
		}
		if id := ConvergenceOf(*flow, *node); id != "" {
			node.JoinNode = id
			n++
		}
	}
	return n
}
