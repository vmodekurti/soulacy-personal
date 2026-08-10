// flow.go — Story E25: declarative cyclic flow graphs.
//
// CompileFlow validates a sdk/reasoning.FlowSpec; RunFlow walks the graph:
// nodes run through an injected runner (the engine bridges this to RunTool;
// the registered "flow" strategy bridges it to env.Tools), edges are
// evaluated in declaration order with Go-template predicates over the flow
// vars, and EVERY edge carries a traversal budget (default 1) so cycles
// terminate by construction. A global node-execution budget backstops
// pathological graphs. FlowHooks let the runtime checkpoint each node visit
// (visit-indexed keys) so resume-after-crash replays completed work instead
// of re-running it.
package reasoning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

// DefaultFlowBudget is the global node-execution ceiling when the spec
// doesn't set MaxNodeExecutions.
const DefaultFlowBudget = 100

const maxFlowParallelism = 32
const maxFlowMapItems = 1000

var flowItemVarPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// FlowGraph is a compiled, validated flow.
type FlowGraph struct {
	spec  sdkr.FlowSpec
	nodes map[string]sdkr.FlowNode
	out   map[string][]int // node id → indexes into spec.Edges, declaration order
	entry string
}

// Node returns the compiled node by id (zero value if unknown).
func (g *FlowGraph) Node(id string) sdkr.FlowNode { return g.nodes[id] }

// Entry returns the entry node id.
func (g *FlowGraph) Entry() string { return g.entry }

// Spec returns the underlying spec (for GUI rendering).
func (g *FlowGraph) Spec() sdkr.FlowSpec { return g.spec }

// CompileFlow validates the spec and returns an executable graph.
func CompileFlow(spec sdkr.FlowSpec) (*FlowGraph, error) {
	if len(spec.Nodes) == 0 {
		return nil, fmt.Errorf("flow: no nodes declared")
	}
	nodes := make(map[string]sdkr.FlowNode, len(spec.Nodes))
	usesEscalate := false
	for i, n := range spec.Nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("flow: node %d has no id", i)
		}
		if _, dup := nodes[n.ID]; dup {
			return nil, fmt.Errorf("flow: duplicate node id %q", n.ID)
		}
		// Kind inference: tool set → tool, agent set → agent, code set → python,
		// none of those → branch.
		if n.Kind == "" {
			switch {
			case n.Tool != "":
				n.Kind = sdkr.FlowNodeTool
			case n.Agent != "":
				n.Kind = sdkr.FlowNodeAgent
			case n.Code != "":
				n.Kind = sdkr.FlowNodePython
			default:
				n.Kind = sdkr.FlowNodeBranch
			}
		}
		// Tolerate common entry/exit synonyms a builder model sometimes emits
		// ("start"/"entry"/"begin" for the first node, "end"/"finish"/"done" for
		// the last). These are structural passthroughs — the real entry is the
		// `entry` field — so map them rather than hard-failing the whole flow.
		switch strings.ToLower(strings.TrimSpace(n.Kind)) {
		case "start", "entry", "begin", "receive", "input_node":
			n.Kind = sdkr.FlowNodeTrigger
		case "end", "finish", "done", "output_node":
			n.Kind = sdkr.FlowNodeExit
		}
		switch n.Kind {
		case sdkr.FlowNodeTool:
			if n.Tool == "" {
				return nil, fmt.Errorf("flow: node %q is kind=tool but names no tool", n.ID)
			}
		case sdkr.FlowNodeAgent:
			if n.Agent == "" {
				return nil, fmt.Errorf("flow: node %q is kind=agent but names no agent", n.ID)
			}
		case sdkr.FlowNodePython:
			// A python node must carry either inline Code or reference a
			// deployed python tool by name.
			if n.Code == "" && n.Tool == "" {
				return nil, fmt.Errorf("flow: node %q is kind=python but has neither inline code nor a tool", n.ID)
			}
		case sdkr.FlowNodeLLM:
			// LLM transform nodes use Input plus params.system/params.response_format
			// at runtime; no extra required field beyond the node itself.
		case sdkr.FlowNodeBranch, sdkr.FlowNodeTrigger, sdkr.FlowNodeExit:
			// Structural endpoint/routing nodes: no action, nothing to validate.
		case sdkr.FlowNodeParallel:
			// Fan-out node: the edge-shaped checks (>= 2 outgoing edges, quorum
			// bounds, barrier reachability) need the edge index, so they run in a
			// second pass below once `out` is built.
		default:
			return nil, fmt.Errorf("flow: node %q has unknown kind %q", n.ID, n.Kind)
		}
		switch n.OnError {
		case "", "abort", "skip", "retry":
		case "escalate":
			usesEscalate = true
		default:
			return nil, fmt.Errorf("flow: node %q has unknown on_error %q", n.ID, n.OnError)
		}
		if n.ForEach != "" {
			// for_each fans out over DATA, kind=parallel fans out over EDGES. Stacking
			// them would make "which copy of the node does this branch belong to?"
			// unanswerable in the trace, so the composition is refused outright
			// instead of silently picking one mechanism.
			if n.Kind == sdkr.FlowNodeParallel {
				return nil, fmt.Errorf("flow: node %q cannot combine kind=parallel with for_each — use a for_each node inside one of the parallel branches", n.ID)
			}
			if sdkr.IsStructuralKind(n.Kind) {
				return nil, fmt.Errorf("flow: node %q cannot use for_each because %q is structural", n.ID, n.Kind)
			}
			if n.ItemVar != "" && !flowItemVarPattern.MatchString(n.ItemVar) {
				return nil, fmt.Errorf("flow: node %q has invalid item_var %q", n.ID, n.ItemVar)
			}
			if n.MaxParallel < 0 || n.MaxParallel > maxFlowParallelism {
				return nil, fmt.Errorf("flow: node %q max_parallel must be between 0 and %d", n.ID, maxFlowParallelism)
			}
		} else if n.Kind == sdkr.FlowNodeParallel {
			// max_parallel means the same thing on a fan-out as it does on a
			// for_each — how many branches may run at once — and a builder model
			// writes it there because it reads correct. It was rejected outright,
			// which threw away an entire valid three-specialist graph over one
			// inert field. Honour it instead (see runFlowParallel).
			//
			// item_var has no meaning here: a fan-out iterates over EDGES, and
			// there is no item to bind. Studio strips it before this point; a
			// hand-written flow that sets it is told so rather than having it
			// silently ignored.
			if n.ItemVar != "" {
				return nil, fmt.Errorf("flow: node %q is kind=parallel and declares item_var, which only applies to for_each", n.ID)
			}
			if n.MaxParallel < 0 || n.MaxParallel > maxFlowParallelism {
				return nil, fmt.Errorf("flow: node %q max_parallel must be between 0 and %d", n.ID, maxFlowParallelism)
			}
		} else if n.ItemVar != "" || n.MaxParallel != 0 {
			return nil, fmt.Errorf("flow: node %q declares item_var/max_parallel without for_each", n.ID)
		}
		// Join settings only mean something at a fan-out point. Silently ignoring
		// them elsewhere would let an author believe a plain branch waits for
		// something, which it never does.
		if n.Kind != sdkr.FlowNodeParallel && (n.Join != "" || n.JoinQuorum != 0 || n.JoinNode != "") {
			return nil, fmt.Errorf("flow: node %q declares join settings but is not kind=parallel", n.ID)
		}
		nodes[n.ID] = n
	}

	out := map[string][]int{}
	for i, e := range spec.Edges {
		if _, ok := nodes[e.From]; !ok {
			return nil, fmt.Errorf("flow: edge %d from unknown node %q", i, e.From)
		}
		if !flowEdgeTerminal(e.To) {
			if _, ok := nodes[e.To]; !ok {
				return nil, fmt.Errorf("flow: edge %d to unknown node %q", i, e.To)
			}
		}
		// Typed ports (Story S0.3): empty FromPort/ToPort = implicit single
		// port (unchanged). When a port is named it must exist among the
		// referenced node's declared ports.
		if e.FromPort != "" && !flowHasPort(nodes[e.From].Outputs, e.FromPort) {
			return nil, fmt.Errorf("flow: edge %d from_port %q not declared on node %q outputs", i, e.FromPort, e.From)
		}
		if e.ToPort != "" && !flowEdgeTerminal(e.To) && !flowHasPort(nodes[e.To].Inputs, e.ToPort) {
			return nil, fmt.Errorf("flow: edge %d to_port %q not declared on node %q inputs", i, e.ToPort, e.To)
		}
		out[e.From] = append(out[e.From], i)
	}

	// Fan-out contract (Story ST-06). Checked once the edge index exists, because
	// every rule here is about a parallel node's OUTGOING edges and its barrier.
	if err := validateParallelNodes(spec, nodes, out); err != nil {
		return nil, err
	}

	// Branch scoping (see branchscope.go). Runs after the fan-out shape checks,
	// so an unreachable barrier is reported as such rather than as a variable
	// that "the branch cannot see".
	if err := validateBranchScope(spec, nodes, out); err != nil {
		return nil, err
	}

	entry := spec.Entry
	if entry == "" {
		entry = spec.Nodes[0].ID
	}
	if _, ok := nodes[entry]; !ok {
		return nil, fmt.Errorf("flow: entry node %q does not exist", entry)
	}

	// Escalation (LLM-managed unknowns): on_error: escalate routes a failed
	// visit to a DECLARED node instead of aborting, so the target must exist
	// and any use of escalate must have a target to route to.
	if spec.Escalation != "" {
		if _, ok := nodes[spec.Escalation]; !ok {
			return nil, fmt.Errorf("flow: escalation node %q does not exist", spec.Escalation)
		}
	}
	if usesEscalate && spec.Escalation == "" {
		return nil, fmt.Errorf("flow: a node declares on_error: escalate but the flow names no escalation node")
	}

	// P0-2 contract check. Runs LAST, so a shape complaint never masks a
	// structural one (an unknown node id is more actionable than a type
	// mismatch involving it). Because every Studio surface — generate, edit,
	// repair, save — funnels through CompileFlow, adding it here covers all
	// four lifecycle stages in one place.
	if err := validatePortContracts(spec, nodes); err != nil {
		return nil, err
	}

	return &FlowGraph{spec: spec, nodes: nodes, out: out, entry: entry}, nil
}

