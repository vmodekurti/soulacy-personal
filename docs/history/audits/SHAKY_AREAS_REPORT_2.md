# Soulacy — Operational Fragility Review, Part 2 (User-Story Format)

*Review date: 2026-06-16. Read-only analysis. This is the second pass, going beyond the example themes in `SHAKY_AREAS_REPORT.md` to cover the failure classes that don't show up until the system is running under load, crashing, reloading, or being shut down: **process lifecycle, hot-reload, crash/restart durability, delivery semantics, subprocess hygiene, and observability.***

> **Verification note.** Findings tagged ✅ **verified** were confirmed by me directly against the source. Findings tagged 🔎 **traced** came from a structured code read and the *behavior* was followed to a real path, but the exact file:line may have drifted slightly — spot-check before acting. I've also called out, honestly, the places where the framework is **already robust**, so this reads as an assessment and not just a list of complaints.

---

## What's already solid (don't "fix" these)

Worth stating up front, because it changes the priority of everything below:

- **SQLite is configured correctly for concurrency** — WAL mode + `busy_timeout=30s` + `synchronous=NORMAL`, tuned pool, and separate DB files per concern (actionlog / memory / knowledge) so they don't contend (`internal/sqlitex/sqlitex.go:62-104`). "Database is locked" is unlikely except under heavy concurrent KB ingestion. 🔎
- **There is a crash-recovery path.** On boot, `replayIncompleteRuns` scans the actionlog for `message.in` events with no matching `message.out`/`error` and replays them for durable channels (`internal/app/recover.go:48-73`). This is more than most frameworks do. 🔎
- **The scheduler persists state and does catch-up.** `LastCompleted` is written atomically (tmp+rename) and `runMissedOnStartup` re-fires crons missed during downtime within a 24h window (`internal/scheduler/scheduler.go:115-182`, `378-388`). 🔎
- **Knowledge ingestion is transactional** — document + chunks + vectors commit in one transaction with deferred rollback, so a mid-ingest failure doesn't leave half a document searchable (`internal/knowledge/store.go:385-430`). 🔎
- **The subprocess stdout/stderr drain follows the correct Go pattern** — stderr drained in a goroutine, `cmd.Wait()` only after the pipe goroutine exits, avoiding the classic pipe-buffer deadlock (`internal/runtime/engine.go:2819-2861`). 🔎
- **HTTP handlers are panic-protected** by Fiber's `recover` middleware (`internal/gateway/server.go:360`). ✅

Keep these. The issues below are mostly about the *edges*: the async paths, the failure paths, and the visibility into both.

---

## Theme A — Process lifecycle: panics, shutdown, goroutine leaks

### S2.1 — Panic isolation for non-HTTP runs 🔴 ✅ *verified*
**As an** operator, **I want** a panic inside a tool, LLM decode, or channel-driven run to fail *that run* — not crash the whole gateway, **so that** one malformed tool output can't take down every agent and every channel at once.

- **Rationale:** The synchronous HTTP path is covered by Fiber's `recover` middleware, but **channel- and cron-driven runs are not.** The worker pool calls `engine.Handle` directly inside a bare goroutine (`internal/app/wire_subsystems.go:885` — `for msg := range chanReg.Inbox() { … }`), and `engine.Handle` has named-return error bookkeeping but **no `recover()`**. The only `recover()` calls in the codebase are Fiber's middleware and one WS handler (`server.go:360,499`). ✅
- **Blow-out scenario:** A Python tool returns something that makes a built-in handler panic (nil map index, bad type assertion — and the engine is saturated with `map[string]any`), on a Telegram-triggered run. The worker goroutine panics with no recover → **the entire process dies**, killing every channel, the scheduler, and all in-flight HTTP requests. A single bad message becomes a full outage.
- **Guardrail:** Wrap `engine.Handle` (or each worker iteration, each tool-handler invocation, and each per-turn tool goroutine) in `defer func(){ if r:=recover(); … }()` that converts the panic into a run error + logged stack + metric. This is the single highest-leverage robustness fix in this report.

### S2.2 — Clean, bounded graceful shutdown 🟠 🔎 *traced*
**As an** operator, **I want** `SIGTERM` to drain in-flight work within a bounded deadline and then exit, **so that** deploys/restarts don't hang or leak goroutines.

