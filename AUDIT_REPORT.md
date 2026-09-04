# Soulacy — Technical Audit & Improvement Plan

*Audit date: 2026-06-10. Analysis only — no code was modified. Every file:line cited was read and verified; claims that could not be verified offline are explicitly labeled.*

---

## 1. Executive Summary

**Overall health: C+.** The core engineering is B+/A− quality — disciplined error wrapping, timing-safe auth primitives, parameterized SQL, real API contract tests, non-blocking hot paths, indexed tables — but the grade is dragged down hard by repo hygiene and security posture: live credentials committed to git, remote-code-execution enabled by default, and a gateway that runs wide open when no API key is set. These are launch-blocking for a project whose README invites `curl | bash`.

**Top 3 risks:** (1) 3,578 live WhatsApp session files including private keys are tracked in git (`.soulacy/whatsapp-web/default/creds.json`) — account hijack via repo access. (2) `shell_exec` gives every agent unsandboxed host shell access *by default* (`internal/runtime/engine.go:920` + `internal/config/config.go:487`), and auth is default-open (`internal/auth/engine.go:146-150`) — together, an empty-key deploy bound beyond localhost is unauthenticated remote shell. (3) Every external-integration layer (Postgres, MCP, NATS, Qdrant, executor, the entire `sy` CLI) has zero tests — divergence ships undetected.

**Top 3 opportunities:** (1) CI already has the bones; adding lint/race/secret-scan gates is cheap and locks in the existing quality culture. (2) Splitting `engine.go`'s 751-line `buildSystemTools` pays for itself immediately — it's also where the security-sensitive code lives. (3) The codebase's own conventions (`PRODUCTION_AUDIT →` markers, consumer-side interfaces, extracted `llm/retry.go`) show the team already knows the target patterns — most fixes are "finish what you started."

---

## 2. Repo Map

**Purpose.** Self-hosted AI agent runtime — "Ollama for agents." One Go binary serves REST + WebSocket + embedded Svelte GUI; agents are single `SOUL.yaml` files that call LLMs (Ollama/OpenAI/Anthropic/Gemini/Groq), Python tools, and channels (Slack/Telegram/Discord/WhatsApp).

**Stack.** Go 1.25 (`go.mod:3`), gofiber/fiber v2, SQLite (cgo: mattn + sqlite-vec) or Postgres, optional NATS + Qdrant, cobra/viper, zap, Prometheus + OTel. Svelte 4 + Vite GUI embedded via `go:embed`. Go SDK (`sdk/`, separate module) and Python SDK (`sdk/python`). ~115K LOC Go across ~50 `internal/` packages; 4,259 tracked files.

**Architecture.**

```
cmd/soulacy (gateway, thin main)        cmd/sy (CLI → gateway REST API)
        │
internal/app/wire.go  ── 1,324-line Run() wires everything
        │
internal/gateway (16.6K LOC: handlers, WS, embedded GUI)
        │
internal/runtime (15.7K LOC: engine, builtin tools, sessions)
        │
internal/llm ── providers          internal/channels ── Slack/TG/Discord/WA
        │
internal/storage · memory · knowledge   (SQLite default / Postgres)
cross-cutting: auth, rbac, sandbox (rlimit re-exec), plugins/pkgregistry,
               scheduler, events, audit + actionlog
```

**Key directories.** `internal/gateway` (API surface), `internal/runtime` (engine + tools), `internal/app` (wiring), `internal/llm`, `internal/channels`, `internal/studio` (visual builder plugin), `gui/`, `sdk/`, `docs/` (MkDocs), `examples/`.

**Maturity.** Late-stage solo project pushing toward public OSS release. Polished release pipeline and docs coexist with AI-session journals, `session7-*.command` one-offs, and a dev config full of live keys.

**Surprises.** 84% of tracked files are a committed WhatsApp session; `shell_exec` defaults on; test quality (where tests exist) is far above prototype norm.

