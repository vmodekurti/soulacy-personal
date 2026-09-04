# Studio — Session Handoff

> Goal of Studio: **let anyone create an agent visually.** Everything below serves that north star.
> This session hardened Studio's reliability (generation → validation → repair) and surfaced the real
> next step: **simplify the authoring model** (typed ports, coarse blocks, code-encapsulated complexity,
> logging) so the brittleness goes away at the source instead of being patched.

---

## 0. TL;DR for the next session

- **Branch:** `feature/studio-soul-yaml-editor` (3 commits ahead of `origin/main`, **+ ~13 uncommitted files** — commit them first, see §3).
- **What works now:** Canvas⇄SOUL.yaml editor, deterministic validation, generation **auto-repair** (deterministic + 1 LLM pass), editable **rulebook** injected into generate/fix, **AI review**, **Fix with AI**, dual-pane prompt editor, raw-prompt persistence.
- **The real problem (agreed):** the fixed-graph **workflow** model wires blocks with brittle Go-template strings (`{{ .notebook.id }}`). Even strong models (gemini-2.5-pro, mistral-large) fumble it. We've been patching symptoms.
- **Agreed next direction:** redesign authoring around **typed input/output ports (no templates), fewer coarse blocks, complexity in one Python module, log everything, visual-first.** Start with **Phase 1: typed ports + per-block run logging.** (See §7.)
- **Build gotcha that wasted hours:** `make build` reuses a stale cache and a stale process keeps the old port. Always: `go clean -cache && make gui && make build && pkill -9 -f soulacy && ./scripts/run-runtime.sh`. (See §1.)

---

## 1. Environment, build & run (read this first)

- **Repo:** `~/Documents/Documents - Srinivas's MacBook Pro - 1/Vasu/Personal/Development/agentic/soulacy`
  (note: the folder lives in **iCloud Drive** — that produced `…2.go` conflict-copy files and is a likely cause of build flakiness; consider cloning to `~/dev/soulacy` outside iCloud).
- **Run the gateway:** `./scripts/run-runtime.sh` (foreground; runs `bin/soulacy` from a clean workspace at `~/.soulacy/soulspace`). It does **not** rebuild.
- **GUI is embedded** into `bin/soulacy` at build time from `internal/webui/dist`. `make gui` builds the Svelte GUI into `dist`; `make build` compiles the Go binary embedding it.

### The reliable rebuild (use this every time)
```bash
cd ~/Documents/Documents*MacBook*1/Vasu/Personal/Development/agentic/soulacy
go clean -cache && make gui && make build
pkill -9 -f soulacy          # kill any stale gateway holding :18789
./scripts/run-runtime.sh
# then hard-refresh the browser: Cmd+Shift+R
```

### Build/deploy gotchas we hit (so you don't re-debug them)
1. **Stale Go build cache** — `make build` silently reused a cached `gateway`/`studio` package object, so new routes/code never made it into the binary even though it relinked. Fix: `go clean -cache` before `make build`.
2. **Stale process** — killing/restarting didn't help when an old gateway process was still bound to `:18789`. Confirm with `lsof -nP -iTCP:18789 -sTCP:LISTEN`, then `pkill -9 -f soulacy`.
3. **GUI vs binary mismatch** — a fresh `make gui` updates the served GUI, but new **routes** need `make build` + restart. If a new endpoint 404s but the new buttons appear, you rebuilt the GUI but not the binary.
4. **`.git/HEAD.lock`** — committing from a sandbox left a stale lock; if git says "another process," `rm -f .git/HEAD.lock`.
5. **Verify a route is live:** `curl -s -o /dev/null -w '%{http_code}\n' -X POST http://localhost:18789/api/v1/studio/fix-yaml` → `400/401` = registered, `404` = old binary.

---

## 2. What was built this session (feature inventory)

All in the `studio` area. Endpoints are under `/api/v1/studio/...`.

