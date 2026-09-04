# Robustness Hardening — Implementation Notes

Branch: `robustness/operability-hardening`. All changes build (`go build ./...`)
and ship with tests. Every item below is one (or more) commit.

## Landed (with tests)

| Story | What changed | Key files |
|-------|--------------|-----------|
| **S2.1 Panic isolation** | `recover()` in `engine.Handle` converts a panic on the channel/cron worker path (not covered by Fiber's recover) into a normal run error; metric `soulacy_agent_panics_total`. | `internal/runtime/engine.go` |
| **Story 5 / S8.1 Config validation** | `Config.Validate()` at boot: parses every duration, range-checks numerics; `tool_timeout: 120` now errors instead of silently → 30s. Adds `runtime.max_turns_ceiling`. | `internal/config/validate.go` |
| **Story 1 / S3.1 Cost budget** | SOUL.yaml `budget:` block (`max_tokens`, `max_llm_calls`) checked before every LLM call; halts with a terminal reply; metric `soulacy_agent_budget_halts_total`. | `internal/runtime/engine.go`, `pkg/agent/types.go` |
| **S3.2 max_turns ceiling** | Engine clamps any agent's `max_turns` to `runtime.max_turns_ceiling` (default 50). | `internal/runtime/engine.go` |
| **Story 3 Timeout attribution** | LLM errors name the clock: run cancelled (shutdown) vs `run_timeout` exceeded on provider/model. | `internal/runtime/engine.go` |
| **Story 6 Secure defaults** | `allow_shell: true` agent flag as a readable alias for `capabilities: [system]`. (SEC-3 gate + SEC-4 bind refusal already existed.) | `pkg/agent/types.go` |
| **Story 2 / S1.x Provider+model validation** | Boot probe of each provider's `Models()`; agents with an unavailable model are quarantined (disabled in memory) with an `ollama pull X`-style log; unreachable providers warned. Cron agents auto-disable after N consecutive failures. | `internal/gateway/server.go`, `internal/agentvalidate/validate.go`, `internal/runtime/loader.go`, `internal/scheduler/scheduler.go` |
| **Story 4 / S5.1 Context management** | Per-model context-window table + chars/4 estimator; oldest-first history trim before each call (preserves system block, no orphan tool-results); on a provider context-exceeded 400, halve history and retry once. | `internal/runtime/context_window.go`, `internal/runtime/engine.go` |
| **S2.8 Channel send chunking** | `SplitForLimit` chunks replies over Telegram 4096 / Discord 2000 (rune-safe, prefers line breaks); adapters surface API status≥400. | `internal/channels/chunk.go`, `…/telegram/adapter.go`, `…/discord/adapter.go` |
| **S2.4 Watcher liveness** | Hot-reload watcher tracks health and logs loudly if its fsnotify loop dies; `Healthy()` for deep health. | `internal/runtime/watcher.go` |
| **S2.13 Deep health** | `GET /health?deep=1` reports channel adapter connection state + watcher health, so it can't return `ok` while a socket is dead. | `internal/gateway/api.go`, `server.go` |
| **S2.2 Graceful shutdown** | Workers select on `ctx.Done()` instead of ranging the never-closed inbox (no more shutdown hang); session-eviction goroutine registered on the shutdown stack. | `internal/app/wire.go`, `wire_subsystems.go` |

New Prometheus metrics: `soulacy_agent_panics_total`, `soulacy_agent_budget_halts_total`.

## Already implemented in the codebase (verified — not re-done)

- SEC-4: non-localhost + empty `api_key` refusal (`checkAuthBindSafety`).
- SEC-3: system/shell built-ins gated behind per-agent `capabilities:[system]`.
- WAL + `busy_timeout=30s`; crash-recovery replay (`app/recover.go`); scheduler missed-fire catch-up; transactional KB ingest; correct subprocess stderr-drain; file-watcher already on the shutdown stack.

## How to test (this environment needs a couple of one-time setup steps)

CGO + sqlite-vec needs `sqlite3.h`. Reuse the header vendored in the
mattn/go-sqlite3 module so you don't have to fetch the amalgamation:

```bash
# 1. One-time: point CGO at a dir containing sqlite3.h
MATTN=$(go env GOMODCACHE)/github.com/mattn/go-sqlite3@v1.14.22
mkdir -p /tmp/sqlite-inc
cp "$MATTN/sqlite3-binding.h" /tmp/sqlite-inc/sqlite3.h
cp "$MATTN/sqlite3ext.h"      /tmp/sqlite-inc/sqlite3ext.h
export CGO_CFLAGS="-I/tmp/sqlite-inc"

# 2. The embedded GUI needs a dist dir to exist (placeholder is fine for backend):
mkdir -p internal/webui/dist && touch internal/webui/dist/.gitkeep

# 3. Build everything
go build ./...
```

Then run the tests:

```bash
# All the new/changed packages at once
go test ./internal/config/ ./pkg/agent/ ./internal/runtime/ \
        ./internal/agentvalidate/ ./internal/scheduler/ \
        ./internal/channels/... ./internal/gateway/ ./internal/app/

# Targeted: each story's regression tests
go test ./internal/runtime/        -run 'TestHandleRecoversPanic'            -v  # S2.1 panic isolation
go test ./internal/config/         -run 'TestValidate'                      -v  # S8.1 config validation
go test ./internal/runtime/        -run 'TestBudget|TestTurnsCeiling'       -v  # S3.1/S3.2 budget + ceiling
go test ./internal/agentvalidate/  -run 'TestDefinitionFailsUnavailableModelWhenAuthoritative' -v  # S1.x model validation
go test ./internal/scheduler/      -run 'TestCronAutoDisable'               -v  # S2.2/S7.2 cron auto-disable
go test ./internal/runtime/        -run 'TestModelContextLimit|TestTrim|TestIsContextExceeded' -v  # S5.1 context mgmt
go test ./internal/channels/       -run 'TestSplitForLimit'                 -v  # S2.8 send chunking
go test ./internal/runtime/        -run 'TestSessionEvictionStopsCleanly'   -v  # S2.2 shutdown/no-leak
go test ./pkg/agent/               -run 'TestAllowShell'                    -v  # Story 6 secure default

# Race detector over the concurrency-sensitive changes
go test -race ./internal/runtime/ -run 'TestSessionEvictionStopsCleanly|TestHandleRecoversPanic|TestBudget'
```

Manual smoke checks once a gateway is running locally:

```bash
# Deep health surfaces channel + watcher state (S2.13)
curl -s 'http://127.0.0.1:18789/health?deep=1' | jq

# Config validation refuses a bad duration (S8.1)
SOULACY_RUNTIME_TOOL_TIMEOUT=120 soulacy serve   # expect a startup error naming runtime.tool_timeout

# Budget halt (S3.1): set a tiny budget in an agent's SOUL.yaml and watch it stop
#   budget: { max_llm_calls: 1 }
```
