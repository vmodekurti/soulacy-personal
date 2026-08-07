package studio

// macrosize.go — how big a graph FEELS to read, as opposed to how many nodes it
// contains.
//
// The macro-workflow size rule counts raw nodes: 6-8 warns, 9 or more blocks
// outright. That is a fair proxy for a graph drawn as a straight line, where
// every extra node is another thing to hold in your head. It is not a fair
// proxy for a fan-out, and the difference showed up on the first real
// multi-agent request:
//
//	fetch → guard branch → parallel → 3 reviewers → editor → quiet path
//
// Nine nodes, so Studio generated exactly what was asked for and then blocked
// saving it for being too complex — with a suggested fix ("switch Mode to Auto")
// that abandons the fan-out, since a reasoning agent cannot express one. The
// shape that most needs a graph was the shape the graph rule refused.
//
// Three peer reviewers are not three things to understand. They are one:
// "review it three ways at once". A reader who has understood one branch has
// understood all of them, which is exactly why the canvas draws them side by
// side. So the size judgement counts a fan-out's branches ONCE — at the width of
// its largest branch — and leaves everything else alone.
//
// Deliberately narrow. Only siblings of a kind=parallel node are discounted,
// only the ones exclusive to a single branch, and the raw count is still what
// the message reports. A long sequential pipeline is judged exactly as before.

import sdkr "github.com/soulacy/soulacy/sdk/reasoning"

// MacroStepCount is the number of steps a reader must actually follow: every
// node, minus the sibling branches of each fan-out beyond its widest one.
//
// Never larger than the node count, and equal to it for any graph without a
// parallel step — so this can only relax the rule, never tighten it on a shape
// that used to pass.
func MacroStepCount(flow Flow) int {
	n := len(flow.Nodes)
	if n == 0 {
		return 0
	}
	for _, node := range flow.Nodes {
		if node.Kind != sdkr.FlowNodeParallel {
			continue
		}
		n -= fanOutDiscount(flow, node.ID, node.JoinNode)
	}
	if n < 1 {
		return 1
	}
	return n
}

// fanOutDiscount is how many nodes this fan-out contributes beyond a single
// branch's worth: sum of the branch-exclusive sizes, minus the largest.
//
// Exclusive is the operative word. A node several branches pass through is
// shared work, counted once already, and discounting it would undercount the
// graph. Only nodes belonging to exactly one branch are candidates.
func fanOutDiscount(flow Flow, id, barrier string) int {
	starts := branchStarts(flow, id)
	if len(starts) < 2 {
		return 0
	}

	walks := make([]map[string]bool, len(starts))
	for i, s := range starts {
		walks[i] = reachableFrom(flow, s, id)
		walks[i][s] = true
		if barrier != "" {
			delete(walks[i], barrier)
		}
	}

	sum, largest := 0, 0
	for i := range walks {
		exclusive := 0
		for nodeID := range walks[i] {
			if nodeID == barrier {
				continue
			}
			only := true
			for j := range walks {
				if j != i && walks[j][nodeID] {
					only = false
					break
				}
			}
			if only {
				exclusive++
			}
		}
		sum += exclusive
		if exclusive > largest {
			largest = exclusive
		}
	}
	return sum - largest
}
