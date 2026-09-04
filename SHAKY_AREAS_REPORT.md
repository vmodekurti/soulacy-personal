# Soulacy — Operational Fragility Review (User-Story Format)

*Review date: 2026-06-16. Read-only analysis. Complements `AUDIT_REPORT.md` (security/hygiene) and `improvements.md` (backlog). This report deliberately covers the gap those two under-address: **runtime robustness** — wrong provider/model picks, timeout blowouts, cost runaways, and the missing guardrails that turn a misconfiguration into an outage or a surprise bill.*

> File:line references come from a code read and should be spot-verified before acting — a few may have drifted. The *behaviors* described were each traced to real code paths.

---

## How to read this

Each item is a **user story** (`As a … I want … so that …`) plus four things: the **rationale** for why it matters, the **current state** in code, the **blow-out scenario** (the concrete way it bites), and the **guardrail** to build. Items are grouped by theme and tagged with a rough **severity × likelihood**.

Legend: 🔴 high impact · 🟠 medium · 🟡 low. Likelihood: ▲ common · ◆ occasional · ▽ rare.

---

## Theme 1 — Provider & model selection (the "did it even pick the right brain?" problem)

### S1.1 — Fail fast on a missing/unreachable provider 🟠 ◆
**As an** operator deploying an agent, **I want** the system to refuse to run (or clearly warn) when an agent's provider isn't actually usable, **so that** I find out at deploy time, not when a user gets a cryptic error.

- **Rationale:** A typo in `llm.provider`, or an empty `provider` with a misconfigured `default_provider`, is a config mistake that should surface immediately. Today it can slip through to runtime.
- **Current state:** `agentvalidate` does check provider registration — unknown provider in the running gateway is an *error*, but "empty provider + no default" is **warn-only** (`internal/agentvalidate/validate.go:~137`). At call time, `Router.Complete` hard-errors with `"llm: unknown provider %q"` (`internal/llm/router.go:~63`). There is **no reachability preflight** — a registered-but-down provider (wrong base_url, dead Ollama host, bad key) is only discovered on the first real message.
- **Blow-out scenario:** Agent deploys "green," sits on a channel, and the first user message returns a raw `connection refused` / `401`. No health signal warned you.
- **Guardrail:** Promote "empty provider + no default" to a hard error. Add a boot-time/`sy doctor` **provider reachability probe** (cheap `Models()` call per configured provider) that marks providers healthy/unhealthy and is visible in the GUI and `/health`.

### S1.2 — Validate the model name before runtime 🔴 ▲
**As an** operator, **I want** a wrong/nonexistent model name caught at load time, **so that** I don't ship an agent that 404s on every call.

- **Rationale:** Model names are the single most error-prone field — vendors rename them constantly (`claude-sonnet-4-5` vs `claude-3-5-sonnet-latest`), and Ollama models must be *pulled* before use. The name is sent verbatim to the provider (`anthropic.go`, `ollama.go`, `openai.go`, `gemini.go` `Complete()`).
- **Current state:** Model validation is **load-time only and best-effort** — it runs only if `ProviderModels` happens to be populated (`agentvalidate/validate.go:~150`). If that map is empty (common), validation is **silently skipped** and the bad name is discovered at call time as a provider 404.
- **Blow-out scenario:** You point an agent at a model you forgot to `ollama pull`, or a deprecated cloud model. Every invocation fails. With a scheduled agent, it fails silently on a cron forever (see S7.2).
- **Guardrail:** Make model validation mandatory: fetch and cache `Provider.Models()` at boot, validate every agent's model against it, and **fail the agent (not the gateway)** with a clear "model X not available on provider Y; did you mean Z / run `ollama pull X`." Re-validate on hot-reload.

### S1.3 — Differentiate and surface provider failure classes 🟠 ▲
**As a** user/operator, **I want** distinct, actionable errors for "provider down" vs "auth bad" vs "model missing" vs "context too large," **so that** I can fix the right thing fast.