func flowEdgeTerminal(to string) bool { return to == "" || to == "end" }

// flowHasPort reports whether ports declares one named name (Story S0.3).
func flowHasPort(ports []sdkr.FlowPort, name string) bool {
	for _, p := range ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

// validateParallelNodes enforces the kind=parallel contract at compile time, so
// a fan-out that cannot possibly behave as drawn is refused at author time
// instead of producing a run that silently drops branches or never converges.
func validateParallelNodes(spec sdkr.FlowSpec, nodes map[string]sdkr.FlowNode, out map[string][]int) error {
	// Declaration order keeps the first reported error stable across runs.
	for _, decl := range spec.Nodes {
		n := nodes[decl.ID]
		if n.Kind != sdkr.FlowNodeParallel {
			continue
		}
		edges := out[n.ID]
		// One outgoing edge is not a fan-out — it's an edge. Accepting it would
		// hide a half-wired graph behind concurrency machinery that never runs.
		if len(edges) < 2 {
			return fmt.Errorf("flow: node %q is kind=parallel but has %d outgoing edge(s) — a fan-out needs at least 2 (use a plain edge otherwise)", n.ID, len(edges))
		}
		switch n.Join {
		case "", sdkr.JoinAll, sdkr.JoinAny, sdkr.JoinBestEffort:
			if n.JoinQuorum != 0 {
				return fmt.Errorf("flow: node %q sets join_quorum but its join policy is %q — quorum size only applies to join: quorum", n.ID, n.Join)
			}
		case sdkr.JoinQuorum:
			// A quorum above the fan-out width can never be met; below one is met
			// before anything runs. Both are authoring mistakes, not runtime states.
			if n.JoinQuorum < 1 || n.JoinQuorum > len(edges) {
				return fmt.Errorf("flow: node %q has join: quorum with join_quorum %d — it must be between 1 and the %d outgoing branches", n.ID, n.JoinQuorum, len(edges))
			}
		default:
			return fmt.Errorf("flow: node %q has unknown join policy %q (want all, any, quorum or best_effort)", n.ID, n.Join)
		}
		if n.JoinNode == "" {
			continue
		}
		if _, ok := nodes[n.JoinNode]; !ok {
			return fmt.Errorf("flow: node %q names join_node %q, which does not exist", n.ID, n.JoinNode)
		}
		if n.JoinNode == n.ID {
			return fmt.Errorf("flow: node %q names itself as its join_node", n.ID)
		}
		// A branch that cannot reach the barrier never converges: the join would
		// wait for a walk that ends somewhere else, and the barrier's inputs would
		// be permanently missing that branch's contribution.
		for _, ei := range edges {
			e := spec.Edges[ei]
			if flowEdgeTerminal(e.To) {
				return fmt.Errorf("flow: node %q declares join_node %q but its branch to end terminates before the barrier", n.ID, n.JoinNode)
			}
			if !flowReaches(spec, out, e.To, n.JoinNode) {
				return fmt.Errorf("flow: node %q declares join_node %q, but its branch starting at %q cannot reach it", n.ID, n.JoinNode, e.To)
			}
		}
		// Two nested fan-outs sharing one barrier make "whose join is this?"
		// ambiguous — the inner group would consume the outer group's convergence
		// point and the outer branches would stop early at a node that already ran.
		for _, other := range spec.Nodes {
			o := nodes[other.ID]
			if o.ID == n.ID || o.Kind != sdkr.FlowNodeParallel || o.JoinNode != n.JoinNode {
				continue
			}
			if flowReaches(spec, out, n.ID, o.ID) {
				return fmt.Errorf("flow: nodes %q and %q are nested parallel nodes sharing join_node %q — give the inner fan-out its own barrier", n.ID, o.ID, n.JoinNode)
			}
		}
	}
	return nil
}

// flowReaches reports whether target is forward-reachable from start over the
// declared edges, ignoring predicates and iteration budgets. Deliberately
// optimistic: compile time cannot know which predicates fire, so it only refuses
// graphs where convergence is impossible by SHAPE, never by data.
func flowReaches(spec sdkr.FlowSpec, out map[string][]int, start, target string) bool {
	if start == target {
		return true
	}
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, ei := range out[cur] {
			to := spec.Edges[ei].To
			if to == target {
				return true
			}
			if flowEdgeTerminal(to) || seen[to] {
				continue
			}
			seen[to] = true
			queue = append(queue, to)
		}
	}
	return false
}

