# Soulacy — Post-Audit Roadmap & Story Backlog

Derived from AUDIT_REPORT.md (2026-06-10) + owner decisions. Stories are sized S (<2h), M (half-day), L (1–2 days). Work milestones in order; within a milestone, stories without dependencies can run in any order. Story IDs use theme prefixes: SEC (security), HYG (hygiene), CI, TEST, ARCH, PERF, DOC, DEP, REL, SDK.

Suggested first week: SEC-1 → SEC-2/HYG-1 → CI-1..CI-4 → SEC-4 → ARCH-1 (everything else builds on a clean, gated repo).

## Milestone 0 — Safety Net

Goal: nothing can regress while you fix things. All CI stories are independent; land as separate small PRs.

### CI-1 · Add golangci-lint to CI — S
Add a minimal `.golangci.yml` (govet, staticcheck, unused, errcheck) and a lint step to `ci.yml`. Configure errcheck to permit explicit `_ =` initially (132 sites exist — ARCH-1 fixes the dangerous ones; ratchet later). Tune config until current main passes — do not bulk-suppress with `//nolint`. Files: `.github/workflows/ci.yml`, `.golangci.yml` (new). AC: CI fails on a deliberately introduced lint error; green on main. The existing `make lint` target (Makefile:79) and CI use the same config.