| Feature | Where | Notes |
|---|---|---|
| **Canvas ⇄ SOUL.yaml toggle + rich highlighted editor** | `gui/src/lib/studio/YamlView.svelte`, `Studio.svelte` | Code view is authoritative on save. YamlView is a zero-dep highlighted editor (transparent textarea over highlighted `<pre>`; needs `min-height` or it collapses to 0 — that bug bit us). |
| **YAML convert/save endpoints** | `studio.go`: `/studio/yaml`, `/studio/from-yaml`, `/studio/save-yaml` | draft→YAML, YAML→draft (+lossiness warnings), authoritative YAML save. |
| **Deterministic validation in code view** | `/studio/validate-yaml` | syntax + `agentvalidate` + `studio.Validate` (graph) + `studio.Preflight` (runtime). |
| **Template-reference check + deterministic fixes** | `internal/studio/temprefs.go` | flags whole-object `{{ .x }}` and repeated `{{ .x.x }}`; `SuggestTemplateFixes`, `ApplyTemplateFixes` (auto-heal, idempotent). |
| **Editable rulebook** | `internal/studio/rules.go`, `/studio/rules` GET/PUT | `DefaultSOULRules` (Tier 1 generic + Tier 2 tool contracts). Stored at `~/.soulacy/soulspace/studio/soul-yaml-rules.md`. Edited via **📋 Rules** button in the Studio toolbar. **Injected into generate + fix.** |
| **Fix with AI** | `/studio/fix-yaml`, `BuildYAMLFixInstruction` | LLM rewrites YAML using rules; **output is deterministically re-healed** so a weak model can't reintroduce bugs. |
| **AI review (rules-grounded)** | `/studio/review-yaml`, `BuildYAMLReviewInstruction`, `ParseReviewFindings` | LLM checks YAML against the rulebook for judgment-call bugs (wrong field/id, broken poll logic, auth ordering). Merges into the validation panel as `source:"ai"`. |
| **Auto-repair at generation** | `studio.go`: `autoRepairWorkflow` + hook in `handleStudioCompile` | After generate: `RepairWiring` + `ApplyTemplateFixes` (free); if blockers remain, **one** LLM repair pass, then heal again. No manual steps for a fresh generation. |
| **Generation prompt hardening** | `internal/studio/compiler.go` `BuildPrompt` | added a template-ref rule + an end-of-prompt **FINAL CHECK** self-check; injects the rulebook. |
| **Light-refine** | `internal/studio/refineprompt.go` | re-refining an already-refined prompt does a light touch-up, not a full rewrite. `StudioRefined` flag persisted. |
| **Dual-pane prompt editor** | `Studio.svelte` (click the prompt box) | shows **original** + **refined** prompt; **Refine** (original→refined) and **Generate from refined** buttons. |
| **Raw-prompt persistence** | `StudioRawIntent` (types.go), `Draft.RawIntent`, save/load | original prompt persists across reload (`studio_raw_intent`). Captured at the refine boundary. |
| **Agents-page raw YAML editor** | `internal/gateway/api.go` `/agents/:id/yaml` GET/PUT, `Agents.svelte` modal | view/edit raw SOUL.yaml of a saved agent. |
| **Studio UX** | `Studio.svelte` | collapsible/resizable test + inspector panels; **+ New agent**; modal action buttons wrap (no overflow). |

---

## 3. Git state — commit before doing anything

- **Branch:** `feature/studio-soul-yaml-editor`
- **Committed (3 ahead of origin):** `26939f4` (editor/validation), `4d6343d` (0-height fix), `e70e166` (auto-fix + fewer false positives).
- **Uncommitted (~13 files):** the rulebook (`rules.go`), `fixyaml.go`, auto-repair, AI review, prompt editor, raw-prompt persistence, etc.