- **Rationale / current state:** The inbox channel is **never closed** on shutdown (✅ confirmed: no `close(inbox)` anywhere). The worker goroutines `range` over it forever, so they never unblock; same pattern flagged for the session-eviction ticker goroutine and the file-watcher loop goroutine, none of which appear registered on the shutdown stack (`wire_subsystems.go:885`, `engine.go` eviction loop, `watcher.go:109`). 🔎 In-flight LLM calls also don't observe context cancellation around response handling, so a shutdown can stall up to the provider timeout (30–120s+) per active run. 🔎
- **Blow-out scenario:** `systemctl restart` / container redeploy hangs until the orchestrator's kill timeout fires and SIGKILLs the process mid-write — exactly when you *don't* want an ungraceful kill. Repeated restarts leak goroutines if anything keeps the process alive.
- **Guardrail:** Give shutdown an explicit ordered sequence with a deadline: stop adapters (no new inbound) → close the inbox → let workers drain with a bounded `context` deadline → cancel remaining runs → stop scheduler/watcher/eviction tickers (register them all on the closer stack) → close DBs last. Add a goroutine-count check in tests to catch leaks.

### S2.3 — Tool-call goroutines must die with their run 🟠 🔎 *traced*
**As an** operator, **I want** the per-turn tool goroutines to stop when the run is cancelled/times out, **so that** a cancelled run doesn't leave work (and subprocesses) running in the background.
- **Guardrail:** Ensure every spawned tool goroutine selects on `ctx.Done()` and that subprocess kills propagate (see Theme E). Tie goroutine lifetime to the run context.

---

## Theme B — Hot-reload robustness (the "I edited a file and things got weird" problem)

### S2.4 — Watcher must not die silently 🟠 🔎 *traced*
**As an** operator, **I want** the file watcher to survive (or loudly report) an `fsnotify` error, **so that** I'm not left running stale agent definitions with no idea reload stopped working.

