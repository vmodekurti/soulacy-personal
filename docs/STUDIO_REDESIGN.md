# Studio — First-Principles Redesign

> A fresh look, written to be acted on. The thesis in one line: **Studio's job is
> to make a working agent from a plain-language description, and the loop that does
> that already exists — so the redesign is mostly subtraction, not addition.**

Status: proposal for review. Supersedes the design intent (not the operational
notes) of `STUDIO_SESSION_HANDOFF.md` §9.

---

## 0. Implemented this session — full-flow observability

The first concrete step toward "the transcript is the UI" is in. The whole
build-verify-repair flow is now **always debuggable and logs everything**, with
no behavior change to the loop itself:

- **`internal/studio/trace.go`** — a `BuildTrace`: a single build's ordered,
  structured event log. Every phase (draft snapshot → preflight → each repair →
  each verify → final result) records a `TraceEvent` with a monotonic sequence,
  wall-clock + elapsed time, step duration, plain-language message, and
  structured `data` (the exact problem set, what repair did, verify
  outcome/run-trace, a draft content-hash so you can see *when* the draft
  actually changed). Events fan out to an always-on in-memory mirror and an
  optional **JSONL file** (one object per line, flushed per event, tail-able).
  Nil-safe by construction, so instrumentation is unconditional.