**Review depth.** Deep: runtime, gateway, auth, sandbox, plugininstall/pkgregistry, config, storage/memory/actionlog, CI/build/release, llm providers. **Lighter review:** `internal/studio`, `reasoning`, `voice`, `workboard`, `knowledge` ingest internals, GUI Svelte components, Python SDK internals, docs prose beyond spot-checks.

---

## 3. Audit Report

Findings are labeled **[F]**act (verified at the cited location) or **[J]**udgment (interpretation/opinion).

### Critical

| # | Finding | Evidence | Why it matters |
|---|---------|----------|----------------|
| C1 | **[F]** Live WhatsApp credentials tracked in git: 3,578 files under `.soulacy/whatsapp-web/`, incl. `creds.json` with `noiseKey.private`, `signedIdentityKey.private` | `git ls-files` count verified; file contents read. No `.soulacy/` entry in `.gitignore` | Anyone with repo access hijacks the WhatsApp account; keys persist in history after deletion |
| C2 | **[F]** Live API keys on disk: Anthropic (`config.dev.yaml:47`), Groq (:58), NVIDIA (:63), Slack (:14,16), Telegram (:28), Rocket Money cookies (:110), gateway admin key (:122); duplicated in `docs/SESSION_HANDOFF.md:43,262,794` | Both files gitignored (`.gitignore:51,76`); `git grep sy_6697 HEAD` confirms **not** in git history | Plaintext on disk, inherited into every sandboxed tool's environment (see H2); treat as compromised, rotate all |
| C3 | **[F]** `shell_exec` runs LLM-supplied strings via `/bin/sh -c`, registered for every agent, default-on | `internal/runtime/engine.go:920`; `internal/config/config.go:487` (`SetDefault("runtime.allow_system_tools", true)`) | Prompt injection or a malicious SOUL.yaml = arbitrary code execution as the gateway user |
| C4 | **[F]** Gateway default-open: empty API key + no JWT/OIDC → all requests pass with only a log warning | `internal/auth/engine.go:146-150`; `.env.example:14` advertises "Leave empty to disable auth" | Combined with C3: unauthenticated remote shell on any non-localhost bind (Docker compose defaults key to empty, `docker-compose.yml:53`) |

### High

