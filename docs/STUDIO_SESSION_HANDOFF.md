# Studio — Session Handoff (2026-06-24)

> This supersedes `docs/STUDIO_HANDOFF.md` (Phase-1 era) for everything done in
> this session. It captures the full robustness/repair stack added on top of the
> visual-builder vision, the live-debugging learnings from driving a real
> NotebookLM-podcast agent end to end, the exact files/functions touched, and a
> framing for the **fresh look at Studio** the next session will take.

---

## 0. TL;DR

- We pursued the **visual, prompt-first builder** vision (drag blocks, describe
  each in plain language, standard JSON handshakes) AND hardened the
  generate→validate→repair stack against the real failure taxonomy of
  LLM-generated workflows.
- All work is **uncommitted** (no git commits this session). Go + GUI build clean;
  all targeted tests green. See §6 for the exact build/test commands.
- A real **NotebookLM Podcast** agent now runs end to end through auth →
  create_notebook → … using **discrete typed MCP tool nodes** (not a fat
  Python/CLI block). Remaining failures were real-world integration (search
  provider key, Google auth) — not framework bugs.
- **Next session wants a fresh look at Studio's design** (§9). The current
  generation stack is large and accreted; there are real tensions worth
  redesigning rather than patching further.

---

## 1. The vision (north star, unchanged)

Anyone composes an agent by dragging blocks and describing each one in plain
language. No tool names, no JSON, no template strings, no code. A **dry-run
playground** with real values turns fuzzy intent into verified behavior. The same
canvas results whether built by hand or generated from one prompt.

Two principles that crystallized this session (driven by the user):
1. **Standard handshakes that always work** — every step's output is JSON; every
   step receives JSON; structured values cross boundaries as real JSON, never Go's
   `map[...]` text.
2. **Compose typed capabilities; don't bury them in code** — when a tool/skill/MCP
   covers an operation, call it as a typed node. Python is for *data glue only*
   (parsing/formatting/computation), **never** for calling tools/MCP or shelling
   to a CLI that wraps one.

Build-order doc: `docs/STUDIO_VISION_BUILD_ORDER.md` (phases + status).

---

## 2. What was built this session (by area)

### Visual builder phases (see STUDIO_VISION_BUILD_ORDER.md for detail)
- **Phase A — trigger/exit as real canvas blocks.** `FlowNodeTrigger`/`FlowNodeExit`
  structural kinds (`sdk/reasoning/flow.go`, `IsStructuralKind`), no-op at runtime
  (`internal/reasoning/flow.go`). `DeriveEndpoints`/`ValidateEndpoints`
  (`internal/studio/endpoints.go`) make the canvas authoritative over
  Trigger/Channels/Entry. GUI: Trigger/Exit palette blocks + inspector config.
- **Phase B — editable connectors.** `CompileGate` (`internal/studio/gate.go`,
  `POST /studio/compile-gate`) turns a plain-language condition into a validated
  flow predicate (deterministic patterns first, LLM fallback). GUI: "condition
  (plain language)" box on the edge inspector.
- **Phase C — per-node "describe this step".** `FlowNode.Intent` +
  `CompileNode`/`BuildNodePrompt` (`internal/studio/compilenode.go`,
  `POST /studio/compile-node`) compile ONE node's intent into concrete config,
  grounded in upstream output shapes. GUI: "Describe this step" box.
- **Phase D — dry-run shape feedback.** `CaptureShape`/`ShapesFromTrace`
  (`internal/studio/shapes.go`); `TestResult.Shapes` feeds real output shapes back
  into `CompileNode` grounding (GUI `lastShapes`).
- **Phase E — parity.** `ensureNodeIntents` seeds a generated node's Intent from
  its description so generated and hand-built canvases are the same editable
  artifact.
- **Phase 2 — coarse composite blocks.** `internal/studio/composite.go`
  (`CompositeBlocks`, `notebooklm_podcast`, `MaterializeNode`,
  `GET /studio/composite-blocks`). NOTE: this leans toward collapsing a dance into
  ONE python block — **in tension** with the later "compose typed MCP nodes"
  direction (§9).