- **`BuildTraceStore`** — bounded in-memory ring of recent builds (mirrors the
  runtime's `flowTraceStore`), plus best-effort JSONL persistence to
  `SOULACY_STUDIO_TRACE_DIR` when set (degrades silently to memory-only).
- **Loop instrumented** (`buildloop.go`) — `BuildOptions.Trace` threads a trace
  through `BuildUntilWorks`; every user-facing `BuildEvent` is also durably
  recorded, and each phase is timed and snapshotted.
- **Gateway** — both `/studio/build` and `/studio/build/stream` open a trace
  spanning the *whole* flow (glue → self-tests → loop) and return its `traceId`;
  new `GET /studio/build-trace[?id=]` (full structured trace, latest if no id)
  and `GET /studio/build-traces` (recent-builds summary) expose it.
- **Tests** — `trace_test.go` covers ordering/sequence, nil-safety, the timed
  step, JSONL round-trip, store bounding, and an end-to-end assertion that a real
  loop run captures snapshot/verify/result phases. Full suite green
  (`studio`, `reasoning`, `gateway`, `sdk/reasoning`); `go build ./...` clean.

- **GUI build inspector** (`gui/src/lib/studio/BuildInspector.svelte`) — a
  dependency-free side panel that renders a build's trace as an ordered timeline:
  each event with its elapsed time, kind, attempt, message, an inline hint
  (problem count / "real run passed" / draft node-count + content hash), step
  duration, expandable structured detail (problem lists + raw JSON), and a
  recent-builds picker. Opened from a "🔍 Inspect" button on the build report;
  `/studio/build` and the stream now return a `traceId` the page threads in.
  `api.js` / `studioApi.js` gain `buildTrace`/`buildTraces`. GUI build green.

This is the substrate the rest of the redesign reports through: the build loop is
now debuggable end to end, from the durable JSONL log to a visible timeline.

## 0b. Implemented this session — typed ports as the default handoff

The redesign's keystone (§4, "typed ports, not template strings") is now the
default on the authoring path:

- **`internal/studio/portize.go` — `PortizeHandoffs`** deterministically lowers a
  whole-value handoff template in a tool/python node's input
  (`{"notebook_id":"{{ .notebook.id }}"}`) into a typed PORT wire: an output port
  on the producer exposing `id`, an input port on the consumer for `notebook_id`,
  and an edge carrying `from_port → to_port`. The template is removed; the runtime
  (`reasoning.resolvePortInputs`) binds the value structurally, with no templating.
- **Control flow is never perturbed.** The first wire on a pair annotates an
  existing direct control edge (keeping its predicate); further wires are added as
  data-only edges (`if:"false"`) — read for data, never traversed for control.
- **Wired into `RepairWiring` as the final step**, after the reconcile passes have
  pointed each reference at the right producer/field. Those passes are now an
  internal normalization stage that *feeds* port generation rather than the
  user-facing handoff mechanism — so the whole template-handoff bug class
  (whole-object dumps, doubled paths, `map[...]` leakage) is structurally absent
  from the default authoring path. (The passes are retained, not yet deleted,
  because they still harden the advanced template escape hatch; deleting them is
  a follow-up once the escape hatch is formally gated.)
- **Conservative + safe:** only clean whole-value handoffs from an *ancestor*
  producer are lowered (prose-with-template, constants, unknown vars, and
  non-ancestor refs are left untouched); idempotent, so it runs on every pass.
- **Tests** (`portize_test.go`): field-path wiring, whole-output (implicit
  from_port), two-wires-one-producer (control + data edge), prose/constant/unknown
  skips, idempotency, ancestor-only, and a load-bearing **end-to-end runtime test**
  that compiles the lowered flow and runs it through `reasoning.RunFlow`, asserting
  the consumer receives the extracted field value template-free. Full suite green
  (`studio`, `reasoning`, `sdk/reasoning`); `go build ./...` clean with cgo.

## 0c. Implemented this session — canvas type-safety + live heartbeat

The first slice of principle #1 at the *interaction* layer and the "feel" both
landed on the real canvas:

- **Design-time type safety** (`gui/src/lib/studio/portcompat.js`): a pure,
  vitest-tested module (15 tests) that decides whether a producer port's type
  structurally satisfies a consumer port's — `json` is the universal handshake,
  untyped/`any` are wildcards, scalars widen into strings, genuine mismatches are
  rejected. Enforced in `isValidConnection` (so a type-mismatched wire is
  physically impossible to draw — xyflow rejects it mid-drag) and in the
  Inspector's manual "Add connection" path (toasts the reason). Wrong wires now
  fail at design time.
- **Live-state particle edges** (`gui/src/lib/studio/LiveEdge.svelte`): a custom
  edge with a weighted, soft resting curve (dashed for conditionals), and — while
  an autonomous build is in flight — glowing accent particles that flow along the
  bezier via SVG `<animateMotion>`, with a soft drop-shadow glow. Registered as
  the `live` edge type; `building` lights every edge so the canvas visibly pulses
  with the run. GPU-smooth, no per-frame JS.

Both verified: all **156** GUI unit tests pass; production `vite build` clean
(458 modules). The deeper canvas craft (full chrome-fade minimalism, snap
micro-animations with organic resistance, glassmorphic node overlays, 120fps
physics inertia, per-node run-state particles driven by the build trace) is the
continuing stream toward the §10 "feel" — these two pieces are its working spine.

## 0d. Implemented this session — typed-port contracts with teeth

Typed ports now carry and are checked against the tools' real schemas, so the
type-safety guarantee actually bites:

- **Schema-typed ports** (`portize.go` + `preflight.go paramTypes`): when
  `PortizeHandoffs` lowers a handoff it stamps the new input port with the
  consumer tool argument's declared type (parsed from the catalog's compact param
  hint), so a generated wire records a real contract — surfaced in the inspector
  and enforced by the GUI's design-time check.
- **Port wires validated against the schema** (`validate.go ValidateToolArgs`):
  closed the blind-trust hole where any declared input port was accepted. A typed
  input port whose bind key (Field override, else Name) isn't a real tool argument
  is now flagged in "fix before saving" — the unexpected-kwarg class, now for
  ports, not just template keys.
- **Tests:** schema-type stamping, bogus-port-binding flagging, and the existing
  port-arg test re-pointed to a real argument (it had encoded the old permissive
  rule). Full Go suite green; `go build ./...` clean with cgo.

## 0e. Implemented this session — per-node run-state on the canvas

The build's outcome is now legible on the graph itself, not just in the report:

- **`gui/src/lib/studio/runstate.js`** (pure, 7 vitest tests) maps a finished
  `BuildReport` + final preflight to `{ nodeId -> 'ok' | 'repaired' | 'problem' |
  'idle' }`: nodes named in residual problems / preflight blockers are `problem`,
  a clean verified/validated build marks the rest `ok`, and nodes named in a
  changing repair attempt are `repaired`.