| # | Finding | Evidence | Why it matters |
|---|---------|----------|----------------|
| H1 | **[F]** Tracked junk: root `SESSION_HANDOFF.md` (88KB AI work journal; only `docs/` copy ignored, `.gitignore:51`), 2× `gui/vite.config.js.timestamp-*.mjs`, `run-story1-tests.command`, `deploy.sh` with `kill -9` on port (:63) | `git ls-files` verified | Leaks internal process; confuses contributors; bloats clones |
| H2 | **[F]** Sandbox is rlimits only: `RLIMIT_CPU/AS/NOFILE/FSIZE`, then exec with full inherited env, no namespaces/seccomp/network/FS isolation; setrlimit failure non-fatal | `internal/sandbox/wrap_unix.go:19-50,73`; `sandbox.go:122-127` | Python tools read every env var (incl. C2 keys), reach network, touch any file. **[J]** Fine for single-user local use; misleading if called a sandbox |
| H3 | **[F]** Swallowed errors on user-visible paths: `_ = e.memory.Write` (`engine.go:1903,2209`), `_ = e.brainStore.Write` (:2224), `_ = s.scheduler.RegisterAgent` (`gateway/api.go:256,297,3006`) | 132 non-test `_ =` sites total | Agent saves "OK" but its cron never registers; session memory silently lost. Logging convention exists (engine.go:1721-23) — just unapplied |
| H4 | **[F]** God files: `engine.go` 3,709 lines (`buildSystemTools` 751 lines at :873; `Handle` 495 lines at :1698-2193 with named-return-err/defer trap); `gateway/api.go` 3,009 lines/45 handlers; `app/wire.go Run()` 1,324 lines | Line counts verified | Security-critical shell code buried mid-function; highest merge-collision files; wiring/shutdown untestable |
| H5 | **[F]** Zero tests on integration layers: 0% coverage for `internal/mcp` (331 stmts), `storage/postgres` (221), `queue/nats`, `vector/qdrant`, `executor`, `telemetry`, `metrics`, all of `cmd/sy` (1,124 stmts); channels ≤13% | Parsed `coverage.out` + no `_test.go` files in those dirs | Postgres path can silently diverge from well-tested SQLite; CLI regressions invisible |
| H6 | **[F]** Unbounded session growth: `sessions.LoadOrStore` with no `Delete`/eviction anywhere in package; `History` appended every turn, no windowing | `engine.go:2387-2400, 1914, 2143, 2203` | Long-running gateway with many channel users grows RSS until OOM |
| H7 | **[F]** CI under-enforces: build + test(`-timeout 60s`) + vet only; no golangci-lint (target exists, `Makefile:79`), no `-race`, no coverage gate, GUI vitest and Python tests never run | `.github/workflows/ci.yml:40-44,60-85` | The team's own local bar (race, 120s) isn't enforced; quality relies on memory |
| H8 | **[F]** Identity/toolchain contradictions: org `soulacy/soulacy` (README:9-11,198) vs `vmodekurti/soulacy-personal` (README:33,58; mkdocs.yml:3-5); Go 1.25 (`go.mod:3`) vs 1.24 (`Dockerfile:28`, `release.yml:77`) vs "1.22+" (README:38); README:133 links nonexistent `docs/configuration.md`; release notes reference never-uploaded `linux-install.sh` (`release.yml:201-229`) | All verified | One set of install URLs is dead for users; builds depend on silent GOTOOLCHAIN downloads |

### Medium

| # | Finding | Evidence |
|---|---------|----------|
| M1 | **[F]** 245 hand-rolled gateway error responses (93×400, 76×500…), no helper | e.g. `api.go:252,265,292` |
| M2 | **[F]** 4 near-duplicate LLM `Complete()` impls; `OpenAIProvider` lives in `ollama.go:323` | `gemini.go:66`, `anthropic.go:79`, `ollama.go:58` |
| M3 | **[F]** Plugin signing (Ed25519+sha256) exists but opt-in; git-URL installs unverified | `pkgregistry/signature.go`; `installer.go:103-106,305` |
| M4 | **[F]** Action log: no rotation; `Tail` reads entire JSONL into memory | `actionlog.go:301-339` |
| M5 | **[F]** Dated deps (fiber 2.52.4, fasthttp 1.51.0, vite ^5.2.0); odd pin `golang.org/x/net v0.51.0` (`go.mod:71`). **CVE status unverified offline — run `govulncheck` + `npm audit`** | `go.mod`, `gui/package.json` |
| M6 | **[F]** Compose defaults: Postgres password `soulacy` (:38,77), empty API key (:53), `qdrant:latest` (:96); releases unsigned, no SBOM (`release.yml:181-185`) | `docker-compose.yml` |
| M7 | **[J]** `internal/audit` (JSONL) and `internal/actionlog` (SQLite) overlap — two "what did the agent do" stores, no authoritative-source guidance | pkg docs read |
| M8 | **[F]** License mismatch: repo Apache-2.0, `sdk/python/pyproject.toml:10,17` says MIT | verified |
| M9 | **[J]** `map[string]any` saturates engine (123 uses in engine.go) — mitigated by `, ok` guards and `argString` helpers; schema typos are runtime-only failures | `engine.go:879-896,1371-73` |

### Low (one line each)