### The robustness / repair stack (the bulk of the live debugging)
All deterministic, run in `RepairWiring` (generate + auto-repair + fix paths)
and/or `Validate`/save:
- **`coerceNodeInputs`** (`compiler.go`) — model emits `"input": {…}` (object)
  instead of stringified JSON → coerced. (Fixed the first "cannot unmarshal object
  … input of type string".)
- **Time helpers** (`internal/reasoning/flowstrategy.go`) — `now`, `today`,
  `nowUnix`, and date formatting under EVERY common name (`dateFmt`/`dateFormat`/
  `formatDate`/`date`) tolerant of Go-ref/`YYYY-MM-DD`/`%Y-%m-%d` layouts. (Fixed
  `function "now"/"dateFormat" not defined`.)
- **`reconcileFieldRefs`** (`autowire.go`) — dangling `{{ .id }}` → `{{ .notebook.id }}`
  via earliest upstream object producer.
- **`fixWholeValueInterpolations`** (`autowire.go`) — `"urls":"{{ .urls }}"` →
  `"urls": {{ toJson .urls }}` (real JSON, not Go `map[...]`), only for collection
  keys, leaving scalar destinations for field-level repair.
- **`ensureOutputVars`** (`autowire.go`, also at save) — every executable node gets
  an output var, so its result is stored and downstream wires don't read `null`.
  (Fixed `{"urls": null}` handoff.)
- **`fixDoubledSegmentPaths`** (`autowire.go`) — `{{ .notebook_id.notebook_id.id }}`
  (doubled segment + over-reach) → `{{ .notebook_id.notebook_id }}`. (Fixed the
  recurring `can't evaluate field id in type interface {}`.)

### Validation backstops (`internal/studio/validate.go`, surfaced in Studio's
"Fix these before saving")
- **`ValidateEndpoints`** — trigger/exit authoring checks.
- **`shellSmellIssues`** — a python node shelling to a CLI is a **BLOCKER** when an
  MCP is connected (use the typed tool), else a warning. Enforces "Python is glue,
  not for calling tools."
- **`ValidateToolArgs`** (`POST /studio/validate` now passes the live catalog) —
  flags a tool node passing an argument the tool's schema doesn't accept (the
  `num_results`-unexpected-kwarg class), for MCP tools that publish params.

