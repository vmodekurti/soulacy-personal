# Studio — Visual, Prompt-First Builder: Build Order

> **North star (your vision):** anyone composes an agent by dragging blocks and
> describing each one in plain language. No tool names, no JSON, no template
> strings, no code. A **dry-run playground** with *real* values (sign-ins, sample
> payloads) is where fuzzy intent becomes verified behaviour. The same canvas is
> the result whether you dragged it or generated it from one top-level prompt.

This document turns that vision into a sequenced build order. Each phase is
**independently shippable and demoable**, ordered so every phase stands on the
one before it. File paths are concrete so work can start immediately.

---

## Where we already are (so we build, not rebuild)

- **Canvas + nodes + edges + inspector:** `gui/src/pages/Studio.svelte`,
  `gui/src/lib/studio/Inspector.svelte`.
- **Node kinds today:** `tool | agent | branch | python` (`sdk/reasoning/flow.go`,
  const block at top). Custom Python block already in the palette.
- **Typed connectors:** Phase-1 ports — `FlowPort` + edge `from_port`/`to_port`,
  resolved at runtime with no templates (`internal/reasoning/flow.go`
  `resolvePortInputs`).
- **Connector gates:** edge predicate `FlowEdge.If` already gates progression.
- **Whole-graph generation from one prompt:** `internal/studio/compiler.go`
  (`BuildPrompt` → `Compile`).
- **Dry-run + per-node mocks + run trace:** `internal/studio/testrun.go`,
  `/api/v1/studio/test`, `/api/v1/studio/run-trace` (Phase-1 live trace).
- **Secrets vault + per-case consent:** `internal/credentials`, the consent gate
  in `studio` save/plan; classifier `internal/studio/codeclass`.
- **Coarse composite blocks (Phase-2 start):** `internal/studio/composite.go`.

The vision needs **four** net-new things on top: trigger/exit as first-class
canvas blocks, an editable connector (gate + ports) UX, **per-node prompt → config
compilation**, and a **real-value dry-run playground**. Everything else is wiring
existing primitives to a better surface.

---

## Phase 0 — Stop the bleeding (prereq, ~0.5–1 day)

The current generator produces template bugs (`{{ now }}`, dangling `{{ .id }}`)
that block *any* save and would also break the dry-run loop. Fix these first so
the rest of the work has a working baseline.

- **Register the missing template functions** so `{{ now }}` etc. are valid:
  add `now`, `today`, `nowUnix`, `dateFmt` to `flowTemplateFuncs`
  (`internal/reasoning/flowstrategy.go`). The Studio pre-save validator already
  parses with the exact same funcset (`FlowTemplateFuncs()`), so this clears the
  two hard "function not defined" blockers at once.
- **Strengthen dangling-ref repair:** in the deterministic pass
  (`internal/studio/autowire.go` `ReconcileVars` + `temprefs.go`), map a bare
  `{{ .id }}` / `{{ .Format }}` to the nearest upstream producer of that field,
  and when a NotebookLM-style dance is detected, prefer rewriting to a typed port
  wire (or the composite block) instead of a template.
- **Tests:** a node input `{{ now }}` validates; a dangling `{{ .id }}` after a
  create-step is repaired to the right reference.

**Demo:** the NotebookLM prompt that currently shows 8 blockers saves clean (or
near-clean) with no manual clicking.

---

## Phase A — Trigger & Exit as first-class canvas blocks (~2–3 days)

Today the trigger is draft-level metadata (`Draft.Trigger`) and "end" is just an
edge target. Make both **draggable palette blocks** with inspector config.

- **Data model:** add node kinds `trigger` and `exit` (`sdk/reasoning/flow.go`).
  A `trigger` node carries `{kind: cron|http|channel, config}`; an `exit` node
  carries `{route: http|channel|console, config}`. Keep back-compat: on
  save/load, map a single `trigger` node ⇄ `Draft.Trigger`, and an `exit` node ⇄
  `Draft.Channels`/output route (`internal/studio/save.go`, `load_agent.go`,
  `compiler.go` `Flow`).