- Threaded through `toFlow(workflow, validation, runState)` and recomputed
  reactively when a build finishes; `StudioNode` renders a restrained semantic
  accent — a colored ring + a glowing status dot (green/amber/red) — so the
  canvas shows *what the build actually did* per node, the principle's "vibrant
  but restrained accents for execution success/failure." All 163 GUI tests pass;
  production build clean.

Combined with the LiveEdge heartbeat and typed wires, the canvas now reads as a
living system: typed connections you can't mis-wire, particles flowing while it
builds, and each node settling to a success/repair/problem state when it lands.

## 0f. Implemented this session — attempt-by-attempt build replay

The durable trace now feeds back onto the canvas as a scrubbable replay:

- **`gui/src/lib/studio/replay.js`** (pure, 6 vitest tests) turns a build trace
  into an ordered list of frames — one per attempt plus a final verdict — each
  mapping every node to `problem` / `repaired` / `ok` / `idle` by diffing the
  per-attempt problem sets.
- The **Build Inspector** gained a transport (step ◀ ▶, play/pause, and a scrub
  slider); each frame is dispatched to the page, which applies it as a run-state
  override so the canvas **replays the loop**: you watch nodes flip red → amber →
  green as the autonomous build diagnoses and repairs them, then settle to the
  final outcome when the inspector closes. All 169 GUI tests pass; build clean.

This closes the observability loop end to end: the build records every step
durably → the inspector shows the structured timeline → the canvas replays it as
living state. The engine, the type-safety, and the live-state legibility of the
vision are now implemented and green; what remains is pure visual finish.

## 0g. Implemented this session — per-node configurable timeouts

When a flow errors with "context deadline exceeded", the developer can now fix the
*specific block* instead of weakening the global safety net:

- **`FlowNode.Timeout`** (SDK, a Go duration like `"10m"`) overrides
  `runtime.tool_timeout` for that node only. Precedence, most-specific-wins:
  **node `Timeout` > per-tool `toolDef.Timeout` > global `runtime.tool_timeout`**.
- **Engine** (`engine.go`): a context-carried override (`WithToolTimeout` /
  `effectiveToolTimeout`) is applied at every tool-call site and, when explicitly
  set, in `RunInlinePython`; the production flow runner (`flow.go runNode`) reads
  `node.Timeout` and carries it down. Slow MCP polls (NotebookLM research/audio)
  are the motivating case. Paired with the actionable timeout error from §earlier,
  a deadline now both *explains itself* and is *fixable per block*.
- **Studio**: `Validate` flags a malformed duration ("fix before saving"), and the
  Inspector has a **timeout** field per node with a plain-language hint.
- **Tests:** context-override round-trip + precedence (runtime), duration
  validation (studio). Full Go suite + 169 GUI tests green; builds clean.

This is the first instance of a broader principle worth generalizing: **every
timeout/limit the runtime enforces should be overridable at the block level**, so
a developer can always fix a failing node in place.

And one step better than "overridable" is "**auto-correct**": `deriveNodeTimeouts`
(`internal/studio/nodetimeout.go`, in `RepairWiring`) reads the wait a node
already declares — `max_wait: 1200`, `timeout_s`, `wait: "20m"`, in input or
params — and sets that block's `Timeout` to match (+60s headroom) when the
developer hasn't set one. So a NotebookLM poll that asks to run 20 minutes no
longer dies at the global 2-minute default; it just works, and the derived value
shows in the inspector's timeout field. Verified by a real-trace case
(`max_wait:1200 → 1260s`) plus params/duration-string/explicit-wins/idempotent
tests; full Go suite + GUI green.

The remaining sections (deleting the now-dormant repair passes once templates are
gated to advanced-only, and the deeper visual-canvas craft) are still proposal —
see §9 below, framed by the design principles in §10.

---

## 1. Purpose, in one sentence

**An amateur developer describes an agent in plain language — or drags a few
blocks — and Studio returns an agent that demonstrably works, having done all the
plumbing, testing, and repair itself.**

