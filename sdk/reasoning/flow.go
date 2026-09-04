package reasoning

// Flow types (Story E25) — declarative cyclic graphs. A FlowSpec is the
// graph form of a workflow: nodes perform work (tool / agent call) or
// branch, edges carry predicates and bounded-cycle budgets. Hosts compile
// SOUL.yaml's workflow block into this shape; the "flow" strategy and the
// checkpointing workflow executor both consume it.
//
// Compatibility: append-only fields, zero-value compatible.

// Flow node kinds.
const (
	FlowNodeTool   = "tool"   // run a tool (Tool + Input template)
	FlowNodeAgent  = "agent"  // invoke a peer agent (Agent = agent id)
	FlowNodeBranch = "branch" // no action; exists to fan edges out
	FlowNodePython = "python" // run inline Python (Code) or a deployed python tool (Tool)
	FlowNodeLLM    = "llm"    // run a constrained LLM transform/extraction step
	// FlowNodeTrigger and FlowNodeExit are STRUCTURAL endpoint blocks (Studio
	// visual authoring): a trigger marks where the flow starts (its Params carry
	// {kind: cron|http|channel, config}); an exit marks where it ends and how the
	// result leaves (Params carry {route: http|channel|console, config}). Both are
	// no-ops at run time — like a branch, they perform no action and just pass
	// control through — so they round-trip and validate without touching the
	// execution engine.
	FlowNodeTrigger = "trigger"
	FlowNodeExit    = "exit"
	// FlowNodeParallel is a FAN-OUT block: every eligible outgoing edge is taken
	// CONCURRENTLY, instead of the walker's normal "first truthy edge wins".
	// It exists because a graph that forks on the canvas used to execute exactly
	// one of its forks — the other branch was silently dropped, which looked like
	// a flaky agent rather than a routing rule. Declaring the fork point makes the
	// intent explicit: the node performs no work itself (it is structural), it
	// only decides that its successors run together. See Join / JoinQuorum /
	// JoinNode for how the branches are waited on and where they converge.
	FlowNodeParallel = "parallel"
)

// Join policies for a kind=parallel node — how many branches must finish, and
// what happens to the rest. Empty = JoinAll.
const (
	// JoinAll waits for every branch and fails the group if ANY branch fails.
	// The conservative default: partial results are usually worse than an honest
	// error, because a downstream node cannot tell "no data" from "not yet".
	JoinAll = "all"
	// JoinAny finishes as soon as the FIRST branch succeeds and cancels the rest.
	// For racing redundant providers (three search APIs, one answer) — the group
	// only fails when every branch has failed.
	JoinAny = "any"
	// JoinQuorum finishes as soon as JoinQuorum branches have succeeded and
	// cancels the rest; it fails once too many branches have failed for the
	// quorum to still be reachable. For "any 2 of 3 sources agree" fan-outs.
	JoinQuorum = "quorum"
	// JoinBestEffort waits for every branch and NEVER fails the group: failed
	// branches contribute a null entry to the aggregate. For enrichment fan-outs
	// where a missing optional source must not take the whole run down.
	JoinBestEffort = "best_effort"
)

// IsStructuralKind reports whether a node kind performs NO runtime action and
// exists only to route/anchor the graph (branch, trigger, exit, parallel). The
// flow engine skips execution for these and only follows their edges.
func IsStructuralKind(kind string) bool {
	return kind == FlowNodeBranch || kind == FlowNodeTrigger ||
		kind == FlowNodeExit || kind == FlowNodeParallel
}