- **Palette + canvas:** add Trigger and Exit blocks to the palette group in
  `Studio.svelte`; render them with distinct styling (they're endpoints).
- **Inspector:** selecting a trigger shows cron/http/channel pickers (reuse the
  Schedule + Channels config); selecting an exit shows route + channel/HTTP
  target.
- **Validation:** exactly one trigger (entry), at least one exit; every path ends
  at an exit. Extend `internal/studio` graph validation.
- **Tests:** trigger node ⇄ `Draft.Trigger` round-trips; a graph with no exit is
  flagged.

**Demo:** drag a Trigger, set it to "cron 0 7 * * *"; drag an Exit, set it to
"Telegram"; both persist and round-trip through SOUL.yaml.

---

## Phase B — Editable connectors (gate + ports) (~2–3 days)

Make the **connector itself** a configurable object, as you described.

- **Select-an-edge inspector:** clicking a connector opens the inspector showing
  its `from_port` / `to_port` (the in/out contract) and its **gate** — the
  condition under which flow proceeds (`FlowEdge.If`). The user edits the gate in
  plain language; we compile it to a predicate (small, scoped LLM call or a
  builder UI: "when `<var>` `<op>` `<value>`").
- **If/else + decision connectors as palette sugar:** an "if/else" drop creates a
  `branch` node with two pre-labelled outgoing connectors (true/false); a
  "decision" creates a branch fanning N labelled edges. These are **UI sugar over
  the existing `branch` + `edge.if`** machinery — no new runtime concept.
- **Visual gate badges:** show each connector's gate on the line (extends the
  Phase-1 `⮑ wired` / condition badges already in the trace).
- **Tests:** an if/else drop produces a valid two-edge branch; a gate phrase
  compiles to a working predicate.

**Demo:** wire two blocks, click the line, type "only if at least one article
was found"; it becomes `{{ gt (len .articles) 0 }}` and validates.

---

## Phase C — Per-component intent: "describe this step" → config (~1–2 weeks, the core)

This is the heart of "no code." Each node gets an **intent prompt**; the system
compiles *that one node* into concrete config.

- **Data model:** add `FlowNode.Intent` (the user's plain-language description of
  the node) to `sdk/reasoning/flow.go`. It persists alongside the compiled config
  so a node is always re-editable as a prompt — this is also what gives **parity**
  (a generated node carries its Intent, so the hand-built and generated canvases
  are identical and equally editable).
- **Compile-one-node endpoint:** `POST /api/v1/studio/compile-node` that takes
  `{intent, node-kind hint, upstream port shapes, catalog}` and returns a filled
  node — a `tool` name + args, an `mcp__…` call, a `read_skill`, an `agent`
  handoff, or a `python` block. This **reuses the compiler seam** (`studio.LLM` +
  a scoped prompt) but with a *tiny* surface, which is far more reliable than
  whole-graph generation.
- **Inspector "Describe this step" box:** every component shows an intent field +
  a "Compile" button; the compiled result is shown read-only (and editable in
  advanced mode). For a python component the body is generated, classified
  (`codeclass`), and consent-chipped automatically.
- **Grounding from the graph:** the node's compile is grounded in its **incoming
  port shapes** (from upstream nodes) so it wires by port, not template — killing
  the dangling-ref class structurally.
- **Tests:** "search the web for AI news" → a `web_search` tool node with a real
  query; "post this to Telegram" → the right channel/exit; "dedupe these by url"
  → a python block that passes the bench.

**Demo:** drop a blank component, type "create a NotebookLM podcast from these
urls," and it compiles to the **composite block** (Phase-2) — one tested node,
not the 6-node dance.

---

## Phase D — Dry-run playground with real values (~1–2 weeks)

The trust loop. The thing that makes "no code" honest.

- **Real-value inputs:** extend the test bench (`internal/studio/testrun.go`,
  `/studio/test`) so the user supplies *real* sample inputs per node and selects
  **secrets** from the vault (sign-ins, API keys) — never typed inline.
- **Run one node / one path:** execute a single component (or the path up to it)
  against the real values, using the Phase-1 **run trace** to show input → output
  → duration → error per block, inline on the canvas.
- **Capture outputs as ground truth:** a node's real output shape is captured and
  fed back into Phase-C compilation and port wiring — so the LLM stops *guessing*
  payload shapes and instead compiles against observed data.
- **Consent at the boundary:** any node that runs with real credentials or shells
  out hits the existing per-case consent gate; the bench defaults beyond-guardrail
  nodes to mocked until explicitly run for real.
- **Tests:** a node runs against a sample payload + a vault secret and produces a
  trace; captured output shape drives a correct downstream wire.

**Demo:** set up the NotebookLM flow, hit "Dry run," provide a real Google sign-in
from the vault and 2 sample URLs, watch each block light up green with real
output, fix the one that fails — all visually.

---

## Phase E — Parity, polish, and the isolation ceiling (ongoing)

- **Prompt-only parity:** the existing whole-graph generate already fills the
  canvas; ensure every generated node carries its `Intent` (Phase C) so the
  generated and hand-built canvases are byte-for-byte the same editable artifact.