**[F]** Ephemeral JWT secret silently generated when empty (`jwt.go:33-40`) — tokens die on restart. **[F]** `/health` leaks goroutines if KB store wedges (`api.go:62-74`). **[F]** Channel backoffs ignore ctx (`telegram/adapter.go:119`, `slack/adapter.go:95,102`) — shutdown stalls up to 10s/adapter. **[F]** Stray `internal/gateway/nodir-agent` fixture dir. **[F]** `make install` to `/usr/local/bin` without sudo (`Makefile:49-52`). **[F]** No CONTRIBUTING/SECURITY/CHANGELOG/CODE_OF_CONDUCT. **[J]** Numbered test shards (`engine2..7_test.go`) carry no meaning. **[F]** No `t.Parallel()` in gateway tests.

### Strengths (preserve these)

All **[F]**, verified: timing-safe key compare via SHA-256 + `subtle.ConstantTimeCompare` (`auth/engine.go:317-324`); HS256-pinned JWT, required expiry, single-use refresh rotation (`jwt.go:79-84,158`); zero SQL string concatenation found across storage layers; traversal-safe, zip-bomb-capped archive extraction (`plugininstall/archive.go:19-25,66,114`); SSRF guard on URL tools (`engine.go:1253`); genuine API contract tests with a "failing test = breaking change" policy (`gateway/contract_test.go:1-137`); non-blocking event/WS paths with bounded buffers and drop-on-full (`events/events.go:120-130`, `gateway/events.go:90-125`); locks released before LLM I/O (`engine.go:2139-42`); indexes on every hot table; zero `SELECT *`; 480 `%w` wraps, zero `%v` wraps in non-test code; only 4 TODOs in 115K LOC; consumer-side interfaces instead of import-cycle hacks (`engine.go:59-65`); non-root Docker with HEALTHCHECK; Prometheus + OTel genuinely wired (`server.go:367-373`, `wire.go:1151-66`); `PRODUCTION_AUDIT →` annotation culture. Healthy dimensions in one sentence each: **SQL/injection posture is sound; DB schema/indexing is sound; logging discipline (zap) is sound; docs nav (mkdocs) fully resolves.**

---

## 4. Improvement Strategy

Five themes explain ~90% of the findings:

**Theme 1 — Dev workflow leaks into the artifact** (C1, C2, H1, M8, deploy.sh, .command files).
*Target state:* the repo contains only the product; secrets exist solely in untracked, documented locations; CI secret-scans every push.
*Principle:* a public repo is an artifact, not a workspace.

**Theme 2 — Unsafe by default** (C3, C4, H2, M3, M6).
*Target state:* a user who runs `soulacy serve` with zero config is safe: system tools off, auth required for non-localhost binds, sandbox limitations documented, signing warnings on unverified installs.
*Principle:* dangerous capabilities are opt-in; the default config is the security review.

**Theme 3 — Standards exist but aren't enforced** (H5, H7, H8, M5).
*Target state:* CI is the bar — lint, race, secret scan, vuln scan, GUI/Python tests, smoke tests for Postgres/MCP/NATS; one canonical org, one Go version, one installer.
*Principle:* if it's not in CI, it isn't a standard.

**Theme 4 — Core files outgrew their structure** (H4, M1, M2, M9).
*Target state:* no non-generated file >2,000 lines; builtin tools in per-domain files; one error-response helper; shared LLM translation layer.
*Principle:* finish the splits the codebase already started (`knowledge.go`/`plugins.go` pattern, `llm/retry.go` pattern).

**Theme 5 — Lifecycle gaps: things grow, fail, or vanish silently** (H3, H6, M4, M7, ctx-less sleeps).
*Target state:* every store has an eviction/rotation policy; every error on a data-bearing path is at least logged.
*Principle:* anything appended must someday be trimmed; anything that can fail must say so.