### CI-2 · Race detector in CI — S
Change the test step to `CGO_ENABLED=1 go test -race -timeout 120s ./...` (matches the local convention; current CI uses 60s, Makefile uses 30s — align all three to 120s). Files: `ci.yml:44`, `Makefile:76`. AC: CI runs with `-race`; any data races found are fixed (budget for this — don't disable), suite green.

### CI-3 · Secret scanning in CI — S
Add a `gitleaks` job as the first job in `ci.yml`. Add a config allowlisting `.env.example` placeholder patterns if needed. Files: `ci.yml`, `.gitleaks.toml` (optional). AC: CI fails when a key-shaped string is committed; green after SEC-2. Depends: SEC-2 (history must be clean or scan scoped to diff).

### CI-4 · govulncheck in CI — S
Add `govulncheck ./...` step to the Go job. Files: `ci.yml`. AC: CI surfaces vuln findings; green after DEP-1 remediation.

### CI-5 · GUI + Python tests in CI — S
GUI job: add `npm test` (vitest suite exists at `gui/src/lib/*.test.js`, currently never run in CI). Python job: add `pytest` invocation (suite is empty today — step passes trivially until SDK-1/tests exist, but the gate is in place). Files: `ci.yml:60-85`. AC: A failing vitest test fails CI.

### TEST-1 · Characterization tests for builtin tools — M
Before any engine.go surgery, pin current behavior of every builtin tool in `buildSystemTools` (engine.go:873-1624): shell_exec (timeout clamp at 600s, output capture), http_request (SSRF guard, byte caps), find_files, fetch_url, file tools. Pure-Go, no network — follow the existing `engine2_test.go` style ("no real LLM, no httptest server"). Files: `internal/runtime/engine_tools_test.go` (new). AC: ≥1 behavior-pinning test per builtin tool; documents the contract ARCH-2 must preserve.

### HYG-1 · Tag + branch protection — S
Tag `pre-audit-fixes`; enable branch protection on main requiring green CI. AC: Tag pushed; direct pushes to main blocked. Depends: CI-1, CI-2.

## Milestone 1 — Critical Fixes

### SEC-1 · Rotate every leaked credential — M (operational, do FIRST)
Treat all of these as compromised: Anthropic (`config.dev.yaml:47`), Groq (:58), NVIDIA (:63), Slack app+bot tokens (:14,16), Telegram bot (:28), AlphaVantage (:90), Rocket Money/auth0 cookies (:110), gateway admin key (:122, also verbatim in `docs/SESSION_HANDOFF.md`). Re-pair the WhatsApp session (its keys are committed to git — see SEC-2). Rotate before purging history. AC: Old keys confirmed revoked at each provider; new keys live only in gitignored files; gateway key regenerated.

### SEC-2 · Purge committed credentials & junk from git history — M
`git filter-repo --invert-paths --path .soulacy/ --path SESSION_HANDOFF.md` plus the two committed `gui/vite.config.js.timestamp-*.mjs` files, `run-story1-tests.command`, `run_memory_tests.sh`. Force-push; invalidate forks. Note: rewrites all SHAs — commit references inside docs go stale (acceptable). AC: `git log --all -- .soulacy/` empty; `git ls-files | grep -c '^\.soulacy/'` = 0; gitleaks full-history scan clean. Depends: SEC-1.

### HYG-2 · Harden .gitignore — S
Add `.soulacy/`, `SESSION_HANDOFF.md`, `*.command`, `deploy.sh` (or remove the files — see HYG-3). Verify with `git status` after touching a WhatsApp session locally. Files: `.gitignore`. AC: Running the gateway with a WhatsApp channel produces zero untracked-file noise. Depends: SEC-2.

### SEC-3 · Gate shell_exec: default off + per-agent capability — M
Flip `v.SetDefault("runtime.allow_system_tools", true)` → `false` (`internal/config/config.go:487`). Partition builtins into safe vs system (shell_exec, run_script, file-write); register system tools only when the global flag AND a new per-agent `capabilities: [system]` SOUL.yaml field both agree. Startup log lists agents with system tools. Breaking change accepted by owner — CHANGELOG entry + examples updated. Gotchas: grep `sy doctor` and studio plugin for shell_exec assumptions; GUI tool-list endpoint must reflect per-agent availability. Files: `internal/config/config.go:487`, `internal/runtime/engine.go:873ff`, `internal/runtime/builder.go`, `examples/`, docs. AC: Fresh install: shell_exec absent from `/api/v1/.../tools`; enabled agent gets it; TEST-1 suite green (updated for gating). Depends: TEST-1.

### SEC-4 · Auth hard-fail on non-localhost empty key — S ⚡ quick win
In `internal/auth/engine.go:146-150` / startup wiring: empty key + bind ≠ 127.0.0.1/::1 → exit non-zero with a clear message and remediation. Keep warn-only for localhost. Provide `--allow-unauthenticated` escape hatch. Reword `.env.example:14` ("Leave empty to disable auth" → explain localhost-only behavior). Also fix `docker-compose.yml:53` empty-key default. AC: `SOULACY_HOST=0.0.0.0` + empty key refuses to start; test added; compose ships with key required.

### SEC-5 · Env allowlist for tool execution — M
Replace `os.Environ()` inheritance (`internal/sandbox/wrap_unix.go:73`) with an allowlist (PATH, HOME, LANG, TMPDIR + per-agent declared vars via a SOUL.yaml `env:` block). This is the final sandbox scope per the single-user-local-first decision. Files: `internal/sandbox/wrap_unix.go`, `internal/executor`, builder/schema. AC: Test asserts a spawned tool cannot see `ANTHROPIC_API_KEY`; declared vars pass through. Depends: TEST-1.

### SEC-6 · Honest sandbox documentation — S ⚡ quick win
Document exactly what the sandbox does (rlimits CPU/AS/NOFILE/FSIZE; env allowlist after SEC-5) and does NOT do (no filesystem/network/namespace isolation; RLIMIT_AS advisory on macOS; setrlimit failure non-fatal per `sandbox.go:122-127`). Add to docs + a doc comment in the package. AC: docs page exists; README makes no isolation claims beyond reality.

### ARCH-1 · Stop swallowing data-path errors — S ⚡ quick win
Log-or-return at: `_ = e.memory.Write` (`engine.go:1903,2209`), `_ = e.brainStore.Write` (:2224), `_ = s.scheduler.RegisterAgent` (`gateway/api.go:256,297,3006`), `_ = s.loader.Upsert` (:385), `_ = c.BodyParser` (:2980). Follow the existing logged-failure convention at engine.go:1721-23. AC: Zero unlogged `_ =` on those paths; failing scheduler registration surfaces in the create-agent response or logs.

### DEP-1 · Dependency vulnerability pass — M
Run `govulncheck ./...` and `npm audit` (gui/). Patch-upgrade fiber v2.x, fasthttp, gorilla/websocket, nats.go, x/* as indicated. Investigate the anomalous `golang.org/x/net v0.51.0` pin (`go.mod:71`). Stay on fiber v2 — no v3 migration. AC: Zero Critical/High from govulncheck; `npm audit` clean or waived with rationale; contract tests + TEST-1 green. Depends: CI-4, TEST-1.

## Milestone 2 — High-Leverage Improvements

### ARCH-2 · Split buildSystemTools out of engine.go — L
Extract the 751-line `buildSystemTools` (engine.go:873) into per-domain files using the existing `BuiltinTool` struct (engine.go:70): `engine_tools_shell.go`, `engine_tools_http.go`, `engine_tools_files.go`, `engine_tools_misc.go`. Mirror the precedent at engine.go:1773 (`dispatchRouter` split). Pure mechanical move — no behavior change. AC: engine.go <2,000 lines; TEST-1 suite green unchanged; `git diff --stat` shows moves, not rewrites. Depends: TEST-1, SEC-3.

### ARCH-3 · Gateway error-response helper — M
Add `s.errJSON(c *fiber.Ctx, status int, err error)` and sweep the ~245 hand-rolled `c.Status(...).JSON(fiber.Map{"error": ...})` sites (93×400, 76×500, 31×503, 30×404; e.g. api.go:252,265,292). Contract tests pin the envelope — extend them first if you want request IDs in errors. Files: `internal/gateway/*.go`. AC: One helper; `grep -c 'JSON(fiber.Map{"error"' internal/gateway/*.go` ≈ 0 outside it; contract tests green.

### PERF-1 · Session eviction — M
Add TTL/LRU eviction to `e.sessions` (engine.go:2387-2400 — currently `LoadOrStore` with no `Delete` anywhere). Config: `runtime.session_ttl` (default e.g. 24h) + max-sessions cap. Sweep on a ticker; persist-then-evict if memory backend present. AC: Eviction unit test; soak test (1k sessions) shows bounded RSS; active sessions never evicted mid-conversation.

### PERF-2 · History windowing — M
Cap per-session `History` (appended at engine.go:1914,2143,2147,2203 with no trim). Config: max turns and/or max bytes; trim oldest first, keep system prompt. Generous default (e.g. 100 turns) — this changes agent context behavior, so document it. AC: Test: 500-turn session holds ≤ window; LLM context assembly unchanged within window. Depends: PERF-1 (same code area — sequence to avoid conflicts).

### PERF-3 · Action-log rotation — M
Size/age-based rotation for per-agent JSONL files (`internal/actionlog/` — currently grow forever). Keep N rotated files, gzip old ones. AC: Rotation test; long-running agent's log dir bounded.

### PERF-4 · Streaming Tail — S
Rewrite `Tail` (actionlog.go:301-339) to read backwards in blocks instead of loading the whole file to return ≤5000 lines. AC: Tail on a synthetic 1GB file allocates O(limit); existing Tail tests green. Depends: PERF-3 (Tail must traverse rotated files or document current-file-only).

### TEST-2 · Postgres backend smoke tests — M
Postgres is advertised product surface (README docker-compose). Add CI service container + parity tests reusing the SQLite test cases for `internal/storage/postgres` (221 stmts, 0% coverage). AC: ≥30% coverage; CRUD + migration round-trip green in CI. Depends: CI-2.

### TEST-3 · MCP client tests — M
`internal/mcp` (331 stmts, 0%): fake MCP server in-process (httptest), cover handshake, tool listing, call/response, error paths. AC: ≥30% coverage; no network in tests.

### DOC-1 · One identity: org, Go version, installer — M
Canonical org = `vmodekurti/soulacy-personal` (owner decision). Fix README badges (:9-11 — also the malformed Docker badge at :11), dev clone (:198), ghcr references (`release.yml:10,150`); align Go 1.25 in `Dockerfile:28` + `release.yml:77`; fix README:38 "Go 1.22+" and :133 dead link (`docs/configuration.md` → `docs/configuration/index.md`); pick ONE installer story — recommended: make root `install.sh` download release tarballs (release.yml:52 comment already expects this) with build-from-source fallback; delete or merge `scripts/install.sh`; fix release notes referencing the never-uploaded `linux-install.sh` (release.yml:201-229). AC: Every URL in README resolves; fresh-machine install via the documented one-liner succeeds; one Go version everywhere.

### DOC-2 · Label NATS/Qdrant experimental — S ⚡ quick win
No known users, 0% tests (owner: "I don't know"). Mark experimental in docs + config comments + a startup log line when enabled. Not deprecation — promotion path is "someone asks + tests added." AC: Docs and config.yaml.example say experimental; startup log on use.

### ARCH-4 · Decompose wire.go Run() — L
Split the 1,324-line `Run()` (`internal/app/wire.go:74`) into per-subsystem constructors `(component, closer, error)` with an ordered shutdown stack (LIFO). Do AFTER TEST-2/TEST-3 exist — they're the net for wiring regressions. AC: Run() <300 lines; shutdown order unit-tested; full suite + a manual `soulacy serve` boot green. Depends: TEST-2, TEST-3.

## Milestone 3 — Quality & Polish

### ARCH-5 · Shared LLM translation layer — L
Extract message/tool-schema translation duplicated across 4 `Complete()` impls (gemini.go:66, anthropic.go:79, ollama.go:58,323). Move `OpenAIProvider` out of ollama.go into `openai.go`. Retry/HTTP layers already extracted (retry.go, httpclient.go) — follow that pattern. AC: One translation path; providers_conformance_test green; a tool-calling fix touches one file.

### SEC-7 · Unsigned-install friction — M
Warn loudly on plugin installs from registries without `signing_key` and on git-URL installs (installer.go:305 — zero verification today); require `--allow-unverified` to proceed. Document signing setup. AC: Unverified install blocked without flag; signed-registry path unchanged.

### DOC-3 · Community files + license fix — S ⚡ quick win
Add SECURITY.md (disclosure policy — non-negotiable for a `curl | bash` project), CONTRIBUTING.md, CHANGELOG.md (backfill from SEC-3's breaking change). Fix `sdk/python/pyproject.toml:10,17` MIT → Apache-2.0. AC: Files exist; licenses agree everywhere.

### PERF-5 · Ctx-aware backoff + /health leak — S ⚡ quick win
Replace bare `time.Sleep` in retry/backoff loops with `select { <-ctx.Done() / <-time.After }`: `telegram/adapter.go:119`, `slack/adapter.go:95,102`, `executor/pool/pool.go:354`. Fix `/health` (api.go:62-74) to use a ctx-timeout call instead of goroutine+channel race. AC: Adapter shutdown <1s in tests; no goroutine growth under repeated health probes with a blocked store.

### DOC-4 · Declare actionlog authoritative — S
Document `internal/actionlog` (SQLite) as THE incident-reconstruction record; demote `internal/audit` JSONL to optional debug output (config-gated, default off) or fold its fields into actionlog. (Auditor recommendation, owner deferred — revisit only if append-only compliance need appears.) AC: Docs page; audit JSONL off by default.

### SDK-1 · Python SDK: truth in advertising — S ⚡ quick win
PyPI publication unverified (pypi.org empty for `soulacy`). Mark `sdk/python` experimental in its README + root README:82-86; remove/soften `pip install soulacy`; drop the `|| true` hedge (Dockerfile:64). Publish later via CI once the SDK has tests (it has zero today). AC: README matches reality; Docker build honest.

### HYG-3 · Remove personal tooling — S ⚡ quick win
Owner confirmed personal-only: delete `deploy.sh`, `session7-*.command`, `build-and-restart.command`, `reinstall.sh`, `reinstall-from-scratch.command`, `run-story1-tests.command`, `run_memory_tests.sh` (move to a private scratch dir outside the repo). Keep gitignore patterns (HYG-2) so they never return. AC: Root `ls` shows product files only.

### REL-1 · Release hardening — M
Sign release artifacts (cosign or GoReleaser signing) over the existing checksums (release.yml:181-185); digest-pin `postgres` and replace `qdrant:latest` (docker-compose.yml:72,96); change default Postgres password from `soulacy` (:38,77); optional SBOM. AC: Release assets include signatures; compose has no `latest` tags and no default credentials.

### TEST-4 · Test ergonomics — S
Add `t.Parallel()` to independent gateway tests (none today — suite runs serially); rename `engine2..7_test.go` shards to match the ARCH-2 file split; delete stray `internal/gateway/nodir-agent` fixture dir and fix the test that writes outside `t.TempDir()`. Depends: ARCH-2. AC: Gateway suite wall-time drops; no stray dirs after `go test ./...`.

## Dependency graph (critical path)

```
SEC-1 → SEC-2 → HYG-2, CI-3
CI-1, CI-2 → HYG-1
TEST-1 → SEC-3, SEC-5, DEP-1 → ARCH-2 → TEST-4
PERF-1 → PERF-2        PERF-3 → PERF-4
CI-2 → TEST-2 ─┐
TEST-3 ────────┴→ ARCH-4
everything else: independent
```

## Quick wins — start today, any order ⚡
SEC-4 · SEC-6 · ARCH-1 · DOC-2 · DOC-3 · PERF-5 · SDK-1 · HYG-3

Story count: 33 (M0: 7 · M1: 9 · M2: 10 · M3: 7). Estimated total: ~4–6 focused weeks solo, with M0+M1 ≈ one week.