```bash
rm -f .git/HEAD.lock
git add internal pkg gui/src/lib/api.js gui/src/lib/studio/studioApi.js gui/src/pages/Studio.svelte gui/src/pages/Agents.svelte docs/STUDIO_HANDOFF.md
git commit -m "feat(studio): rulebook + AI review + auto-repair-at-generation + prompt editor + raw-prompt persistence"
git push -u origin feature/studio-soul-yaml-editor   # branch protection: open a PR; golangci-lint must pass
```
Run `go test ./internal/studio/...` — new tests cover `ApplyTemplateFixes`, `SuggestTemplateFixes`, the light-refine path, and review parsing.

---

## 4. Reliability architecture as it stands (the layers)

```
GENERATE  → rules + FINAL-CHECK in prompt
          → deterministic heal (RepairWiring + ApplyTemplateFixes)   [free]
          → if blockers: ONE LLM repair pass → heal again            [auto, 1 call]
VALIDATE  → deterministic (syntax + definition + graph + runtime)    [free]
          → 🔍 AI review (rules-grounded, semantic)                  [on demand]
FIX       → Quick-fix (deterministic) / ✨ Fix with AI (rules + heal) [on demand]
```
The rulebook is the single source of truth shared by all three phases.

---

## 5. Known issues / pain points (the "why we're redesigning")

- **The fixed-graph workflow model is the root cause.** Blocks are wired with Go-template strings (`{{ .notebook.id }}`). The LLM (even gemini/mistral) routinely produces: dangling refs (`{{ .id }}` with no producer), whole-object refs, wrong nested paths, unsupported template funcs (`{{ now }}`), malformed edge predicates (`"\\"`), orphan nodes, and "poll until done" loops missing `max_iterations`.
- **Repair is symptom-treatment.** Deterministic auto-heal fixes whole-object + name-matched dangling refs; the auto LLM pass handles some of the rest — but it's not guaranteed-convergent and costs tokens.
- **Model is a big variable.** glm-5.2 produced structurally messy graphs; gemini/mistral better but still hit the systemic template bugs. `ollama_cloud` returned HTTP 400 at one point (provider/model config — check the model name + the gateway logs).
- **Build flakiness** (cache/process/iCloud) repeatedly masked working code — see §1.

---

## 6. The agreed next direction — simplify the authoring model

The brittleness lives in **fine-grained, string-templated handoffs**. Fix the cause:

1. **Typed ports, not templates.** Each block declares typed `inputs`/`outputs`; connect output→input with a **wire**. Runtime passes data by port name. Nobody writes `{{ }}`. *(Framework already has `FlowPort` + `from_port/to_port` on edges — make ports the primary/only path; have the LLM emit port connections, not template strings.)* **Deletes the entire template-ref bug class.**
2. **Fewer, coarser blocks.** 3–4 meaningful blocks instead of 13 micro-steps. The multi-step "dance" (e.g. NotebookLM create→add→generate→poll) lives **inside one block**, tested once.
3. **Complexity in one Python/code module.** Polling, retries, error handling belong in **code**, not a `max_iterations` back-edge.
4. **Log everything.** Per-block input/output/duration/error rendered as a run trace on the canvas, so a non-technical user can debug visually. *(Extends the existing Test/Preview-run + failed-runs machinery.)*
5. **Visual-first, code-optional.** Forms + drag-to-connect for everyone; SOUL.yaml/code view becomes the advanced surface. Validation/rules/AI-repair become a **backstop**, not the daily interaction.

### Suggested phasing
- **Phase 1 (recommended start):** generation emits **typed port connections** instead of template strings + **per-block run logging** in the trace. Removes the template-ref class and makes failures legible.
- **Phase 2:** a starter library of **coarse composite blocks** — prototype **one** ("NotebookLM Podcast": URLs in → audio URL out) as a tested tool/skill.
- **Phase 3:** visual port-wiring UX + forms-first config.

---

## 7. How to kick the tires (fast)

