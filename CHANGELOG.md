# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project aims
to follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

_Nothing yet — post-v1.0.0 work lands here._

## [1.0.0] - 2026-07-17

First public release. Ships the full Cohort A–H productization sweep, the
seven-story Cohort F security stack (untrusted-content envelope → intent gate →
Security Doctor), Studio "Debug in Studio" run-repair loop, Learning evidence,
Package v2 with install-time secret gate, credential-backed E2 UAT harness, and
the workspace-scoped intent-gate default. `make production-parity` passes 13
required checks in ~83 s on go1.26.5; the six opt-in checks
(`SOULACY_PARITY_LIVE_CHANNELS`, `SOULACY_PARITY_BROWSER_MCP`,
`SOULACY_PARITY_BROWSER_RENDER`, `SOULACY_PARITY_DOCS_SCREENSHOTS`,
`SOULACY_PARITY_STUDIO_LIVE`, plus `.env.uat` credential-backed smoke) run via
`scripts/uat-parity-full.sh`. See `docs/PRODUCTIZATION_REVIEW.md` for the full
per-cohort ledger with file:line evidence.

### Cohort headlines

- **Cohort F — Security stack (S1–S7).** Untrusted-content envelope
  (`internal/trust/`), 14-pattern injection scanner across 8 families
  (`internal/injection/`), tool-call intent gate that composes with capability
  tier + policy + guardrail + ConfirmTools (`internal/intent/`), production
  readiness verdict blocking privileged agents on shared channels
  (`internal/gateway/securityreadiness.go`), 7-fixture red-team regression pack
  (`internal/regression/security_test.go`), Studio pre-save security preflight
  (`internal/studio/security_preflight.go`), per-agent Security Doctor + dry-run
  simulator (`internal/securitydoctor/`, `GET/POST /api/v1/agents/:id/security_doctor`).
- **Cohort F-Bridge — workspace-scoped intent-gate default.** `security.intent_gate`
  flows through runtime, Studio review, and Doctor; per-agent SOUL.yaml still wins.
- **Cohort F-GUI — Svelte wiring** for every F backend surface (Activity chips,
  Agents Security Doctor drawer, Studio security review panel + save-block,
  Dashboard readiness row, Channels privileged-exposure explainer, Config
  intent-gate toggle).
- **Cohort E — Production go-live.** E1 UAT harness harden with per-step timing
  + screenshot gallery, E2 credential-backed smoke harness
  (`scripts/uat-credential-smoke.sh`), E4 provider/cron/hung-run diagnosers, E5
  docs realign to the "local-first agent operating system" framing.
- **Cohort G — Framework-level gap closure.** STARTTLS ordering fix in delivery
  doctor, CI on `codex/**` branches, 30-test Vitest coverage for GUI helpers,
  end-to-end security pipeline scenario test, config schema version stamp.
- **Cohort H — production-parity blocker sweep.** `golangci-lint` clean sweep,
  `.cache`-as-file recovery in UAT + release smoke.
- **Story 7A — Agent Package v2.** Calendar versioning (`YYYY.MM.DD[.PATCH]`),
  namespaced publisher ids, install-time secret gate refusing import when
  required providers/channels/secrets/mcp-servers/peer-agents are missing,
  `.soulacy-package.json` sidecar, `sy package validate <path>` CLI.
- **Cohorts A/B/C — Productization sweep.** Launch readiness scoring
  (`sy launch check/certify/proof`), Studio contract validators for reasoning
  agents (11 checks), Debug-in-Studio failed-run replay + diff-preview repair,
  Channel setup guides + delivery-doctor category catalog, capability-audit
  save-blocking modal, UAT wired into CI with per-step timing report, learning
  evidence with time-window filter + Studio lesson injection, Local-model
  Studio intent presets (`fast_local`/`reliable_local`/`cloud_quality`) and
  streamed/wizard generation pipeline.

### Launch strategy

- New `docs/LAUNCH_STRATEGY.md` — nine-section memo (§1 exec summary; §2 what
  Soulacy has today, cited to file:line; §3 OpenClaw + competitive landscape;
  §4 where "100x better" is real and where it isn't; §5 launch positioning; §6
  ship-for-launch cohort; §7 launch playbook; §8 risks; §9 locked decisions).
  Recommends the "agent framework you can put in production without a security
  memo" positioning wedge.

## [0.9.x] - pre-1.0 hardening

Post-audit hardening from the `post-audit-fixes` branch.

### Breaking changes

- **Destructive system tools default to OFF (SEC-3).** `runtime.allow_system_tools`
  now defaults to `false`. The OS-level built-ins are split into two partitions:
  - **SAFE** (read-only, always available on the local http channel): `read_file`,
    `list_dir`, `find_files`, `fetch_url`, `http_request`, `env_get`, `sys_info`.
  - **SYSTEM** (privileged): `shell_exec`, `run_script`, `install_library`,
    `write_file`, `download_file`. These are offered ONLY when BOTH the server
    permit (`runtime.allow_system_tools: true`) AND a per-agent
    `capabilities: [system]` declaration are present. The legacy
    `system_tools: true` flag is honoured as an alias for the `system`
    capability. Agents relying on shell/file-write access must now set both the
    server flag and the capability. The gateway logs which agents hold the
    `system` capability at startup.
