# Declarative Cyclic Flow Graphs — Story E25

Workflows gained a graph form: `workflow.nodes` + `workflow.edges` in
SOUL.yaml. Unlike linear `steps`, graphs support conditional routing and
BOUNDED cycles (refine→judge loops, retry-until-pass, escalation paths),
compiled onto the existing checkpointing executor so resume-after-crash
keeps working.

## SOUL.yaml shape

```yaml
workflow:
  entry: refine                # default: first node
  max_node_executions: 50      # global safety budget (default 100)
  nodes:
    - id: refine
      tool: improve_draft      # kind inferred: tool
      input: '{"draft": {{.verdict.feedback | printf "%q"}} }'
    - id: judge
      tool: evaluate_draft
      output: verdict          # flow var holding this node's JSON result
    - id: notify
      agent: editor-agent      # kind=agent → invoked as agent__editor-agent
    - id: fork                 # neither tool nor agent → branch (no action)
  edges:
    - {from: refine, to: judge, max_iterations: 10}
    - {from: judge, to: refine, if: '{{not .verdict.ok}}', max_iterations: 5}
    - {from: judge, to: notify}        # fallback (declaration order matters)
    - {from: notify, to: end}          # "end"/absent target terminates
```

Semantics:

- **Nodes** run a tool (`tool:`), a peer agent (`agent:` → `agent__<id>`),
  or nothing (`branch` — pure routing). `input` is a Go template over the
  flow vars (`trigger` + every node's `output`); `on_error` is
  abort (default) | skip | retry | escalate.
- **Edges** from a node are evaluated in declaration order; the first one
  whose `if` predicate renders truthy AND whose traversal budget remains
  is taken. No eligible edge → the flow ends with the last node's result.
- **Bounded cycles**: every edge has `max_iterations` (default **1**), so
  cycles terminate by construction — a back edge must explicitly raise
  its budget. `max_node_executions` (default 100) backstops the whole run.
- **Checkpoints & resume**: each node visit checkpoints under
  `<node>#<visit>` in the existing store. Re-running a crashed run ID
  restores completed visits (vars included) and recomputes the same
  deterministic path — only unfinished work executes.
- Tool outputs that are JSON documents are unwrapped so predicates can
  address fields (`{{.verdict.ok}}`); plain text stays a string.

## Fan-out and bounded parallelism

A tool, agent, or Python node can map over a JSON array instead of requiring
one copied node per item:

```yaml
workflow:
  nodes:
    - id: search_sources
      tool: web_search
      for_each: '["hbr.org", "technologyreview.com", "gartner.com"]'
      item_var: source_domain
      max_parallel: 3
      input: '{"query":"site:{{ .source_domain }} AI articles","num_results":3}'
      output: source_searches
```

- `for_each` is a strict template that must render a JSON array.
- `item_var` defaults to `item`; its zero-based index is also available as
  `<item_var>_index`.
- `max_parallel: 0` executes sequentially. Values from 2 through 32 enable
  bounded concurrency.
- Results are always collected in input order, regardless of completion order.
- Every item emits its own trace and the node emits one aggregate trace.
- A map is capped at 1,000 items, in addition to the graph's normal execution
  and timeout limits.

Studio renders mapped nodes with a `for each` or `parallel ×N` badge and exposes
the three fields in the node inspector.

## Two entry points, one walker

- **Workflow runs** (cron/schedule): `WorkflowExecutor.Run` detects
  `nodes` and walks the graph with checkpoint hooks (internal/runtime/flow.go).
- **Chat runs**: `reasoning.strategy: flow` routes messages through the
  same graph via the E15 strategy registry (`sdk/reasoning.Config.Flow`);
  node actions go through the engine's standard tool-policy bridge.
  Steps surface as the usual reasoning.step events.

Compile-time validation (duplicate/missing node ids, unknown edge
endpoints, bad kinds, unknown entry) refuses the graph with precise
errors. Contract types live in `sdk/reasoning/flow.go`; the walker in
`internal/reasoning/flow.go` (`CompileFlow`, `RunFlow`, `FlowHooks`).

## Runtime healing & escalation

The runtime's adaptive layer (on by default; opt out with
`runtime.adaptive_nodes: false`, opt a single node in with `adaptive: true`)
keeps a run alive through shape surprises. All decisions are visit-local —
the deployed graph is never mutated — and each is bounded to **one attempt
per node (or edge) per run, across cycle re-visits**:

- **Input repair** — a node input template that fails to render is rebuilt by
  the model from a redacted snapshot of the live vars plus the tool schema.
- **Argument repair** — arguments a tool rejects on contract grounds are
  corrected and the REAL tool retried once. Never after network/auth/delivery
  failures, so a retry cannot duplicate a side effect.
- **Output salvage** — a node that fails (or soft-fails) on unexpected data
  shape has its intended output reconstructed from the actual input. Refused
  for *effectful* nodes (agent calls; send/create/write/…-named tools; python
  holding system/network capabilities): salvage must never fabricate a
  success receipt for an effect that did not happen.
- **Predicate repair** — an edge `if:` that fails to render no longer aborts
  the walk: the model is shown the predicate's intent plus the live values
  and returns a strict `{"take": bool}` verdict. It decides WHETHER the edge
  is taken, never what the data is. Emitted as `flow.heal` kind
  `edge_predicate`.
- **Port-type enforcement** — a declared output-port `type:` is now checked
  against the value the node actually produced. A mismatch is shape drift
  caught at the *producer*: it triggers the node's (bounded) adaptive reshape
  and emits a `flow.portdrift` event; the run itself never fails on a hint
  (`json`/`any`/unknown spellings are unchecked).

When repair is exhausted, **escalation** turns "the LLM couldn't fix it" into
an ordinary declared path instead of a stack trace:

```yaml
workflow:
  escalation: notify_owner       # flow-level failure handler
  nodes:
    - id: fetch
      tool: http.get
      on_error: escalate         # route failures to the escalation node
    - id: notify_owner
      tool: channel.send
      input: '{"text": "run failed at {{.failure.node}}: {{.failure.error}}"}'
```

A failed `escalate` visit records `{node, kind, error, visit}` under the
`failure` flow var and continues the run from the escalation node. The
escalation node failing never re-escalates, and escalation visits count
against `max_node_executions`, so escalate cycles terminate by construction.
`escalate` without a declared `escalation` node (or an unknown target) is
refused at compile time.

## GUI

The Flow page renders graph-form agents read-only: BFS-column layout,
entry wired from the trigger, predicate + `↺×N` budget labels on edges,
cycle back-edges highlighted amber, terminal edges into Output. Editing
arrives in a later story.