Everything else (YAML, templates, tool schemas, ports, validators, repair passes)
is *implementation detail the user should never see.* If the user sees it, we
failed the purpose — not the user.

That is the entire spec. Every feature either serves this sentence or is cut.

---

## 2. Honest diagnosis — why the current Studio disappoints

The disappointment is real and the cause is specific. It is **not** that the
centerpiece is missing. `internal/studio/buildloop.go` (`BuildUntilWorks`) is
exactly the envisioned loop: generate → run against real tools → repair on failure
→ retry to a budget → transparent transcript. The vision is in the code.

The failure is **shape**, in three parts:

1. **The loop is buried, not central.** It is one of ~40 `/studio/*` endpoints and
   one tab inside a single **4,507-line** `Studio.svelte` wired to **58** handlers.
   The user's experience is a control panel, not a promise.

2. **Reliability was pursued by accretion.** ~14,500 lines of non-test Go in
   `internal/studio`. Every live failure spawned a new deterministic patch —
   `coerceNodeInputs`, `reconcileFieldRefs`, `fixDoubledSegmentPaths`,
   `fixWholeValueInterpolations`, `ensureOutputVars`, `fixTemplateTypos`. Each is
   reasonable alone. Together they are a confession: *the system emits broken
   artifacts by default and spends a third of its code catching them.*

3. **The complexity points outward, at the user, instead of inward, hidden.** The
   vision puts all difficulty inside Studio, invisible. The build exposes it:
   "Fix these before saving," a YAML view, a gate compiler, a node compiler, a
   troubleshooter, a diagnoser. These are expert affordances on a tool sold to
   amateurs.

### The single root cause

Almost every repair pass exists to fix **one** decision: **step-to-step handoffs
are Go-template strings that the LLM authors by hand.** Whole-object
interpolation, doubled paths, dangling refs, `map[...]` leakage, missing output
vars — none of these are possible if handoffs are typed connections instead of
hand-written template text. Remove the cause and most of the 14K-line repair
stack has nothing left to do.

---

## 3. Who this is actually for

| | Primary | Secondary | Not the target |
|---|---|---|---|
| **Who** | Amateur / hobbyist developer | Capable dev who wants speed | Framework experts hand-tuning YAML |
| **Wants** | "Build me an agent that does X" | Scaffold fast, then refine | Full manual control |
| **Tolerance for plumbing** | Zero | Low | High |
| **Success feels like** | "It just worked" | "Faster than coding it" | n/a — they'd use the SDK |

Design for the **primary** column. The expert path already exists: it's the Go
SDK and raw YAML. Studio competing with the SDK on control is how it ended up with
40 endpoints. Studio's differentiator is the opposite of control — it's
**confidence without control.**

---

## 4. The one principle: typed ports, not template strings

The authoring surface has **no template strings.** A step's output is typed,
structured JSON. A downstream step consumes an upstream output by **selecting a
field through a typed port** — a connection the user draws or Studio infers, never
text the LLM writes.

Consequences, all of them subtractive:

- `fixWholeValueInterpolations`, `fixDoubledSegmentPaths`, `reconcileFieldRefs`,
  `fixTemplateTypos`, `coerceNodeInputs`, much of `autowire.go` — **deletable.**
  They repair a class of bug that can no longer be expressed.
- Templates survive **only** as an advanced/escape hatch, off the default canvas,
  clearly labeled "raw — you own the correctness."
- The LLM's job shrinks from *author correct template text* to *choose tools and
  draw connections.* That is a job LLMs do reliably; the other one they don't.

This is the move that turns a 14K-line repair stack into a few hundred lines of
typed-port resolution (`internal/reasoning/flow.go` already has `from_port` /
`to_port` / `resolvePortInputs` — make it the *only* path, not the fallback).

---

## 5. The product: one loop, made visible

Studio is **one primitive, repeated**, wrapped in **one loop**, shown honestly.

### The primitive — a Step

A step is created by describing it ("summarize the notebook as 5 bullet points")
or dropping a block. Studio:

1. Picks the typed tool/MCP node that does it.
2. Pins that tool's **real JSON schema** (full, not a hint) and validates against
   it at author time — killing the wrong-arg / wrong-tool / CLI-guessing class.