### Generation prompt rewrites (`compiler.go BuildPrompt`, `compilenode.go
BuildNodePrompt`)
- **STANDARD DATA FORMAT** rule (JSON handshakes; never `"k":"{{ .x }}"` for
  structured values; use `{{ toJson .x }}` or a python node with empty input).
- **Compose typed capabilities** — discrete tool/MCP nodes IN SEQUENCE for
  multi-step jobs; python ONLY for glue; never re-implement a tool in python or
  shell to a CLI that wraps one. Removed the old "collapse into a SINGLE python
  node" / "shelling to a local CLI is fine" language.

### New gateway endpoints (`internal/gateway/server.go` + `studio.go`)
`GET /studio/composite-blocks`, `POST /studio/compile-gate`,
`POST /studio/compile-node`. (`GET /studio/run-trace` was Phase 1.)

### GUI (build-verified only — NOT click-tested)
`gui/src/lib/studio/Palette.svelte` (Trigger/Exit blocks),
`Inspector.svelte` (trigger/exit config, gate box, describe-step box),
`gui/src/pages/Studio.svelte` (drop handlers, `compileGate`, `compileNode`,
`lastShapes`), `gui/src/lib/api.js` + `studioApi.js` (`compileGate`,
`compileNode`, `compositeBlocks`).

---

## 3. New/changed files (inventory)

**internal/studio/** (new): `composite.go`, `endpoints.go`, `gate.go`,
`compilenode.go`, `shapes.go`, plus tests `composite_test.go`,
`endpoints_test.go`, `gate_test.go`, `compilenode_test.go`, `shapes_test.go`,
`parity_test.go`, `fieldrefs_test.go`, `parsedraft_input_test.go`,
`wholevalue_test.go`, `outputvars_test.go`, `doubledpath_test.go`,
`shellsmell_test.go`, `toolargs_test.go`.
**internal/studio/** (modified): `compiler.go`, `autowire.go`, `validate.go`,
`testrun.go`, `save.go`.
**internal/reasoning/**: `flowstrategy.go` (time funcs), `flow.go` (structural
kinds), new `flowtimefuncs_test.go`.
**sdk/reasoning/**: `flow.go` (`FlowNodeTrigger`/`Exit`, `IsStructuralKind`,
`FlowNode.Intent`, `FlowPort.Field` from Phase 1).
**internal/gateway/**: `studio.go`, `server.go`.
**gui/**: as in §2.
**docs/**: `STUDIO_VISION_BUILD_ORDER.md` (new), this file.

---

## 4. Operational learnings (from driving the live NotebookLM agent)

These are environment/integration facts, NOT framework code:
- **web_search needs a provider key.** Built-in `web_search` uses Tavily / Serper /
  Ollama (`internal/runtime/engine.go`). With none configured it returns empty →
  "no notable AI news" fallback. Set `search.provider` + `search.api_key` (or
  `TAVILY_API_KEY`/`SERPER_API_KEY`) in config, restart.
- **NotebookLM auth = `nlm login --force` in a terminal.** The `notebooklm-mcp-cli`
  reads `~/.notebooklm-mcp-cli/auth.json`. An earlier LLM "format cookies" step
  corrupted that file (prose + trailing `\n` → "Illegal header value"); `--force`
  bypasses validating the corrupt file. **Never route credentials/cookies through
  an LLM or string-formatting step** — they need exact bytes; pull from
  `nlm login` / Secrets.
- **`nlm` CLI takes a positional title** (`nlm notebook create "Title"`), not
  `--title` — a good example of why guessing CLI flags is brittle and the MCP
  (typed) path is right.
- **The failure journey** (each now prevented/auto-repaired/flagged): object-typed
  input → Go-`map[...]` handoffs → dropped node outputs (null wires) → missing
  template helpers → CLI-vs-MCP guessing → corrupted credentials → doubled
  template paths → unexpected tool kwargs → python-shelling-to-CLI.

---

## 5. Git state — ALL UNCOMMITTED

Nothing was committed this session (the user asked to leave git alone). The repo
also appears to be edited in parallel on the host (compiler.go/compilenode.go
showed external edits mid-session). Before committing: review the diff, run the
full suite, and reconcile any parallel edits to `BuildPrompt`.

---

## 6. Build & test (verified green this session)

Local macOS (host) rebuild:
```bash
cd ~/Documents/Documents*MacBook*1/Vasu/Personal/Development/agentic/soulacy
go clean -cache && make gui && make build
pkill -9 -f soulacy && ./scripts/run-runtime.sh
```
Tests (Go):
```bash
go build ./...
go test ./internal/studio/... ./internal/reasoning/... ./internal/runtime/... ./internal/gateway/...
(cd sdk && go test ./reasoning/...)
```
Note: in a clean sandbox the cgo/sqlite packages need a `sqlite3.h` on
`CGO_CFLAGS`; the `mattn/go-sqlite3` module bundles one as `sqlite3-binding.h`.
GUI: `cd gui && npm run build` (clean; only pre-existing a11y/unused-CSS warnings).

**Important:** the deterministic repairs + prompt changes affect GENERATION and
PRE-SAVE VALIDATION, not already-saved agents. To apply them to an existing agent,
**regenerate** or **Build until it works**; or hand-edit the offending node.

---

## 7. Studio architecture as it stands (for the fresh look)

Generation pipeline (`internal/studio/compiler.go Compile`):
```
BuildPrompt (canonical example + rules + catalog + patterns + composite grounding)
  → LLM.Complete
  → ParseDraft (strip fences, coerceNodeInputs)
  → normalizeFlow / reconcilePorts
  → RepairWiring  [AutoWire, ReconcileVars, reconcileFieldRefs,
                   fixDoubledSegmentPaths, fixWholeValueInterpolations,
                   fixTemplateTypos, ensureOutputVars]
  → classifyFlowNodes (python capability tiers)
  → ensureNodeIntents (parity)
  → focusedRepair (one LLM pass if data-flow gaps remain)
  → reasoning.CompileFlow (hard contract)
