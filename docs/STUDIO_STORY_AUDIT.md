# Soulacy Studio — Story Implementation Audit

Audited against the codebase on 2026-07-25. Verdicts are per acceptance criterion, based on
whether the logic exists **and** is wired to a gateway route **and** is surfaced in the GUI.

Legend: ✅ implemented · 🟡 partial · ❌ missing

---

## Summary

| Story | Title | P | Status | AC met |
|---|---|---|---|---|
| ST-01 | Intent To Build Spec | P0 | 🟡 Partial | 1 / 4 |
| ST-02 | Deterministic Strategy Advisor | P0 | 🟡 Mostly done | 4 / 5 |
| ST-03 | Plain-Language Plan View | P0 | 🟡 Partial | 2 / 5 |
| ST-04 | Streamed Generation Progress | P1 | 🟡 Partial | 1 / 5 |
| ST-05 | Typed Workflow Compilation | P0 | 🟡 Weak | 0 / 5 |
| ST-06 | Parallel Execution Design | P0 | ❌ Not built | 0 / 5 |
| ST-07 | Unified Readiness Check | P0 | 🟡 Partial | 0 / 5 |
| ST-08 | Inline Dependency Setup | P0 | 🟡 Partial | 1 / 5 |
| ST-09 | Rules And Builder Model Advisor | P1 | 🟡 Weak | 1 / 5 |
| ST-10 | Integrated Test Lab | P0 | 🟡 Partial | 7 / 15 |
| ST-11 | Safe Live Run | P0 | 🟡 Weak | 0 / 5 |
| ST-12 | Build Until It Works | P0 | 🟡 Mostly done | 6 / 12 |
| ST-13 | Actionable Failure Diagnosis | P0 | 🟢 Mostly done | 7 / 10 |
| ST-14 | Verified Self-Heal | P0 | 🟢 Mostly done | 5 / 11 |
| ST-15 | Reusable Workflow Library | P1 | 🟡 Partial | 5 / 14 |
| ST-16 | Save And Deployment Gate | P0 | 🟡 Weak | 2 / 9 |

**GTM path status.** Of the 13 critical stories (`01,02,03,05,06,07,08,10,11,12,13,14,16`),
two are substantially delivered (ST-13, ST-14), one is close (ST-02), and ten have material
gaps. ST-06 and ST-11 are the two that are effectively unbuilt.

---

## The dominant pattern: shipped Go, unshipped product

Five well-tested, well-designed Go packages have **zero non-test callers, no gateway route
and no GUI**. They implement roughly a third of the missing acceptance criteria.

| Dead module | Story coverage it would unlock |
|---|---|
| `internal/studio/buildspec.go` — `ExtractBuildSpec`, `Blockers()`, `deriveQuestions`, `deriveSecurity`, `DiffSpecs`, `MateriallyDifferent` | ST-01 AC1, AC2, AC3 |
| `internal/studio/planview.go` — `BuildPlanView`, `parallelStage`, `describeJoin`, `describeRetry`, `describeCompletion`, `planWarnings` | ST-03 AC2, AC3; ST-06 AC2 |
| `internal/studio/certify.go` — `Certify`, `CertificationRecord`, `RealRunEvidence`, `BlocksScheduling()` | ST-16 AC3, AC7, AC9; ST-07 secrets coverage |
| `internal/studio/repairtxn.go` — `ApplyRepairTransactionally`, `ClassifyFailure`, `AdviseRepair`, `IsRetryable`, `RepairRollback` | ST-14 AC5; ST-13 AC5 |
| `internal/studio/promote.go` — `BuildPromotedTool` | ST-15 reuse |

This is the cheapest available progress on the board: the hard part is written and tested,
the missing part is a handler plus a component.

---

## Epic 1 — Guided Design

### ST-01 Intent To Build Spec — 🟡