- **Palette completeness:** trigger, exit, tool, skill, MCP, agent, python,
  if/else, decision, plain connector — all draggable.
- **Real isolation (the honest ceiling):** running LLM-authored code with real
  credentials on `system`/`network` is resource-limited, **not** isolated today.
  Before non-trusted authoring, code nodes that hold those capabilities need
  container/nsjail/seccomp. Until then: operator-trusted, loud consent. (This is
  already called out in `docs/STUDIO_PYTHON_TOOLS.md` §5.5/§13.)

---

## Why this is doable

Three of the four net-new pieces (trigger/exit blocks, connector UX, parity) are
**surfacing machinery that already runs**. The one hard piece — per-component
prompt→config — is made tractable by two things already in the codebase: **typed
ports** (so handoffs are structural, not string templates) and the **dry-run
bench + run trace** (so compilation is validated against real data instead of
guessed). The vision's own instinct — "LLMs can't figure everything out, so give a
real-value playground" — is exactly the mechanism that makes the no-code promise
reliable rather than aspirational.

## STATUS — all phases built (backend + GUI wiring), 2026-06-23

Every phase below is implemented with Go tests passing and the GUI building clean.
What still needs YOUR eyes: live-LLM generation quality and clicking the real UI
(I can compile/test the Go and build the Svelte, but not run your model or the
browser).

- **Phase 0 — DONE.** `now`/`today`/`nowUnix`/`dateFmt` registered in
  `flowTemplateFuncs` (kills the `{{ now }}` blocker); `reconcileFieldRefs`
  rewrites dangling `{{ .id }}` → `{{ .notebook.id }}` (the create→use dance).
  Tests: `fieldrefs_test.go`, `flowtimefuncs_test.go`.
- **Phase A — DONE.** `trigger`/`exit` are real structural node kinds
  (`sdk/reasoning/flow.go`, no-op in `RunFlow`); `DeriveEndpoints`/`ValidateEndpoints`
  make the canvas authoritative (`endpoints.go`); palette blocks + inspector
  config (`Palette.svelte`, `Inspector.svelte`, `Studio.svelte`). Tests:
  `endpoints_test.go`.
- **Phase B — DONE.** `CompileGate` turns a plain-language connector condition into
  a validated predicate (`gate.go`, `POST /studio/compile-gate`); edge inspector
  "condition (plain language)" box. Tests: `gate_test.go`. (If/else + decision
  connector palette sugar remains a GUI nicety.)
- **Phase C — DONE.** `FlowNode.Intent` + `CompileNode` (`compilenode.go`,
  `POST /studio/compile-node`) compile one node's intent into concrete config,
  reusing all draft-parsing hardening; inspector "Describe this step" box. Tests:
  `compilenode_test.go`.
- **Phase D — DONE.** `CaptureShape`/`ShapesFromTrace` capture real output shapes
  from a run (`shapes.go`); `TestResult.Shapes` feeds them back into `CompileNode`
  grounding via the GUI's `lastShapes`. Un-mocked bench nodes already read real
  secrets from the vault. Tests: `shapes_test.go`.
- **Phase E — DONE (parity) / posture (isolation).** Generated nodes seed `Intent`
  from their description (`ensureNodeIntents`) so generated and hand-built canvases
  are the same editable artifact (`parity_test.go`). **Isolation ceiling stands:**
  running LLM-authored `system`/`network` code with real credentials is
  resource-limited, not isolated — keep it operator-trusted behind the loud
  per-case consent gate until container/nsjail lands (`docs/STUDIO_PYTHON_TOOLS.md`
  §5.5/§13). This is a deployment/infra step, not a code change.

### New gateway endpoints
`POST /studio/compile-gate`, `POST /studio/compile-node`,
`GET /studio/composite-blocks` (all registered in `internal/gateway/server.go`).

### Build/run note
Rebuild to pick up the Go changes: `go clean -cache && make gui && make build`,
then restart (`pkill -9 -f soulacy && ./scripts/run-runtime.sh`). The cgo/sqlite
packages need a `sqlite3.h` on `CGO_CFLAGS` in a clean sandbox (the
`mattn/go-sqlite3` module bundles one as `sqlite3-binding.h`).

## Suggested first cut

Phase 0 (unblocks today) → Phase A (visible, low-risk, proves the block model) →
Phase C scoped to **one** node kind (e.g. "describe a tool step") wired to the
Phase-D bench for that node. That vertical slice demonstrates the entire loop —
drag a block, describe it, dry-run it with a real value — end to end, before
widening to every component type.