// FlowRunNode executes one node's action with its rendered input and
// returns the node's JSON result. Branch nodes are never passed to it.
type FlowRunNode func(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error)

// FlowHooks are optional observation/persistence seams. visitKey is
// "<nodeID>#<visit>" — visit counts per node from 1 — so cyclic re-visits
// checkpoint under distinct keys and resume replays them in order.
//
// CONCURRENCY: every hook MUST be safe for concurrent use. A kind=parallel node
// (and a for_each node with max_parallel > 1) runs branches on separate
// goroutines, and each of them calls Started/Completed/Failed/Observe and may
// call Restore/RepairInput/RepairPredicate. An implementation that appends to a
// slice or writes a map without a lock will corrupt its trace or panic under
// -race; guard the shared state inside the hook.
type FlowHooks struct {
	// Restore returns the persisted state for a visit that already
	// completed in a previous run (resume). ok=false = execute normally.
	Restore func(visitKey string) (state json.RawMessage, ok bool)
	// Started fires before a node visit executes.
	Started func(visitKey string, node sdkr.FlowNode)
	// Completed fires after a visit succeeds (or is skipped on error),
	// with the state that entered the vars.
	Completed func(visitKey string, state json.RawMessage)
	// Failed fires when a visit aborts the flow.
	Failed func(visitKey string, err error)
	// Observe fires after every executed (non-branch) node visit with the full
	// per-node record — rendered input, output, error, and wall-clock duration —
	// so the runtime can build a legible per-block run trace. It fires on
	// success, on a skipped error, and on an aborting error (before Failed).
	// It never fires for a restored (resumed) visit. Purely observational.
	Observe func(rec FlowNodeRun)
	// RepairInput gets one opportunity to recover a node input that could not be
	// rendered from the live flow variables. A successful repair must return the
	// concrete input to execute (not another template). The repair is transient:
	// it affects only this visit and never mutates the compiled workflow.
	RepairInput func(ctx context.Context, node sdkr.FlowNode, inputTemplate string, renderErr error, vars map[string]any) (rendered string, ok bool)
	// RepairPredicate gets one opportunity to DECIDE an edge whose If predicate
	// could not be rendered from the live flow vars — the same shape-drift class
	// RepairInput heals, at the one place that steers routing. ok=true means the
	// hook reached a decision: take reports whether the edge should be taken
	// (false = fall through to the next edge in declaration order). ok=false
	// keeps today's behavior: the render error aborts the flow. The decision is
	// transient and never mutates the compiled workflow.
	RepairPredicate func(ctx context.Context, edge sdkr.FlowEdge, renderErr error, vars map[string]any) (take bool, ok bool)
}

// FlowNodeRun is one executed node's record in a run trace (Story S0.3 /
// per-block logging). It captures exactly what a block received, produced, how
// long it took, and whether it errored — the data a non-technical user needs to
// see WHERE a run went wrong, without reading templates or logs.
type FlowNodeRun struct {
	VisitKey   string          `json:"visitKey"`
	NodeID     string          `json:"nodeId"`
	Kind       string          `json:"kind"`
	Input      string          `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"durationMs"`
	StartedAt  time.Time       `json:"startedAt"`
	// WiredPorts is true when this node's input was assembled from typed port
	// wires (template-free handoff) rather than a Go-template input.
	WiredPorts bool `json:"wiredPorts,omitempty"`
	// Adapted is true when the runtime salvaged this node's output via an LLM
	// because it hit an unexpected data shape (see FlowNode.Adaptive). The output
	// recorded here is the model's reconstruction, not the node's raw result.
	Adapted bool `json:"adapted,omitempty"`
	// BranchID identifies WHICH branch of a kind=parallel fan-out this record came
	// from ("<parallel visit key>[<1-based branch>]"); empty for a node on the
	// sequential walk. Without it a concurrent trace is an unreadable interleaving:
	// records from three branches arrive in completion order and nothing says which
	// belongs to which, so "why did this run take 40s" cannot be answered.
	BranchID string `json:"branchId,omitempty"`
	// ParallelGroup names the kind=parallel node that owns this record — the branch
	// members and the group's own aggregate record alike. Together with StartedAt
	// and DurationMS it makes overlap computable, which is the only way to SHOW
	// that branches really ran at the same time.
	ParallelGroup string `json:"parallelGroup,omitempty"`
}

// adaptedTrackerKey carries a per-run set of node ids the runtime salvaged, so
// the walker can flag them on the trace it emits.
type adaptedTrackerKey struct{}

type adaptedTracker struct {
	mu    sync.RWMutex
	nodes map[string]bool
}

// WithAdaptedTracker returns a context carrying an adapted-node set plus a marker
// the runner calls when it salvages a node. The walker reads the set via
// nodeWasAdapted when building each FlowNodeRun.
func WithAdaptedTracker(ctx context.Context) (context.Context, func(nodeID string)) {
	tracker := &adaptedTracker{nodes: map[string]bool{}}
	return context.WithValue(ctx, adaptedTrackerKey{}, tracker), func(id string) {
		tracker.mu.Lock()
		tracker.nodes[id] = true
		tracker.mu.Unlock()
	}
}