// FlowPort is a declared, named connection point on a node (Story S0.3).
// All fields are optional and purely descriptive — a node with no declared
// ports keeps today's single implicit input/output. Name identifies the
// port for edge wiring (FlowEdge.FromPort / ToPort); Type is an optional
// type hint (e.g. "string", "json") for tooling/validation; Label is an
// optional human-readable display name for editors.
type FlowPort struct {
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Type  string `yaml:"type,omitempty" json:"type,omitempty"`
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	// Field optionally decouples a port's wire name from the data it carries
	// (Story S0.3 runtime resolution). On an OUTPUT port it is the (optionally
	// dotted) path into the producing node's result that the port exposes — e.g.
	// a port named "notebook_id" with Field "notebook.id" carries result.notebook.id.
	// On an INPUT port it is the argument KEY the wired value is bound to in the
	// node's assembled input object, when that key should differ from the port
	// Name. Empty = use Name (the common case: port name == result field == arg
	// key). Purely declarative; the runtime reads it when assembling wired inputs.
	Field string `yaml:"field,omitempty" json:"field,omitempty"`

	// ── Port contracts (P0-2) ────────────────────────────────────────────────
	// These make a port's shape CHECKABLE instead of merely descriptive. All are
	// optional and zero-value compatible: a port declaring none of them behaves
	// exactly as before, so existing workflows keep validating unchanged.

	// Required marks an INPUT port that must be wired (or supplied by the node's
	// static Input) before the flow can run. Ignored on output ports. A required
	// input with nothing feeding it becomes a compile error instead of a null at
	// run time — which is the failure mode this replaces: the value rendered as
	// "<no value>" and a tool rejected it two nodes downstream.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`

	// Cardinality declares whether this port carries ONE value or MANY:
	// "" (unset — treated as one) | "one" | "many". It is what makes the
	// fan-out/aggregation contract checkable: a for_each node consumes a "many"
	// port and hands each item to the body as "one". Wiring a "many" producer
	// into a "one" consumer with no aggregating step is exactly the bug class
	// that silently stringifies a list into "[map[...] map[...]]".
	Cardinality string `yaml:"cardinality,omitempty" json:"cardinality,omitempty"`

	// Nullable allows this port's value to be absent/null. A non-nullable input
	// refuses a nullable producer unless an adapter supplies a default, so "the
	// API returned null for that field" surfaces at author time rather than as a
	// downstream template failure.
	Nullable bool `yaml:"nullable,omitempty" json:"nullable,omitempty"`

	// Adapter marks this port's node as an explicit, author-acknowledged
	// CONVERSION point. Type/cardinality/nullability mismatches are refused
	// between ordinary nodes; routing the wire through a node whose consuming
	// port sets Adapter:true permits the conversion, because someone has taken
	// responsibility for reshaping the data. This is the "conversions require
	// explicit adapter nodes" rule: the graph must SHOW the reshape instead of
	// hiding it inside a template.
	Adapter bool `yaml:"adapter,omitempty" json:"adapter,omitempty"`
}

// Port cardinality tokens.
const (
	CardinalityOne  = "one"
	CardinalityMany = "many"
)