**Explicit non-fixes (trade-offs):**
- **Real sandboxing (namespaces/seccomp/containers): not now.** XL effort, platform-specific, and the product is single-user local-first. Instead: document honestly, strip secrets from tool env, gate `shell_exec`. Revisit only if multi-tenant hosting becomes a goal.
- **80% coverage on channel adapters: no.** They're thin API wrappers; smoke tests + the existing fakes pattern give most of the value. Effort better spent on Postgres/MCP parity tests.
- **Typed schemas replacing `map[string]any` wholesale: no.** The LLM boundary is JSON; guards are disciplined. A typed `ToolSpec` builder for *new* tools only.
- **fiber v3 migration / dependency rewrites: no.** Patch-upgrade within v2 after govulncheck; v3 is churn without a driving CVE.
- **SBOM/sigstore provenance: defer to Milestone 3.** Worth doing pre-1.0, not before the credential and default-safety fires are out.
- **audit/actionlog merge: decide, don't build.** Pick one as authoritative and document; consolidation code can wait.

**Definition of done (measurable):**
1. `gitleaks` (or equivalent) passes in CI; `git ls-files | grep .soulacy/` returns nothing; all C2 credentials rotated.
2. Fresh default install: `shell_exec` unavailable until explicitly enabled; gateway refuses non-localhost bind with empty key.
3. CI fails on: golangci-lint errors, `-race` failures, govulncheck Criticals, GUI vitest failures.
4. No non-generated `.go` file >2,000 lines; `grep -c '_ = s.scheduler\|_ = e.memory.Write' ` → 0.
5. `internal/storage/postgres`, `internal/mcp` ≥ smoke-test coverage (>30%); session map has a tested eviction path.
6. README install URLs, badges, and Go versions agree and resolve.

---

## 5. Task Plan

### Milestone 0 — Safety net (before any refactor)

| ID | Task | Files/areas | Acceptance criteria | Effort | Risk | Deps |
|----|------|-------------|--------------------|--------|------|------|
| T0.1 | **CI gates**: add golangci-lint, `go test -race`, govulncheck, gitleaks, GUI `vitest run`, Python import+pytest stub; align test timeout to 120s | `.github/workflows/ci.yml`, `.golangci.yml` (new) | CI red on lint/race/secret/vuln failure; green on current main (after T1.x fixes) | M | Low — CI only | — |
| T0.2 | **Characterization tests for builtin tools** before splitting engine.go: pin current behavior of shell_exec/http_request/find_files (args, truncation caps, timeout clamp) | `internal/runtime/engine_tools_test.go` (new) | Each builtin tool has ≥1 behavior-pinning test; suite green | M | None | — |
| T0.3 | Tag current state (`pre-audit-fixes`), enable branch protection requiring CI | git/GitHub settings | Tag exists; main requires green CI | S | None | T0.1 |

### Milestone 1 — Critical fixes (security & correctness)