func nodeWasAdapted(ctx context.Context, id string) bool {
	if tracker, ok := ctx.Value(adaptedTrackerKey{}).(*adaptedTracker); ok {
		tracker.mu.RLock()
		adapted := tracker.nodes[id]
		tracker.mu.RUnlock()
		return adapted
	}
	return false
}

type flowNodeExecution struct {
	result json.RawMessage
	record FlowNodeRun
	err    error
}

func executeFlowNode(
	ctx context.Context,
	g *FlowGraph,
	node sdkr.FlowNode,
	nodeID, visitKey string,
	vars map[string]any,
	run FlowRunNode,
	hooks FlowHooks,
) flowNodeExecution {
	overlay, hasPorts := resolvePortInputs(g, nodeID, vars)
	rendered := ""
	renderStarted := time.Now()

	switch {
	case node.Input != "":
		var rerr error
		rendered, rerr = renderNodeInputTemplate(node.Input, vars)
		if rerr != nil && hooks.RepairInput != nil {
			if repaired, ok := hooks.RepairInput(ctx, node, node.Input, rerr, vars); ok {
				rendered = repaired
				rerr = nil
			}
		}
		if rerr != nil {
			err := fmt.Errorf("flow: node %q: render input: %w", nodeID, rerr)
			return flowNodeExecution{
				err: err,
				record: FlowNodeRun{
					VisitKey:   visitKey,
					NodeID:     nodeID,
					Kind:       node.Kind,
					Input:      node.Input,
					Error:      err.Error(),
					DurationMS: time.Since(renderStarted).Milliseconds(),
					StartedAt:  renderStarted.UTC(),
					WiredPorts: hasPorts,
				},
			}
		}
	case node.Kind == sdkr.FlowNodePython && !hasPorts:
		if b, jerr := json.Marshal(vars); jerr == nil {
			rendered = string(b)
		} else {
			rendered = "{}"
		}
	}

	if hasPorts {
		base := map[string]any{}
		if s := strings.TrimSpace(rendered); s != "" {
			_ = json.Unmarshal([]byte(s), &base)
		}
		for k, v := range overlay {
			base[k] = v
		}
		if b, jerr := json.Marshal(base); jerr == nil {
			rendered = string(b)
		}
	}

	start := time.Now()
	result, err := run(ctx, node, rendered)
	if err != nil && node.OnError == "retry" {
		result, err = run(ctx, node, rendered)
	}
	rec := FlowNodeRun{
		VisitKey:   visitKey,
		NodeID:     nodeID,
		Kind:       node.Kind,
		Input:      rendered,
		Output:     result,
		DurationMS: time.Since(start).Milliseconds(),
		StartedAt:  start.UTC(),
		WiredPorts: hasPorts,
		Adapted:    nodeWasAdapted(ctx, nodeID),
	}
	if err != nil {
		rec.Error = err.Error()
	}
	return flowNodeExecution{result: result, record: rec, err: err}
}

func executeMappedFlowNode(
	ctx context.Context,
	g *FlowGraph,
	node sdkr.FlowNode,
	nodeID, visitKey string,
	vars map[string]any,
	run FlowRunNode,
	hooks FlowHooks,
) flowNodeExecution {
	start := time.Now()
	renderedItems, err := renderNodeInputTemplate(node.ForEach, vars)
	if err != nil {
		ferr := fmt.Errorf("flow: node %q: render for_each: %w", nodeID, err)
		return flowNodeExecution{err: ferr, record: FlowNodeRun{
			VisitKey: visitKey, NodeID: nodeID, Kind: node.Kind, Input: node.ForEach,
			Error: ferr.Error(), DurationMS: time.Since(start).Milliseconds(), StartedAt: start.UTC(),
		}}
	}
	var items []any
	if err := json.Unmarshal([]byte(renderedItems), &items); err != nil {
		ferr := fmt.Errorf("flow: node %q: for_each must render a JSON array: %w", nodeID, err)
		return flowNodeExecution{err: ferr, record: FlowNodeRun{
			VisitKey: visitKey, NodeID: nodeID, Kind: node.Kind, Input: renderedItems,
			Error: ferr.Error(), DurationMS: time.Since(start).Milliseconds(), StartedAt: start.UTC(),
		}}
	}
	if len(items) > maxFlowMapItems {
		ferr := fmt.Errorf("flow: node %q: for_each produced %d items, exceeding the limit of %d", nodeID, len(items), maxFlowMapItems)
		return flowNodeExecution{err: ferr, record: FlowNodeRun{
			VisitKey: visitKey, NodeID: nodeID, Kind: node.Kind, Input: renderedItems,
			Error: ferr.Error(), DurationMS: time.Since(start).Milliseconds(), StartedAt: start.UTC(),
		}}
	}

	itemVar := strings.TrimSpace(node.ItemVar)
	if itemVar == "" {
		itemVar = "item"
	}
	parallel := node.MaxParallel
	if parallel <= 0 {
		parallel = 1
	}
	if parallel > len(items) {
		parallel = len(items)
	}
	if parallel == 0 {
		empty := json.RawMessage("[]")
		return flowNodeExecution{result: empty, record: FlowNodeRun{
			VisitKey: visitKey, NodeID: nodeID, Kind: node.Kind, Input: renderedItems,
			Output: empty, DurationMS: time.Since(start).Milliseconds(), StartedAt: start.UTC(),
		}}
	}

	executions := make([]flowNodeExecution, len(items))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(index int, value any) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				executions[index] = flowNodeExecution{err: ctx.Err()}
				return
			}
			itemVars := make(map[string]any, len(vars)+2)
			for k, v := range vars {
				itemVars[k] = v
			}
			itemVars[itemVar] = value
			itemVars[itemVar+"_index"] = index
			itemVisit := fmt.Sprintf("%s[%d]", visitKey, index+1)
			executions[index] = executeFlowNode(ctx, g, node, nodeID, itemVisit, itemVars, run, hooks)
		}(i, item)
	}
	wg.Wait()

	results := make([]json.RawMessage, len(executions))
	var firstErr error
	for i, execution := range executions {
		if hooks.Observe != nil {
			hooks.Observe(execution.record)
		}
		if execution.err != nil {
			if node.OnError == "skip" {
				results[i] = json.RawMessage("null")
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("item %d: %w", i+1, execution.err)
			}
			continue
		}
		if execution.result == nil {
			results[i] = json.RawMessage("null")
		} else {
			results[i] = execution.result
		}
	}
	aggregate, marshalErr := json.Marshal(results)
	if marshalErr != nil && firstErr == nil {
		firstErr = marshalErr
	}
	rec := FlowNodeRun{
		VisitKey: visitKey, NodeID: nodeID, Kind: node.Kind, Input: renderedItems,
		Output: aggregate, DurationMS: time.Since(start).Milliseconds(), StartedAt: start.UTC(),
	}
	if firstErr != nil {
		rec.Error = firstErr.Error()
	}
	return flowNodeExecution{result: aggregate, record: rec, err: firstErr}
}