// FlowNode is one vertex of the graph.
type FlowNode struct {
	// ID is unique within the flow; checkpoint keys derive from it.
	ID string `yaml:"id" json:"id"`
	// Kind is tool | agent | branch (default tool when Tool is set,
	// branch when neither Tool nor Agent is set).
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`
	// Tool names the tool to invoke (kind=tool).
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`
	// Agent names a peer agent to invoke as agent__<id> (kind=agent).
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`
	// Description is a short, concrete human-readable line of exactly what this
	// node does (Studio shows it under the node on the canvas and in the
	// inspector). Purely descriptive; ignored by execution.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Intent is the user's plain-language description of what this node should do
	// (Studio "describe this step"). It is the source the per-node compiler turns
	// into concrete config (tool+args / mcp / skill / agent / python). Persisted
	// so a node is always re-editable as a prompt and a generated node round-trips
	// identically to a hand-built one (Phase C parity). Ignored by execution.
	Intent string `yaml:"intent,omitempty" json:"intent,omitempty"`
	// Code is the inline Python source for a kind=python node (Studio "Custom
	// Python" block). When set, the runtime executes it in the sandboxed Python
	// executor (process-per-call); inputs arrive as a JSON `inputs` payload and
	// the node's printed/returned value becomes its Output. Empty for a
	// python node that instead references a deployed tool via Tool.
	Code string `yaml:"code,omitempty" json:"code,omitempty"`
	// Requires lists the capabilities a kind=python node needs, inferred from
	// its Code by the Studio classifier (internal/studio/codeclass): a subset of
	// {"system","network"}. Empty = ReadOnly (inside the default guardrails).
	// Drives the per-case consent model; never widens what the runtime grants.
	Requires []string `yaml:"requires,omitempty" json:"requires,omitempty"`
	// Consent is the per-case grant recorded for a beyond-guardrail kind=python
	// node (Studio §13). It is bound to the exact code via Hash; the runtime
	// REFUSES to execute the node unless this stamp is present, its Hash matches
	// the current Code, and it covers the code's required capabilities. nil =
	// no grant — valid only for ReadOnly code. Pure data; the decision logic
	// lives in internal/studio/consent.
	Consent *FlowConsent `yaml:"consent,omitempty" json:"consent,omitempty"`
	// Input is a Go template producing the node's input from flow vars.
	Input string `yaml:"input,omitempty" json:"input,omitempty"`
	// Output names the flow var that stores this node's result.
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
	// ForEach optionally turns this node into a bounded map operation. It is a
	// Go template that must render a JSON array. The node executes once per item
	// and stores an ordered JSON array under Output. Empty preserves the normal
	// single-execution behavior.
	ForEach string `yaml:"for_each,omitempty" json:"for_each,omitempty"`
	// ItemVar is the template variable bound to the current ForEach item
	// (default "item"). The zero-based item index is also available as
	// "<item_var>_index".
	ItemVar string `yaml:"item_var,omitempty" json:"item_var,omitempty"`
	// MaxParallel bounds concurrent ForEach item execution. Zero/one is
	// sequential; values above one opt into real parallel fan-out while result
	// ordering remains deterministic.
	MaxParallel int `yaml:"max_parallel,omitempty" json:"max_parallel,omitempty"`
	// Join is the wait policy for a kind=parallel node: all | any | quorum |
	// best_effort (empty = all). It answers the question a fan-out cannot leave
	// implicit — "when is this group DONE?" — because without it a single slow or
	// broken branch decides the fate of the whole run by accident: either it
	// blocks a result that three healthy branches already produced, or its
	// failure quietly disappears. Ignored on every other node kind.
	Join string `yaml:"join,omitempty" json:"join,omitempty"`
	// JoinQuorum is how many branches must SUCCEED when Join=="quorum". It must be
	// between 1 and the number of outgoing edges; a quorum larger than the fan-out
	// can never be met, so it is rejected at compile time rather than hanging the
	// group until every branch has failed. Ignored for other join policies.
	JoinQuorum int `yaml:"join_quorum,omitempty" json:"join_quorum,omitempty"`
	// JoinNode is the id of the BARRIER node where this parallel node's branches
	// converge: each branch walks until it reaches that node and stops WITHOUT
	// executing it, then the join runs it exactly once with every branch's results
	// merged. Empty = no barrier; branches simply run to their natural
	// termination. Naming the barrier is what prevents the classic fan-in bug of
	// the merge step running once per branch (three emails instead of one summary)
	// — which is invisible on the canvas because the graph looks identical either
	// way. It must exist and be reachable from every branch.
	JoinNode string `yaml:"join_node,omitempty" json:"join_node,omitempty"`
	// OnError is retry | skip | escalate | abort (default abort). "escalate"
	// routes a failed visit to the flow's declared Escalation node (the failure
	// is exposed to it under the "failure" flow var) instead of aborting; it
	// requires FlowSpec.Escalation to name a node.
	OnError string `yaml:"on_error,omitempty" json:"on_error,omitempty"`
	// Adaptive opts this node into runtime LLM salvage: when it fails or produces
	// a soft error (its output reports an error) because a real tool/API returned
	// an unexpected shape, the runtime asks the model to produce the node's
	// intended output from the actual input so the flow keeps running instead of
	// aborting. Bounded to one salvage attempt per node. Independent of the global
	// runtime.adaptive_nodes default. Applies to tool, python, and llm nodes.
	Adaptive bool `yaml:"adaptive,omitempty" json:"adaptive,omitempty"`
	// Timeout optionally overrides the global runtime.tool_timeout for THIS node's
	// execution (a Go duration string, e.g. "30s", "10m"). It lets a developer fix
	// a single slow-by-design block — e.g. a NotebookLM research/audio poll — by
	// raising just that block's budget, without weakening the global safety net for
	// every other node. Empty = use the global default. Invalid values are ignored
	// (and flagged by Studio validation). Applies to tool, agent, and python nodes.
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	// X is the visual layout X coordinate.
	X float64 `yaml:"x,omitempty" json:"x,omitempty"`
	// Y is the visual layout Y coordinate.
	Y float64 `yaml:"y,omitempty" json:"y,omitempty"`
	// Inputs declares named typed input ports (Story S0.3). Optional:
	// empty/nil = today's single implicit input port (unchanged behavior).
	Inputs []FlowPort `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	// Outputs declares named typed output ports (Story S0.3). Optional:
	// empty/nil = today's single implicit output port (unchanged behavior).
	Outputs []FlowPort `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	// Params is optional typed per-node configuration (Story S0.3) carried
	// alongside the node. nil = none. The flow runtime passes it through
	// untouched; it does not affect Input templating or execution order.
	Params map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
}

// FlowConsent records a user's per-case consent for a beyond-guardrail python
// node (system/network/dynamic code). It is content-bound: Hash is the
// first 12 hex chars of sha256(Code) at grant time, so editing the code voids
// the grant. Capabilities is the set the user approved. Scope is one of
// "run" | "workflow" | "until_revoked". Purely data — see internal/studio/consent.
type FlowConsent struct {
	Hash         string   `yaml:"hash" json:"hash"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Scope        string   `yaml:"scope,omitempty" json:"scope,omitempty"`
	GrantedAt    string   `yaml:"granted_at,omitempty" json:"granted_at,omitempty"`
	GrantedBy    string   `yaml:"granted_by,omitempty" json:"granted_by,omitempty"`
}

