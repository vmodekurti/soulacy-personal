// addstep.go — insert a single step into a workflow (Epic 3, "add one step in
// natural language"). The per-node compiler (CompileNode) turns an instruction
// into a FlowNode; AppendLinearStep places that node at the end of the flow,
// wires an edge from the current terminal node to it, and makes it the flow's
// output. Pure and unit-tested; the LLM step lives in the gateway handler.
package studio

import (
	"fmt"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// AppendLinearStep returns a copy of draft with node appended at the end of the
// flow and linearly wired: an edge is added from the current terminal node to
// the new node, and the new node becomes the flow Output (and Entry if the flow
// was empty). The node's ID is made unique if blank or colliding.
func AppendLinearStep(draft Draft, node sdkr.FlowNode) Draft {
	flow := draft.Flow
	existing := map[string]bool{}
	for _, n := range flow.Nodes {
		existing[n.ID] = true
	}
	node.ID = uniqueNodeID(node.ID, existing)

	terminal := terminalNodeID(flow)

	newNodes := append(append([]sdkr.FlowNode{}, flow.Nodes...), node)
	newEdges := append([]sdkr.FlowEdge{}, flow.Edges...)
	entry := flow.Entry
	if len(flow.Nodes) == 0 {
		entry = node.ID
	} else if terminal != "" {
		newEdges = append(newEdges, sdkr.FlowEdge{From: terminal, To: node.ID})
	}

	draft.Flow = Flow{
		Nodes:             newNodes,
		Edges:             newEdges,
		Entry:             entry,
		Output:            node.ID,
		MaxNodeExecutions: flow.MaxNodeExecutions,
	}
	return draft
}

// terminalNodeID returns the node the flow currently ends at: the declared
// Output if it exists, else the first node with no outgoing edge, else the last
// node.
func terminalNodeID(flow Flow) string {
	if flow.Output != "" && nodeExists(flow, flow.Output) {
		return flow.Output
	}
	hasOut := map[string]bool{}
	for _, e := range flow.Edges {
		if e.From != "" {
			hasOut[e.From] = true
		}
	}
	for i := len(flow.Nodes) - 1; i >= 0; i-- {
		id := flow.Nodes[i].ID
		if !hasOut[id] {
			return id
		}
	}
	if len(flow.Nodes) > 0 {
		return flow.Nodes[len(flow.Nodes)-1].ID
	}
	return ""
}

func nodeExists(flow Flow, id string) bool {
	for _, n := range flow.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func uniqueNodeID(want string, taken map[string]bool) string {
	if want != "" && !taken[want] {
		return want
	}
	base := want
	if base == "" {
		base = "step"
	}
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s_%d", base, i)
		if !taken[cand] {
			return cand
		}
	}
}