// flowRunState is the mutable bookkeeping shared by every walk of ONE run:
// per-node visit counters, per-edge traversal budgets and the global
// node-execution budget. It is mutex-guarded because a kind=parallel node walks
// its branches on separate goroutines. Keeping this state shared rather than
// per-branch is the point: the safety budgets must stay GLOBAL (a fan-out must
// not multiply the ceiling that stops a runaway graph), and visit numbers must
// stay unique across branches or two concurrent visits would checkpoint under
// the same key and overwrite each other on resume.
type flowRunState struct {
	mu         sync.Mutex
	visits     map[string]int // node id → times visited
	traversed  map[int]int    // edge index → times traversed
	executions int
	budget     int
}

// visit records another visit to a node and returns its 1-based visit number.
func (s *flowRunState) visit(nodeID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.visits[nodeID]++
	return s.visits[nodeID]
}

// charge counts one node execution against the global budget. ok=false once the
// ceiling is passed — whichever branch happens to reach it first aborts the run,
// so concurrency can never buy a graph more executions than it was granted.
func (s *flowRunState) charge() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executions++
	return s.executions, s.executions <= s.budget
}

// edgeAvailable is the cheap pre-check before rendering a predicate.
func (s *flowRunState) edgeAvailable(index, maxIter int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traversed[index] < maxIter
}

// takeEdge claims one traversal of an edge. It re-checks under the lock because
// a concurrent branch may have consumed the last of the budget between the
// pre-check and the predicate render; false means "someone else took it".
func (s *flowRunState) takeEdge(index, maxIter int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.traversed[index] >= maxIter {
		return false
	}
	s.traversed[index]++
	return true
}

// flowWalkScope labels the walk a node record belongs to. The zero value is the
// main sequential walk; inside a kind=parallel branch it carries the branch id
// and the owning group so a concurrent trace can be untangled after the fact.
type flowWalkScope struct {
	branchID string
	group    string
}

// flowEdgeMaxIterations is an edge's traversal budget (default 1 — cycles are
// bounded unless a back edge explicitly raises it).
func flowEdgeMaxIterations(e sdkr.FlowEdge) int {
	if e.MaxIterations <= 0 {
		return 1
	}
	return e.MaxIterations
}

// flowEdgePredicate evaluates one edge's If template against the live vars.
// Predicates address fields into node outputs, so they hit the same shape-drift
// class RepairInput heals for node inputs — at the one place that steers
// routing. The hook gets one chance to decide the edge before the walk aborts.
func flowEdgePredicate(ctx context.Context, e sdkr.FlowEdge, vars map[string]any, hooks FlowHooks) (bool, error) {
	if e.If == "" {
		return true, nil
	}
	cond, err := renderTemplate(e.If, vars)
	if err != nil {
		take, repaired := false, false
		if hooks.RepairPredicate != nil {
			take, repaired = hooks.RepairPredicate(ctx, e, err, vars)
		}
		if !repaired {
			return false, fmt.Errorf("flow: edge %q→%q: render predicate: %w", e.From, e.To, err)
		}
		return take, nil
	}
	cond = strings.TrimSpace(cond)
	if cond == "" || cond == "false" || cond == "0" || cond == "<no value>" {
		return false, nil
	}
	return true, nil
}

// RunFlow walks the compiled graph. vars seeds the template namespace
// (callers typically set "trigger"); node outputs land under their Output
// names. Returns the last executed node's result.
func RunFlow(ctx context.Context, g *FlowGraph, vars map[string]any, run FlowRunNode, hooks FlowHooks) (json.RawMessage, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	budget := g.spec.MaxNodeExecutions
	if budget <= 0 {
		budget = DefaultFlowBudget
	}
	state := &flowRunState{
		visits:    map[string]int{},
		traversed: map[int]int{},
		budget:    budget,
	}
	return walkFlow(ctx, g, state, vars, g.entry, "", run, hooks, flowWalkScope{})
}

// RunFlowFrom is RunFlow starting at an arbitrary node instead of the declared
// entry. It exists for the Studio test bench's "start from this step" (ST-10):
// iterating on step 7 of 9 otherwise means re-running six steps that were
// already known to work, which is slow and — where those steps are mocked —
// tests less than it appears to.
//
// Nodes upstream of `from` never execute, so anything they would have produced
// must be supplied by the caller as a seeded var or a mock. That is the caller's
// contract to state; the walker cannot detect the omission, because an absent
// upstream value is indistinguishable from a legitimately empty one.
//
// An empty `from` means the declared entry, so this is a safe drop-in.
func RunFlowFrom(ctx context.Context, g *FlowGraph, from string, vars map[string]any, run FlowRunNode, hooks FlowHooks) (json.RawMessage, error) {
	if strings.TrimSpace(from) == "" {
		return RunFlow(ctx, g, vars, run, hooks)
	}
	if _, ok := g.nodes[from]; !ok {
		return nil, fmt.Errorf("flow: start node %q is not in this graph", from)
	}
	if vars == nil {
		vars = map[string]any{}
	}
	budget := g.spec.MaxNodeExecutions
	if budget <= 0 {
		budget = DefaultFlowBudget
	}
	state := &flowRunState{
		visits:    map[string]int{},
		traversed: map[int]int{},
		budget:    budget,
	}
	return walkFlow(ctx, g, state, vars, from, "", run, hooks, flowWalkScope{})
}