1. **Build & run** (§1), hard-refresh.
2. Studio → type a prompt (e.g. *"Every weekday 7am, gather AI news, make a NotebookLM podcast, post the link to Telegram"*) → **Generate**. Watch "Still needs configuration" — auto-repair should make it clean or near-clean **with no clicking**.
3. Switch to **`</> SOUL.yaml`** → **✓ Validate** (deterministic) → **🔍 AI review** (semantic, rules-grounded) → **✨ Fix with AI** if needed.
4. **📋 Rules** (toolbar) → read/edit the rulebook; add a Tier 2 tool contract, Save, regenerate → confirm it's honored.
5. Click the **prompt box** → dual-pane editor (original + refined) → **Refine** / **Generate from refined** / **OK**. Save, reopen from **My Workflows** → original prompt should persist (`studio_raw_intent`).
6. Compare a generation on **gemini-2.5-pro** vs a weaker model to see how much is model vs pipeline.

### Useful key references
- Endpoints: `/studio/yaml`, `/studio/from-yaml`, `/studio/save-yaml`, `/studio/validate-yaml`, `/studio/fix-yaml`, `/studio/review-yaml`, `/studio/rules` (GET/PUT), `/studio/compile`, `/studio/compile-agent`, `/agents/:id/yaml` (GET/PUT). Registered in `internal/gateway/server.go`.
- Generation + repair: `internal/gateway/studio.go` (`handleStudioCompile`, `autoRepairWorkflow`).
- Deterministic repair: `internal/studio/autowire.go` (`RepairWiring`, `ReconcileVars`), `internal/studio/temprefs.go` (`ApplyTemplateFixes`, `SuggestTemplateFixes`).
- Prompt builders: `internal/studio/compiler.go` (`BuildPrompt`), `internal/studio/fixyaml.go` (`BuildYAMLFixInstruction`, `BuildYAMLReviewInstruction`), `internal/studio/rules.go` (`DefaultSOULRules`, `RulesPromptBlock`).
- Flow types: `sdk/reasoning` (`FlowNode`, `FlowEdge`, `FlowPort`) — **ports live here; central to the redesign.**
- GUI: `gui/src/pages/Studio.svelte`, `gui/src/lib/studio/{YamlView.svelte,studioApi.js}`, `gui/src/lib/api.js`.

---

## 8. First task for the fresh session (suggested)

> Implement **Phase 1**: make the Studio compiler emit **typed port connections** (using `sdk/reasoning` `FlowPort` + edge `from_port/to_port`) instead of `{{ }}` template strings for block handoffs, and add **per-block run logging** to the run trace. Verify the NotebookLM agent generates with wired ports and a readable run trace, and that the template-ref bug class no longer appears. Keep the existing repair/validation stack as a backstop.

---

## 9. Phase 1 — DONE (typed-port handoffs + per-block run trace)

**Decision:** runtime-native port resolution (handoffs resolved by the runtime, no template syntax) + live-run trace (not just dry-run). Existing validation/repair/temprefs kept as backstop.