| ID | Task | Files/areas | Acceptance criteria | Effort | Risk | Deps |
|----|------|-------------|--------------------|--------|------|------|
| T1.1 | **Purge `.soulacy/` from git history; rotate everything**: filter-repo the 3,578 files; re-pair WhatsApp; rotate every key in `config.dev.yaml` + `docs/SESSION_HANDOFF.md` (Anthropic, Groq, NVIDIA, Slack, Telegram, gateway key, Rocket Money session) | git history; external services | `git log --all -- .soulacy/` empty; gitleaks clean on full history; old keys confirmed revoked | M | Medium — history rewrite breaks existing clones; coordinate force-push | — |
| T1.2 | **Untrack junk**: root `SESSION_HANDOFF.md`, `gui/vite.config.js.timestamp-*`, `run-story1-tests.command`, `run_memory_tests.sh`; add `.soulacy/`, `SESSION_HANDOFF.md` to `.gitignore`; move/label `deploy.sh` as dev-only | `.gitignore`, root | `git ls-files` shows none of the above | S | Low | T1.1 (same history pass) |
| T1.3 | **Default `allow_system_tools=false`** + per-agent opt-in (`tools.system: true` in SOUL.yaml or config allowlist); startup log when enabled | `internal/config/config.go:487`, `engine.go:873ff`, docs | Fresh install: shell_exec absent from tool list; enabling documented; existing tests updated | M | Medium — **breaking change accepted by owner (pre-1.0)**; CHANGELOG entry required | T0.2 |
| T1.4 | **Auth hard-fail**: empty key + bind ≠ 127.0.0.1 → refuse to start (clear error + how to fix); keep warn-only for localhost | `internal/auth/engine.go:146-150`, `internal/app/wire.go`, `.env.example:14` wording | Gateway exits non-zero on `0.0.0.0` + empty key; test added | S | Low–Medium — breaks intentionally-open deploys (document override flag) | — |
| T1.5 | **Stop swallowing data-path errors**: log-or-return at `engine.go:1903,2209,2224`, `api.go:256,297,3006`, `api.go:385,2980` | those files | Zero `_ =` on memory.Write/RegisterAgent/Upsert; failures visible in logs | S | Low | — |
| T1.6 | **Vuln pass**: run `govulncheck ./...` + `npm audit` in `gui/`; upgrade fiber/fasthttp/x-deps as indicated; verify the odd `x/net v0.51.0` pin | `go.mod`, `gui/package-lock.json` | govulncheck zero Critical/High; CI gate from T0.1 enforces ongoing | M | Medium — dep bumps; contract tests + T0.2 cover | T0.1, T0.2 |
| T1.7 | **Strip secrets from tool env**: sandbox/tool exec passes an allowlisted env, not `os.Environ()`. Per the confirmed single-user-local-first threat model, this + rlimits + honest docs is the **final** sandbox scope — no namespace/seccomp work planned | `internal/sandbox/wrap_unix.go:73`, executor | Spawned tool sees only PATH/HOME/declared vars; test asserts ANTHROPIC_API_KEY absent | M | Medium — tools relying on inherited env break; provide per-agent env declaration | T0.2 |

### Milestone 2 — High-leverage improvements

| ID | Task | Files/areas | Acceptance criteria | Effort | Risk | Deps |
|----|------|-------------|--------------------|--------|------|------|
| T2.1 | **Split `buildSystemTools`** into `engine_tools_shell.go`, `engine_tools_http.go`, `engine_tools_files.go` etc. using existing `BuiltinTool` struct (engine.go:70); rename numbered test shards to match | `internal/runtime/` | engine.go <2,000 lines; behavior tests (T0.2) green unchanged | L | Medium — mechanical but large; characterization tests are the net | T0.2, T1.3 |
| T2.2 | **Gateway error helper** `s.errJSON(c, status, err)`; sweep ~245 call sites; standardize envelope (contract_test pins it) | `internal/gateway/*.go` | One helper; contract tests green; grep for `JSON(fiber.Map{"error"` ≈ 0 outside helper | M–L | Low — mechanical | T0.1 |
| T2.3 | **Session lifecycle**: TTL/LRU eviction on `e.sessions`; history windowing (config: max turns or tokens). **Priority raised** — owner confirms concurrent-agent workloads are expected | `engine.go:2387-2400` + session struct | Eviction test passes; soak test (1k sessions) shows bounded RSS | M | Medium — windowing changes agent context behavior; default window generous | T0.2 |
| T2.4 | **Integration smoke tests — Postgres + MCP only** (Postgres is advertised product surface via the README docker-compose path; MCP is core). NATS + Qdrant: label **experimental** in docs/config instead of testing — no known users; promote to tested status only on demand | `internal/storage/postgres`, `internal/mcp`; docs for NATS/Qdrant | Postgres + MCP ≥30% coverage in CI behind service containers; NATS/Qdrant marked experimental | M (was L) | Low | T0.1 |
| T2.4b | **Action-log rotation + streaming Tail** (promoted from M3 — concurrent agents make unbounded JSONL growth a near-term problem, not polish) | `actionlog.go:301-339` | Tail O(limit) not O(file); rotation test | M | Low | — |
| T2.5 | **Decompose `wire.go Run()`** into per-subsystem constructors returning `(component, closer, error)`; ordered shutdown stack | `internal/app/wire.go` | Run() <300 lines; shutdown order unit-tested | L | Medium — wiring regressions; do after T2.4 smoke tests exist | T2.4 |
| T2.6 | **One identity — canonical org is `vmodekurti/soulacy-personal`** (owner decision): fix README badges (:9-11), dev-clone URL (:198), `release.yml:10` ghcr path, ghcr image references; align Go 1.25 in Dockerfile + release.yml; fix README:133 link; reconcile installer story (root install.sh vs scripts/install.sh vs release tarballs) | README, Dockerfile, `.github/workflows/release.yml`, mkdocs.yml | All URLs resolve under vmodekurti; one documented install path; CI builds with pinned matching toolchain | M | Low | — (unblocked) |