- **Rationale / current state:** `watcher.loop()` returns and ends the goroutine if the fsnotify Events/Errors channel closes (`internal/runtime/watcher.go:64-87`), with no supervision, restart, or alert. If it dies, every subsequent SOUL.yaml edit is silently ignored. 🔎 (Good news: a *parse error in one* SOUL.yaml only skips that agent — `LoadAll` is per-agent isolated, `loader.go` — so a half-saved file doesn't nuke all agents. 🔎)
- **Blow-out scenario:** fsnotify hiccups (common on network filesystems, editor atomic-saves, inotify limits). The watcher dies. You "deploy" agent changes for a week by editing files; nothing takes effect; no error anywhere.
- **Guardrail:** Supervise the watcher goroutine — on Events/Errors channel close, log at ERROR, emit a metric, and attempt to re-establish the watch with backoff. Surface "watcher healthy" in `/health`.

### S2.5 — Reload must not orphan a schedule 🟠 🔎 *traced*
**As an** operator, **I want** a scheduled agent to never silently lose its cron because a reload's re-registration failed, **so that** editing an unrelated field doesn't stop a cron from firing.
- **Current state:** Reload does `DeregisterAgent` then `RegisterAgent`; if the register half fails, the agent is left **deregistered** with only a warning, no rollback (`watcher.go` reload path). 🔎 (This is the same class as `improvements.md` ARCH-1's swallowed scheduler errors, but at reload time specifically.)
- **Guardrail:** Make reload re-registration atomic (register-then-swap, or roll back to the prior entry on failure); surface a per-agent "scheduled / not scheduled" status the GUI can show.

### S2.6 — Don't share mutable nested config across a reload 🟡 🔎 *traced*
**As a** developer, **I want** `loader.Get`'s copy to be deep enough that a hot-reload can't mutate a running agent's nested config, **so that** the "shallow copy protects running agents" guarantee actually holds.
- **Current state:** The clone is one level deep; nested structures like `LLM.OutputSchema` (a `map[string]any`) share backing storage (`pkg/agent/types.go` clone helper). 🔎 Low real-world risk today because those nested values are treated as read-only at runtime — but it's a latent foot-gun.
- **Guardrail:** Deep-copy the nested maps/slices in the definition clone, or document them as strictly immutable and enforce it.

---

## Theme C — Delivery semantics (duplicates and drops)

### S2.7 — At-least-once delivery without duplicate side effects 🟠 🔎 *traced*
**As a** user, **I want** the system to not run my message twice (or lose it) across reconnects, crashes, and webhook retries, **so that** I don't get double replies or silence.

- **Rationale / current state — several adapter-level gaps:**
  - **Telegram:** the poll offset is advanced only *after* a successful inbox enqueue; on inbox-full the message is dropped **and** the offset isn't advanced, so it's re-fetched and reprocessed next poll (`telegram/adapter.go:~169`). Drop + later duplicate. 🔎
  - **Discord:** no Resume opcode — reconnects re-Identify, replaying buffered events as duplicates (`discord/adapter.go:31-37`). 🔎
  - **WhatsApp:** inbound messages aren't deduped by `message.id`, and Meta resends webhooks on any non-200 → double execution (`whatsapp/adapter.go:~273`). 🔎
  - **Slack:** the envelope is ACK'd immediately; if the inbox is full the message is then dropped — Slack considers it delivered and never retries → silent loss (`slack/adapter.go:~163,203`). 🔎
- **Blow-out scenario:** A reconnect or a slow run that pushes the inbox to capacity produces either duplicate replies (annoying, and *doubles cost*) or silent message loss (worse — the user thinks you ignored them).
- **Guardrail:** Add a small dedup cache keyed by platform message id (Telegram update_id, Discord message id, WhatsApp wamid, Slack event id) checked before processing; only ACK/advance-offset **after** the message is durably enqueued (or after `message.in` is logged, to align with the existing replay-recovery path). Implement Discord Resume.

---

## Theme D — Channel send robustness (the reply that never lands)

### S2.8 — Platform-aware outbound: chunk, retry, classify 🟠 🔎 *traced*
**As a** user, **I want** long replies to actually arrive (split if needed) and transient send failures to be retried, **so that** a 4097-character answer or a momentary network blip doesn't silently eat the whole response.

- **Rationale / current state:**
  - **No length handling:** Telegram's 4096-char and Discord's 2000-char limits aren't enforced or chunked; an over-limit reply just fails the send (`telegram/adapter.go:~210`, `discord/adapter.go:~239`). 🔎 Agents that produce long answers (very common) will intermittently fail to deliver.
  - **No outbound retry / no error classification:** every adapter's `Send` fails fast; a transient 429/5xx/network error aborts the run instead of retrying with backoff. 🔎
  - **Reconnect loops use fixed 10s sleeps and don't distinguish auth (don't retry) from transient (back off)** — a revoked token makes Discord/Slack hammer the API every 10s indefinitely (`discord/adapter.go:~107`, `slack/adapter.go:~95`), which can earn a rate-limit ban. 🔎 (This overlaps `improvements.md` PERF-5's ctx-aware backoff, but the *auth-vs-transient* distinction is the new part.)
- **Guardrail:** Add per-platform message chunking; wrap `Send` in bounded retry with backoff on retryable statuses; classify 401/403 as terminal (stop, mark channel unhealthy, alert) vs transient (back off).

---

## Theme E — Subprocess hygiene

### S2.9 — Kill the whole process tree on timeout 🟠 🔎 *traced*
**As an** operator, **I want** a tool timeout to kill the tool *and everything it spawned*, **so that** orphaned child processes don't pile up and consume the box.

- **Rationale / current state:** Tools run via `exec.CommandContext`; on timeout Go SIGKILLs the direct child, but there's **no process-group setup** (`SysProcAttr.Setpgid`), so a tool that itself shells out (`subprocess.run(...)`, a spawned server, a `curl`) leaves orphans when the parent is killed (`engine.go:~2769`, `sandbox/sandbox.go:74-111`). 🔎
- **Blow-out scenario:** A tool that launches a child which hangs; the run times out, the parent dies, the child keeps running and holding resources. Repeat across many runs → process-table / memory exhaustion that looks like a mystery leak.
- **Guardrail:** Start tool processes in their own process group and kill the group on timeout/cancel; ensure the sandbox wrapper forwards signals to its child.

### S2.10 — A global cap on concurrent subprocesses 🟠 🔎 *traced*
**As an** operator, **I want** a hard ceiling on total concurrent tool subprocesses, **so that** a runaway loop (or many concurrent runs) can't fork-bomb the host.
- **Current state:** The pooled executor reuses workers, but the process-per-call backend spawns a fresh `python3` per call with no global cap (`executor/process/process.go`). Combined with the unbounded `max_turns` and arg-jitter loop evasion from Report 1 (S3.2/S3.3), the subprocess count is effectively unbounded. 🔎
- **Guardrail:** A global semaphore on live tool subprocesses (config-driven), with queueing or a clear "tool capacity exceeded" error when saturated.

### S2.11 — Defensive output handling 🟡 🔎 *traced*
**As a** developer, **I want** oversized or non-UTF-8 / non-JSON tool output handled gracefully, **so that** one weird tool result doesn't corrupt the next LLM turn or error the run opaquely.
- **Current state:** The pool scanner caps a line at 1 MiB and errors "token too long" beyond it; binary/non-UTF-8 stdout is read as-is and can corrupt the LLM input; stdout result is `TrimSpace`'d with no JSON validation (mitigated by routing `print()` to stderr, but `sys.stdout.write` bypasses that) (`executor/pool/pool.go:~246`, `engine.go:~2872`). 🔎
- **Guardrail:** Cap and clearly truncate tool output with a marker; validate/normalize encoding; if a tool declares structured output, validate it and return a typed error rather than passing garbage forward.

---

## Theme F — Observability blind spots (you can't fix what you can't see)

### S2.12 — See stuck/in-flight runs in real time 🟠 🔎 *traced*
**As an** operator, **I want** an endpoint/GUI view of currently-active runs — their age, agent, channel, and current step, **so that** I can spot a hung run *now*, not reconstruct it from logs later.
- **Current state:** Run/session stats are computed from *completed* runs in the DB; there's no live in-flight registry (`gateway/runmetrics.go`, `gateway/api.go`). 🔎 A run stuck in a slow LLM call or a hung tool is invisible until it times out.
- **Guardrail:** Maintain an in-memory active-run table (start time, agent, channel, current turn/tool) exposed at `GET /runs/active` and in the GUI, with a "running longer than N" highlight.

### S2.13 — A deep health check 🟠 🔎 *traced*
**As an** operator / load balancer, **I want** `/health` to actually probe dependencies (DB, configured providers, channel adapter connection state, MCP servers), **so that** it doesn't report healthy while Slack is disconnected and the provider is down.
- **Current state:** `/health` lists provider *ids* (no connectivity test) and does a KB probe, but never calls adapters' existing `Status()` methods or checks provider reachability (`gateway/api.go:54-106`). 🔎 It can return `200 ok` with a dead Slack socket and an unreachable LLM.
- **Guardrail:** Add a `?deep=1` health mode that calls each adapter's `Status()`, pings the DB, and does a cheap provider reachability check; report per-dependency status and an overall `healthy/degraded/unhealthy`.

### S2.14 — The metrics that matter for this system 🟠 🔎 *traced*
**As an** operator, **I want** signals specific to an agent runtime, **so that** I can alert before users notice.
- **Current state (good baseline):** HTTP/LLM/tool/run latency + outcome, actionlog queue depth/drops, active-run gauge, per-channel inbox drops are instrumented (`internal/metrics/metrics.go`). 🔎
- **Missing:** per-provider/per-model **error rate** (only success/error aggregate), **reload/config-parse failures**, **duplicate-delivery counts**, **live subprocess count**, **tool-timeout frequency**, and a **gateway heartbeat** metric. 🔎
- **Guardrail:** Add those counters; ship an example Prometheus alert rules file (inbox_drops>0, provider_error_rate>X, reload_failures>0, heartbeat stale) so operators get alerting out of the box.

### S2.15 — Push alerting on failure, not just pull metrics 🟡 🔎 *traced*
**As an** operator, **I want** an optional push notification on critical failures (agent dead, provider down, repeated errors), **so that** I don't have to be watching a dashboard.
- **Current state:** A `FailureNotifier` interface exists and can be wired, but there's **no default implementation** and all alerting is otherwise pull-based (`engine.go:~272`). 🔎
- **Guardrail:** Provide a built-in notifier (webhook/Slack/email) configurable in `config.yaml`, fired on the conditions in S2.14.

---

## Theme G — Durability edges (mostly already-good, with two real gaps)

### S2.16 — Bound knowledge ingestion memory 🟠 🔎 *traced*
**As an** operator, **I want** large document uploads to not be read entirely into RAM, **so that** a big PDF can't OOM the gateway.
- **Current state:** Ingest reads the whole file into memory and the PDF parser slurps the entire document (`knowledge/ingest.go:26-78`). A 1 GB upload ≈ 1 GB+ heap. 🔎
- **Guardrail:** Enforce a configurable max upload size; stream extraction where the parser allows; reject/curate oversized inputs with a clear error.

### S2.17 — Make schema evolution explicit 🟡 🔎 *traced*
**As a** maintainer, **I want** schema changes versioned and verified instead of best-effort `ALTER … // nolint:errcheck`, **so that** an upgraded binary on an old DB doesn't silently lack a column (and so Postgres doesn't drift from SQLite).
- **Current state:** A `schemaversion` mechanism exists for some stores, but knowledge uses unversioned `ALTER TABLE` with errors ignored, and Postgres has no tests (audit H5), inviting SQLite/PG drift; no downgrade path (`knowledge/store.go:~175`, `sqlitex/schemaversion.go`). 🔎
- **Guardrail:** Route all schema changes through the versioned migrator; add a startup `integrity_check`; add the Postgres parity tests already scoped in `improvements.md` TEST-2.

### S2.18 — Surface auth failures from rotated keys 🟡 🔎 *traced*
**As an** operator, **I want** the running gateway to detect and surface provider/channel auth failures (e.g., a key rotated out from under it), **so that** I'm not debugging "why is every run failing" blind.
- **Current state:** Most provider/channel keys require a restart to update (only the Ollama hosted key is hot-swappable), and there's no detection/surfacing of downstream 401/403 beyond the raw error (`engine.go:~1134`, channels). 🔎 (Pairs with Report 1 S1.3's error classification.)
- **Guardrail:** Detect 401/403 from providers/channels, mark the dependency unhealthy (feeds S2.13/S2.15), and support hot credential reload via the existing config-reload path.