| AC | Verdict | Notes |
|---|---|---|
| Identifies trigger/schedule/inputs/stages/outputs/delivery/integrations/security | 🟡 | Shipped path is the LLM `BuildRefinePromptInstruction` (`refineprompt.go:75`) with 5 free-text sections. `integrations` and `security` never reach the client. The structured `ExtractBuildSpec` is dead code. |
| Missing details → questions or blockers | 🟡 | LLM `questions[]` render in the refine modal (`Studio.svelte:5750`) and feed compile, but never block. Generate is enabled whenever `refined_intent` is non-empty (`:5773`). |
| Refine → materially different spec + change summary | 🟡 | `refinement.summary` is a description of the automation, not a diff. `DiffSpecs` / `MateriallyDifferent` unwired. Nothing verifies material difference. |
| Original prompt available and editable | ✅ | Dual-pane editor `Studio.svelte:5501-5515`, persisted as `workflow.raw_intent`. |

### ST-02 Deterministic Strategy Advisor — 🟡 (closest to done)

| AC | Verdict | Notes |
|---|---|---|
| Considers determinism, branching, side effects, model capability, tool reliability | ✅ | `AdviseStrategy` (`strategy_advisor.go:32-155`) + `LookupCapabilities`. |
| Fixed schedules → Workflow | ✅ | `strategy_advisor.go:72-83`. |
| Dynamic conversational → Auto when native tool calling | ✅ | `:129-136`; unprofiled models fall back to Workflow, not Auto. |
| UI explains decision, informed override | 🟡 | `CapabilityWarning`, `Confidence`, `Capabilities` are computed then dropped — `compiler.go:154` flattens advice to `{Mode, Rationale}`. Forcing ReAct on a weak model produces no warning. The mode banner only renders when mode ≠ workflow. |
| ReAct advanced, never auto-selected | ✅ | Only reachable via `explicitReActRequested`. |

### ST-03 Plain-Language Plan View — 🟡

| AC | Verdict | Notes |
|---|---|---|
| Trigger / Work Plan / Delivery separation | ✅ | Client-side six-lane view (`planlanes.js`, `PlanView.svelte`). Different shape than the Go model, but satisfies intent. |
| Parallel stages and fan-in distinguishable | ❌ | No parallel or fan-in affordance anywhere in the GUI. Backend support exists only in dead `planview.go`. |
| Stage shows input, output, retry, completion condition | 🟡 | Only Type / Produces / Input. Retry and completion live behind the canvas Inspector — the advanced view the story is trying to avoid. |
| Editing a plan stage updates compiled workflow | ✅ | `ConfigCard` → `updateNodeConfig` → revalidate. |
| Plan / Canvas / SOUL.yaml without losing changes | 🟡 | **Bug:** `showPlanView` (`Studio.svelte:1610`) lacks the `fromYaml` reparse that `showCanvasView` has, so Code → Plan → Code silently discards YAML edits. Plan tab is also gated to `currentMode === 'workflow'`. |

---

## Epic 2 — Generation

### ST-04 Streamed Generation Progress — 🟡

| AC | Verdict | Notes |
|---|---|---|
| Understand / Select Pattern / Build Graph / Validate Contracts stages | ✅ | 5 phases in `generatepipeline.go:25-30`, SSE via `POST /studio/generate/stream`. "Select Pattern" is computed but not rendered. |
| Nodes appear incrementally | ❌ | `build_graph` is atomic — one start, one complete with aggregate counts. Canvas populates once at the end. |
| Elapsed time, current action, progress, cancellation | 🟡 | Current action only. `PipelineEvent` has no timestamp; `streamSSE` has no `AbortController`; server uses `context.WithoutCancel` so disconnect can't stop the run. The *build* stream already has `elapsed_ms` — copy that pattern. |
| Failures identify stage, preserve partial draft | 🟡 | Stage yes. GUI discards the partial draft on error (`Studio.svelte:3311`). |
| Transcript distinguishes planner from LLM | 🟡 | Backend emits `deterministic_plan` in payload; GUI never reads payload — rows are visually identical. |

### ST-05 Typed Workflow Compilation — 🟡