// walkFlow is the single-pointer walk, from `from` until the graph terminates or
// until it reaches `stopAt` — the barrier node, which it returns BEFORE
// executing so the join can run it exactly once for all branches. stopAt is
// empty for the main walk and for branches with no declared join_node.
//
// Every walk in a run shares one flowRunState but owns its own vars map, which
// is what lets branches render templates concurrently without racing.
func walkFlow(
	ctx context.Context,
	g *FlowGraph,
	state *flowRunState,
	vars map[string]any,
	from, stopAt string,
	run FlowRunNode,
	hooks FlowHooks,
	scope flowWalkScope,
) (json.RawMessage, error) {
	// Stamp every record this walk emits with its branch identity, so a caller's
	// Observe hook can reconstruct which concurrent branch produced what without
	// the walker having to thread the scope through every execute path. Innermost
	// scope wins: a record from a nested fan-out keeps the inner branch's id.
	if scope.branchID != "" && hooks.Observe != nil {
		inner := hooks.Observe
		hooks.Observe = func(rec FlowNodeRun) {
			if rec.BranchID == "" {
				rec.BranchID = scope.branchID
			}
			if rec.ParallelGroup == "" {
				rec.ParallelGroup = scope.group
			}
			inner(rec)
		}
	}

	var lastResult json.RawMessage
	current := from
	for {
		if flowEdgeTerminal(current) || (stopAt != "" && current == stopAt) {
			return lastResult, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("flow: cancelled at node %q: %w", current, err)
		}
		node := g.nodes[current]
		visitNo := state.visit(current)
		visitKey := fmt.Sprintf("%s#%d", current, visitNo)

		if node.Kind == sdkr.FlowNodeParallel {
			// Fan-out: the branches consume this node's outgoing edges, so the walk
			// either resumes at the barrier or ends here with the aggregate. Restore
			// is deliberately NOT consulted for the group itself — the branch nodes
			// carry their own visit keys and restore individually, so resuming
			// replays only the work that had not completed.
			if hooks.Started != nil {
				hooks.Started(visitKey, node)
			}
			aggregate, perr := runFlowParallel(ctx, g, state, vars, current, visitKey, node, run, hooks)
			if perr != nil {
				switch {
				case node.OnError == "skip":
					aggregate = nil
				case node.OnError == "escalate" && g.spec.Escalation != "" && current != g.spec.Escalation:
					vars["failure"] = map[string]any{
						"node":  current,
						"kind":  node.Kind,
						"error": perr.Error(),
						"visit": visitNo,
					}
					current = g.spec.Escalation
					continue
				default:
					ferr := fmt.Errorf("flow: node %q: %w", current, perr)
					if hooks.Failed != nil {
						hooks.Failed(visitKey, ferr)
					}
					return nil, ferr
				}
			}
			if aggregate != nil {
				lastResult = aggregate
			}
			applyFlowResult(vars, node, aggregate)
			if hooks.Completed != nil {
				hooks.Completed(visitKey, aggregate)
			}
			if node.JoinNode == "" {
				return lastResult, nil
			}
			current = node.JoinNode
			continue
		}

		if !sdkr.IsStructuralKind(node.Kind) {
			if _, within := state.charge(); !within {
				err := fmt.Errorf("flow: node-execution budget exhausted (%d) at %q — check cycle bounds", state.budget, current)
				if hooks.Failed != nil {
					hooks.Failed(visitKey, err)
				}
				return nil, err
			}

			restored := false
			if hooks.Restore != nil {
				if saved, ok := hooks.Restore(visitKey); ok {
					applyFlowResult(vars, node, saved)
					if saved != nil {
						lastResult = saved
					}
					restored = true
				}
			}

			if !restored {
				if hooks.Started != nil {
					hooks.Started(visitKey, node)
				}
				var execution flowNodeExecution
				if node.ForEach != "" {
					execution = executeMappedFlowNode(ctx, g, node, current, visitKey, vars, run, hooks)
				} else {
					execution = executeFlowNode(ctx, g, node, current, visitKey, vars, run, hooks)
				}
				if hooks.Observe != nil {
					hooks.Observe(execution.record)
				}
				result, err := execution.result, execution.err
				if err != nil {
					switch {
					case node.OnError == "skip":
						result = nil
					case node.OnError == "escalate" && g.spec.Escalation != "" && current != g.spec.Escalation:
						// Route the failure to the flow's declared escalation node
						// instead of aborting: exhausted salvage becomes an ordinary,
						// declared path. The failure is exposed under the "failure"
						// flow var so the escalation node's templates can address it
						// ({{.failure.node}} / {{.failure.error}}). The escalation
						// node itself failing never re-escalates (guard above), and
						// escalation visits count against the global execution budget,
						// so escalate cycles terminate by construction.
						vars["failure"] = map[string]any{
							"node":  current,
							"kind":  node.Kind,
							"error": err.Error(),
							"visit": visitNo,
						}
						current = g.spec.Escalation
						continue
					default:
						ferr := fmt.Errorf("flow: node %q: %w", current, err)
						if hooks.Failed != nil {
							hooks.Failed(visitKey, ferr)
						}
						return nil, ferr
					}
				}
				applyFlowResult(vars, node, result)
				if result != nil {
					lastResult = result
				}
				if hooks.Completed != nil {
					hooks.Completed(visitKey, result)
				}
			}
		}

		// Pick the next edge: declaration order, first whose predicate is
		// truthy AND whose traversal budget (default 1) isn't exhausted.
		next := ""
		found := false
		for _, ei := range g.out[current] {
			e := g.spec.Edges[ei]
			maxIter := flowEdgeMaxIterations(e)
			if !state.edgeAvailable(ei, maxIter) {
				continue
			}
			take, err := flowEdgePredicate(ctx, e, vars, hooks)
			if err != nil {
				return nil, err
			}
			if !take {
				continue
			}
			if !state.takeEdge(ei, maxIter) {
				continue
			}
			next = e.To
			found = true
			break
		}
		if !found || flowEdgeTerminal(next) {
			return lastResult, nil
		}
		current = next
	}
}

// flowBranch is one claimed outgoing edge of a kind=parallel node: the edge
// index (its traversal budget is already spent) and the node the branch starts
// at. The slice order is DECLARATION order, which is what makes the aggregate
// and the vars merge deterministic no matter how the branches finish.
type flowBranch struct {
	edge  int
	entry string
}

// selectFlowBranches claims every eligible outgoing edge of a fan-out node, in
// declaration order. Eligible = traversal budget left AND predicate truthy — the
// same two tests the sequential walker applies, except the walker stops at the
// first match and this takes them all.
func selectFlowBranches(
	ctx context.Context,
	g *FlowGraph,
	state *flowRunState,
	vars map[string]any,
	nodeID string,
	hooks FlowHooks,
) ([]flowBranch, error) {
	var branches []flowBranch
	for _, ei := range g.out[nodeID] {
		e := g.spec.Edges[ei]
		maxIter := flowEdgeMaxIterations(e)
		if !state.edgeAvailable(ei, maxIter) {
			continue
		}
		take, err := flowEdgePredicate(ctx, e, vars, hooks)
		if err != nil {
			return nil, err
		}
		if !take {
			continue
		}
		if !state.takeEdge(ei, maxIter) {
			continue
		}
		branches = append(branches, flowBranch{edge: ei, entry: e.To})
	}
	return branches, nil
}

