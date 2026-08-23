# Soulacy v1.0.0

**Released:** 2026-07-17
**Tag:** `v1.0.0`
**Positioning:** The agent framework you can put in production without a security memo.

Soulacy 1.0 is a self-hosted, single-binary "local-first agent operating
system." You write agents as declarative YAML files, run them anywhere Go runs
— laptop, $5 VPS, Raspberry Pi — and deploy them to Telegram, Slack, Discord,
WhatsApp, email, Teams, Google Chat, or HTTP. There is no Docker orchestration
to set up, no Postgres to provision, no cloud dependency.

The 1.0 release is anchored on a shipped, integrated defense-in-depth security
stack that no other agent framework in the field ships today: untrusted-content
envelopes, prompt-injection scanning, an intent gate, production-readiness
verdicts, a red-team regression pack, a Studio pre-save preflight, and a
per-agent Security Doctor with a dry-run simulator. Prompt injection is up
340% YoY and ranks #1 on OWASP LLM Top 10 — Soulacy 1.0 is the first
framework you can point at that entire threat surface and hand to production
without adding a second tool.

## Highlights

### Security stack (Cohort F + F-Bridge)

Every external tool result is wrapped in an `<external_content trust="untrusted"
source="…">…</external_content>` envelope, teaches every agent a short handling
rule in the system prefix, and rides the `tool.result` event with a
classification so downstream filters see it. A deterministic 14-pattern
scanner across 8 injection families (`prompt_override`, `role_swap`,
`secret_exfiltration`, `tool_incitement`, `hidden_text`, `obfuscation`,
`data_exfiltration`, `channel_abuse`) runs on every wrapped body. A tool-call
intent gate composes with the existing capability tier, policy, guardrail, and
ConfirmTools layers — Deny short-circuits the entire dispatch pipeline before
any handler runs. Production readiness blocks the `production` profile when any
privileged agent is exposed on a shared external channel without an explicit
`accept_privileged_exposure:true`. Every Cohort F story ships with a Svelte
surface in the dashboard — the Security Doctor drawer, the intent-decision
Activity chips, the Studio pre-save security panel with save-blocking behavior,
the channel privileged-exposure explainer.

### Studio "Debug in Studio"

A failed run in Activity opens Studio pre-filled with the failing input, the
structured trace, and a before/after diff panel that the operator applies or
cancels before the canvas changes. `Build until it works` explains what
changed and stops on "Needs your input" when external credentials or bot
invites are required — no more silent give-ups.

### Learning loop

Successful runs generate reviewable proposals; accepted skills are injected
into future planning with visible provenance; longitudinal reuse counters and
repeated-error reduction render on the Brain Memory portfolio panel.

### Ten shipped channels + delivery doctor

HTTP, Telegram, Slack, Discord, WhatsApp Cloud, WhatsApp Web, Email/SMTP, MS
Teams, Google Chat, generic Webhook. Every adapter is exercised by a live-send
Diagnose button that classifies failures into 15 stable categories
(`bad_token`, `bot_not_invited`, `starttls_required`, `auth_failed`,
`rate_limited`, …) with concrete fix wording — no more raw `535 5.7.8` codes
leaking to operators.

### Agent Package v2

Calendar versioning (`YYYY.MM.DD[.PATCH]`), namespaced publisher IDs
(`publisher/package`), install-time secret gate that refuses import when
required providers, channels, secrets, MCP servers, or peer agents are
missing (bypassable via `acknowledge_missing:true` after operator review), and
a `.soulacy-package.json` sidecar recording provenance. `sy package validate
<path>` runs the structural check locally without hitting the gateway.

### Launch readiness + Doctor + Provider Doctor

`sy launch check/certify/proof` gates against a scored readiness payload with
per-category journey items. `sy doctor` runs 17 checks with categorized fixes
and a local-vs-remote provider distinction (a stopped local Ollama is a warn,
not a fail). `Provider Doctor` classifies 14 error categories per provider
(missing key / bad key / forbidden / rate limited / quota exceeded / region
blocked / model not found / …) with per-provider fix wording.

### Production-parity harness

`make production-parity` runs 13 required checks (`go vet`, `golangci-lint`,
full Go tests, GUI tests, regression smoke, clean-runtime UAT, release smoke,
race detector, `govulncheck`, Python SDK import, docs strict build, install
fresh build, installed CLI version) in ~83s on go1.26.5. Six opt-in checks
covering live channel delivery, browser MCP, browser rendering, docs
screenshot refresh, Studio live UAT, and credential-backed E2 smoke are gated
behind env vars and run via `scripts/uat-parity-full.sh` with a populated
`scripts/.env.uat`.

## Breaking changes vs. pre-1.0

None for operators upgrading from a recent `codex/**` branch. If you're
upgrading from a much earlier build, note the SEC-3/SEC-4/SEC-5 changes
already captured in the pre-1.0 CHANGELOG entries — destructive system tools
default OFF, tool subprocesses no longer inherit the full gateway environment,
and non-loopback binds require an API key or explicit
`--allow-unauthenticated`.

## What Soulacy is NOT (positioning honesty)

- **Not a hosted SaaS.** No `soulacy.cloud`. Ever.
- **Not a LangGraph replacement.** If you need explicit state-machine graphs
  with checkpoints and resumable execution, use LangGraph.
- **Not a personal assistant.** Soulacy runs headless and delivers to
  channels; it doesn't ship a wake-word, a live Canvas, or a native mobile
  app. If you want an iMessage / WeChat / Signal / Matrix personal assistant,
  use OpenClaw.
- **Not vendor-locked.** Not tied to Anthropic, OpenAI, Google, or any
  provider. Provider-agnostic via config.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy/main/install.sh | bash
soulacy serve
# open http://127.0.0.1:1947 and paste the printed API key
```

Docker path: `docker run -d -p 9000:1947 ghcr.io/vmodekurti/soulacy:v1.0.0`

See [README.md](https://github.com/vmodekurti/soulacy#readme) for the full
install matrix.

## Full changelog

`CHANGELOG.md` in the repo. Per-cohort evidence lives in
`docs/PRODUCTIZATION_REVIEW.md` (canonical status doc with file:line
citations for every shipped item).

## Verifying the artifacts

Every artifact in this release is:

- **Cosign-signed** (verify with `cosign verify-blob --certificate cert.pem
  --signature sig.sig <artifact>`).
- **Listed in SBOM** (`sbom.spdx.json` attached to the release).
- **Checksum-listed** in `SHA256SUMS`.
- **macOS binaries notarized** with Apple Developer ID.

## Known limitations shipped intentionally in 1.0

- **No TypeScript SDK.** The Python SDK is experimental. TypeScript SDK
  ships in Q4 — sooner if the launch signal warrants it.
- **No native mobile / voice.** The web GUI is responsive and installable as a
  PWA; voice is deferred (see `docs/LAUNCH_STRATEGY.md` §9 decision 3).
- **No package marketplace.** Package v2 (7A) ships; the marketplace (7B/7C)
  is fast-follow in the first 90 days.
- **No hosted product.** Categorically ruled out for the launch year.

## Thanks

Every reviewer whose critique landed in `docs/CRITIQUE_RESPONSE.md` and every
issue reporter whose bug shaped the 1,000+ line Cohort A–H sweep.

## Feedback

- Issues: <https://github.com/vmodekurti/soulacy/issues>
- Security: <mailto:security@soulacy.dev> (see `SECURITY.md`)
- Discussion: <https://github.com/vmodekurti/soulacy/discussions>