| AC | Verdict | Notes |
|---|---|---|
| Every node declares typed I/O, required, cardinality | 🟡 | `FlowPort` schema is complete (`sdk/reasoning/flow.go:42-95`) but ports are **optional by design**; nothing forces the generator to emit them; `describePortSet` renders `(untyped)`. |
| Missing node/port/field references fail | 🟡 | Nodes and ports hard-fail (`internal/reasoning/flow.go:139-153`). Fields do not — `resolvePortInputs` is "forgiving by design" and falls back to the whole upstream value; template refs are warn-only. |
| Parallel outputs must pass an aggregate/join stage | ❌ | No such check, no `aggregate`/`join` node kind. Nearest is a cardinality check that only fires when both ends declare cardinality and is bypassed by `Adapter:true`. |
| Templates evaluated against representative test data | 🟡 | Parsed in `Preflight`, but never *rendered with data* on the generate path. Evaluation lives behind the separate Test / Build-until-works buttons. |
| Invalid workflows repaired or rejected before "success" | ❌ | **Concrete bug:** the streamed path never assigns `Result.Contract`, and the GUI blocker gate keys off exactly that field (`Studio.svelte:1240`). A blocked draft lands on the canvas as a clean success. One-field fix. |

### ST-06 Parallel Execution Design — ❌

| AC | Verdict | Notes |
|---|---|---|
| Planner detects independent stages, creates parallel group | ❌ | `SpecStage.Parallel` is written by heuristic and never read. The only real fan-out is hard-coded in the NotebookLM podcast macro-pattern (`deterministic_workflow.go:236`). |
| Join policy: all / any / quorum / best effort | ❌ | All four constants exist — in dead `planview.go`, inferred read-only from `on_error`, not authorable, not persisted, not executed. |
| Traces show concurrent start/completion times | 🟡 | `FlowNodeRun{VisitKey, StartedAt}` is captured and persisted; the GUI renders duration only and never reads `startedAt` or `visitKey`. |
| Failed branch follows retry and partial-success policy | 🟡 | Only within a `for_each` node (`on_error:"skip"` → null placeholder). Retry is a single un-configurable attempt. |
| Aggregated output includes every successful branch | 🟡 | True for `for_each`. **Not possible across nodes:** `RunFlow` is a single-pointer walker that follows exactly one outgoing edge (`internal/reasoning/flow.go:598-637`). |

> ⚠️ **Correctness hazard, not just a gap.** A graph that *looks* parallel on the canvas —
> two nodes off one producer — silently executes only the first matching edge.

---

## Epic 3 — Readiness

### ST-07 Unified Readiness Check — 🟡

| AC | Verdict | Notes |
|---|---|---|
| Covers providers, models, secrets, MCP, channels, schedule, permissions, consent | 🟡 | MCP, channels, schedule, permissions, consent covered. **Providers and models absent.** **Secrets collected and never read** — `PreflightInput.SecretsSet` has no consumer outside dead `certify.go`. Readiness can be green with every API key missing. |
| Ready / Warning / Blocker classification | 🟡 | Backend emits all three; GUI filters `pass` out entirely. |
| Each blocker has a direct configuration action | 🟡 | Prose `Fix` text only. Machine-readable deep links (`open_providers`, `open_mcp`) exist in dead `certify.go:42`. |
| Run Live disabled while blockers remain | ❌ | Neither client nor server gates `/studio/try-agent`. Only Save is gated. |
| Preflight re-runs after generation, import, repair, config change | 🟡 | Generation and repair yes (partially). Import / template / draft-load **clears** preflight without re-running. No environment-change trigger at all. |

Also: readiness is stitched together **client-side** from three endpoints (`/studio/compile`,
`/studio/security_review`, `/studio/plan`), and a security-review network failure degrades
silently while `ok` still computes true.

### ST-08 Inline Dependency Setup — 🟡

| AC | Verdict | Notes |
|---|---|---|
| Secrets / providers / MCP test / delivery bots / consent inline | 🟡 | Secrets ✅ and scoped consent ✅ are inline. Provider selection is builder-model only. MCP testing lives on a different page. Delivery picks a channel id, not a bot/destination. |
| Secrets masked, never returned to browser | ✅ | `handleListSecrets` returns `{name, category, env_var, description, set}` only. |
| Connection tests show actionable errors | 🟡 | Rich errors exist in `Providers.svelte` / `MCP.svelte`, unreachable from Studio readiness. |
| Setup updates readiness immediately | 🟡 | `setSecretVal` refreshes only the secret list — and since preflight never reads secrets, readiness couldn't change anyway. |
| Never silently broadens permissions | 🟡 | Strong on the save path. **Run Live silently escalates:** `ToAgentDefinition(draft, true)` + `def.Unattended = true` (`gateway/studio.go:1047,1054`). The regression guard `detectPrivilegedRegression` is wired at exactly one call site. |