### What changed
- **Typed ports now alter execution.** `sdk/reasoning.FlowPort` gained an optional `Field` (output: which result field/path the port exposes; input: arg-key override). `internal/reasoning/flow.go` `RunFlow` now, before running a node, calls `resolvePortInputs`: for every incoming edge with a `to_port`, it reads the producer's stored output, extracts the `from_port` field, and binds it under the consumer's input-port key — assembling the node input with **no Go template**. Wires overlay onto the node's static `Input` JSON (constants stay in `Input`, dynamic handoffs are wires). **Back-compat:** a node with no wired `to_port` behaves exactly as before (template / all-vars path). Resolution rule: empty `from_port` = whole output; a port name matching a result field = that field; a generic port name with no matching field = whole output; explicit `Field` = strict dotted path.
- **Per-block run trace.** `FlowHooks` gained `Observe(FlowNodeRun)` (input/output/duration/error/wiredPorts per visit). `internal/runtime/flow.go` wires it to (a) a new in-memory bounded **`flowTraceStore`** on the `Engine` (`internal/runtime/flowtrace.go`: `RecordFlowNode`/`FlowTrace`/`LatestFlowTrace`) and (b) a streamed `flow.node` event. Dry-run `studio.TestRun` builds its trace from the same hook, so dry and live traces share one shape (`TraceEntry` gained `durationMs`/`error`/`wiredPorts`).
- **Endpoint:** `GET /api/v1/studio/run-trace?agentId=…&runId=…` (`handleStudioRunTrace`, registered in `server.go`). Returns the per-block trace; empty (not error) when nothing retained.
- **GUI:** `Studio.svelte` has a **"Run trace (live)"** panel (fetches the loaded agent's latest real run; `loadedAgentId` is set on save / load-onto-canvas) and the dry-run trace now shows `⮑ wired` / duration / `✕ error` badges. API: `api.studio.runTrace` + `bridge.runTrace`.
- **Compiler:** `BuildPrompt` now teaches **TYPED PORT WIRES** as the preferred tool/python handoff (with a NotebookLM create→add example) and a FINAL-CHECK item; static args stay in `input`, dynamic handoffs are wires.
- **Backstop unchanged:** `RepairWiring`/`ReconcileVars`/`temprefs`/`reconcilePorts` still run. (Note: an automatic *template→port* converter was deliberately deferred — adding data edges could perturb control flow; revisit in Phase 2.)

### New/changed files
- `sdk/reasoning/flow.go` (FlowPort.Field), `internal/reasoning/flow.go` (+`FlowNodeRun`, `Observe`, `resolvePortInputs`, `extractNamedField`/`extractField`), `internal/reasoning/flow_test.go` (new `TestRunFlow_TypedPortHandoff`, split params regression).
- `internal/runtime/flowtrace.go` (new), `internal/runtime/flow.go` (Observe wiring), `internal/runtime/engine.go` (+2 fields), `internal/runtime/flow_test.go` (`TestWorkflowExecutor_TypedPortHandoffAndTrace`).
- `internal/gateway/studio.go` (`handleStudioRunTrace`), `internal/gateway/server.go` (route).
- `internal/studio/compiler.go` (prompt), `internal/studio/testrun.go` (trace via Observe), `internal/studio/zz_pp_test.go` (new prompt test — see note).
- `gui/src/lib/api.js`, `gui/src/lib/studio/studioApi.js`, `gui/src/pages/Studio.svelte`.

### Tests
All green: `go test ./internal/reasoning/... ./internal/studio/... ./internal/runtime/... ./internal/gateway/...` and (from `sdk/`) `go test ./reasoning/...`. `go build ./...` and `npm run build` (GUI) clean.

### Still to verify LIVE (couldn't run a real LLM/gateway here)
Build & run (§1), generate the NotebookLM podcast agent, confirm the draft uses `from_port`/`to_port` for the notebook-id handoff (no `{{ .notebook.id }}`), enable it, let it run once, then open **Run trace (live)** (or `GET /studio/run-trace?agentId=…`) and confirm a per-block trace with `wiredPorts:true` on the handoff and legible errors. The runtime + compile paths are covered by the new unit/e2e tests; only the live model generation is unverified.

### Two housekeeping notes
1. **`internal/studio/temprefs.go`** — I aligned two warning message strings ("…**nested object**…", "…the **whole** … object…") so the prior session's already-committed-but-failing `temprefs_test.go` cases pass. These were failing on the working tree **before** my changes (message wording drift), unrelated to ports; the wording is equivalent/clearer. Re-check if you intended different copy.
2. **`internal/studio/zz_pp_test.go`** — a valid, useful prompt test, but the filename is non-ideal. It was created in-sandbox and the **iCloud mount refused `unlink`** (the same iCloud issue noted in §1), so I couldn't rename it. On the host, feel free to `git mv internal/studio/zz_pp_test.go internal/studio/prompt_ports_test.go`.

---

## 10. Phase 2 (started) — coarse composite blocks: the NotebookLM Podcast block

**Decision:** kill the brittle 4-node template "dance" at its source by shipping a
COARSE composite block — ONE node that encapsulates the whole create → add →
generate → poll sequence behind a single typed `urls`+`title` → `audio_url`
contract. The multi-step logic, the notebook-id handoff, polling, retries, and
graceful failure all live in code, written and **tested once**, instead of being
relearned by the model and re-wired with `{{ }}` templates every generation. This
is the §6 direction made concrete and is the template for a future block library.

### What changed
- **New `internal/studio/composite.go`.** `CompositeBlock` type + `CompositeBlocks()`
  catalog (ships ONE block: `notebooklm_podcast`). A block declares its public
  typed ports (`urls string[]`, `title string` → `audio_url string`, the output
  port carrying `Field:"audio_url"` for template-free downstream wiring) and an
  inline Python `def run(inputs)` body that performs the entire dance by shelling
  to a NotebookLM CLI (`nlm`, overridable via `$NLM_BIN`). `MaterializeNode()`
  turns the block into a single `kind=python` `sdkr.FlowNode` with the ports, the
  code, and **classifier-derived `Requires`** (it shells out → `system` → the
  existing save/consent gate fires automatically). Helpers: `CompositeBlockByID`,
  `MatchCompositeBlocks`, `writeCompositeBlockGrounding`.
- **Compiler grounding.** `BuildPrompt` now calls `writeCompositeBlockGrounding`
  (next to the pattern grounding): for a matching intent it instructs the model to
  emit ONE coarse python block (naming the node id + port contract) and explicitly
  **not** to decompose it into create/add/poll/generate tool nodes glued with
  templates. No-op for unrelated intents.
- **Endpoint.** `GET /api/v1/studio/composite-blocks` (`handleStudioCompositeBlocks`
  in `internal/gateway/studio.go`, registered in `server.go`) returns each block's
  id/name/summary/requirements/ports + the drop-ready materialised node, for the
  palette/canvas to consume.
- **The CLI contract** the block assumes (kept small + JSON-first so it's mockable):
  `nlm create-notebook --title <t> --json` → `{"id":...}`; `nlm add-source
  --notebook <id> --url <u>`; `nlm generate-audio --notebook <id> --json`; `nlm
  audio-status --notebook <id> --json` → `{"status":"ready","audio_url":...}`.

### Tests (all green here)
`internal/studio/composite_test.go`: catalog/lookup, port contract, materialised
node (kind, ports, `system` in Requires, passes `CompileFlow`), beyond-guardrail
classification, grounding presence/absence + `BuildPrompt` wiring, **plus a live
end-to-end run of the real embedded Python against a fake `nlm` CLI** (pending →
ready) proving the dance returns the `audio_url` and a graceful-error path on
empty input. The Python test `t.Skip`s when no `python3` is on PATH.
Verified: `go build ./...`, `go test ./internal/studio/... ./internal/reasoning/...
./internal/runtime/... ./internal/gateway/...`, `go vet`, `gofmt` clean on the new
files. (Note: building the cgo/sqlite packages needs a `sqlite3.h` on
`CGO_CFLAGS`; the `mattn/go-sqlite3` module bundles one as `sqlite3-binding.h`.)

### Not done / next
- **GUI palette + drop-onto-canvas** for composite blocks (Phase 3 "visual-first"):
  add a palette group fed by `GET /studio/composite-blocks` and a drop handler that
  inserts `node` onto the canvas. Backend is ready; this is GUI-only.
- **Live generation check:** confirm a real LLM, given a NotebookLM-podcast intent,
  now emits the single coarse block (grounding steers it) instead of the old dance.
- **Auto-substitution (optional):** a compile-time pass that, if the model still
  emits the fine-grained notebooklm dance, swaps it for the materialised composite
  node. Deliberately NOT done — grounding-only is lower risk; revisit if the model
  keeps decomposing.
- The older `notebooklm_podcast` **Pattern** in `patterns.go` (the 4-node template
  guidance) is intentionally left in place for when a NotebookLM MCP with discrete
  tools is connected; the composite block is the preferred coarse path. Decide
  whether to retire the pattern once the block proves out live.