---

## Updated priority shortlist (merging both reports)

If I were sequencing the operability work, top to bottom:

1. **S2.1 — Recover panics on the channel/cron worker path.** ✅ confirmed; one bad tool result on a Telegram message currently crashes the whole gateway. Cheap, highest leverage. 🔴
2. **S3.1 (Report 1) — Per-run/per-day spend ceiling with mid-loop enforcement.** The biggest *cost* exposure. 🔴
3. **S1.1 / S1.2 (Report 1) — Mandatory provider/model validation + reachability probe.** Kills the most common "green but broken" failure, especially for crons. 🔴
4. **S2.2 — Bounded, ordered graceful shutdown + close the inbox + register the leaking goroutines.** Stops restart hangs and leaks. 🟠
5. **S2.13 + S2.14 + S2.12 — Deep health check, the missing metrics, and a live active-runs view.** You can't operate what you can't see; this also makes every other fix verifiable. 🟠
6. **S2.7 / S2.8 — Delivery dedup + platform-aware send (chunk/retry/classify).** Removes duplicate-reply cost and silent message loss. 🟠
7. **S2.4 / S2.5 — Supervise the watcher; make reload re-registration atomic.** Stops silent "my edits don't apply" and orphaned crons. 🟠
8. **S2.9 / S2.10 — Process-group kill + global subprocess cap.** Closes the orphan/fork-bomb vector. 🟠

Items 1–3 are the "stop the bleeding" set; 4–8 are "make it operable and predictable."

---

## One-line framing per theme

- **Lifecycle:** *A panic on a Telegram message should not kill the gateway.*
- **Hot-reload:** *A watcher that dies silently is worse than no watcher.*
- **Delivery:** *At-least-once is fine — at-least-once with double billing is not.*
- **Outbound:** *A 4097-character answer should still arrive.*
- **Subprocess:** *Kill the tree, cap the count.*
- **Observability:** *Green health with a dead socket is a lie you'll pay for at 2am.*
- **Durability:** *The crash path is already good — protect the upload and the schema.*