### ST-09 Rules And Builder Model Advisor — 🟡

| AC | Verdict | Notes |
|---|---|---|
| Rules cover security defaults, limits, retries, parallelism, contracts, validation | 🟡 | Limits and typed contracts yes. **No retry rule, no parallelism rule, no security-defaults rule.** |
| Model cards show capabilities | 🟡 | `capability.go:35-53` defines exactly the five fields — no endpoint, no component renders them. `RecordProbe` has no callers, so unknown models stay permanently unknown. |
| Warn when model can't do prompt refinement | ❌ | `handleStudioRefinePrompt` performs no fitness check; bad model output silently falls back to local deterministic refinement. |
| Strategy deterministic regardless of builder model | ✅ | Rule-based advisor runs before compile; the LLM never emits graph JSON. Caveat: capability deliberately biases mode, so different builder models can yield different architectures for identical intent. |
| Rule changes versioned and auditable | ❌ | Bare `os.WriteFile` over one file, no history, no `recordAdminAudit`. The versioning machinery (`rulediff.js`) exists one subsystem over, used by Memory. |

---

## Epic 4 — Testing

### ST-10 Integrated Test Lab — 🟡 (7 / 15)

✅ Per-node mocks · assertions on node outputs and final result · duration · input · errors ·
full-workflow test · `POST /studio/test`

🟡 Input payload is a single free-text string · no explicit node status field (derived
client-side) · output is raw, not normalized · assertions have a backend home
(`Draft.Outcome.Assertions`) that the GUI never writes to

❌ Variables · environment values · retry counts in trace · start-from-selected-node ·
fixtures saved with workflow

> **Highest-leverage fix in this epic:** `Studio.svelte` never reads or writes
> `workflow.outcome`. Bench mocks and assertions are component-local state, lost on every
> reload. Wiring `collectAssertions()` → `workflow.outcome.assertions` also activates
> `AssessAssertions` and runtime contract re-evaluation.

### ST-11 Safe Live Run — ❌ weakest P0

| AC | Verdict | Notes |
|---|---|---|
| Preview tools/destinations/data/consent before execution | 🟡 | The security panel exists but is a *save-time* panel; nothing gates `tryAgent()`. |
| Side-effecting tools require confirmation | ❌ | Explicitly bypassed: `def.Unattended = true // auto-approve confirmations so the throwaway run can't hang`. Real tools fire. |
| Live trace without truncated JSON-only rows | ❌ | `/studio/try-agent` is a blocking, non-streaming POST whose trace strings pass through `truncate()` — precisely what the story prohibits. SSE machinery already exists for `/studio/build/stream`. |
| Expandable summaries + raw JSON | 🟡 | True for the test bench and Activity; not for the live run rows. |
| Cancel stops pending work, reports completed actions | ❌ | `context.WithoutCancel` + fixed 120s timeout; no cancel control anywhere. |

---

## Epic 5 — Diagnosis And Repair (strongest epic)

### ST-12 Build Until It Works — 🟡 (6 / 12)

✅ Per-attempt validation, diagnosis, repair · UI shows attempt / issue / applied change /
verification (`BuildInspector.svelte` + `/studio/build-trace`) · max attempts enforced ·
success requires blockers resolved + assertions passing

❌ Elapsed-time budget · token budget · side-effect policy field

⚠️ **Polarity inverted on sandboxing.** `gateway/studio.go:730` defaults `verify=true` →
`RealRunVerifier` → real `engine.RunTool` / `RunInlinePython`, and the GUI hardcodes the flag
on. `DryRunVerifier` — documented in-code as "the safe default" — is unreachable from the UI.
The story requires production side effects mocked *unless explicitly enabled*.

### ST-13 Actionable Failure Diagnosis — 🟢 (7 / 10)

✅ Root cause · affected node · evidence · recommended action · link to affected node
(`revealNode`) · original trace (`/studio/run-trace`) · raw error