// FlowEdge is one directed edge. Edges from a node are evaluated IN ORDER;
// the first edge whose If renders truthy (and whose traversal budget is
// not exhausted) is taken. No eligible edge = the flow ends.
type FlowEdge struct {
	From string `yaml:"from" json:"from"`
	// To is the target node id; "end" (or empty) terminates the flow.
	To string `yaml:"to,omitempty" json:"to,omitempty"`
	// If is a Go template predicate over flow vars; empty/"true"/non-zero
	// output = take the edge, ""/"false"/"0" = don't.
	If string `yaml:"if,omitempty" json:"if,omitempty"`
	// MaxIterations bounds how many times THIS edge may be traversed per
	// run (bounded cycles). Default 1 — cycles are bounded unless a back
	// edge explicitly raises its budget.
	MaxIterations int `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
	// FromPort names a declared output port on the From node (Story S0.3).
	// Optional: "" = the implicit single output port (current behavior).
	// When set, it must match one of the From node's declared Outputs.
	FromPort string `yaml:"from_port,omitempty" json:"from_port,omitempty"`
	// ToPort names a declared input port on the To node (Story S0.3).
	// Optional: "" = the implicit single input port (current behavior).
	// When set, it must match one of the To node's declared Inputs.
	ToPort string `yaml:"to_port,omitempty" json:"to_port,omitempty"`
}

// FlowSpec is the whole graph.
type FlowSpec struct {
	Nodes []FlowNode `yaml:"nodes" json:"nodes"`
	Edges []FlowEdge `yaml:"edges,omitempty" json:"edges,omitempty"`
	// Entry is the starting node id (default: first node).
	Entry string `yaml:"entry,omitempty" json:"entry,omitempty"`
	// Output is the id of the node whose result becomes the flow's final output
	// (delivered to channels). Empty = the last node executed (default).
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
	// MaxNodeExecutions is the global safety budget across the whole run
	// (default 100). Exceeding it aborts the flow.
	MaxNodeExecutions int `yaml:"max_node_executions,omitempty" json:"max_node_executions,omitempty"`
	// Escalation is the id of the node that handles failures for nodes declaring
	// on_error: escalate. When such a node fails, the walker records the failure
	// under the "failure" flow var ({node, kind, error, visit}) and continues the
	// run from this node instead of aborting — so "the LLM couldn't fix it"
	// becomes an ordinary, declared path (notify a human, park the run) rather
	// than a stack trace. Empty = no escalation path; escalate is then invalid.
	Escalation string `yaml:"escalation,omitempty" json:"escalation,omitempty"`
}