### Milestone 3 — Quality & polish

| ID | Task | Areas | Acceptance | Effort | Risk | Deps |
|----|------|-------|-----------|--------|------|------|
| T3.1 | ~~Action-log rotation~~ **promoted to M2 as T2.4b** (concurrent-agent workloads confirmed) | — | — | — | — | — |
| T3.2 | Shared LLM message/tool translation layer; move OpenAIProvider out of ollama.go | `internal/llm/` | One translation path; conformance suite green | L | Medium | T0.1 |
| T3.3 | Plugin trust: warn loudly on unsigned/git installs; document signing setup | `pkgregistry`, `plugininstall` | Unverified install prints warning + requires `--allow-unverified` | M | Low | — |
| T3.4 | Community files: SECURITY.md (disclosure policy — needed for `curl\|bash` project), CONTRIBUTING.md, CHANGELOG.md; fix `sdk/python/pyproject.toml` license → Apache-2.0 | root, sdk/python | Files exist; licenses agree | S | None | — |
| T3.5 | Ctx-aware backoff in channel adapters + executor pool; fix `/health` goroutine leak | `telegram/adapter.go:119`, `slack/adapter.go:95,102`, `pool.go:354`, `api.go:62-74` | Shutdown <1s in adapter tests; health uses ctx timeout | S | Low | — |
| T3.6 | **Document `actionlog` as the authoritative record** (auditor's recommendation, accepted by default — SQLite, indexed, queryable, already powers the GUI; demote `internal/audit` JSONL to optional debug output or fold it in); JWT empty-secret hard-fail option; compose digest pins + non-default PG password; release signing/SBOM | misc | actionlog documented as authoritative; release artifacts signed | M | Low | — |
| T3.7 | **Python SDK reality check**: PyPI publication could not be verified (pypi.org returned empty for `soulacy` — possibly unpublished). Either publish via CI on release, or remove the `pip install soulacy` claim (README:85) and mark `sdk/python` experimental. Recommendation: mark experimental now (S), publish later when CI tests it (per H7 the SDK has zero tests) | README:82-86, `sdk/python`, `Dockerfile:64` | README claim matches reality; no `\|\| true` hedge in Dockerfile | S | Low | — |
| T3.8 | **Remove personal tooling** (owner confirmed): delete or move out of repo `deploy.sh`, `session7-*.command`, `build-and-restart.command`, `reinstall*.{sh,command}`, `run-story1-tests.command`, `run_memory_tests.sh`; keep gitignore patterns so they never return | repo root | None of these tracked; root `ls` shows product files only | S | None | T1.2 |

### Quick wins (high impact, S effort — do immediately)

- **T1.2** untrack junk + .gitignore (S)
- **T1.4** auth hard-fail on non-localhost empty key (S)
- **T1.5** stop swallowing scheduler/memory errors (S)
- **T3.4** license fix + SECURITY.md (S)
- README link + Go-version text fixes (subset of T2.6) (S)
- Delete stray `internal/gateway/nodir-agent` fixture (S)

### Implementation sketches — top 3 tasks

**T1.1 — History purge & rotation.**
Approach: `git filter-repo --invert-paths --path .soulacy/ --path SESSION_HANDOFF.md --path gui/vite.config.js.timestamp-1779738886153-6e3d119da1655.mjs ...` on a fresh clone; force-push; invalidate forks/clones. *Order matters:* rotate credentials **first** (assume already leaked), purge second — otherwise the window between purge and rotation is false comfort. Re-pair WhatsApp from scratch (session is unrecoverable by design once `creds.json` rotates — that's the point). Gotchas: filter-repo rewrites all SHAs — the story-ID/commit-hash references inside SESSION_HANDOFF.md and docs become stale (acceptable); GitHub keeps unreachable objects for a while — contact support or rely on rotation; add gitleaks to CI (T0.1) in the same PR so it can't recur.

**T1.3 — Gate `shell_exec`.**
Approach: flip `config.go:487` to `false`; in `buildSystemTools` (engine.go:873), partition tools into `safe` (time, math, etc.) and `system` (shell_exec, run_script, file write, http with private hosts); register `system` only when global flag **and** per-agent `capabilities: [system]` (new SOUL.yaml field, parsed in `runtime/builder.go`) agree. Log at startup which agents have system tools. Key steps: config default → builder field → engine registration filter → update examples + docs + CHANGELOG breaking-change note → fix tests asserting tool presence. Gotchas: `sy doctor` and the studio plugin may assume shell_exec exists — grep before flipping; the GUI tool list endpoint must reflect per-agent availability or users see ghost tools.

**T0.1 — CI gates.**
Approach: extend `ci.yml` go job: `golangci-lint run` (start with a minimal `.golangci.yml`: govet, errcheck, staticcheck, unused — tune to pass, don't bulk-suppress), `go test -race -timeout 120s ./...`, `govulncheck ./...`; add `gitleaks/gitleaks-action` as a first job; add `npm test` to the GUI job. Gotchas: errcheck will immediately flag the 132 `_ =` sites — configure it to allow explicit `_ =` initially and ratchet after T1.5, or you'll block all PRs day one; `-race` + cgo/sqlite needs `libsqlite3-dev` (already installed in ci.yml:23); race may expose real failures — budget time to fix rather than disabling.

---

## 6. Decisions (resolved 2026-06-10 with owner)

1. **Canonical org: `vmodekurti/soulacy-personal`.** T2.6 unblocked — fix all `soulacy/soulacy` references (badges, dev clone, ghcr image path).
2. **Threat model: single-user-local-first.** Sandbox scope is final at rlimits + env allowlist (T1.7) + honest documentation. No namespace/seccomp investment. If shared hosting ever enters the roadmap, this decision must be revisited *first*.
3. **Postgres/NATS/Qdrant usage: unknown.** Auditor's call: Postgres stays supported + gets smoke tests (it's the advertised README docker-compose path); NATS and Qdrant are labeled **experimental** until someone asks — testing deferred, not deprecated (T2.4 rescoped).
4. **`shell_exec` default flip: accepted as a breaking change.** No deprecation cycle; CHANGELOG note required (T1.3).
5. **Python SDK: "do what is right."** PyPI publication could not be verified (empty responses from pypi.org for `soulacy`) — the right thing is: mark experimental + remove/soften the `pip install` claim now, publish via CI once the SDK has tests (T3.7).
6. **Concurrent agents: expected.** Session eviction (T2.3) priority confirmed; action-log rotation promoted M3→M2 (T2.4b).
7. **audit vs actionlog: owner undecided.** Auditor recommends **actionlog** as authoritative (SQLite, indexed, queryable, GUI-backed); `internal/audit` demoted to optional debug (T3.6). Override if there's a compliance reason for append-only JSONL.
8. **deploy.sh / `.command` scripts: personal tooling.** Remove from repo (T3.8).