// runFlowParallel executes a kind=parallel node: it fans out over every eligible
// outgoing edge concurrently, waits according to the node's join policy, merges
// what the branches wrote back into the parent vars, and returns the ordered
// aggregate. It emits ONE Observe record for the group whose StartedAt/DurationMS
// span the whole fan-out, so a reader can see the branches overlapping rather
// than having to infer concurrency from timestamps.
func runFlowParallel(
	ctx context.Context,
	g *FlowGraph,
	state *flowRunState,
	vars map[string]any,
	nodeID, visitKey string,
	node sdkr.FlowNode,
	run FlowRunNode,
	hooks FlowHooks,
) (json.RawMessage, error) {
	start := time.Now()
	policy := strings.TrimSpace(node.Join)
	if policy == "" {
		policy = sdkr.JoinAll
	}

	observe := func(input string, aggregate json.RawMessage, err error) {
		if hooks.Observe == nil {
			return
		}
		rec := FlowNodeRun{
			VisitKey:      visitKey,
			NodeID:        nodeID,
			Kind:          node.Kind,
			Input:         input,
			Output:        aggregate,
			DurationMS:    time.Since(start).Milliseconds(),
			StartedAt:     start.UTC(),
			ParallelGroup: nodeID,
		}
		if err != nil {
			rec.Error = err.Error()
		}
		hooks.Observe(rec)
	}

	branches, err := selectFlowBranches(ctx, g, state, vars, nodeID, hooks)
	if err != nil {
		observe("", nil, err)
		return nil, err
	}
	if len(branches) == 0 {
		// Every branch was gated off by its predicate (or its traversal budget was
		// spent on an earlier pass through a cycle). That is a legitimate outcome,
		// not an error: the group produced nothing.
		empty := json.RawMessage("[]")
		observe(describeFlowBranches(policy, node, branches), empty, nil)
		return empty, nil
	}

	entries := make([]string, 0, len(branches))
	for _, b := range branches {
		entries = append(entries, b.entry)
	}
	input := describeFlowBranches(policy, node, branches)

	n := len(branches)
	results := make([]json.RawMessage, n)
	errs := make([]error, n)
	// Each branch renders templates against its OWN vars copy; sharing one map
	// would race the moment two branches wrote a node output at the same time.
	base := copyFlowVars(vars)
	branchVars := make([]map[string]any, n)

	branchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, fanOutLimit(node, n))
	finished := make(chan int, n)
	var wg sync.WaitGroup
	for i, b := range branches {
		wg.Add(1)
		go func(index int, br flowBranch) {
			defer wg.Done()
			// Ordered before wg.Done by LIFO defer, so a receiver of `finished` is
			// guaranteed to see this branch's results/errs slot already written.
			defer func() { finished <- index }()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-branchCtx.Done():
				errs[index] = branchCtx.Err()
				return
			}
			bv := copyFlowVars(vars)
			branchVars[index] = bv
			results[index], errs[index] = walkFlow(branchCtx, g, state, bv, br.entry, node.JoinNode, run, hooks,
				flowWalkScope{branchID: fmt.Sprintf("%s[%d]", visitKey, index+1), group: nodeID})
		}(i, b)
	}

	// any/quorum are the racing policies: stop waiting the moment enough branches
	// have succeeded (or enough have failed that the target is unreachable) and
	// cancel the rest through the derived context. all/best_effort want every
	// branch's answer, so they simply wait.
	if need := flowJoinTarget(policy, node, n); need > 0 {
		succeeded, failed := 0, 0
		for range branches {
			i := <-finished
			if errs[i] == nil {
				succeeded++
			} else {
				failed++
			}
			if succeeded >= need || n-failed < need {
				break
			}
		}
		cancel()
	}
	// Always join the goroutines before reading their slots: cancellation asks a
	// branch to stop, it does not make it stop instantly, and a half-written
	// branch is exactly the race this whole struct exists to avoid.
	wg.Wait()

	aggregate := make([]json.RawMessage, n)
	succeeded := 0
	for i := range branches {
		// Success is "the branch walk did not error", NOT "the branch produced a
		// value": a branch whose last node was skipped or structural returns nil,
		// and counting that as a failure would sink an any/quorum join that in
		// fact went exactly as declared.
		if errs[i] == nil {
			succeeded++
		}
		if errs[i] != nil || results[i] == nil {
			aggregate[i] = json.RawMessage("null")
			continue
		}
		aggregate[i] = results[i]
	}
	// Declaration order, not completion order: two runs of the same graph must
	// merge the same way even when the branches finish in a different sequence.
	for i := range branches {
		if errs[i] == nil {
			mergeBranchVars(vars, base, branchVars[i])
		}
	}

	out, merr := json.Marshal(aggregate)
	if merr != nil {
		observe(input, nil, merr)
		return nil, merr
	}

	gerr := flowJoinVerdict(policy, node, entries, errs, succeeded)
	observe(input, out, gerr)
	if gerr != nil {
		return out, gerr
	}
	return out, nil
}

// flowJoinTarget is how many successes let a policy stop early; 0 = wait for all.
// fanOutLimit is how many branches of a fan-out may run at once: the engine cap,
// narrowed by the author's max_parallel when they set one, and never more than
// the number of branches there are.
//
// Extracted so the bound is testable without standing up a run — it is the whole
// behaviour behind accepting the field in the first place, and accepting a
// setting while ignoring it would be its own quiet lie.
func fanOutLimit(node sdkr.FlowNode, branches int) int {
	limit := maxFlowParallelism
	if node.MaxParallel > 0 && node.MaxParallel < limit {
		limit = node.MaxParallel
	}
	if branches < limit {
		limit = branches
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

func flowJoinTarget(policy string, node sdkr.FlowNode, branches int) int {
	switch policy {
	case sdkr.JoinAny:
		return 1
	case sdkr.JoinQuorum:
		if node.JoinQuorum > branches {
			return branches
		}
		return node.JoinQuorum
	default:
		return 0
	}
}

// flowJoinVerdict turns the branch outcomes into the group's error (or nil). The
// error always names the failing branch's ENTRY node, because "node fan_out
// failed" is unactionable when three different things run under it.
func flowJoinVerdict(policy string, node sdkr.FlowNode, entries []string, errs []error, succeeded int) error {
	entry, err := firstBranchFailure(entries, errs)
	switch policy {
	case sdkr.JoinBestEffort:
		// Partial success IS the contract here: a failed branch contributes null.
		return nil
	case sdkr.JoinAny:
		if succeeded > 0 {
			return nil
		}
		return fmt.Errorf("join any: all %d branches failed; branch %q: %w", len(entries), entry, err)
	case sdkr.JoinQuorum:
		if succeeded >= node.JoinQuorum {
			return nil
		}
		// The count of survivors is deliberately absent: once the quorum is out of
		// reach the group cancels the branches still in flight, so how many of them
		// had finished is a race, and an error message that changes between
		// identical runs is worse than no number at all.
		if err == nil {
			return fmt.Errorf("join quorum: fewer than %d of %d branches succeeded", node.JoinQuorum, len(entries))
		}
		return fmt.Errorf("join quorum: fewer than %d of %d branches succeeded; branch %q: %w",
			node.JoinQuorum, len(entries), entry, err)
	default:
		if err != nil {
			return fmt.Errorf("branch %q: %w", entry, err)
		}
		return nil
	}
}

// firstBranchFailure picks the branch failure worth reporting: the first genuine
// one in declaration order, preferring a real error over a cancellation. A
// branch that was cancelled because the group had already given up is a
// CONSEQUENCE of the failure, and naming it would send the reader to the wrong
// node — and to a different node on every run.
func firstBranchFailure(entries []string, errs []error) (string, error) {
	fallbackEntry, fallbackErr := "", error(nil)
	for i, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return entries[i], err
		}
		if fallbackErr == nil {
			fallbackEntry, fallbackErr = entries[i], err
		}
	}
	return fallbackEntry, fallbackErr
}

