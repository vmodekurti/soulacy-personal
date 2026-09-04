# Studio Story Build — Session Handoff

Companion to `STUDIO_STORY_AUDIT.md`. Records what was built, what is verified, and
what remains. Written 2026-07-25.

> **Session ended on infrastructure failure, not completion.** The Linux sandbox ran out
> of disk and the shell became permanently unusable partway through Wave 2. Everything
> below is accurate about *state*, including the parts that are unverified. Read the
> "Verification status" table before trusting any section.

---

## First thing to do next session

```bash
# 1. The sandbox needs a restart (disk exhausted). Then re-create the toolchain:
#    Go is not preinstalled; the repo needs go1.25 + cgo headers for sqlite-vec.
cd /tmp && curl -sSL -o go.tgz https://go.dev/dl/go1.24.5.linux-arm64.tar.gz \
  && mkdir -p goroot && tar -C /tmp/goroot --strip-components=1 -xzf go.tgz
mkdir -p /tmp/sqinc && cp "$(go env GOMODCACHE)"/github.com/mattn/go-sqlite3@v1.14.22/sqlite3-binding.h /tmp/sqinc/sqlite3.h

cat > /tmp/goenv.sh <<'EOF'
export PATH=/tmp/goroot/bin:$PATH
export GOFLAGS=-mod=mod
export GOCACHE=/tmp/gocache
export GOMODCACHE=/tmp/gomodcache
export CGO_CFLAGS="-I/tmp/sqinc"
export CGO_ENABLED=1
cd /path/to/soulacy
EOF

# 2. Then run the sweep that was never completed:
source /tmp/goenv.sh && go build ./internal/... && go test ./internal/... -count=1
```