3. Wires it to upstream steps by **typed ports** against their real output shapes.
4. **Dry-runs it against real data** the moment it's placed, so "does this work"
   is answered immediately, per-step, not at the end.

Per-node compile grounded in real upstream shapes (Phase C, `compilenode.go`) was
already the most reliable thing in the codebase. **Make it the default authoring
unit**, not an advanced box.

### The loop — Build

The user clicks one thing: **Build.** `BuildUntilWorks` runs: assemble → execute
against real tools → on failure, repair and explain → retry to a budget → stop
when it genuinely passes. The transcript is the UI: *"Attempt 2: the search step
returned nothing — added an API key check and a fallback. Attempt 3: passed."*
The user watches Studio do the plumbing. That visible competence **is** the
differentiator.

### The whole UX, end to end

> Describe the agent → Studio scaffolds typed steps → each step self-tests against
> real data as it lands → press **Build** → watch it verify-and-repair until green
> → **Use it.** No YAML, no "fix before saving," no template, no 40 tabs.

---

## 6. Keep / Delete / Demote

Decisions, not suggestions. Reconcile against the live tree before cutting.

**KEEP (the spine):**

- `buildloop.go` — promote to the center of the product.
- `compilenode.go` + `shapes.go` — per-node compile grounded in real shapes = the
  default authoring unit.
- `internal/reasoning/flow.go` typed ports — make them the *only* handoff path.
- `verifier.go` / real-execution verifier — "verified" must mean it really ran.
- Live tool **schema** pinning + validation against the real schema (extend
  `ValidateToolArgs` from "MCP tools that publish params" to "always, full schema").

**DELETE (the repair stack the typed-port move makes inert):**

- `fixWholeValueInterpolations`, `fixDoubledSegmentPaths`, `reconcileFieldRefs`,
  `fixTemplateTypos`, `coerceNodeInputs`, the template-repair bulk of `autowire.go`.
- The time-helper zoo in `flowstrategy.go` (`dateFmt`/`dateFormat`/`formatDate`/…
  under every name) — typed ports + one canonical time node replace guess-the-helper.
- Template-class tests that exist only to pin the above behavior.

**DEMOTE (move off the default surface, keep as advanced/escape hatch):**

- Raw YAML view, `fix-yaml`, `validate-yaml`, `from-yaml` — advanced tab only.
- Composite blocks (`composite.go`) — keep **only** for genuinely tool-less
  multi-step glue; when a typed MCP exists, discrete typed nodes win. Resolves the
  §8.1 tension permanently.
- `troubleshoot` / `diagnose-run` — fold into the Build transcript, not separate
  surfaces.

**Endpoint target: from ~40 to ~6.** A defensible set:
`POST /studio/step` (describe→typed node, schema-validated, dry-run),
`POST /studio/wire` (typed-port connect against real shapes),
`POST /studio/build` (the loop, streaming the transcript),
`GET /studio/catalog` (tools + pinned schemas),
`POST /studio/save`, `GET /studio/agents`.
Everything else is either folded into these or demoted to an advanced namespace.

---

## 7. Why this is the differentiator other frameworks don't have

Most agent frameworks give you primitives and a blank file. The few visual ones
give you a canvas and leave correctness to you. **No one ships the autonomous
build-verify-repair loop as the product.** Soulacy already wrote it. The redesign
is simply the decision to *make that loop the whole experience* and delete the
scaffolding that hides it. The moat isn't more nodes or more validators — it's
that, alone in the category, Studio hands back something it has **proven runs.**

---

## 8. Sequencing — how to get there without a big-bang rewrite

Do it as subtraction along one vertical, not a parallel rebuild.

1. **Prove the single loop.** One vertical end to end: describe a step → typed MCP
   node → typed port → validated against the tool's real schema → dry-run on real
   data → Build self-heals to green. No templates anywhere in it. If this one path
   is bulletproof, it becomes the authoring primitive.
2. **Make typed ports the only handoff.** Flip `resolvePortInputs` from fallback to
   default; templates become opt-in advanced. The repair stack goes dormant.