🟡 Classification exists in **two disconnected taxonomies**, neither matching the spec:
`classifyFlowError` returns prose, not an enum; the richer `RepairClass` set
(`auth`/`permission`/`network`/`rate_limit`/`assertion`) is never called outside its package.
No `graph` / `contract` / `configuration` / `delivery` class in either.

❌ Links to config screens · repeated-failure grouping (flat per-DLQ list)

### ST-14 Verified Self-Heal — 🟢 (5 / 11)

✅ LLM repair strictly limited to `(field, value)` structured proposals with redacted samples ·
before/after diff inline **and** full SOUL.yaml diff via `/studio/diff` · repaired workflow
validated (`NormalizeAndCheck`) · "Fix automatically" reports all three outcomes correctly

🟡 Deterministic-first ordering is correct, but only *template* repair is deterministic —
contract and routing have no live-repair class; consent is correctly *refused* rather than
repaired; timeout is folded into retryable network

❌ **Repaired workflow is never rerun in a sandbox** — the GUI just says "Run Live again to
confirm." `ApplyRepairTransactionally` (validate + replay + re-check contract + rollback) is
dead code.

❌ Unattended half unimplemented: `RepairProposal.Auto` is set and badged, but no code path
consumes it to auto-apply.

---

## Epic 6 — Workflow Lifecycle

### ST-15 Reusable Workflow Library — 🟡 (5 / 14)

✅ Template required capabilities · template readiness before install · Edit action ·
autosaved drafts with timestamps

🟡 Two sections (Saved agents / Drafts), not the three-way deployed / saved / drafts split ·
Export only for the currently open draft

❌ No search input at all — none of the four filter dimensions · no Clone · no Test ·
no Deploy from library · recovery is a single overwritten autosave slot, not history

### ST-16 Save And Deployment Gate — 🟡 (2 / 9)

✅ Save blocks structural errors and execution-contract errors — **server-authoritative**
(422 on `contract.Blockers > 0`, 409 on privileged-exposure consent). This part is solid.

❌ Everything downstream of save:
- Warning acceptance is a one-click bypass with no reason captured and no field to store one
- No deployment record — no workflow version, rules version, provider config, or test evidence
- No rollback (all `rollback` hits are unrelated: repair undo, memory rules, SQL)
- **Scheduler has no readiness gate.** `CertificationRecord.BlocksScheduling()` and
  `isScheduled(def)` were written for exactly this and are never consulted. The only
  protection is that saved agents start `Enabled=false`.

---

## Recommended sequencing

**Tier 1 — correctness and safety (these are hazards, not gaps)**

1. `RunFlow` executes one edge per step — parallel-looking graphs silently run one branch (ST-06)
2. Streamed generate never sets `Result.Contract`, so blocked drafts show as success (ST-05) — one-field fix
3. Run Live sets `Unattended = true` and accepts privileged exposure with no gate (ST-07, ST-08, ST-11)
4. Build-until-works defaults to real tool execution; `DryRunVerifier` unreachable (ST-12)
5. Scheduler starts workflows with no readiness or delivery check (ST-16)

**Tier 2 — wire the dead code (highest ratio of story coverage to effort)**

6. `buildspec.go` → handler + GUI ⇒ most of ST-01
7. `planview.go` → replace/augment `planlanes.js` ⇒ ST-03 AC2/AC3, ST-06 AC2
8. `certify.go` → save/deploy path + scheduler gate ⇒ ST-16 AC3/AC7/AC9
9. `ApplyRepairTransactionally` → apply-repair handler ⇒ ST-14 AC5
10. `capability.go` → endpoint + model cards ⇒ ST-09 AC2/AC3

**Tier 3 — surface what the backend already computes**

11. `CapabilityWarning` / `Confidence` through `Recommendation` (ST-02)
12. `startedAt` / `visitKey` in the trace UI (ST-06 AC3)
13. `workflow.outcome.assertions` read/write in the bench (ST-10)
14. SSE for `/studio/try-agent`, reusing the build-stream pattern (ST-11)
15. `AbortController` + elapsed timer on both streams (ST-04, ST-11, ST-12)

**Tier 4 — net-new**

16. Library search, filters, Clone/Test/Deploy, draft history (ST-15)
17. Rules versioning + audit (ST-09), warning-acceptance reason capture (ST-16)