// describeFlowBranches records what the group actually fanned out to, as the
// group record's "input". A trace that only showed the aggregate could not
// distinguish "the branch returned null" from "the branch never ran because its
// predicate was false".
func describeFlowBranches(policy string, node sdkr.FlowNode, branches []flowBranch) string {
	entries := make([]string, 0, len(branches))
	for _, b := range branches {
		entries = append(entries, b.entry)
	}
	desc := map[string]any{"join": policy, "branches": entries}
	if policy == sdkr.JoinQuorum {
		desc["quorum"] = node.JoinQuorum
	}
	if node.JoinNode != "" {
		desc["join_node"] = node.JoinNode
	}
	b, err := json.Marshal(desc)
	if err != nil {
		return ""
	}
	return string(b)
}

// copyFlowVars gives a branch its own view of the flow variables. The copy is
// deep through the JSON container types (maps and slices) because that is what a
// node output decodes into and what a template walks; anything else is shared by
// reference, which is safe as long as branches only READ those values.
func copyFlowVars(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = copyFlowValue(v)
	}
	return dst
}

func copyFlowValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = copyFlowValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = copyFlowValue(val)
		}
		return out
	default:
		return v
	}
}

// mergeBranchVars folds one branch's variables back into the parent. Only keys
// the branch actually WROTE are merged — anything still equal to the pre-fork
// snapshot is left alone, so a branch that merely read a variable cannot undo a
// sibling's write to it. Comparison is against the snapshot rather than the live
// parent map for exactly that reason: the parent has already absorbed earlier
// branches by the time later ones are merged.
func mergeBranchVars(parent, base, branch map[string]any) {
	for k, v := range branch {
		if prev, had := base[k]; had && reflect.DeepEqual(prev, v) {
			continue
		}
		parent[k] = v
	}
}

// resolvePortInputs assembles a node's input from its incoming WIRED edges
// (Story S0.3 runtime resolution). For every edge whose To is this node and that
// declares a ToPort, it reads the producing node's stored output var, extracts
// the field the FromPort exposes, and binds it under the consumer's input-port
// key. The result is the structured, template-free input the runtime overlays
// onto the node's static base. ok=false when the node has no wired inputs — the
// caller then keeps today's template/all-vars behavior unchanged.
//
// Forgiving by design: a producer that hasn't run yet (value absent) binds the
// zero value rather than erroring, and a FromPort whose field can't be walked
// falls back to the whole upstream value — so a slightly-off wire degrades
// gracefully instead of aborting the run.
func resolvePortInputs(g *FlowGraph, nodeID string, vars map[string]any) (map[string]any, bool) {
	consumer, ok := g.nodes[nodeID]
	if !ok {
		return nil, false
	}
	overlay := map[string]any{}
	used := false
	for _, e := range g.spec.Edges {
		if e.To != nodeID || e.ToPort == "" {
			continue
		}
		src, ok := g.nodes[e.From]
		if !ok {
			continue
		}
		var srcVal any
		if src.Output != "" {
			srcVal = vars[src.Output]
		}
		// Which value does the from_port expose?
		//   - no from_port            → the whole producer output
		//   - port has explicit Field → strict dotted path into the output
		//   - else (port NAME)        → the output's same-named field IF present,
		//                               otherwise the WHOLE output (a generic port
		//                               name like "result"/"output" addresses the
		//                               whole thing; a specific one like "id"
		//                               addresses that field).
		var val any
		switch {
		case e.FromPort == "":
			val = srcVal
		default:
			if op := findPort(src.Outputs, e.FromPort); op != nil && op.Field != "" {
				val = extractField(srcVal, op.Field)
			} else {
				val = extractNamedField(srcVal, e.FromPort)
			}
		}
		// Bind under the consumer's input-port key (Field override or port name).
		key := e.ToPort
		if ip := findPort(consumer.Inputs, e.ToPort); ip != nil && ip.Field != "" {
			key = ip.Field
		}
		overlay[key] = val
		used = true
	}
	return overlay, used
}

// findPort returns the named port (or nil) from a port list.
func findPort(ports []sdkr.FlowPort, name string) *sdkr.FlowPort {
	for i := range ports {
		if ports[i].Name == name {
			return &ports[i]
		}
	}
	return nil
}

// extractNamedField resolves a port-NAME reference against a producer output:
// when the output is an object that HAS the key, it returns that field; in every
// other case (output isn't an object, or has no such key) it returns the whole
// output. This makes a generic port name ("result", "output") carry the whole
// value while a specific one ("id", "url") carries just that field — without the
// author having to declare an explicit Field.
func extractNamedField(v any, name string) any {
	if m, ok := v.(map[string]any); ok {
		if val, present := m[name]; present {
			return val
		}
	}
	return v
}

// extractField walks a dotted path into a decoded JSON value and returns the
// addressed leaf. An empty path returns the whole value. If a segment can't be
// walked (the value isn't an object at that depth), it returns what it reached
// so far — so wiring the whole of a scalar output via a named port still yields
// the scalar instead of nil.
func extractField(v any, path string) any {
	if strings.TrimSpace(path) == "" {
		return v
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return cur
		}
		cur = m[seg]
	}
	return cur
}

// applyFlowResult stores a node result under its Output var (parsed JSON
// when possible, raw string otherwise) — same semantics as workflow steps.
func applyFlowResult(vars map[string]any, node sdkr.FlowNode, result json.RawMessage) {
	if node.Output == "" || result == nil {
		return
	}
	var v any
	if err := json.Unmarshal(result, &v); err == nil {
		vars[node.Output] = v
	} else {
		vars[node.Output] = string(result)
	}
}