3. **Delete the dormant repair stack** once nothing on the default path produces
   its inputs. Tests prove the bug classes are now unrepresentable.
4. **Collapse the UI.** `Studio.svelte` (4,507 lines) becomes describe-pane +
   canvas + Build-transcript. The other 50+ handlers retire or move to Advanced.
5. **Reduce the endpoints** to the ~6 above; namespace the rest under `/advanced`.

Each step ships a smaller, more confident Studio. There is no flag day.

---

## 9. Success criteria (so we know it worked)

- An amateur describes an agent and reaches **"Use it"** without seeing YAML, a
  template, a tool name, or a "fix before saving" panel.
- The median agent reaches verified-green in **≤ 3** Build attempts, and the user
  can read *why* in plain language.
- `internal/studio` non-test LOC drops from ~14.5K toward **≤ 4K**, and `/studio`
  endpoints from ~40 to **~6**, with equal or better reliability.
- Zero template-class runtime failures on the default path — because they're
  unrepresentable, not because they're repaired.

If we hit these, Studio stops being a control panel for plumbing and becomes the
thing you envisioned: *describe it, watch it build itself, use it.*

---

## 10. Design principles — the canonical north star

Studio is built at the intersection of unshakeable engineering and obsessive
design: an *instrument*, not a tool. Three pillars govern every decision.

### 1. The core engine must be bulletproof (Karpathy / Steinberger)

- **Deterministic compilation.** The editor is a projection of a rigid AST
  (`SOUL.yaml` / `FlowSpec`). Every gesture — move a node, draw an edge —
  compiles immediately to a strictly-typed JSON/YAML structure. *(Status: the
  canvas already compiles to `FlowSpec`; `reasoning.CompileFlow` is the strict
  contract.)*
- **Type-safe wires, not magic strings.** Connecting A→B is a typed PORT wire the
  editor guarantees, never a hand-typed `{{ .node.output }}`. *(Status: landed at
  the data layer — `PortizeHandoffs` makes ports the default handoff and the
  template bug class structurally absent; §0b. Still to do: enforce it at the
  *interaction* layer so a wrong wire is impossible to draw.)*
- **Fail at design time.** An invalid flow highlights the exact failure with
  sub-millisecond latency; ideally the wrong connection cannot be made at all.
  *(Status: author-time validation + the build loop's preflight exist; the
  instant in-canvas highlight is still to build.)*

### 2. Radical minimalism (Jony Ive)

- **The canvas is the hero.** The grid is the interface; chrome, sidebars, and
  menus appear only when needed and fade away when not.
- **Tactile feedback.** A dragged wire has physical weight — it snaps to ports
  with a satisfying micro-animation and an ease-in-out curve; valid connections
  glow softly, invalid ones offer subtle organic resistance.
- **Progressive disclosure.** A node shows its essence — name, core function,
  active connections — by default; full configuration lives in a translucent
  overlay invoked only when the user taps in.
  *(Status: this is the major open stream. The current 4,507-line `Studio.svelte`
  must collapse to canvas + describe-pane + build transcript, with these
  interaction qualities as acceptance criteria, not afterthoughts.)*

### 3. The macro-workflow philosophy

- Encourage high-level orchestration: a single node may encapsulate a 5-step
  Python transformation, or an entire agent reasoning loop. The editor's job is to
  connect macro steps cleanly, not to litter the screen with micro-logic.
- This resolves the §8.1 composite-vs-discrete tension with a precise rule:
  **discrete typed nodes when a typed tool exists; one macro node (Python/Agent)
  for genuinely tool-less multi-step glue** — never a 15-node monstrosity for a
  simple task.

### The "feel"

Precision-machined glass, not an enterprise IDE: pan/zoom locked to 60–120fps
with physics-based inertia; intentional dark-mode hues with restrained semantic
accents (success/failure) and glassmorphic depth; **live state** — a running
workflow gently pulses and data flows as glowing particles along the bezier
edges, giving a visceral read of the system's heartbeat. The build inspector
(§0) is the first step toward this live-state legibility; the particle-on-edge
canvas is its visual realization.
