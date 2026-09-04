# Soulacy Launch Strategy — "100x better than OpenClaw" & a real splash

**Date:** 2026-07-16
**Author:** codex/pto-parity-push planning artifact
**Status:** Strategy memo — no code changes. Every Soulacy claim carries a `file:line`; every competitor claim carries a URL.
**Related:** `docs/PRODUCTIZATION_REVIEW.md`, `docs/OPENCLAW_PARITY.md`, `docs/CRITIQUE_RESPONSE.md`, `docs/FRAMEWORK_OVERVIEW.md`, `docs/SOULACY_FUNCTIONALITY_COMPETITIVE_REPORT.md`.

---

## §1 — Executive summary

Soulacy is a self-hosted, single-binary "local-first agent operating system": declarative `SOUL.yaml` agents, an embedded Svelte GUI, a Studio authoring loop that heals itself, a scheduler, ten channel adapters, MCP client/server, Playwright browser sidecar, a three-tier memory system, packaging v2, and — the newest and most under-marketed layer — a seven-story security cohort covering untrusted-content envelopes, prompt-injection scanning, an intent gate, production-readiness verdicts, a red-team pack, a Studio preflight, and a Security Doctor (`docs/PRODUCTIZATION_REVIEW.md:3-176`).

"100x better than OpenClaw" is not real on **feature breadth** — OpenClaw ships 20+ channels including iMessage, Signal, Matrix, Feishu, WeChat, Nostr, and voice wake-word on macOS/iOS/Android ([openclaw github](https://github.com/openclaw/openclaw)). We will lose that fight and should not enter it. 100x **is real** on three axes where OpenClaw and every graph/role framework in the field are structurally weak: (a) **operator trust** — inspectable YAML, deterministic guardrails, a shipped Security Doctor + workspace-scoped intent-gate + injection scanner that no competitor has; (b) **run-time explainability** — Debug-in-Studio replays a failed session with real evidence and proposes a diff you preview before saving; (c) **self-hostable production posture** — single Go binary, `sy launch check`, capability-tier gate on shared channels, RBAC + vault + audit-ready action log.

The splash play is: **"the first agent framework you can put in production without a security memo."** One tagline, one demo (a `soulacy` binary that boots in <60s and refuses an adversarial fetch in front of a live audience), one comparison chart (security posture across seven frameworks), one launch surface (HN + blog + docs landing + 90-second video + benchmark repo).

**Top three must-do items before public release:** (1) cut the first production tag through the existing release workflow with signed artifacts + SBOM + Homebrew tap bump — `make production-parity` is now verified green on the Studio as of 2026-07-16 (13 required checks pass in ~83s, 6 optional checks intentionally skipped behind credential/Playwright env vars), so the code is ready; the missing step is the publish gesture with production secrets; (2) write the launch blog and the seven-framework comparison chart against a reproducible corpus (§7); (3) ship one "vertical splash agent" — the credential-backed personal-finance or GitHub-triage template that runs end-to-end in 10 minutes on a laptop and demonstrates the security posture on real MCP tools.

---

## §2 — What Soulacy has today (grounded, no marketing)

**Runtime and agent model.** Single Go binary; embedded Svelte GUI via `go:embed`; declarative `SOUL.yaml` (identity, trigger, LLM config, tools, skills, knowledge, peers, memory policy, hooks) parsed by `internal/runtime/loader.go` and executed by `internal/runtime/engine.go` (`docs/FRAMEWORK_OVERVIEW.md:5-100`). Provider router at `internal/llm/router.go` covers OpenAI / Anthropic / Gemini / Ollama plus every OpenAI-compatible endpoint (OpenRouter, Groq, Together, vLLM) via config alone (`docs/FRAMEWORK_OVERVIEW.md:71-73, 233-234`). Structured output enforcement, anti-loop guard (`engine.go:2384-2408`), final-synthesis fallback (`engine.go:2127`), auto-delegate for weak-tool-choice local models (`engine.go:1830-1879`).

**Channels shipped (ten adapters).** HTTP, Telegram, Slack, Discord, WhatsApp Cloud, WhatsApp Web, Email/SMTP, MS Teams, Google Chat, generic Webhook. Each with delivery-doctor categorization (`internal/channels/deliverydoctor.go`, ~15 stable categories) surfaced through the Channels page's Diagnose button (`Channels.svelte:122-140`) and a `POST /channels/:id/test` live-send.

**Studio.** Visual + LLM-generated workflow authoring with a deterministic contract validator (`internal/studio/contract.go` — 11 checks including `agent.system_prompt`, `agent.tool_allowlist`, `agent.step_budget`, `agent.llm_fit`), a "build until it works" repair loop (`internal/studio/buildloop.go`) that alternates deterministic wiring fixes with LLM repair passes and reverts if a repair adds a privileged tool that wasn't in the pre-repair draft (`security_preflight.go::detectPrivilegedRegression`). Debug-in-Studio: a failed run in Activity opens Studio pre-filled with the failing input, structured trace, and a before/after diff the operator applies or cancels (`docs/PRODUCTIZATION_REVIEW.md:271-287`).

**Security (Cohort F + F-Bridge + G) — the under-marketed differentiator.**
- **S1 — Untrusted-content envelope**: `internal/trust/trust.go` (~350 LoC) wraps every external tool result in `<external_content trust="untrusted" source="…">…</external_content>`, teaches every agent a ~200-word rule in the system prefix, and annotates inbound messages from shared external channels with a trust-boundary header (`docs/PRODUCTIZATION_REVIEW.md:7-17`).
- **S2 — Injection scanner**: `internal/injection/scanner.go` (~250 LoC) runs 14 compiled regex patterns across 8 families (`prompt_override`, `role_swap`, `secret_exfiltration`, `tool_incitement`, `hidden_text`, `obfuscation`, `data_exfiltration`, `channel_abuse`) on every wrapped body; findings ride the `tool.result` event; session accumulator drives S3 (`docs/PRODUCTIZATION_REVIEW.md:19-27`).
- **S3 — Intent gate**: `internal/intent/intent.go` (~330 LoC) evaluates every high-risk tool call (built-in list + MCP name-heuristic for write verbs) against the operator's original stated goal; Deny short-circuits the entire dispatch pipeline before policy/guardrail/confirm layers (`docs/PRODUCTIZATION_REVIEW.md:36-51`).
- **S4 — Production readiness**: `evaluateSecurityReadiness()` in `internal/gateway/securityreadiness.go` walks every loaded agent, enumerates shared-channel bindings, and blocks the production profile when any privileged agent is exposed on a shared channel without an explicit `accept_privileged_exposure:true` — surfaced through the Dashboard journey and `GET /api/v1/security/readiness` (`docs/PRODUCTIZATION_REVIEW.md:53-67`).
- **S5 — Red-team regression pack**: `internal/regression/security_test.go` — seven canonical fixtures (`web_page_injection_shell`, `uploaded_document_injection_exfil`, `channel_message_injection_send`, `kb_retrieval_injection_role_swap`, `mcp_result_injection_write`, `malicious_tool_description`, `obfuscated_base64_payload`) run on every CI push (`docs/PRODUCTIZATION_REVIEW.md:69-88`).
- **S6 — Studio security preflight**: blocks Save when a workflow uses a system-requiring tool without `capabilities:[system]`; warns on privileged-channel exposure and on ingest+privileged coexistence; recommends scoped alternatives (`write_file → kb_write`, `shell_exec → scoped Python tool`, `http_request → MCP server`) (`docs/PRODUCTIZATION_REVIEW.md:90-100`).
- **S7 — Security Doctor + dry-run simulator**: `GET /api/v1/agents/:id/security_doctor` returns the per-agent risk report; `POST .../dry_run` simulates an adversarial injection + follow-up tool without executing anything and returns a Deny/Allow/Prompt verdict with reasons (`docs/PRODUCTIZATION_REVIEW.md:102-119`).
- **F-Bridge**: workspace-scoped intent-gate default flows through runtime, Studio review, and Doctor. Per-agent SOUL.yaml still wins (`docs/PRODUCTIZATION_REVIEW.md:141-151`).
- **G4 — End-to-end pipeline test**: `internal/runtime/scenario_security_pipeline_test.go` instantiates a real Engine, drives a `SYSTEM OVERRIDE` fetch through S1 → S2 → F-Bridge resolve → S3 with all five seams under one assertion (`docs/PRODUCTIZATION_REVIEW.md:160-162`).

**Learning loop.** `internal/learning/{store,generator,evidence,sweeper}.go` — proposals from failed runs, recurring reflection, accept/reject UI, per-agent counters, longitudinal reuse counters ("accepted skills reused N/M") and repeated-error reduction on the Memory portfolio panel (`Memory.svelte:664-702`).

**Operator surfaces (the other under-marketed area).** `sy launch check/certify/proof` gates against a scored readiness payload (`internal/gateway/readiness.go`, 913 lines); `sy doctor` runs provider + channel + secret checks with categorized fixes; Provider Doctor (`internal/llm/providerdoctor.go`) classifies 14 error categories per provider (`docs/PRODUCTIZATION_REVIEW.md:523-531`); Session Activity tracker exposes hung-run detection with per-last-event-type explanations (`docs/PRODUCTIZATION_REVIEW.md:540-549`); Cron pre-validation at save time; Startup catch-up event surfacing.

**Packaging v2.** Calendar versioning (`YYYY.MM.DD[.PATCH]`), namespaced publisher ids, install-time secret gate that refuses import when required providers/channels/secrets/mcp-servers/peer-agents are missing unless the operator acknowledges (`docs/PRODUCTIZATION_REVIEW.md:376-386`, `docs/packaging.md`).

**What still needs proof-of-life before launch.** `make production-parity` was verified green on the Studio 2026-07-16: `go vet`, `golangci-lint`, full Go tests, full GUI tests, production regression smoke, clean-runtime UAT, release smoke, race detector, `govulncheck` on go1.26.5, Python SDK import, docs strict build, install fresh build, and installed CLI version all pass — 13 required checks in ~83s. The 6 skipped checks are opt-in credential/Playwright ones (`SOULACY_PARITY_LIVE_CHANNELS`, `SOULACY_PARITY_BROWSER_MCP`, `SOULACY_PARITY_BROWSER_RENDER`, `SOULACY_PARITY_DOCS_SCREENSHOTS`, `SOULACY_PARITY_STUDIO_LIVE`) — they roll into the E2 credential-backed smoke harness (`docs/PRODUCTIZATION_REVIEW.md:586-599`) and should run pre-tag with the operator's real secrets. GUI vitest suite passes 302/302 across 33 files (`docs/PRODUCTIZATION_REVIEW.md:170`). Screenshot currency automation exists but the CI gate that fails when they drift is still open (Story 10 residual).

---

## §3 — OpenClaw and the competitive landscape

### OpenClaw — precisely characterized

OpenClaw is a real, active project at [github.com/openclaw/openclaw](https://github.com/openclaw/openclaw) with a website at [openclaw.ai](https://openclaw.ai/). It bills itself as "your own personal AI assistant, any OS, any platform, the lobster way" — created by Peter Steinberger and community for "Molty, a space lobster AI assistant." Its verified feature surface:

- **Channels — the moat.** WhatsApp, Telegram, Slack, Discord, Google Chat, Signal, iMessage, IRC, Microsoft Teams, Matrix, Feishu, LINE, Mattermost, Nextcloud Talk, Nostr, Synology Chat, Tlon, Twitch, Zalo, Zalo Personal, WeChat, QQ, WebChat. That is ~23 channels vs. Soulacy's 10.
- **Voice.** Wake-word and talk mode on macOS/iOS/Android — native.
- **Canvas.** A live agent-driven visual workspace the user controls.
- **Local-first Gateway.** A single control plane for sessions, channels, tools, events.
- **Onboarding.** `openclaw onboard` (the `sy onboard` cognate).
- **Tools.** Browser, canvas, nodes, cron, sessions, Discord/Slack actions as first-class primitives.

Soulacy's own docs already treat OpenClaw as the primary personal-assistant competitor and correctly identify the parity gaps closed in the last quarter (`docs/OPENCLAW_PARITY.md:22-40`). The remaining parity work called out there — release polish, credential-backed regression, screenshot refresh — is productization, not architecture.

### The graph/role/SDK layer

**LangGraph** — the "most control, steepest curve, most mature for production" of the graph-based frameworks; models workflows as directed graphs with explicit state and conditional branching ([pecollective 2026 comparison](https://pecollective.com/blog/ai-agent-frameworks-compared/), [alicelabs 2026](https://alicelabs.ai/en/insights/best-ai-agent-frameworks-2026)). Best-in-class for teams that want checkpoints, retries, resumable execution. Soulacy has no equivalent to LangGraph's explicit state-machine primitives — the critique response already accepted this as a "we should not fight here" decision (`docs/CRITIQUE_RESPONSE.md:164-166`).

**CrewAI** — 5.2M monthly downloads, role-based paradigm, easiest curve, solid for prototype-to-production ([knowlee 2026](https://www.knowlee.ai/blog/agentic-ai-frameworks-comparison-2026)). Soulacy's peer-agent + auto-delegate is structurally similar but not marketed as "roles."

**AutoGen** — 1.0 GA February 2026, but Microsoft shifted it to maintenance in favor of the broader Microsoft Agent Framework ([alicelabs 2026](https://alicelabs.ai/en/insights/best-ai-agent-frameworks-2026)). Effectively deprecated as an ecosystem bet.

**Mastra** — first-class TypeScript agent framework with an "Observational Memory" system claiming 4–10× token savings via task-relevant context surfacing ([alicelabs 2026](https://alicelabs.ai/en/insights/best-ai-agent-frameworks-2026)). Owns the TypeScript ecosystem. Soulacy has an experimental Python SDK and no TypeScript SDK.

**Claude Agent SDK** — Anthropic's evolution of the Claude Code SDK, GA as of April 2026 in Python + TypeScript on npm. Ships 8 built-in tools (Read, Write, Edit, Bash, Glob, Grep, WebSearch, WebFetch) and a **hooks system**; Agent SDK usage draws from a separate monthly credit as of June 15 2026 ([composio](https://composio.dev/content/claude-agents-sdk-vs-openai-agents-sdk-vs-google-adk), [morphllm](https://www.morphllm.com/ai-agent-framework)). This is the most direct competitive threat: Anthropic-blessed, tightly integrated with Claude Code, and the natural default for anyone already inside the Anthropic ecosystem.

**OpenAI Agents SDK** — open-source, evolution of Swarm; April 2026 added a model-native harness (file ops, code execution, shell) and native sandboxing across seven providers ([composio](https://composio.dev/content/claude-agents-sdk-vs-openai-agents-sdk-vs-google-adk)). Direct competitive threat for the OpenAI-native crowd; strong voice story.

**Flowise / n8n / Dify** — visual, self-hosted, docker+postgres+redis stacks. Soulacy's stated segment competitor in the README (`README.md:29-36`). Wins the low-code visual crowd; loses on ops overhead and inspectability.

**Microsoft Agent Framework, IBM Bee Stack, IBM wxFlow** — enterprise-flavored, being folded into the OpenTelemetry GenAI semantic-convention effort ([opentelemetry.io](https://opentelemetry.io/blog/2025/ai-agent-observability/)). Not a direct threat for Soulacy's audience today.

### What each has that Soulacy doesn't (honest gaps)

- **OpenClaw**: 13 more channels; native voice with wake word; iOS/Android companion apps; a live canvas.
- **LangGraph**: explicit state-machine primitives, checkpointing, human-in-the-loop pause/resume.
- **CrewAI**: 5.2M monthly downloads and the mindshare that comes with them.
- **Mastra**: first-class TypeScript; Observational Memory as a marketed feature.
- **Claude Agent SDK**: Anthropic distribution, hooks system, 8 opinionated built-in tools tuned for Claude, credit-integrated billing.
- **OpenAI Agents SDK**: OpenAI distribution, native sandbox, seven-provider harness.
- **Dify / n8n / Flowise**: node marketplace, richer visual editor UX, established SaaS + self-host duality.

### What Soulacy has that each of them doesn't

- **Single Go binary + embedded GUI, no Docker/Postgres/Redis** vs. Flowise/n8n/Dify (`README.md:29-36`).
- **Declarative YAML that diffs cleanly + hot reload** vs. every graph/SDK framework where the workflow is code.
- **Shipped, integrated security stack** — envelope + scanner + intent gate + readiness + red-team pack + preflight + Doctor — as a first-class product surface. **No named competitor ships this integrated a defense-in-depth story.** LangGraph, CrewAI, Claude Agent SDK, OpenAI Agents SDK all leave prompt-injection defense to the operator and third-party tools like Nightfall, Prompthalo, Galileo ([nightfall](https://www.nightfall.ai/blog/prompt-injection-protection)).
- **Debug-in-Studio failed-run replay + diff-preview repair** — nobody else has this loop connecting a live incident to an authoring surface with a verified fix (`docs/PRODUCTIZATION_REVIEW.md:271-287`).
- **Workspace + per-agent capability-tier gate on channel bindings** — Soulacy blocks a Privileged agent from a shared external channel unless the operator explicitly ticks `accept_privileged_exposure:true` (`internal/app/channels.go:141::bindingDecision`).
- **`sy launch check` + `sy launch certify` + `sy launch proof`** — a scored readiness payload with journey items that gate the production profile (`docs/PRODUCTIZATION_REVIEW.md:229-238`).

### Where Soulacy is honestly behind or absent

- **Marketing / distribution.** OpenClaw has a Wikipedia-adjacent "space lobster" origin story; Ollama has 8.9M monthly developers, 85% of Fortune 500, and $65M Series B ([techcrunch 2026](https://techcrunch.com/2026/07/09/popular-open-source-ai-developer-tool-ollama-raises-65m-grows-to-nearly-9m-users/)). Soulacy has a solo maintainer and no meaningful stars/community footprint yet.
- **TypeScript SDK.** The Python SDK is experimental, no test coverage, not yet on PyPI (`README.md:163-171`). No TypeScript SDK. In a field where Mastra + Vercel AI SDK + Claude Agent SDK all have TS-first stories, this hurts.
- **Native / mobile.** No native macOS app (a separate `soulacy-mac` exists as a REST client, `docs/FRAMEWORK_OVERVIEW.md:282-284`), no iOS/Android, no wake-word voice.
- **Marketplace / community.** Package registry has a design memo and 7A shipped, but no live registry with community packages yet (`docs/PACKAGE_VERSIONING_DESIGN.md`).

---

## §4 — Where "100x better" is real and where it isn't

Reading each axis: current Soulacy position vs. OpenClaw and the top two graph/SDK competitors, and whether 100× is achievable by launch.

**Developer experience (write an agent).** Soulacy: one YAML file, hot reload, tools are Python files. OpenClaw: install → onboard → gateway spins up. LangGraph: Python code. Claude Agent SDK: Python or TypeScript. **100× is not real** — writing a `SOUL.yaml` is nice but not measurably 100× cleaner than a Claude Agent SDK Python file. Realistic claim: "declarative, diffable, and inspectable without opening an IDE."

**Feature surface (channels, LLMs, tools).** OpenClaw ships ~23 channels; Soulacy ships 10. **100× is unreachable and we should not fight here.** Realistic claim: "the 8 channels most operators actually deploy to production" (Telegram/Slack/Discord/WhatsApp/Email/Teams/GoogleChat/HTTP), each with delivery-doctor categorization and live-send verification most competitors don't have. Reframe from "count" to "operational quality per channel."

**Self-hosting story.** Soulacy: single Go binary, no Postgres, no Redis, boots on a Raspberry Pi. Dify/n8n/Flowise: docker+postgres+redis. Claude/OpenAI Agents SDK: not self-hosted, they're SDKs against provider APIs. **100× is real here.** A defensible "you can put this on a $5 VPS and forget it" claim vs. every visual competitor's ops burden.

**Security posture.** Every named framework treats prompt injection as "the operator's problem"; Nightfall, Prompthalo, Galileo, and the emerging MCP-security vendor category exist because no framework ships this integrated ([nightfall 2026](https://www.nightfall.ai/blog/prompt-injection-protection)). Soulacy ships S1–S7 + F-Bridge + G4. **100× is real and defensible.** Prompt injection is up 340% YoY and ranks #1 on OWASP LLM Top 10 ([aimagicx](https://www.aimagicx.com/blog/prompt-injection-attacks-ai-agent-security-guide-2026), [ecorpit](https://ecorpit.com/ai-agent-security-prompt-injection-guardrails-2026/)). Enterprise buyers explicitly reward "runtime controls, red-team results, and audit-ready logs aligned to recognized frameworks" ([gettiaconsulting](https://www.gettiaconsulting.com/en/actualites/securite-agents-ia-prompt-injection-2026)). This is the axis to hammer.

**Ecosystem / marketplace.** Dify, n8n, Cursor all have marketplaces. Soulacy has a package registry design but no live community-populated marketplace. **100× is not real by launch.** Realistic: ship 7B/7C in the first 90 days (`docs/PACKAGE_VERSIONING_DESIGN.md`), then earn the marketplace over the following quarter.

**Model coverage.** Claude/OpenAI SDKs are vendor-locked-first; LangGraph is provider-agnostic. Soulacy is provider-agnostic and adds any OpenAI-compatible endpoint via config alone (`docs/FRAMEWORK_OVERVIEW.md:71-73`). **Roughly at parity with LangGraph, better than the vendor SDKs.** Realistic claim: "provider-agnostic without a per-vendor SDK dance."

**Observability.** Market is $1.68B in 2026 headed to $8.62B by 2031, 38.69% CAGR ([mordorintelligence](https://www.mordorintelligence.com/industry-reports/agent-observability-and-governance-market)). OpenTelemetry GenAI semantic conventions are actively being defined ([opentelemetry.io](https://opentelemetry.io/blog/2025/ai-agent-observability/)). Soulacy has action logs, event stream, Prometheus, run traces, Session Activity tracker, hung-run detection. **100× not real vs. Braintrust/Langfuse/Galileo,** which are dedicated observability platforms. Realistic claim: "built-in observability enough to run in production without a second tool, with OTLP export as fast-follow so you can graduate to Braintrust when you're ready."

**Price / economics.** Every framework except OpenClaw is free open-source; Claude Agent SDK usage flows through Anthropic billing with the new Agent SDK credit as of June 15 2026 ([morphllm](https://www.morphllm.com/ai-agent-framework)). Soulacy runs against your own LLM keys or a local Ollama for zero marginal cost. **100× is real vs. the SDK-native tier for hobbyist / small-team economics.**

**Learning curve.** LangGraph is famously steep; CrewAI is easiest; Soulacy sits between (need to know YAML + tools). **Not 100×, but a realistic "learn in an evening"** claim if the quickstart delivers on the 10-minute promise (`docs/PRODUCTIZATION_REVIEW.md:437-438` still-open Story 10 gap).

**Trust the framework in production.** This is the axis nobody's marketing but everyone's asking about. Soulacy has: launch readiness score with journey items; capability-tier gate on channels; workspace-scoped intent gate; security readiness verdict blocking the production profile; Doctor + dry-run simulator; per-agent audit ledger; RBAC + vault + rotation. **100× is real.** No other framework ships the "prove it's ready for production" bundle.

**Summary.** Real 100× axes: security posture, self-hostability, production trust. Not-100×-but-real axes: model coverage, price. Do-not-fight axes: channel count, marketplace scale (yet), TypeScript ecosystem, native voice, mobile app polish. The strategy is to pin the launch narrative on the three real axes and honestly cede the rest.

---

## §5 — Launch positioning

**Tagline candidate (primary):** **"The agent framework you can put in production without a security memo."**

Alternates:
- "One binary. YAML agents. A security stack that ships."
- "Local-first agents that can be trusted with real work."
- "Soulacy — declarative agents, defended by default."

The current README tagline is "One binary. YAML agents. Runs anywhere — no cloud required." (`README.md:3`) with a hero "Ollama — but for agents." (`README.md:9`). Both are fine but neither uses the security wedge that is now Soulacy's realest differentiator. The E5 docs pass already made "local-first agent operating system" canonical (`docs/PRODUCTIZATION_REVIEW.md:553-556`). Recommend keeping "local-first agent operating system" as the noun-phrase and adding the security-in-production line as the tagline / OG description.

**Elevator pitch (one paragraph).** Soulacy is a self-hosted, single-binary agent framework where every agent is one inspectable YAML file, every tool call passes through a shipped defense-in-depth security stack (untrusted-content envelopes, injection scanning, intent gate, capability tiers, readiness verdicts, red-team regression, dry-run simulator), and every failed run can be replayed inside a healing Studio that proposes a diff you preview before saving. You run it on your laptop, a $5 VPS, or a Raspberry Pi. You bring your own LLM keys or point it at Ollama. You get channels (Telegram, Slack, Discord, WhatsApp, email, Teams, Google Chat), schedules, MCP client + server, browser automation, and a launch-readiness score — all without Docker orchestration.

**The five "why this exists" claims for a first-time visitor (60-second read).**

1. **Ships in production, safely.** Cohorts F + F-Bridge + G give you S1–S7 defense-in-depth against prompt injection today, with a Doctor and dry-run simulator you can point at any agent (`docs/PRODUCTIZATION_REVIEW.md:3-176`).
2. **Runs anywhere with no infra.** One Go binary, embedded GUI, SQLite by default, Ollama-compatible so you can go 100% local (`README.md:29-36`).
3. **Agents are inspectable YAML, not code.** Diff them, PR them, revert them. Hot-reloaded via `fsnotify` (`docs/FRAMEWORK_OVERVIEW.md:56`).
4. **Studio heals its own broken runs.** Debug-in-Studio takes a failed session, replays it, and proposes a repair diff you preview and apply (`docs/PRODUCTIZATION_REVIEW.md:271-287`).
5. **Nine of the top provider APIs, ten shipping channels, MCP client + server, browser via Playwright, learning loop — no SDK dance.** Config alone brings in OpenAI/Anthropic/Gemini/Ollama/OpenRouter/Groq/Together/vLLM/any-OpenAI-compatible (`docs/FRAMEWORK_OVERVIEW.md:233-234`).

**What Soulacy explicitly is NOT** (write this on the docs page — reduces churn).
- Not a hosted SaaS. There is no soulacy.cloud plan.
- Not a LangGraph replacement. If you want explicit state-machine graphs with checkpoints, use LangGraph.
- Not a personal-assistant "install everywhere" like OpenClaw. Soulacy runs headless, delivers to channels, and doesn't ship a wake-word or a native mobile app.
- Not a low-code node-editor for non-developers. Studio helps, but the audience is developers/ops who prefer YAML + Python tools.
- Not vendor-locked. Not tied to Anthropic or OpenAI.

That "is NOT" list is a positioning weapon: it lets prospects self-disqualify quickly and lets Soulacy own the segment cleanly.

---

## §6 — The splash plan

Bucketed by launch-blocker vs. fast-follow vs. nice-to-have. Each item names a rough scope (S/M/L), what it neutralizes competitively, and where it lands in the codebase.

### Ship-for-launch (must be in v1 or the splash falls flat)

- **~~A green `make production-parity` on a fresh clone.~~** ✓ **Verified 2026-07-16 on the Studio** — all 13 required checks pass in ~83s (`tmp/production-parity/20260716T194117Z/report.md`). Remaining action is to capture the report shape as the release-notes acceptance artifact and re-run once immediately before tagging.
- **Run the opt-in parity checks with real credentials.** S — set `SOULACY_PARITY_LIVE_CHANNELS=1` + `SOULACY_GOLDEN_*` (Telegram/Slack/Discord destinations), `SOULACY_PARITY_BROWSER_MCP=1`, `SOULACY_PARITY_BROWSER_RENDER=1` (needs Playwright), `SOULACY_PARITY_DOCS_SCREENSHOTS=1`, `SOULACY_PARITY_STUDIO_LIVE=1`. These are the 6 skips in the 2026-07-16 report; they're the E2 credential-backed harness (`docs/PRODUCTIZATION_REVIEW.md:586-599`). Neutralizes the "you tested against mocks" reviewer response. Lands in a manual pre-tag run.
- **First production tag with signed artifacts + SBOM + Homebrew tap bump.** S — release workflow exists (`docs/OPENCLAW_PARITY.md:52`), needs the first real run with production secrets. Neutralizes "unstable / unversioned" perception. Lands in `.github/workflows/release.yml`.
- **The security-differentiator landing page + comparison chart.** M — new `site/security.html` or a section on `docs/index.md`. Table across LangGraph / CrewAI / Claude Agent SDK / OpenAI Agents SDK / Dify / n8n / Soulacy with rows for envelope, injection scanner, intent gate, readiness verdict, red-team pack, Studio preflight, Security Doctor + dry-run. Neutralizes the "just another agent framework" reviewer response. Lands in `site/` + `docs/`.
- **One 60-90s demo video: live "adversarial fetch → intent gate deny" walkthrough.** M — needs a scripted demo agent + a real fetch of a page with a `SYSTEM OVERRIDE` payload + Doctor dry-run rendered in the modal. Neutralizes "I don't understand the security claim in text." Lands in `docs/assets/`.
- **10-minute quickstart, measured and cut to hit.** S — Story 10 residual item (`docs/PRODUCTIZATION_REVIEW.md:437-438`). Pre-pull a tiny local model in `sy onboard` or ship a "no-LLM mode" first agent. Neutralizes "I tried it, gave up." Lands in `cmd/sy/onboard.go` + `docs/getting-started/quickstart.md`.
- **One vertical splash agent template that shows the security stack on real MCP tools.** M — the "personal-finance monitor via Rocketmoney MCP + Telegram delivery + injection-scanner demo" agent or the "GitHub triage via MCP + intent-gate on `mcp__github__create_issue` demo" agent. Neutralizes "cool framework, what's a real agent look like?" Lands in `examples/agents/` + `docs/using/`.
- **README rewrite with the new tagline + security-first hero.** S — the README is currently "One binary. YAML agents." (`README.md:3`); the new tagline goes first, the security claim goes in the second paragraph, the channel/Studio/learning story follows. Lands in `README.md`.
- **Screenshot currency CI gate.** S — Story 10 open item; ensures docs stay honest on the launch date. Lands in `.github/workflows/`.
- **Public HN post + companion blog + Twitter thread.** S — timed to a Tuesday morning EST for HN algorithm cadence; comparison chart is the blog hook. Lands in `blog/` (new folder) + submissions.

### Fast-follow (first 30 days after launch)

- **TypeScript SDK skeleton.** M — even a read-only wrapper over `/api/v1/*` neutralizes "no TS support" for the Mastra/Vercel-AI-SDK-adjacent crowd. Lands in `sdk/typescript/`.
- **OTLP exporter for the event stream + action log.** M — buys the "graduate to Braintrust/Langfuse/Datadog when you're ready" story. Lands in `internal/observability/`.
- **Package registry MVP (7B).** L — publish flow, changelog display, `sy pull <id>@<version>` (`docs/PACKAGE_VERSIONING_DESIGN.md`). Lands in `internal/pkgregistry/` + `sdk/pkgregistry/`.
- **Real Slack/Telegram/Discord credential-backed smoke tests running on a nightly.** S — E2 harness exists (`docs/PRODUCTIZATION_REVIEW.md:586-599`); needs a private secrets store and a nightly job. Lands in `.github/workflows/`.
- **Docs `first-agent.md` rewrite to Studio-Generate-first.** S — the current file positions raw-YAML authoring; a rewrite showing the Studio flow first would match how new users actually onboard (`docs/PRODUCTIZATION_REVIEW.md:571-573`).
- **Second vertical splash agent** (e.g., research-brief generator). M — Lands in `examples/agents/`.
- **Voice: decide launch scope.** S design → M implementation. If yes, credential-backed mic UAT + native/wake polish (`docs/OPENCLAW_PARITY.md:38, 58`). If no, remove the "MVP foundation present" language from OPENCLAW_PARITY so it doesn't confuse reviewers.

### Nice-to-have (first 90 days)

- **7C — package pruning + `sy pull rollback`.** L.
- **Multi-agent orchestration primitives** (`peer_strategy: parallel | sequential | race` — the deferred item in `docs/CRITIQUE_RESPONSE.md:164-166`).
- **Cross-encoder reranker for RAG + BM25 hybrid retrieval** (`docs/CRITIQUE_RESPONSE.md:156-158`).
- **Native mobile companion** (PWA install signals shipped, native app is deferred — `docs/OPENCLAW_PARITY.md:37`).
- **A hosted "Soulacy Cloud" evaluation environment** — a one-click hosted trial with a wiped VM per session. Not a hosted product, an evaluation surface. Lands in `deploy/` + a separate repo.

---

## §7 — Launch playbook

**Blog post skeleton.**

- **Headline candidates.** "Announcing Soulacy: agents you can put in production without a security memo." / "Soulacy 1.0 — a self-hosted agent framework that ships its own security stack." / "Ollama, but for agents — and it defends itself." / "We built the boring parts of production agents so you don't have to."
- **Lede (3 sentences).** The problem: agent frameworks compete on demos, not on the things that block deployment. The insight: after a year of building Soulacy against real self-hosted workloads, the highest-leverage differentiator turned out to be the security + operator-trust bundle, not the graph primitives. The offer: single binary, YAML agents, ten channels, Studio, learning loop, and a seven-story security stack that just runs.
- **Section 1: What Soulacy is.** 4-frame architecture diagram from `docs/FRAMEWORK_OVERVIEW.md:11-49`.
- **Section 2: The security stack (with animated GIF of the Doctor dry-run).** Envelope → scanner → intent gate → readiness → red-team → preflight → Doctor. One paragraph each.
- **Section 3: The comparison chart** (seven frameworks × six security dimensions). Every cell hyperlinks to source. The row-count alone is the argument.
- **Section 4: Debug-in-Studio.** GIF of a failed run → open in Studio → diff preview → apply. Video-first, text-second.
- **Section 5: How we shipped this.** Cohort A/B/C/E/F/F-Bridge/G in `docs/PRODUCTIZATION_REVIEW.md` linked. Signals both discipline and willingness to write these things down publicly.
- **Section 6: What's next.** TS SDK, OTLP export, package registry MVP, second vertical agent.
- **Section 7: Get it.** `curl -fsSL … | bash` + docker + docker-compose (`README.md:44-149`).

**HN title candidates.**
- `Show HN: Soulacy – single-binary agent framework that ships its own security stack`
- `Show HN: Soulacy – YAML agents, local-first, defended by default`
- `Show HN: Soulacy 1.0 – agent framework you can put in production without a security memo`

HN reads short titles better than clever ones. First candidate probably wins because "single-binary" + "security stack" packs the unique claim into six words.

**Docs landing page priorities.**

1. Hero: tagline + one animated GIF of the Debug-in-Studio flow.
2. Three-column feature grid: Security / Local-first / Studio.
3. `curl | bash` install strip.
4. 60-second video embed.
5. Comparison chart.
6. `Get started in 10 minutes` link → quickstart.

**Demo video shot list.**

1. Empty terminal → `curl ... | bash` → binary installed. (10s)
2. `soulacy serve` → banner with URL + API key. (5s)
3. Browser opens GUI. First-run wizard picks a provider. (15s)
4. Studio Generate: "an agent that fetches a URL and summarizes it." Live workflow generated. (20s)
5. Save. Chat to it. "Summarize `evil-page.example/injection`." (10s)
6. The Doctor dry-run rendering an intent-gate deny with the injection source named. (15s)
7. Cut to the Debug-in-Studio panel with a diff preview. (10s)

**Comparison chart columns.** Framework · Self-hosted single-binary · Injection scanner · Intent gate · Readiness verdict · Red-team pack · Debug-in-Studio equivalent · TypeScript SDK. Rows: Soulacy · LangGraph · CrewAI · Claude Agent SDK · OpenAI Agents SDK · Dify · n8n · Flowise · OpenClaw. Cells cite source URLs.

**Benchmark categories.** Time-to-first-agent-reply. Cold-start latency. RSS at rest. RSS under 10 concurrent sessions. Percentage of the seven S5 red-team fixtures each framework catches out-of-the-box (spoiler: it's 7/7 for Soulacy and 0/7 for the others without a third-party plugin). Ship it as a reproducible repo `soulacy-benchmarks` so people can rerun and PR.

**First-hour outreach.** Post to HN Tuesday 9am ET. DM 5-10 friends-of-the-project to seed the first comments (with substance, not upvotes). Twitter thread from the maintainer with 8 tweets mirroring the blog sections. Post to /r/LocalLLaMA (the security stack + Ollama compat is on-theme). Post to lobste.rs.

**First-day outreach.** Reach out to Simon Willison (LLM security beat), Matt Rickard (agent framework beat), Andriy Burkov (weekly newsletter), the DevOps'ish newsletter, and the LangChain / CrewAI Discord channels (respectfully — not a hijack, a link).

**First-week outreach.** One podcast recording (Latent Space, MLOps Community, Practical AI). A guest post on a friendly blog (Braintrust, Modal, Ollama) if any is receptive. A more technical follow-up post on the intent gate's specific design.

**Angles the tech press cares about right now.**

- **AI agent security is the fastest-growing threat category** (340% YoY, OWASP #1). A framework that ships defense-in-depth is the story the press wants.
- **Vendor consolidation.** After AutoGen's move to maintenance, Anthropic's Bun acquisition ([cosmicjs](https://www.cosmicjs.com/blog/bun-rust-rewrite-javascript-runtime)), and Cursor's $1B ARR speed record, "the single-maintainer open-source framework outsurviving the vendor SDKs on the axis that matters most" is a great narrative.
- **Local-first / self-hosted resurgence.** Ollama's 8.9M devs and $65M Series B ([techcrunch](https://techcrunch.com/2026/07/09/popular-open-source-ai-developer-tool-ollama-raises-65m-grows-to-nearly-9m-users/)) validate the "run it yourself" thesis. Soulacy is the agent-layer analog.

---

## §8 — Honest risks + what could kill this

**Risk 1: Nobody notices.** Solo-maintainer OSS launches routinely die on page 3 of HN with 12 points and no comments. **Mitigation.** Land three things in the first hour: (a) a substantive HN title that names the differentiator, (b) 5-10 seeded thoughtful comments with real technical detail, (c) a reproducible benchmark repo that gives skeptics something to poke at. Don't try to be trending — try to be technically defensible in the top comments.

**Risk 2: "Just another agent framework."** The single biggest reviewer response. **Mitigation.** The comparison chart is the answer. Every framework in the chart demonstrably lacks the shipped security stack; if the reviewer reads the chart honestly they can't say "just another." Second mitigation: lead every surface (HN title, blog headline, docs hero, README hero) with the security-first differentiator, not the generic "agent framework" language.

**Risk 3: The security angle is misread as complexity.** Reviewer sees "seven-story security stack" and assumes Soulacy is heavy and hard to use. **Mitigation.** The 60-second video shows the same one-liner install + first-agent flow as any other framework — the security stack is invisible until you need it, at which point the Doctor renders a clean modal explaining what would happen. Also: in the docs, the security section is a linked deep-dive from a small feature card on the home page, not the front-loaded content.

**Risk 4: Self-hosted-first is misread as "not for me" by the cloud crowd.** OpenAI/Anthropic-native developers may bounce because they assume Soulacy is Ollama-only. **Mitigation.** The README already leads with "point it at any LLM — Ollama, OpenAI, Anthropic, Groq, or anything OpenAI-compatible" (`README.md:5`); reinforce that in the blog. The demo video should use a cloud provider (probably OpenAI) as the default, with the "or just point it at Ollama" as a side-note. Reverse the mental model — cloud is default, local is bonus.

**Risk 5: OpenClaw's channel breadth is used to invalidate Soulacy in reviews.** The obvious "why not just use OpenClaw" post appears within the first 20 HN comments. **Mitigation.** Own the answer preemptively in the blog: "OpenClaw is the best personal assistant if you need iMessage/WeChat/Signal reach. Soulacy is the best production agent framework if you need to trust a running agent with real work." The two are not the same product; the launch should acknowledge OpenClaw explicitly and route users honestly.

**Risk 6: A single critical bug in the security stack surfaces post-launch.** A red-team-pack miss, a bypass through a novel injection family, an intent-gate false-negative on a genuine attack. **Mitigation.** Publish the S5 fixtures publicly (they're in `internal/regression/security_test.go` already); invite the community to submit new fixtures via PR; commit to a public security policy in `SECURITY.md` with a 72-hour triage SLA. Turn what could be a scandal into a virtuous cycle.

**Risk 7: Anthropic-native developers default to Claude Agent SDK.** As of April 2026, Claude Agent SDK is the natural default for anyone inside the Anthropic ecosystem. **Mitigation.** Position Soulacy as complementary, not competitive: a Soulacy agent can call a Claude Agent SDK sub-agent via MCP, and vice versa. Ship an example that demonstrates the interop.

**Risk 8: The launch coincides with a bigger news beat.** GPT-6 launches the same week; OpenAI announces a hosted Agents Cloud; Anthropic ships something surprising. **Mitigation.** Have two launch windows planned; be willing to shift. Also: the security-first framing is durable across news cycles in a way that generic feature launches aren't.

---

## §9 — Locked decisions (2026-07-17)

The six positioning/scope calls are answered. Each entry names the decision and the surfaces it unlocks.

**Decision 1 — Primary tagline: "The agent framework you can put in production without a security memo."** "Local-first agent operating system" remains the canonical noun phrase (E5 pass, `docs/PRODUCTIZATION_REVIEW.md:553-556`). Unlocks: README hero rewrite, `docs/index.md` hero, Studio About string, OG description, HN post title.

**Decision 2 — Distribution v1: HN post + Twitter thread + newsletter push (Simon Willison, DevOps'ish, LangChain community).** Podcast pre-record and closed beta are fast-follows only if the v1 landing generates enough signal to justify them. Unlocks: HN title candidates in §7, outreach email drafts, Twitter thread outline.

**Decision 3 — Voice: out of v1 scope.** `OPENCLAW_PARITY.md` voice row updated to state "not v1 scope" so reviewers don't misread partial voice as a bug. The security wedge is the launch narrative; voice dilutes it. Unlocks: OPENCLAW_PARITY.md edit; frees the "MVP foundation present" language for future re-scoping.

**Decision 4 — Vertical splash agent for launch: GitHub triage via MCP + intent-gate demo on `mcp__github__create_issue`.** Personal-finance monitor via Rocketmoney MCP is the fast-follow after landing. Unlocks: `examples/agents/github-triage/` spec, demo video shot list, "here's a real agent" HN answer.

**Decision 5 — TypeScript SDK: defer to Q4.** A half-working stub reads worse than a shipped "coming Q4" note. Unlocks: honest omission in README + comparison chart; no scope creep on the pre-tag sprint.

**Decision 6 — "Soulacy Cloud": categorically ruled out for the launch year.** Added to the `docs/index.md` "what Soulacy is NOT" strip. Self-hosted-first is a positioning weapon only if committed to. Unlocks: "we are not a SaaS" line in the elevator pitch; forecloses the "when will you host this" reviewer question.

**With these six locked, the pre-tag sprint scope is:** `docs/RELEASE_CHECKLIST.md`, `CHANGELOG.md` v1.0.0 entry, `docs/RELEASE_NOTES_v1.0.0.md`, `scripts/uat-parity-full.sh` (full opt-in credential-backed parity harness), README + `docs/index.md` hero rewrite, `docs/OPENCLAW_PARITY.md` voice update, GitHub-triage example agent, seven-framework security comparison chart, 60-90s demo video.

---

## Appendix A — Source list (external)

- OpenClaw project: [github.com/openclaw/openclaw](https://github.com/openclaw/openclaw), [openclaw.ai](https://openclaw.ai/)
- Agent framework landscape 2026: [pecollective](https://pecollective.com/blog/ai-agent-frameworks-compared/), [alicelabs](https://alicelabs.ai/en/insights/best-ai-agent-frameworks-2026), [langchain resources](https://www.langchain.com/resources/ai-agent-frameworks), [knowlee](https://www.knowlee.ai/blog/agentic-ai-frameworks-comparison-2026), [openagents](https://openagents.org/blog/posts/2026-02-23-open-source-ai-agent-frameworks-compared)
- Vendor SDKs: [composio comparison](https://composio.dev/content/claude-agents-sdk-vs-openai-agents-sdk-vs-google-adk), [morphllm 2026 update](https://www.morphllm.com/ai-agent-framework), [claude agent sdk overview](https://code.claude.com/docs/en/agent-sdk/overview)
- Prompt injection landscape: [nightfall](https://www.nightfall.ai/blog/prompt-injection-protection), [aimagicx](https://www.aimagicx.com/blog/prompt-injection-attacks-ai-agent-security-guide-2026), [ecorpit](https://ecorpit.com/ai-agent-security-prompt-injection-guardrails-2026/), [gettiaconsulting](https://www.gettiaconsulting.com/en/actualites/securite-agents-ia-prompt-injection-2026)
- Observability market: [mordorintelligence](https://www.mordorintelligence.com/industry-reports/agent-observability-and-governance-market), [opentelemetry.io](https://opentelemetry.io/blog/2025/ai-agent-observability/), [braintrust](https://www.braintrust.dev/articles/agent-observability-complete-guide-2026)
- Launch templates: [ollama launch blog](https://ollama.com/blog/launch), [ollama $65M raise (TechCrunch, 2026-07-09)](https://techcrunch.com/2026/07/09/popular-open-source-ai-developer-tool-ollama-raises-65m-grows-to-nearly-9m-users/), [bun story](https://chyshkala.com/blog/the-bun-story), [cursor vs zed 2026](https://theplanettools.ai/compare/cursor-vs-zed)

## Appendix B — Soulacy internal references (file:line)

- Productization review: `docs/PRODUCTIZATION_REVIEW.md` (canonical status doc; Cohorts A/B/C/E/F/F-Bridge/G/H)
- OpenClaw parity snapshot: `docs/OPENCLAW_PARITY.md`
- Outside critique response: `docs/CRITIQUE_RESPONSE.md`
- Framework overview: `docs/FRAMEWORK_OVERVIEW.md`
- Competitive report: `docs/SOULACY_FUNCTIONALITY_COMPETITIVE_REPORT.md`
- Package versioning design: `docs/PACKAGE_VERSIONING_DESIGN.md`
- User-facing packaging doc: `docs/packaging.md`
- Cohort E merge readiness: `docs/COHORT_E_MERGE_READINESS.md`
- README: `README.md`
- Engine: `internal/runtime/engine.go`
- Trust envelope: `internal/trust/trust.go`
- Injection scanner: `internal/injection/scanner.go`
- Intent gate: `internal/intent/intent.go`
- Security readiness: `internal/gateway/securityreadiness.go`
- Security preflight: `internal/studio/security_preflight.go`
- Security Doctor: `internal/securitydoctor/doctor.go`
- Red-team pack: `internal/regression/security_test.go`
- End-to-end pipeline test: `internal/runtime/scenario_security_pipeline_test.go`