- **Rationale:** All four are common and have completely different fixes; today they arrive as one opaque error string.
- **Current state:** `engine.Handle` wraps the provider error as `fmt.Errorf("engine: llm call: %w", err)` and emits it (`internal/runtime/engine.go:~1707`). No structured error code. Retry covers only 408/429/5xx + transport errors (`internal/llm/retry.go:~87`); 400/401/403/404 pass straight through (correct — but the message isn't classified).
- **Guardrail:** Map provider HTTP statuses to a small typed error enum (`ErrProviderUnavailable`, `ErrAuth`, `ErrModelNotFound`, `ErrContextExceeded`, `ErrRateLimited`) and render user-facing, remediation-oriented messages. This also unlocks S1.4 and S5.x.

### S1.4 — Optional provider/model fallback 🟡 ◆
**As an** operator running production agents, **I want** an optional fallback chain (e.g., primary cloud model → cheaper/backup model), **so that** a single provider outage doesn't take the agent down.

- **Rationale:** Single-provider agents have zero redundancy.
- **Current state:** `Router.Complete` dispatches to exactly one provider; **no fallback** (`router.go:~55`).
- **Guardrail:** Allow `llm.fallback: [{provider, model}, …]`; on a *retryable-exhausted* or `ErrProviderUnavailable`, try the next entry. Keep it opt-in so cost/behavior stays predictable.

---

## Theme 2 — Timeouts & long generations (the "it hung / it got killed mid-thought" problem)

### S2.1 — Coherent, layered timeout model 🔴 ▲
**As an** operator, **I want** the interaction between `run_timeout`, `tool_timeout`, and the LLM HTTP timeout to be predictable and documented, **so that** I'm not guessing which one fired.

- **Rationale:** Three independent clocks race. Whichever fires first wins, and the resulting error (`context deadline exceeded`) doesn't say which one it was.
- **Current state:**
  - Ollama HTTP timeout is **deliberately 0 / disabled** (`internal/llm/ollama.go:~46`) because a 120s cap was killing large local models loading from disk. Upper bound becomes the agent's `run_timeout` context — **and if `run_timeout` is unset, there is effectively no ceiling.**
  - Anthropic/Gemini use 180s, OpenAI 120s (per-provider hardcoded).
  - `run_timeout` defaults to 5 min (per `FRAMEWORK_OVERVIEW`); `tool_timeout` default 120s.
- **Blow-out scenario A (false kill):** A genuinely slow 72B local model on a cold load gets cancelled because the agent's `run_timeout` was left at a low value — the user sees a deadline error on a request that *would* have succeeded.
- **Blow-out scenario B (hang forever):** Ollama timeout disabled **and** `run_timeout` unset → a wedged generation hangs a worker indefinitely, permanently consuming one of the 100 worker slots.
- **Guardrail:** (a) Always set a non-zero effective `run_timeout` ceiling (refuse `0`/unset → apply a sane default and log it). (b) Tag timeout errors with their source ("agent run_timeout", "tool_timeout", "llm http"). (c) Document the precedence rules in one place. (d) Consider a separate, generous "first-token" timeout for local models instead of an all-or-nothing disable.

### S2.2 — Cold-load awareness for local models 🟡 ◆
**As a** user of Ollama-backed agents, **I want** the system to distinguish "model is loading" from "model is stuck," **so that** first-call latency doesn't look like a failure.
- **Guardrail:** Surface Ollama load state (the `/api/ps` or load events) as a "warming up" status rather than a silent multi-minute stall; optionally pre-warm models named by enabled agents at boot.

---

## Theme 3 — Cost & runaway protection (the "surprise bill / infinite loop" problem)

### S3.1 — A hard per-run / per-day spend ceiling 🔴 ◆
**As an** operator, **I want** a configurable token/cost budget that **stops** a run (and alerts) when exceeded, **so that** a runaway loop or prompt-injection can't drain my API credits.

- **Rationale:** This is the single biggest missing guardrail. Cost is **recorded but never enforced.**
- **Current state:** `internal/costs/` writes usage to SQLite after the fact; `recordUsage()` is a no-op when no store is set. Token quotas in `internal/ratelimit` are **checked, but recording happens after the run completes** — so a single run that blows 10× the daily quota completes fully, *then* trips the limit for the *next* run. There is **no mid-run budget check** and **no global cost ceiling**.
- **Blow-out scenario:** Worst-case fan-out from a *single inbound message* is large: peer recursion depth 5 × `max_turns` (default 20, no hard cap) × multiple tool calls per turn ≈ **hundreds of LLM calls**. Add the final-synthesis call that fires *outside* the turn counter. One bad message → a very real bill.
- **Guardrail:** Introduce a `budget:` block (per-run token cap, per-agent/day cap, optional dollar estimate using the price table) checked **before each LLM call inside the loop** — abort with a clear "budget exceeded" terminal message when crossed. Make recording synchronous enough that quotas can't be evaded by a single oversized run.

### S3.2 — A real upper bound on `max_turns` and recursion 🟠 ◆
**As an** operator, **I want** the framework to enforce a ceiling on `max_turns` and total LLM calls per inbound message, **so that** a misconfigured agent can't self-authorize 10,000 turns.

- **Current state:** `default_max_turns` is 20; engine floors `≤0` to 10 but enforces **no upper cap** (`engine.go:~1654`). Recursion depth cap is 5 (`maxAgentCallDepth`), which is good, but it *multiplies* with turns and tool fan-out, and there's no aggregate counter across the whole run tree.
- **Guardrail:** Add a config max (e.g., `runtime.max_turns_ceiling`) that clamps any agent's `max_turns`, and a **global per-message LLM-call counter** shared across peer recursion that terminates the whole tree when exceeded.

### S3.3 — Harden the anti-loop guard against arg-jitter 🟠 ◆
**As an** operator, **I want** the duplicate-call guard to resist trivial argument variation, **so that** a model can't evade it and bill me for the same work 50 times.

- **Rationale:** The guard memoises on exact `name + json(args)`. A model that appends a space or reorders keys evades it; and the loop only short-circuits when **every** call in a turn is a duplicate.
- **Current state:** `engine.go:~1640` (seen map), `~1778` (all-duplicate short-circuit).
- **Guardrail:** Normalize args before hashing (canonical JSON, trim/lowercase where safe); track a per-run repeat *count* per normalized call and nudge/terminate after N near-identical calls even if not all calls in the turn are dupes.

---

## Theme 4 — Rate limiting & abuse (the "spammer drains my wallet" problem)

### S4.1 — Safe-by-default rate limits 🟠 ▲
**As an** operator, **I want** sensible default rate/token limits out of the box, **so that** a channel spammer can't trigger unbounded LLM spend.

- **Rationale:** The worker pool caps *concurrency*, not *spend*. 100 concurrent runs × hundreds of LLM calls each is still a lot of money, and inbound messages over the buffer are silently dropped (no backpressure to Telegram/Discord).
- **Current state:** All `ratelimit` knobs (`PerUserRPM`, `PerAgentRPM`, `PerUserTokensDay`, `PerAgentTokensDay`) **default to disabled** (`internal/ratelimit/config.go`). Inbox buffer is a hardcoded 512 with drop-on-full (`internal/channels/channel.go`, `wire.go:~260`).
- **Blow-out scenario:** A public Telegram bot gets spammed; even with drops, the first 100 messages each kick off full agent runs and bill you.
- **Guardrail:** Ship conservative non-zero defaults for per-user/per-agent RPM and daily token caps; emit a startup warning when limits are disabled on a non-localhost bind. Surface inbox-drop counts prominently (they're counted but easy to miss).

---

## Theme 5 — Context window & token management (the "400: context too large" problem)

### S5.1 — Preflight context-window awareness 🔴 ▲
**As an** operator, **I want** the engine to know each model's context limit and manage history/tools against it, **so that** long sessions don't fail with a mid-run 400.

- **Rationale:** No token counting happens before the call. History windowing exists (`maxHistoryTurns`, default ~100) but is **turn-based, not token-based** — a small-context model (4K) with 100 turns still overflows.
- **Current state:** `engine.go:~1660` builds the request with no token budget; KB chunking has a `chars/4` heuristic but it isn't applied to prompt assembly. A `context_length_exceeded` returns as a 400, which is **not retried** (`retry.go:~87`).
- **Blow-out scenario:** A busy agent slowly accretes history until one day every call 400s, and because 400 isn't retried there's no graceful degradation.
- **Guardrail:** Maintain a per-model context-window table; estimate tokens for (system + tools + history + KB results) and **trim/summarize to fit before calling**; on a context-exceeded 400, auto-trim oldest history and retry once.

---

## Theme 6 — MCP & external dependency resilience (the "tool server vanished" problem)

### S6.1 — Graceful MCP degradation + reconnect 🟠 ◆
**As an** operator, **I want** MCP server failures to degrade gracefully and reconnect, **so that** a flaky `npx` server doesn't silently strip an agent's tools or hang a run.

- **Rationale:** MCP servers are external processes/endpoints that fail in normal operation (npx not installed, server crash, HTTP down).
- **Current state:** Boot is non-fatal and parallel with a 120s per-server connect timeout — *good* (`internal/mcp/mcp.go:~144`). But: a dead server's tools simply **vanish from the catalog** with no signal to the agent author; tool calls to a crashed subprocess error out with **no auto-reconnect**; nothing prevents the LLM from requesting a now-offline tool.
- **Blow-out scenario:** An MCP server the agent depends on dies; the agent quietly loses a capability and starts giving wrong/incomplete answers with no error.
- **Guardrail:** Add MCP health status to `/health` and the GUI; auto-reconnect with backoff on transport death; when a declared-but-offline tool is requested, return a clear "tool temporarily unavailable" result rather than a generic failure.

---

## Theme 7 — Scheduler safety (the "cron that eats itself" problem)

### S7.1 — Cron sanity validation 🟠 ◆
**As an** operator, **I want** absurd schedules (every second) and overlap hazards rejected or flagged, **so that** a fat-fingered cron can't DoS my own gateway.

- **Current state:** Expressions are parsed for validity, but there's **no interval-sanity check** (`scheduler.go:~193`) — `* * * * * *` (every second) is accepted. Overlap prevention (`TryStartRun`) uses a hardcoded ~1h `maxRunDuration`, which can mis-handle agents whose `run_timeout` exceeds it.
- **Guardrail:** Warn/reject sub-minute intervals (configurable floor); derive the overlap window from the agent's actual `run_timeout` rather than a fixed hour.

### S7.2 — Visibility + backoff for chronically failing scheduled agents 🟠 ▲
**As an** operator, **I want** to be told when a scheduled agent fails every run, **so that** a broken cron agent (bad model name, dead provider) doesn't fail invisibly for weeks.

- **Rationale:** This is where S1.2/S1.1 hurt most — a scheduled agent with a bad model name 404s on every fire with no human watching.
- **Current state:** A failed scheduled run logs and exits; next fire is the next interval. No retry storm (good), but **no alerting and no backoff** for persistent failure.
- **Guardrail:** Track consecutive-failure counts per scheduled agent; surface in GUI/`/health`; optionally auto-disable after N consecutive failures with a clear status, and apply backoff for transient errors.

---

## Theme 8 — Config validation & fail-fast (the "5 vs 5s" problem)

### S8.1 — Validate config and SOUL.yaml at boot, reject the pathological 🟠 ▲
**As an** operator, **I want** the gateway to validate durations, numeric ranges, and required fields at startup and refuse to start (or loudly warn) on garbage, **so that** I don't get silent fallbacks that mask my mistake.

- **Rationale:** Several fields silently fall back to defaults on parse failure, hiding the error.
- **Current state:** Config is unmarshalled with viper defaults but values aren't range-validated (`internal/config/config.go`). `tool_timeout` is a *string* parsed late in `wire.go:~112` — **unparseable → silent fallback to 30s, no error**. `max_turns` accepts any int including negatives (floored later) and absurd highs (no cap). No rejection of `tool_timeout: 1000h` etc.
- **Blow-out scenario:** You write `tool_timeout: 120` (missing the `s`), it silently becomes 30s, and your long tools start getting killed — with nothing in the logs pointing at the typo.
- **Guardrail:** Add a `config.Validate()` pass at boot: parse all durations eagerly and **error on failure**, range-check numerics (`max_turns`, pool sizes, buffers, timeouts) with sane min/max, and log every applied default explicitly. Extend `agentvalidate` to reject negative/over-ceiling `max_turns`.

---

## Theme 9 — Embedder / KB lifecycle (the "RAG silently broke" problem)

### S9.1 — Guard embedder changes against existing KBs 🟠 ◆
**As an** operator, **I want** the system to detect when a KB's embedder is gone or its dimensions changed, and tell me how to fix it, **so that** RAG doesn't silently start failing or returning nothing.

- **Rationale:** KBs bind to a specific embedder provider/model/dimension at creation. Changing embedder config later breaks them.
- **Current state:** At query time, a missing embedder errors `"no embedder registered for provider"` (`internal/knowledge/service.go:~114`) and a dimension mismatch errors `"query vector dim != kb dim"` (`store.go:~521`). Correct failures — but only **at query time**, with no migration path or boot-time warning.
- **Blow-out scenario:** You swap `nomic-embed-text` (768) for an OpenAI embedder (1536); every `kb_search` on old KBs now errors, and agents quietly lose their knowledge.
- **Guardrail:** At boot, cross-check every KB's recorded embedder against the configured registry and warn on mismatch; offer a re-embed/migration command; consider per-KB embedder pinning that's independent of the global default.

---

## Theme 10 — Structured output & tool_choice reliability (the "the model ignored me" problem)

### S10.1 — Don't pair weak models with structured output silently 🟠 ◆
**As an** operator, **I want** a hard stop (or very loud warning) when a small/quantized/embedding model is asked for `output_schema` or forced `tool_choice`, **so that** I don't ship an agent that fails to comply at runtime.

- **Rationale:** Local models notoriously ignore `tool_choice` and produce invalid JSON. The framework already has an auto-delegation workaround and a final-synthesis retry, but the root mismatch is detectable up front.
- **Current state:** Load-time `weakStructuredOutputModel` check exists but is **warn-only** (`agentvalidate/validate.go:~163`). Structured-output enforcement retries **once**, then **returns the invalid content anyway** (`engine.go:~1841`) — no schema guarantee to the caller.
- **Guardrail:** Make the weak-model + structured-output combination an error (or require an explicit `i_know_this_model_is_weak: true` acknowledgment). On final structured-output failure, return a typed error rather than silently emitting unvalidated content, so downstream consumers can branch.

---

## Theme 11 — Session & memory lifecycle (cross-ref, confirmed live)

### S11.1 — Bounded sessions and history 🟠 ▲
**As an** operator running concurrent agents, **I want** session eviction and token-aware history windowing, **so that** a long-lived gateway doesn't grow RSS until OOM.

- **Status:** Already captured in `improvements.md` (PERF-1/PERF-2, raised in priority) and `AUDIT_REPORT.md` (H6). Reconfirmed here because it directly interacts with S5.1 (context overflow) and S2.1 (stuck workers). `sessions.LoadOrStore` has no `Delete`/eviction; history append has no token cap.
- **Guardrail:** TTL/LRU session eviction + token-based (not just turn-based) history windowing. (No new work item — pointer to existing backlog.)

---

## Theme 12 — Security defaults that double as reliability risks (cross-ref)

These are fully covered in `AUDIT_REPORT.md`; flagged here only where they *also* create operational fragility:

- **`shell_exec` default-on (C3)** — beyond the RCE risk, it means any prompt-injected message can spawn arbitrary host subprocesses, which is also a *resource/cost* blow-out vector. Gating it (SEC-3) closes both.
- **Auth default-open on non-localhost (C4)** — an open gateway is also an open door to the cost-runaway vectors in Themes 3 & 4. SEC-4's hard-fail closes both.
- **Env inheritance into tools (H2)** — secrets in tool env is a security issue; it's also why a buggy tool can reach the network/credentials unexpectedly.

---

## Priority shortlist (if you only do five things)

1. **S3.1 — Per-run/per-day spend ceiling with mid-loop enforcement.** The biggest missing guardrail; one bad message can otherwise rack up hundreds of LLM calls. 🔴
2. **S1.2 + S1.1 — Mandatory model/provider validation at load + reachability probe.** Kills the most common "agent is green but 404s forever" failure, especially for cron agents (S7.2). 🔴
3. **S2.1 — Coherent timeout model with a non-zero `run_timeout` floor and source-tagged timeout errors.** Stops both false kills and forever-hangs. 🔴
4. **S5.1 — Token-aware context management with auto-trim-and-retry on context-exceeded.** Removes the slow-motion 400 that eventually hits every busy agent. 🔴
5. **S8.1 — Boot-time config validation that errors on bad durations/ranges instead of silently defaulting.** Cheap, and it surfaces every other misconfiguration earlier. 🟠

---

## One-line framing for each theme

- **Providers/models:** *Validate the brain before you trust it.*
- **Timeouts:** *Three clocks, one story — and never zero.*
- **Cost:** *Record-only is not a guardrail; enforce a ceiling.*
- **Rate limits:** *Concurrency caps protect the box, not the bill.*
- **Context:** *Count tokens before the provider does it for you.*
- **MCP:** *External tools fail; degrade loudly and reconnect.*
- **Scheduler:** *A cron with no sanity check is a self-inflicted DoS.*
- **Config:** *Fail fast and loud beats a silent default.*
- **Embedders:** *Changing the embedder orphans your knowledge.*
- **Structured output:** *Weak models say "no" at runtime — catch it at deploy.*