```
Per-node compile (`compilenode.go`): the same hardening scoped to one node, fed
upstream output shapes (incl. captured real shapes from a dry run).

Validation (`validate.go Validate` + endpoint): CompileFlow + flow/trigger
warnings + endpoint checks + shell-smell (blocker w/ MCP) + tool-arg check.

Runtime handoffs (`internal/reasoning/flow.go`): typed PORTS (`from_port`/`to_port`,
`resolvePortInputs`) resolve values with no templates; templates are the fallback.

---

## 8. Known tensions / open issues

1. **Composite blocks (Phase 2) vs. compose-typed-MCP-nodes.** The composite block
   collapses a multi-step dance into ONE python block; the later (correct)
   direction is discrete typed MCP nodes in sequence. These pull opposite ways —
   reconcile in the redesign (composite blocks are best when there is NO typed
   MCP; otherwise prefer discrete nodes).
2. **Macro-workflow bias.** `BuildPrompt` still nudges toward "simple/few nodes,"
   which can conflict with "emit the discrete tool nodes a multi-step MCP job
   needs." Decide the rule precisely.
3. **Render fragility.** A single bad `{{ }}` path aborts the WHOLE run (not just
   the node). The offered-but-unbuilt **graceful render** (bad ref → empty + node
   error subject to on_error, surfaced in trace) is still open.
4. **GUI is build-verified only.** The drag/drop, inspector boxes, and palette
   blocks compile but were never click-tested.
5. **Repairs only at generate/validate time**, not retroactively on saved agents.
   Consider a "re-heal saved agent" action.
6. **Two prompt surfaces** (`compiler.go` whole-graph and `compilenode.go`
   per-node) must stay consistent — they drifted this session (CLI-shelling allowed
   in one, forbidden in the other).

---

## 9. The "fresh look" — questions to redesign around

The user wants to re-examine how Studio works from first principles. Framing for
that conversation:

- **Where should reliability live?** Today it's a thick deterministic
  repair/validate stack patching model output. The vision says reliability should
  come from **typed contracts** (typed ports, typed tool schemas) + the
  **dry-run-with-real-values loop**, with the LLM only choosing/sequencing. How
  much of the repair stack is a symptom of the LLM authoring raw templates at all?
- **Templates vs. ports.** Every template-class bug (whole-object, doubled path,
  dangling ref, `map[...]`) exists because handoffs are Go-template strings. If
  tool→tool handoffs were ALWAYS typed ports (never templates), most of §2's
  repair stack becomes unnecessary. Should templates be removed from the
  authoring surface entirely (advanced-only)?
- **Whole-graph generation vs. per-node compile.** Per-node compile (Phase C),
  grounded in real upstream shapes, was far more reliable than whole-graph
  generation. Should the default authoring flow be "scaffold structure, then
  compile each node against real data," rather than "generate the whole graph"?
- **Catalog grounding quality.** Many failures (wrong args, wrong tool, CLI
  guessing) trace to the model not having, or not trusting, the tool's real
  schema. Could Studio fetch and pin each MCP tool's full JSON schema (not a
  compact hint) and validate against it at author time?
- **Coarse vs. fine blocks.** Composite blocks (one tested block) vs. discrete
  typed nodes — pick a principle: discrete typed nodes when a typed tool exists;
  coarse blocks only for genuinely tool-less multi-step glue.
- **Credentials/auth as a first-class concern.** Auth setup repeatedly broke runs.
  Studio should treat credentials as out-of-band (Secrets / native CLI login),
  never something a workflow step formats or an LLM touches.

### Suggested first move for the fresh start
Pick ONE vertical: "describe a step → it becomes a typed MCP/tool node, wired to
upstream by typed port, validated against the tool's real schema, dry-run against
real data." If that single loop is rock-solid, it becomes the authoring primitive
and most of the whole-graph repair stack can retire.