GUI tests need one additive, non-destructive install (the checked-in `node_modules` is
macOS-only, so rollup's linux binary is missing in the sandbox):
`cd gui && npm install --no-save --no-package-lock @rollup/rollup-linux-arm64-gnu && npm test`
Baseline was **34 files / 317 tests passing**.

---

## Verification status

| Wave | Work | Build | Tests | Confidence |
|---|---|---|---|---|
| 1 | Parallel execution engine (ST-06) | ✅ | ✅ incl. `-race -count=3` | **High** |
| 1 | Build-loop budgets + mocked default (ST-12) | ✅ | ✅ 14 new tests | **High** |
| 1 | Streamed contract gate (ST-05) | ✅ | ✅ | **High** |
| 1 | Live-run safety gate (ST-07/08/11) | ✅ | ✅ | **High** |
| 1 | Deployment records + scheduler gate (ST-16) | ✅ | ✅ | **High** |
| 2 | build-spec / plan-view / model-capabilities routes | ✅ | ⚠️ own tests only | Medium |
| 2 | Preflight secrets/providers/models + `Readiness()` | ✅ | ✅ `internal/studio` full | Medium-high |
| 2 | Rules versioning store | ✅ | ✅ | Medium-high |
| 2 | Transactional apply-repair | ✅ | ⚠️ partial | Medium |
| 2 | **Repair adoption fix (see below)** | ❌ | ❌ | **UNVERIFIED** |
| 3 | **GUI wave (see "Wave 3" below)** | ❌ | ❌ | **UNVERIFIED** |
| 3–4 | Streaming/cancel, diagnosis taxonomy, library, rules UI | — | — | **Not started** |

**Never run:** a full `go test ./internal/... -count=1` after Wave 2. Do this first.

---

## Wave 1 — complete and verified

### Parallel execution (ST-06) — the correctness hazard is closed
`RunFlow` was a single-pointer walker: a graph that looked parallel on the canvas
executed exactly one branch. Now:

- `sdk/reasoning/flow.go` — `FlowNodeParallel` kind; `Join` / `JoinQuorum` / `JoinNode`
  on `FlowNode`; policies `all` / `any` / `quorum` / `best_effort`. Append-only.
- `internal/reasoning/flow.go` — the walk was extracted into `walkFlow(..., stopAt)`
  over a mutex-guarded `flowRunState`, so `visits` / `traversed` / the global execution
  budget stay correct across concurrent branches. `runFlowParallel` claims eligible edges
  in declaration order, deep-copies vars per branch, bounds concurrency at
  `maxFlowParallelism`, and merges branch writes back **in declaration order against a
  pre-fork snapshot** — so the aggregate and the merged vars are deterministic regardless
  of completion order.
- `any` / `quorum` cancel stragglers but always `wg.Wait()` before reading results —
  cancellation asks a branch to stop, it does not make it stop.
- Tracing: `FlowNodeRun` gained `BranchID` + `ParallelGroup`; the group emits its own
  record spanning the fan-out.
- `FlowHooks` are now documented as requiring concurrency safety; `internal/runtime/flow.go`
  and `internal/studio/testrun.go` gained the mutexes that makes true.

Tested with a real 3-way barrier (a sequential walker times out rather than passing by
luck), all four policies, shuffled completion orders, budget enforcement, nested groups,
and 13 compile-error cases. `-race -count=3` clean.

**Known gap:** `internal/studio/genschema.go` `ValidNodeKinds` still excludes `"parallel"`,
so the LLM node generator will not emit parallel nodes yet. The engine supports them; the
authoring surface does not. This is a deliberate follow-up, not an oversight.

### Build-until-works (ST-12) — sandbox polarity inverted back
`SideEffectPolicy` with `SideEffectsMocked` as the **zero value**, so a call site that
forgets the field cannot fire production tools. `effectiveVerifier()` additionally
*downgrades* any verifier reporting `RealSideEffects()` unless the policy explicitly opts
in, and says so loudly in the trace. Budgets: `MaxElapsed` (default 10m), `MaxTokens`,
`MaxCostUSD`, reusing `costs.UsageRecord` rather than a parallel accounting type.
`StopReason` distinguishes `converged` / `max_attempts` / `time_budget` / `token_budget` /
`cost_budget` / `no_progress`. `RegressionTests` carry across attempts and are returned
**only on convergence** — an unproven suite must not be promoted.

### Live run (ST-07/08/11) — no more silent escalation
`handleStudioTryAgent` no longer sets `def.Unattended = true` and no longer passes
`acceptPrivilegedExposure=true` unconditionally. It now returns **422** on contract
blockers and **409** with a structured preview when side effects are unacknowledged;
`POST /studio/run-preview` returns that preview without running anything.

I additionally fixed a hole the agent left: `registerEphemeralPeers` still forced
`Unattended: true` on synthesized peer stubs, which reopened the same gap one level down
(an attended parent reaching a confirm-required tool *through* a peer). Stubs now inherit
`def.Unattended`.

### Deployment + scheduler (ST-16)
`DeploymentRecord` captures workflow hash, rules version, redacted provider config, and
the certification record; history is append-only; `Rollback` restores the previous
definition **and appends a new version** rather than mutating history.
`internal/scheduler/readiness.go` adds an injectable `ReadinessGate` consulted before each
fire, with block state surfaced on `ScheduleEntry` for the UI. `internal/app/schedgate.go`
adapts Studio's deployment history to it.

The gate's most important decision: **an agent with no deployment record returns
`ok=false` (no opinion)**, not "blocked". Blocking those would silently stop every
hand-written YAML cron agent in the workspace — a far worse failure than the one the gate
prevents.

---

## Wave 2 — landed, verification incomplete

### New endpoints (all registered in `internal/gateway/server.go`)

| Route | Story | Returns |
|---|---|---|
| `POST /api/v1/studio/build-spec` | ST-01 | structured spec + `questions[]` + `blockers[]`; with `previous_intent`, also `diff[]` + `materially_different` |
| `POST /api/v1/studio/plan-view` | ST-03/06 | Trigger/Work/Delivery stages with input, output, retry, completion; parallel groups with declared join |
| `GET /api/v1/studio/model-capabilities` | ST-09 | full registry as model cards, or one via `?model=` |
| `POST /api/v1/studio/run-preview` | ST-11 | side-effect preview without executing |

`planview.go` was updated so a `kind=parallel` node's **declared** `Join` wins over the old
`on_error` inference; inference is retained only for the legacy shape.

Compile responses now carry `capability_warning`, `confidence`, `capabilities`,
`strategy_mode`, `strategy_reason` — ST-02's "informed override", previously computed and
discarded.

### Preflight and readiness
`PreflightInput` gained `RequiredSecrets` / `ProvidersAvailable` / `ModelsAvailable`;
`PreflightIssue` gained `Severity` + machine-readable `Action` + `ActionParams` using
certify.go's existing token vocabulary. `PreflightResult.Passes` restores the Ready tier.
New `studio.Readiness(ReadinessInput) ReadinessReport` composes preflight + contract +
security + consent into **one** verdict, reporting any section it could not evaluate as
`unknown` and forcing `OK=false` — replacing the client-side stitching that degraded
silently.

Backward compatibility is preserved by making nil mean "caller supplied nothing":
`SecretsSet == nil` produces no secret verdicts.

> ⚠️ **Ship blocker flagged by the implementing agent, not yet addressed.** Today
> `SecretsSet` marks `Set=true` only for *vault-stored* values. A provider whose key lives
> in plaintext `config.yaml` will therefore read as missing and produce a **false
> blocker**. Widen that map to "credential resolvable" before enabling the secret checks
> in production.

### Rules versioning (ST-09)
`internal/studio/rulesstore.go` — one JSON file per version, append-only enforced with
`O_EXCL`, content-hash version identity, `RulesHistory` / `RulesAt` / `LatestRules` /
`DiffRules`. **Not yet adopted by the gateway**: `handleStudioSaveRules` still does a bare
`os.WriteFile`. See "Remaining work".

### Transactional apply-repair (ST-14) — and the regression I found and fixed

The agent wired `ApplyRepairTransactionally` into `/studio/apply-repair` with a sandbox
`ReplayFunc` (mocked `TestRun`, seeded from `node_trace` so the repaired template
re-renders against the bytes that actually broke it). Good.

But it also made adoption conditional on promotion, and promotion requires a replay. With
**no trace, the replay is nil → nothing is promoted → the original draft is returned**.
Since the GUI does not send `node_trace` today, this meant *every repair silently did
nothing*, and the learning loop stopped recording entirely — a worse regression than the
unverified-repair problem it was closing. It surfaced as a failing
`TestStudioLearningLoop_AcceptThenInject`.

I considered replaying against synthetic stubs instead and rejected it: that produces
**false negatives** — a shape-drift fix reads a field the stub lacks, the walk errors, and
a *correct* repair gets rolled back for an unrelated reason. Looking like proof is worse
than admitting there is none.

The fix distinguishes three outcomes rather than two:

| Outcome | Condition | Behaviour |
|---|---|---|
| **Promoted** | replayed against real evidence, passed | apply · record lesson · record corpus case |
| **Unproven** | validated, no evidence to replay against | apply · record lesson · **no** corpus case · disclosed in the response |
| **Rejected** | replayed and failed | roll back, return original |

Changes made:
- `RepairAttempt.Unproven` (new field) and the `replay == nil` branch now returns the
  **candidate** rather than the original, flagged unproven.
- `applyRepairVerification.EvidenceSeeded` so a client can tell the two proof strengths
  apart instead of reading both as "verified".
- Lessons and corpus cases were split by standard of evidence: a **lesson** records what
  the provider actually returned (observed fact from the failing run — true regardless of
  whether this patch was replayed); a **corpus case** asserts this patch works and still
  requires proof.

**All of the above is unverified — written after the shell died.** It is a coherent set of
edits I reasoned through carefully, but nothing has compiled. Files touched:
`internal/studio/repairtxn.go`, `internal/gateway/studio_repair.go`,
`internal/gateway/studio_repair_test.go`.

Also unverified: `TestStudioApplyRepair_PromotesWhenReplayPasses` asserts `valid == true`,
which depends on `NormalizeAndCheck` accepting a two-node `web_search → agent` draft. The
fixture mirrors a previously-passing one but was never executed.

---

## Wave 3 — GUI (written without a compiler; **all unverified**)

The sandbox was already wedged for this wave, so none of it has been built, run, or
tested. The two new pure modules ship with vitest suites that have never executed.

### New tested modules
- **`gui/src/lib/studio/repairverdict.js`** (+ `.test.js`, 8 cases) — turns an
  apply-repair response into one honest sentence. The old UI said "Run Live again to
  confirm" for every outcome, which understated a proven fix and hid a rejected one.
  Exports `repairVerdict`, `repairTone`, `repairProofLabel`.
- **`gui/src/lib/studio/benchfixtures.js`** (+ `.test.js`, 15 cases) — moves bench state
  onto `workflow.outcome`. Reconciles the two shapes: mocks are raw JSON on the wire but
  *text* in the editor (half-typed JSON must survive a keystroke). Un-parseable mock text
  is dropped rather than persisted, so a typo cannot make the next load fail somewhere
  far away. Exports `fixturesFromWorkflow`, `outcomeWithFixtures`, `hasFixtures`.

### API layer
`api.js` / `studioApi.js` gained `buildSpec`, `planView`, `modelCapabilities`,
`runPreview`; `applyRepair` now carries `failing_input`, `node_trace` and `preview`;
`tryAgent` carries `confirm_side_effects` / `acknowledged_tools`.

### Studio.svelte
- **Run Live gate wired.** `tryResult` now keeps the `input` that produced the run (a
  repair is only proven by replaying the exact input that broke it). 409 opens a
  side-effect confirmation modal listing what the run would touch, grouped by *what it
  does* rather than by tool id; 422 renders the execution blockers. The Run Live button
  is disabled while blockers remain. `runAck` is invalidated by any draft edit and by
  any draft swap — an acknowledgement must never outlive the draft it described.
- **Test fixtures persist** (ST-10's biggest gap). `hydrateBench` / `persistBench` read
  and write `workflow.outcome` on every bench mutation, so mocks and assertions survive
  reload and travel with the workflow. `persistBench` no-ops when the serialized shape is
  unchanged, otherwise every keystroke would churn `workflow` and re-trigger the
  validation and security passes watching it.
- **`showPlanView` reparse bug fixed** (ST-03 AC5) — it now mirrors `showCanvasView`, so
  Code → Plan → Code no longer discards unsaved SOUL.yaml edits.
- New bench state for variables / environment / start node, with mutators that persist.

### Backend changes made from the GUI side
- `OutcomeSpec` gained `Mocks`, `SampleInput`, `Variables`, `Environment`, `StartNode`
  (`internal/studio/outcomespec.go`, plus an `encoding/json` import). Deliberately **not**
  part of `ToAgentContract` — fixtures are build-time scaffolding and must not reach the
  deployed agent.
- `applyRepairRequest.Preview` (`internal/gateway/studio_repair.go`) suppresses lesson and
  corpus recording. Without it, the UI's "show me what this would change" button taught
  the generator from a repair the user had not accepted — a side effect from merely
  looking.

> **`TestRun` does not yet consume `Variables` / `Environment` / `StartNode`.** The spec
> carries them and the UI collects them, but the runner ignores them, so those three
> ST-10 criteria are persisted-but-inert until `internal/studio/testrun.go` is extended.

### Wave 3b — library, strategy advice, warning audit (also unverified)

**`gui/src/lib/studio/libraryfilter.js`** (+ `.test.js`, 20 cases) — `partitionLibrary`,
`filterLibrary`, `libraryFacets`, `hasActiveFilters`, `emptyQuery`.

- **Three-way split (ST-15 AC1).** "Saved agents" conflated a workflow that is deployed
  and running on its schedule with one that is saved and inert — opposite operational
  meanings distinguished only by a small badge. `enabled` is now the deployed/saved
  boundary and each section renders separately.
- **Faceted search (ST-15 AC4-7).** Free text plus trigger / strategy / integration /
  status / owner. Facet dropdowns are derived from values actually present, so a filter
  never offers a choice that matches nothing. Filtering is deliberately tolerant of
  missing fields — the list endpoints do not return every facet for every item, and an
  item that declares no strategy must not vanish from an unrelated search.
- **Missing actions added (ST-15 AC9-12).** Clone (opens a copy, deliberately does *not*
  save — a browse action must not silently create a stored agent), Test (loads it and
  runs the bench), Export (downloads SOUL.yaml without disturbing the open draft), and
  Deploy/Undeploy via `agents.enable` / `agents.disable`, confirmed on deploy because it
  decides whether the schedule actually fires.
- Factored `downloadText` out of `exportDraft` so both export paths share one code path.

**Strategy advice surfaced (ST-02 AC4).** `applyCompile` now captures
`capability_warning` / `confidence` / `capabilities` from the compile response and
renders a confidence badge on the recommendation strip plus a capability warning with an
expandable "what this model supports" panel — placed next to the mode controls, because
"you can switch to ReAct" and "this model can't sustain ReAct" are only useful together.

### Wave 3c — strategy contract, built to the mockups (unverified)

First increment built **against the screenshots** rather than only the stories.

**`internal/studio/agentpolicy.go`** (+ `_test.go`, 8 cases) — `AgentPolicy` with three
sub-blocks: `AgentContract` (goal, instructions, completion criteria, tool choice,
recovery retries), `ReActPolicy` (objective, stop conditions, recovery behaviour,
invalid-step budget, repeated-tool limit, confidence threshold, preserve-best-result,
fallback-to-Auto) and `PlanExecutePolicy` (steps, replan-after-failure, parallel
independent steps, approval-before-side-effects, plan timeout). Plus
`DefaultAgentPolicy`, `EffectivePolicy`, `ValidatePolicy`. `Draft.Policy` is one new
append-only field.

Why it exists: the mode picker set a *string*. Everything that actually governed a run —
when to stop, what counts as done, how many bad steps to tolerate — was a hidden default
or prose inside `SystemPrompt`, so two agents on the same strategy could behave
completely differently with nothing on screen explaining why.

Design decisions worth knowing:
- The three blocks are separate, not one flat bag. A ReAct budget means nothing under
  Plan-Execute, and flattening made it impossible to tell which fields were in force.
  `EffectivePolicy` drops a block that does not match the strategy (stale config, not
  intent).
- The contract field-merges over defaults; the **loop blocks replace wholesale**. A user
  who edited the ReAct budget stated the whole thing — half-merging produces a
  combination nobody chose, and it is the only way an explicit `false` survives.
- Defaults are *exported and displayed* rather than applied silently, because a default
  the user cannot see is indistinguishable from behaviour they cannot control.
- `ValidatePolicy` returns warnings, never errors: an aggressive budget is the operator's
  tradeoff to make.

**`gui/src/lib/studio/StrategyPanel.svelte`** — the Build-step screens from the mockup:
mode tabs, verdict banner (recommended / advanced / *overriding the recommendation*),
model chip with a capability badge, the shared agent contract, the per-mode policy panel,
the drawn ReAct loop, and the Plan-Execute planner/executor split with a parallelisable
grouping. Mounted in the agent-spec view.

> **One deliberate deviation from the mockup.** The screens put a model chip next to the
> capability badge. `studioModelLabel` is the **builder** model, but those badges are
> computed from the **runtime** model's profile — showing one beside the other is the
> exact conflation that makes a capability warning untrustworthy. The label is therefore
> derived from `strategyAdvice.capabilities` itself, and renders empty when the advisor
> has no profile: better to name no model than the wrong one.

### Wave 3d — "Studio understood" / Describe screen (unverified)

**`gui/src/lib/studio/buildspecview.js`** (+ `.test.js`, 18 cases) — `specRows`,
`specBlockers`, `specQuestions`, `specReady`, `changeSummary`, `strategyLabel`.
**`gui/src/lib/studio/BuildSpecPanel.svelte`** — the Describe step's right pane.
The prompt editor modal is now a two-column Describe screen: prompt on the left, what
Studio understood on the right, because verifying a reading against the words that
produced it is the whole point — separating them means checking the spec from memory.

Behaviours that are deliberate:
- **Empty sections render as "not specified" rather than being hidden.** A missing
  delivery destination is exactly what someone needs to catch at this step; dropping the
  row hides it until the run delivers nothing.
- **No security row when there is nothing to report.** An empty security row reads as
  reassurance, and this function has no basis for reassuring anyone.
- **Blockers gate Generate and each renders as an input.** "Telegram destination
  required" with nowhere to type it is a dead end. The live gate is the *unresolved*
  list, not the server's `ready` flag — that flag reflects the spec before the user
  typed their answers.
- **`changeSummary` returns null when nothing was compared**, which is different from
  "compared and found identical". Claiming "no changes" when we never had a baseline
  would be a lie; `refineFromModal` pins `buildSpecPrevIntent` so the comparison is real.
- A failed spec read sets an error but never blocks Generate — it is a review aid, not a
  gate, and an empty panel would read as "Studio understood nothing".
- The fetch is debounced and token-guarded so a slow response cannot overwrite a newer one.

### Wave 4 — Wizard IA (unverified, structural)

The requested restructure: Studio is now **Describe › Build › Test › Save** rather than a
canvas with tabs. `viewMode` (Plan / Canvas / SOUL.yaml) demotes to sub-navigation *within
Build*, which is what made the order of operations invisible before.

**`gui/src/lib/studio/wizard.js`** (+ `.test.js`, 26 cases) — `stepStates`, `canEnter`,
`isDone`, `nextStep`/`prevStep`, `autoStep`, `saveBlockedReason`.
**`gui/src/lib/studio/WizardRail.svelte`** — the rail.

The gating rules are where this gets subtle, and the failure mode to avoid is a wizard
that traps people:

- **Navigation is permissive; only actions are gated.** You can always open Save to see
  *why* you cannot save. A step that refuses to open cannot explain itself.
- **Build is reachable with a broken draft** — that is exactly when you need the canvas.
  Gating on validity would hide the graph precisely when it needs fixing.
- **`done` means the work was accomplished, not that the step was visited.** Test is only
  done when a test *passed*: a red run must not read as progress on the rail.
- **`autoStep` never moves the user backwards.** A freshly generated workflow advances to
  Test rather than dumping you back at Describe, but an edit that invalidates a test
  result must not yank someone out of Save mid-review. Auto-advance also stops entirely
  once the user navigates themselves (`stepPinned`).
- **`saveBlockedReason` treats "unknown" as blocking.** A readiness check that could not
  run is not a check that passed — the same principle the readiness endpoint enforces.

Layout consequences wired up: the palette and inspector render only on Build (a palette
offering drag-and-drop onto an off-screen canvas is noise); the body collapses to one
column on Describe/Save rather than leaving two empty rails; Build collapses the bench to
give the canvas room, Test expands it so "Test" does not land you on a canvas with a strip
at the bottom to go find. Describe is now an inline two-pane step rather than a modal.

> **Transition state.** The old prompt-editor modal and the legacy preflight dialog still
> exist and still work; the Describe and Save steps duplicate their function inline. Once
> verified, the modal should be removed and the preflight dialog reduced to the
> `ReadinessPanel` it now wraps.

### Wave 4b — Test Lab inputs, end to end (unverified)

**`gui/src/lib/studio/TestLabInputs.svelte`** — the four tabs from the mockup
(Input / Mock Data / Variables / Environment), reusing the existing
`lib/KeyValueEditor.svelte` rather than reinventing it. Replaces the mocks-only bench
section: variables, environment and the start node had **no UI at all**, and the mocks
were buried down a scrolling bench, which made "reproduce the exact conditions of this
failure" a hunt rather than a task. Tab badges show counts so a configured mock cannot
silently change a run's result while invisible.

**These are no longer decorative.** Previously the spec carried them and the UI collected
them, but `TestRun` ignored them. Now plumbed end to end:

- `TestOptions` gained `Variables`, `Environment`, `StartNode`
  (`internal/studio/testrun.go`).
- **`trigger` and `env` are reserved variable names.** A variable that quietly replaced
  the trigger would make a passing test prove nothing about the real run, so the
  collision is reported as a warning rather than silently resolved.
- `Environment` is exposed to templates as `.env` and is **test-run scope only** — never
  written to the saved agent, so a sandbox value cannot leak into production.
- **`reasoning.RunFlowFrom`** (new, `internal/reasoning/flow.go`) starts the walk at an
  arbitrary node. The parallel-engine refactor made this ~10 lines, since `RunFlow` was
  already a thin wrapper over `walkFlow`. An empty `from` delegates to `RunFlow`, so it
  is a safe drop-in.
- **A stale start node is an error, not a fallback.** Silently reverting to the entry
  would tell the user their run passed while it tested something else. Starting
  mid-pipeline also emits a warning that upstream values must come from a mock or
  variable — the walker cannot detect the omission, because an absent upstream value is
  indistinguishable from a legitimately empty one.
- `studioTestRequest` + `api.js` + `studioApi.js` carry all three; the fields are omitted
  when empty so a plain run's payload is unchanged.
- The request snapshot pushed to history includes them, so replaying an entry re-runs the
  **same** conditions rather than today's.

### Wave 5 — ST-04 streamed generation + ST-11 completed-actions (unverified)

**ST-04.** `PipelineEvent` gained `ElapsedMS` and `Source` (`planner` | `llm`); new
statuses `StatusNode` and `StatusCancelled`. The transcript now labels every row **rules**
or **model**, which it could not before — planner and LLM rows rendered identically, so a
user could not tell which decisions a model made. Elapsed ticks client-side as well as
from events, because a long phase that emits nothing would otherwise show a frozen
counter, which is exactly what "is this stuck?" looks like.

**Cancellation now actually cancels.** The handler kept `context.WithoutCancel` (correct —
Fiber hands the connection to a stream writer, so `c.Context()` is already done) but is
now `WithCancel` on top of it, and the stream writer cancels on a failed flush. A failed
flush is the *only* disconnect signal available once the connection is handed over.
Client side, `streamSSE` takes an `AbortSignal` and a Cancel button drives it; an abort
mid-read is the expected stop path, so it returns rather than surfacing as an error. The
writer also drains the channel after cancelling so the producer goroutine cannot leak.

**Partial drafts are preserved.** The old client did
`if (done.error) { compileError = … } else if (result.compile) { applyCompile(…) }`, which
threw the draft away exactly when the user most needed to see it. Now the draft is adopted
whenever one exists and the error is appended as context.

> **Honest limitation on incremental nodes.** `emitNodes` announces each node in
> declaration order *after* the planner returns, because the deterministic planner is one
> synchronous pass. That is strictly more than the old aggregate count — the user sees
> which steps exist and what each is — and it is emitted at full speed with **no
> artificial pacing**, because faking a delay to look like incremental construction would
> be theatre that also makes a fast generate slower. Emitting from *inside* the planner
> needs a callback threaded through `CompileDeterministicWorkflow`/`Agent`; that is the
> real fix and remains outstanding.

**ST-11 partial.** `executed_side_effects` now records which side-effecting tools actually
reached the outside world, in order, with timestamps — classified against
`preview.SideEffectTools` so the run reports against exactly what the operator
acknowledged. Present on the error path especially, since "did it already send the
message?" is the first question after a half-completed run. Tool trace rows also gained
`args_full` / `result_full` alongside the truncated summaries.

> **Still outstanding: SSE for `/studio/try-agent`.** It remains a blocking POST. Doing
> that conversion means restructuring the handler that fires real tools, and with no
> compiler available that is the single highest-risk change on the board — a mistake there
> executes real side effects rather than failing a test. Deliberately not attempted blind.
> The observers are already the right hook: they just need to push to a channel instead of
> only appending.

### Wave 4c — Failed runs / self-heal screen (unverified)

**`gui/src/lib/studio/failuregroup.js`** (+ `.test.js`, 22 cases) and
**`FailedRunsPanel.svelte`**. Closes two genuine ST-13 gaps.

**Grouping (AC8).** `/studio/failed-runs` returns a flat list, so an agent failing the
same way every morning produced one row per run — and twelve identical rows tell you
*less* than one row saying "12 times since Tuesday", because the single genuinely
different failure is buried among them. `groupFailures` collapses by signature
(subject + category + normalised message), sorted **most-recent-first rather than by
count**: the loudest failure is rarely the newest information.

**Taxonomy (AC5).** The story asks for graph / contract / configuration / provider /
permission / delivery / transient. The backend has two other vocabularies that match
neither the story nor each other. `classifyFailure` maps what the server *does* send onto
the story's vocabulary, preferring an explicit category, then the structured repair
class, and only then falling back to prose — deliberately last and deliberately narrow,
because a misleading label sends someone troubleshooting in the wrong direction. Anything
it cannot name returns `unknown` rather than a guess.

Small but real consequences: "Retry unchanged" is offered **only** for transient faults
(a retry button beside a configuration error invites the user to waste a run), and
applying a repair to a deployed agent requires a recorded note.

> **Payload-shape bug caught before it shipped.** I had written the grouper against an
> assumed `failed_node` field. The real payload is
> `{id, agentId, agentName, error, attempts, failedAt, healable}` — and `failedAt` is a
> **timestamp**, not a node. Reading it as the subject would have given every run a
> unique signature and defeated grouping entirely. Fixed, with a regression test pinning
> it. Also discovered `attempts`: the DLQ retries before giving up, so counting each
> queue entry as one failure under-reports. `count` is now occurrences and `entries` is
> distinct queue items.

### Wave 3e — readiness endpoint + Save screen (unverified)

**`POST /api/v1/studio/readiness`** — `internal/gateway/studio_readiness.go`, registered
in `server.go`. Finally exposes `studio.Readiness()`, replacing the client-side stitch of
`/studio/compile` + `/studio/security_review` + `/studio/plan`.

The handler's real job is being honest about what it could not collect. If the grounded
catalog comes back with no tools *and* no MCP servers, that is not "a workspace with
nothing installed" — every workspace has builtins — so preflight and contract are marked
`Unknown` rather than run. Judging tool references against an empty catalog would
manufacture confident false blockers ("web_search does not exist"), which is worse than
admitting we could not look. Returns 200 regardless of verdict: "not ready" is a
successful answer to the question asked.

**`gui/src/lib/studio/ReadinessPanel.svelte`** — the Save step's review. Renders **four**
states per section, not two: `Unknown` is shown as prominently as a blocker, because a
check that did not run is not a check that passed. The headline deliberately refuses to
say "Ready" while any section is unknown, even when everything that ran was clean.
Sections that could not be evaluated show *why* — "not checked" without a reason is
indistinguishable from a bug. Items carrying a machine-readable `action` render a real
button; anything that edits the current draft keeps the user inside Studio rather than
navigating away and losing unsaved work.

Wired into the existing preflight dialog *above* the legacy lists, keyed on the preflight
object's identity so it re-checks when re-opened but not on every render. A failed
readiness call clears the report rather than leaving a stale one beside a fresh error —
the old stitch's exact bug was letting a failed section quietly disappear.

> **Transition state:** the legacy blocker/warning lists still render below the new panel.
> They should be removed once this is verified against a live server.

**Warning acceptance is now recorded (ST-16 AC3).** "Save anyway" requires a typed reason
and is disabled until one is given; `accept_warnings_reason` travels with the save;
`handleStudioSave` writes an admin audit entry with status `accepted_warnings`. The
reason is cleared after the save that consumed it so it is never silently reused.

---

## Remaining work

### Immediate (finish Wave 2)
1. **Run the full test sweep.** Nothing after Wave 1 has had one.
2. **Fix `SecretsSet`** to mean "credential resolvable", not "in the vault" — otherwise the
   new secret blockers are false positives.
3. **Adopt the rules store** in `handleStudioSaveRules`: replace `os.WriteFile` with
   `studio.SaveRulesWithNote(rulesDir, rules, author, note)`; have `soulRules()` read
   `LatestRules` first, falling back to the legacy file then `DefaultSOULRules`. Add
   `GET /studio/rules/history`, `/studio/rules/:version`, `/studio/rules/diff`.
4. **Expose `studio.Readiness()`** as `POST /api/v1/studio/readiness` and switch the GUI
   to it (replacing the three-endpoint client-side stitch).
5. Delete two scratch files the sandbox refused to remove (`rm` returned
   *Operation not permitted* on that mount): `internal/gateway/zz_probe_test.go` and
   `internal/studio/zzprobe_test.go`. Both currently pass; the second was rewritten into
   two real guards, the first only logs and asserts nothing.

### GUI — partially done in Wave 3, still unconsumed endpoints
Done (unverified): repair trace plumbing, repair verdict messaging, Run Live 409/422
handling, fixture persistence, the `showPlanView` reparse fix.

Still missing:
1. **Screens for `buildSpec`, `planView`, `modelCapabilities`, `readiness`.** The API
   bindings exist; nothing calls them. The "Studio understood…" panel should render
   `build-spec` (with its `diff` as the visible change summary); the Plan tab should
   consume `plan-view` so parallel groups and join policies finally appear; a model card
   should render `model-capabilities`.
2. Replace the client-side readiness stitch with the single `readiness` call once the
   endpoint is exposed.
3. **Warnings are still dropped when there are no blockers.** `applyCompile` only sets
   `preflight` when `generatedGate.blockers.length` is non-zero, so a warnings-only
   report never reaches the user after generation (ST-07 AC2). Fixing this needs a
   "show without gating" flag so the modal does not pop on every compile — left alone
   deliberately rather than risking that regression unverified.
4. **Draft recovery history** (ST-15 AC14) — still a single overwritten autosave slot.
   `api.agents.versions/version/rollback` already exist for deployed agents and are now
   exposed on the bridge (`agentVersions`, `agentRollback`); drafts have no equivalent.

### Not started at all
- **ST-04** streamed generate: incremental node events, elapsed time, `AbortController`
- **ST-11** live-run SSE (still a blocking POST with `truncate()`d rows) + cancellation
- **ST-10** `TestRun` must actually consume `Variables` / `Environment` / `StartNode`
- **ST-13** unified error taxonomy + repeated-failure grouping + config deep links
- **ST-05** typed-port enforcement and the fan-out-without-aggregate blocker

---

## Judgement calls worth revisiting

1. **Unproven repairs are applied, not refused.** Rationale above. If you would rather be
   strict, the alternative is to make the GUI always send `node_trace` and then refuse —
   but that fails closed on a client bug, and the failure is silent.
2. **Lessons learn from evidence, corpus cases require proof.** This is what makes the
   learning loop survive missing traces. If lesson quality degrades, tighten the lesson
   path first rather than re-coupling both to promotion.
3. **The scheduler gate has no opinion on non-Studio agents.** Deliberate; see above.
4. **Unseeded sandbox replay was rejected.** Worth re-examining if stub fidelity ever
   improves enough to avoid false negatives.