- **Environment allowlist for tool subprocesses (SEC-5).** Spawned Python tool
  processes no longer inherit the gateway's full environment. They receive only
  a base allowlist (`PATH`, `HOME`, `LANG`, `TMPDIR`) plus any variable names
  declared in a per-agent `env: [...]` list. Gateway secrets such as
  `ANTHROPIC_API_KEY` are no longer visible to tool code unless explicitly
  allow-listed.

### Planned breaking changes

- **Auth hard-fails on non-localhost binds with an empty key (SEC-4).** Binding
  to a non-localhost address without an API key will refuse to start unless
  `--allow-unauthenticated` is passed.

### Security / Dependencies (DEP-1)

- Patch-upgraded Go dependencies: `gofiber/fiber/v2` 2.52.4 → 2.52.13,
  `gorilla/websocket` 1.5.1 → 1.5.3, plus transitive bumps (`fasthttp` stack,
  `klauspost/compress`, `mattn/*`). Build + full test suite green.
- `npm audit` (gui): one **moderate** advisory remains — Svelte SSR XSS, fixed
  only in Svelte 5. **Waived**: the gateway GUI is a client-rendered SPA that
  does not use Svelte SSR, and migrating Svelte 4 → 5 (runes rewrite) is a
  separate, out-of-scope effort. Revisit when the GUI moves to Svelte 5.
- `govulncheck` is wired into CI (CI-4); it could not run in the offline build
  sandbox (vuln DB unreachable), so the authoritative scan happens in CI.

### Added

- **Studio "Custom Python" nodes with a per-case consent model.** Studio can now
  author inline Python steps: a `python` flow-node kind (`sdk/reasoning` +
  `internal/reasoning.CompileFlow`), a draggable "Custom Python" palette block, an
  Inspector code editor, and execution via the existing sandboxed process
  executor (`Engine.RunInlinePython` + an inline `run(inputs)` harness in
  `internal/executor/process`). The compiler emits these nodes for glue the
  available tools can't do (e.g. shelling out to a local CLI). Security is
  per-case (docs/STUDIO_PYTHON_TOOLS.md §13): a static classifier
  (`internal/studio/codeclass`) infers `system`/`network`/`dynamic` from the code;
  beyond-guardrail nodes need explicit, content-hash-bound consent collected in
  the save dialog (`internal/studio/consent`, `plan.go`), and the engine is
  **fail-closed** — it refuses to run a beyond-guardrail node without a matching
  grant and the `allow_system_tools` ceiling. Editing the code voids the grant.
  Studio-saved agents also now carry a generated, well-defined system prompt.

### Changed

- **Studio is now built into the core dashboard (ARCH-6).** The visual workflow
  builder is no longer a sandboxed iframe plugin. Its Svelte UI moved from
  `examples/plugins/studio/ui-src` into the main GUI (`gui/src/pages/Studio.svelte`
  + `gui/src/lib/studio/`), is embedded into the gateway binary by `make gui`, and
  is reachable as a first-class route at `/studio` (and `#studio`). The
  host-mediated `postMessage` RPC bridge is gone: Studio now calls the existing
  `/api/v1/studio/*` endpoints directly with the user's authenticated session
  (`gui/src/lib/studio/studioApi.js`). The studio-specific relay was removed from
  `PluginFrame.svelte`, which remains the generic host for other plugin UIs. The
  `examples/plugins/studio` directory was deleted, the Makefile `plugin-ui` target
  and `make all`'s separate plugin build step were removed, and `install.sh` no
  longer copies anything into `<workspace>/plugins/studio`. The `/studio/*`
  endpoints keep their existing user RBAC and are not in the plugin route
  allowlist, so scoped plugin tokens are still rejected. Drafts still persist to
  `<workspace>/studio/drafts/`.
- Data-path write failures (session memory, brain memory, scheduler
  registration, agent upsert) are now logged instead of silently discarded
  (ARCH-1).
- `buildSystemTools` extracted from `internal/runtime/engine.go` into
  per-domain files (`engine_tools_shell.go`, `engine_tools_http.go`,
  `engine_tools_files.go`, `engine_tools_misc.go`) — pure mechanical move,
  no behaviour change (ARCH-2).

### Documentation

- Python SDK marked experimental and no longer advertised as published to PyPI;
  added `sdk/python/README.md` (SDK-1).
- Added `SECURITY.md`, `CONTRIBUTING.md`, and this changelog (DOC-3).

### Fixed

- Python SDK license metadata corrected from MIT to Apache-2.0 to match the
  repository license (DOC-3).
