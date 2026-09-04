# Soulacy Functionality And Competitive Roadmap

Date: 2026-07-02

## Executive Summary

Soulacy is best understood as a self-hosted agent runtime, not merely a personal assistant. Its strongest current advantages are: single-binary Go deployment, declarative `SOUL.yaml` agents, a broad local-first control plane, provider-agnostic LLM routing, MCP client support, scheduled agents, channel adapters, encrypted secrets, plugin/sidecar infrastructure, observability, Workboard, and a rich Svelte GUI.

The most important competitive lesson from OpenClaw and Hermes is that users do not buy "a runtime" in the abstract. They buy an always-on assistant that remembers, learns, reaches them where they already are, and completes real workflows. Soulacy already has much of the machinery. The next leap is to turn the machinery into a coherent, self-improving, multi-channel assistant experience.

OpenClaw competes on breadth, assistant feel, native companions, channels, voice, and "it just does things." Hermes competes on the learning loop: persistent memory, self-created skills, cross-session recall, and sub-agent execution. Soulacy can challenge both by becoming the most reliable local-first agent operating system: auditable, hackable, safe, extensible, and genuinely useful for scheduled and interactive work.

## Rerun Verdict After Implementation Sprint

Soulacy has moved from "powerful runtime with promising pieces" to a credible local-first agent operating system. The major gaps called out in the earlier analysis are no longer blank spaces: the product now has a visible learning loop, Studio self-heal, build-until-it-works repair, provider/secrets doctor checks, Docker/SSH executor backends, browser automation via MCP, process cleanup for MCP tools, and clearer Telegram default-output routing.

The competitive center of gravity has changed:

- Against OpenClaw, Soulacy is now much stronger in inspectable workflows, scheduled agents, Studio repair, observability, provider control, and safety-oriented local operation. OpenClaw still wins on native companion polish, very broad channel coverage, onboarding, voice, and the simple "assistant that follows me everywhere" story.
- Against Hermes, Soulacy now has enough learning infrastructure to contest the self-improvement story, especially because proposals are visible, reviewable, backed by recurring reflection, and available to agents through safe session search. Hermes still wins on the crispness of its learning narrative, automatic memory/skill compounding, and remote-agent positioning.
- Soulacy's strongest wedge is not "another chatbot" or "another automation canvas." It is a local-first command center where scheduled, interactive, multi-channel, tool-using agents can be built, tested, repaired, audited, and delivered to real channels.

Go-to-market readiness is now less about inventing core architecture and more about productization: first-run setup, excellent templates, safer defaults, regression suites around real workflows, a mobile/PWA companion, public docs, and a tighter story.

## What Is Implemented Today

### Runtime And Agent Model

Implemented:

- Single gateway binary with embedded GUI.
- `SOUL.yaml` per-agent configuration.
- Agent triggers: channel, cron, oneshot/internal-style peer execution.
- Agent hot reload through filesystem watcher.
- Per-agent LLM provider/model/temperature/max-token/tool-choice configuration.
- Agent-as-tool peer delegation through `agent__<id>`.
- Reasoning strategies, including ReAct and flow/workflow execution.
- Python tool execution and built-in system tools.
- Structured output enforcement paths.
- Run timeouts, max turns, and streaming flags.

Representative code/docs:

- `internal/runtime/engine.go`
- `internal/runtime/loader.go`
- `internal/runtime/watcher.go`
- `pkg/agent/types.go`
- `docs/agents/soul-yaml.md`
- `docs/FRAMEWORK_OVERVIEW.md`

### GUI

Implemented pages:

- Dashboard
- Agents
- Chat
- Studio
- Templates
- Brain Memory
- Knowledge
- Workboard
- Channels
- Schedule
- Skills
- MCP
- Plugins
- Providers
- Secrets
- Activity
- Config
- Logs

Notable implemented chat features:

- Chat sessions.
- Branch/fork support.
- Token/cost deltas.
- Message actions.
- Markdown rendering.
- Run metrics.
- Provider/model controls.
- Tool confirmation flow.

Representative files:

- `gui/src/pages/Chat.svelte`
- `gui/src/pages/Agents.svelte`
- `gui/src/pages/Studio.svelte`
- `gui/src/pages/Workboard.svelte`
- `gui/src/pages/Schedule.svelte`
- `gui/src/pages/Channels.svelte`
- `gui/src/lib/chatactions.js`
- `gui/src/lib/chatbranch.js`

### Studio

Implemented:

- Visual flow editor.
- Node palette and inspector.
- Trigger/output nodes.
- Tool/agent/python/branch style workflow concepts.
- Flow save/load.
- Build/refine/preflight/test-run infrastructure.
- Autowire, portization, template reference checking, output var inference.
- Composite blocks.
- Python node support and validation.
- Model-assisted build loop.
- Runtime troubleshooting loop: failed Activity sessions can be handed to Studio,
  diagnosed from real action-log evidence, repaired with the build/verify loop,
  and reviewed before saving the fixed agent.
- Whole-workflow integrity checks for missing inputs, invalid template references,
  broken ports, missing channel outputs, and generated Python variable mistakes.
- LLM extraction nodes for normalizing user intent before brittle downstream
  tool calls.
- "Build until it works" repair path and failed-run self-heal panel.

Current product issue:

Studio has deep compiler/runtime machinery, but the user experience still asks users to reason in low-level graph terms. Entry/exit blocks and manual wiring are easy to misunderstand. The correct long-term direction is an intent-first workflow builder where the LLM proposes the plan, Studio renders it as a rich editable plan, and advanced users can manually add Python/tool/agent blocks.

Representative files:

- `internal/studio/*`
- `gui/src/pages/Studio.svelte`
- `gui/src/lib/studio/*`
- `docs/STUDIO_REDESIGN.md`
- `docs/STUDIO_PYTHON_TOOLS.md`

### LLM Providers

Implemented:

- Provider router.
- Built-in providers for OpenAI-compatible endpoints, Anthropic, Gemini, Ollama, Ollama Cloud style endpoints through OpenAI-compatible config.
- Provider model listing.
- Dynamic provider registration from GUI saves.
- Provider credential handling through config and encrypted vault.
- Retry/body replay fixes.
- Embedder abstraction for RAG.

Representative files:

- `internal/llm/router.go`
- `internal/llm/openai.go`
- `internal/llm/anthropic.go`
- `internal/llm/gemini.go`
- `internal/llm/ollama.go`
- `internal/gateway/api.go`
- `internal/secrets/*`

### Channels And Scheduled Output

Implemented:

- HTTP chat channel.
- Telegram.
- WhatsApp Web.
- Slack.
- Discord.
- Channel status and GUI configuration.
- Outbound-only Telegram mode for scheduled output.
- Scheduled output routing through `schedule.output`.
- Failure notifications through `notify_on_failure`.
- Channel binding safety for privileged agents.
- Channel delivery diagnostics in Channels and Doctor for missing credentials,
  offline adapters, invalid bot mappings, and default-output gaps.

Representative files:

- `internal/channels/*`
- `internal/app/wire_channels.go`
- `internal/scheduler/scheduler.go`
- `docs/channels/*`

### Scheduling

Implemented:

- Cron agents.
- One-shot style scheduling support in model/docs.
- Missed run behavior.
- Schedule page.
- Manual run/history/watch actions.
- Scheduled output delivery to configured channel adapter.

Representative files:

- `internal/scheduler/scheduler.go`
- `gui/src/pages/Schedule.svelte`
- `docs/using/schedules.md`

### Memory, Knowledge, And RAG

Implemented:

- Conversation history.
- Session forking.
- Memory store/archive layers.
- Agent memory and rule logs.
- Knowledge base UI/API.
- SQLite-backed document/chunk storage.
- Embedding registry.
- KB search tool.
- Brain memory UI concepts.

Representative files:

- `internal/memory/*`
- `internal/session/*`
- `internal/agentmemory/*`
- `internal/knowledge/*`
- `internal/llm/embed.go`
- `gui/src/pages/Memory.svelte`
- `gui/src/pages/Knowledge.svelte`

Gap:

Soulacy now has the first visible layer of a Hermes-style learning loop: agents can enable learning, successful or manually selected runs can generate proposals, recurring background reflection can discover proposals from action logs, agents can search relevant past sessions during planning, accepted learned skills are injected into future planning, and the Brain Memory UI supports review/edit/accept/reject. The remaining gap is stronger user-facing evidence that the product is getting better over time.

### MCP, Skills, Plugins, Sidecars

Implemented:

- MCP client configuration and tool exposure.
- Skills loader using `SKILL.md` style.
- Skill source discovery and registry probing.
- Plugin manifest, plugin install gate, plugin capabilities.
- External Channel Protocol.
- External Storage Protocol.
- Sidecar supervision.
- Plugin credential delegation.
- Plugin GUI iframe mounting.
- Custom distribution build system.
- MCP process janitor that cleans up tool descendants after runs.
- Browser automation quick-start through the Playwright MCP sidecar.

Representative files/docs:

- `internal/mcp/*`
- `internal/skills/*`
- `internal/plugins/*`
- `internal/plugininstall/*`
- `internal/pkgregistry/*`
- `sdk/extchannel/*`
- `sdk/extstorage/*`
- `docs/PLUGIN_MANIFEST.md`
- `docs/PLUGIN_CAPABILITIES.md`
- `docs/EXTERNAL_CHANNEL_PROTOCOL.md`
- `docs/EXTERNAL_STORAGE_PROTOCOL.md`
- `docs/PACKAGE_REGISTRIES.md`

### Workboard And Observability

Implemented:

- Workboard task model.
- Kanban UI.
- Assignment to agents.
- Task runs/retries.
- Run metrics and costs.
- Action log and event stream.
- Event publishing and signed outbound webhook infrastructure.
- Costs API.
- Prometheus metrics.

Representative files:

- `internal/gateway/workboard.go`
- `internal/costs/*`
- `internal/actionlog/*`
- `internal/gateway/events.go`
- `gui/src/pages/Workboard.svelte`
- `gui/src/lib/RunMetrics.svelte`
- `docs/EVENTS.md`

### Security And Administration

Implemented:

- API key auth.
- RBAC structures/middleware.
- Encrypted credential vault.
- Global secrets API.
- Credential rotation/versioning support.
- Capability tiers for agent/channel exposure.
- Sandbox rlimits for subprocesses.
- Rate limiting.
- Config GUI.
- Doctor/onboard/daemon CLI work appears at least partially present.
- Provider/secrets doctor surface in the GUI/API.
- Channel delivery doctor checks for production readiness.

Representative files:

- `internal/auth/*`
- `internal/rbac/*`
- `internal/credentials/*`
- `internal/secrets/*`
- `internal/tier/*`
- `internal/sandbox/*`
- `internal/ratelimit/*`
- `cmd/sy/*`

## Competitive Baseline

### OpenClaw

Public positioning: OpenClaw describes itself as a personal AI assistant that runs on your devices, answers on existing channels, can speak/listen on native platforms, renders a live Canvas, and supports many channels. Its GitHub README lists channels including WhatsApp, Telegram, Slack, Discord, Google Chat, Signal, iMessage, Teams, Matrix, LINE, Twitch, WeChat, WebChat, and more. It recommends `openclaw onboard --install-daemon` as the preferred setup path.

Competitive implication:

- OpenClaw feels like an always-on personal assistant first.
- Breadth of channels and native app story matters.
- Onboarding and daemonization are table stakes.
- Its ecosystem pitch is more vivid than Soulacy's runtime pitch.

### Hermes

Public positioning: Hermes describes itself as the agent that grows with you: persistent memory, automated skill creation, multi-platform reach, scheduled automations, parallel sub-agents, browser/web control, and multiple execution backends. The GitHub README emphasizes a built-in learning loop, skill creation from experience, self-improving skills, past-conversation search, and a deepening user model.

Competitive implication:

- Hermes owns the "learning loop" narrative.
- It turns memory into a product promise, not just infrastructure.
- Sub-agents, remote execution, and skill creation are core.
- Soulacy needs a first-class learning loop to compete directly.

## Where Soulacy Is Strongest

1. Local-first runtime architecture.
2. Declarative YAML agents that are easy to inspect, diff, and version.
3. Single-binary deployment model.
4. Deep gateway/API/GUI surface already implemented.
5. Strong plugin and sidecar architecture.
6. Capability/security model is more principled than simple contact pairing.
7. Scheduled agents and channel output are already practical.
8. Workboard + observability gives Soulacy operational depth.
9. Studio has serious compiler/runtime machinery underneath it.
10. Provider abstraction is flexible enough for most OpenAI-compatible ecosystems.
11. Studio can now learn from real runtime failures and propose repairs instead of leaving the user alone with stack traces.
12. Default outbound channel routing makes non-interactive cron agents practical.

## Updated Competitive Scorecard

| Capability | Soulacy Now | OpenClaw | Hermes | Read |
| --- | --- | --- | --- | --- |
| Local-first assistant runtime | Strong | Strong | Strong | All three can claim local/self-hosted control; Soulacy's Go binary and declarative agents are a clean operator story. |
| Channel breadth | Medium | Very strong | Medium | OpenClaw still wins reach with many messaging/native surfaces. Soulacy should compete through generic adapters, sidecars, and excellent Telegram/Slack/Discord/WhatsApp reliability first. |
| Scheduled agents | Strong | Strong | Strong | Soulacy is competitive because schedules, Activity, watch/history, and channel output are first-class. |
| Workflow builder | Strong but complex | Medium | Low/medium | Studio is a differentiator if it becomes intent-first. The compiler/repair machinery is already deeper than a simple canvas. |
| Self-improvement | Strong foundation | Medium | Very strong | Soulacy has visible proposals, review, background reflection, and session search. Hermes still owns the narrative around automatic memory/skill compounding. |
| Remote execution | Medium | Strong | Strong | Soulacy has Docker/SSH/local executor foundations; needs productized selection, vault helpers, and cloud presets. |
| Browser/computer automation | Medium | Strong | Strong | Soulacy has MCP/Playwright quick-start and process cleanup; needs viewer, trace, and domain policy UX. |
| Observability/debugging | Strong | Medium | Medium | Soulacy's Activity logs, Studio repair, run replay, and Workboard are a real advantage. |
| Onboarding/go-to-market polish | Weak/medium | Strong | Medium/strong | This is the largest commercial gap. Users need a guided path from install to first useful assistant in 10 minutes. |
| Safety/governance | Strong foundation | Medium/strong | Medium | Soulacy's capability tiers, RBAC, vault, doctor, and local-first design can become a trust moat if surfaced cleanly. |

## Current Weaknesses

1. Product narrative is fragmented: runtime, Studio, Workboard, chat, schedules, plugins, memory all exist, but do not yet feel like one assistant.
2. Studio is too graph-centric for non-expert users.
3. Chat is improving but still behind ChatGPT/Claude polish: artifacts, files, search, project context, rich editing, citations, share/export, inline tool previews, and keyboard ergonomics need work.
4. Learning proposals, background reflection, session search, accepted procedures, and accepted learned-skill injection exist; longitudinal evidence is now surfaced through the learning Evidence panel (skill reuse counts and repeated-error reduction over time). Remaining work is broadening the evidence to cross-agent and multi-week trend views.
5. Channel breadth is narrower than OpenClaw.
6. Native/mobile companion story is absent or not productized.
7. Voice now has a Chat push-to-talk MVP, ephemeral OpenAI Realtime key flow, and readiness/parity visibility; it still needs credential-backed mic UAT plus native/wake/talk-back polish if positioned as a differentiator.
8. Browser/computer control exists through MCP, but needs a polished session viewer, domain policy, and trace UX.
9. Provider/key flows are better with the doctor, but onboarding still needs to prevent broken state before users hit failures.
10. Documentation has many deep docs, but needs a crisp public product guide.
11. Channel delivery semantics are still too technical; default outbound bots, agent-specific bots, and channel-safe formatting need clearer guided setup.
12. Release packaging, daemon management, upgrade flow, and health checks need to feel boring and dependable before a commercial launch.

## Major Feature Additions To Challenge OpenClaw And Hermes

### 1. Soulacy Learning Loop

Goal: challenge Hermes directly.

Build a closed loop around every completed task. The first proposal/review layer is implemented; the next step is turning approved learnings into future behavior:

- Detect task success/failure.
- Extract reusable facts, preferences, procedures, and tool recipes.
- Propose memory writes with provenance and confidence.
- Auto-create or update `SKILL.md` files after repeated successful workflows.
- Run periodic reflection jobs. *(implemented)*
- Search past sessions and memories during planning. *(session search implemented; memory search already available through KB/vector tools)*
- Show "what I learned" in the GUI.

MVP stories:

- Add `learning.enabled` per agent. *(implemented)*
- Add a post-run learning pipeline fed by action logs. *(implemented)*
- Add activity-triggered learning proposals from real runs. *(implemented)*
- Add memory proposals UI: accept/reject/edit. *(implemented)*
- Add skill proposal UI: generated `SKILL.md`, tests/checklist, install button. *(implemented)*
- Add "why I remembered this" provenance on memory entries. *(partially implemented through learning summary source/tool metrics)*
- Inject accepted procedures/skills into future planning with clear attribution. *(implemented for procedural memory and accepted installed skills)*
- Add recurring reflection jobs with approval gates. *(implemented)*
- Add learning quality metrics: accepted/rejected proposal rate, confidence, installed skills, source/tool provenance. *(implemented)*
- Add longitudinal learning evidence: skill reuse counts (per accepted skill) and repeated-error reduction before/after learning. *(implemented via `GET /api/v1/learning/evidence` and the Brain Memory "Is it working?" panel)*

### 2. Intent-First Studio

Goal: make Studio richer and more intuitive than drag/drop flow builders.

Replace "build a graph manually" with:

- User describes desired automation.
- Studio generates a plan with lanes: Trigger, Gather, Think, Act, Verify, Deliver.
- Graph is a visualization of the plan, not the primary authoring model.
- Users can add Python/tool/agent blocks manually when they know what they want.
- LLM suggests when Python is useful, but user can override.
- Entry/exit blocks become implicit anchors, not draggable concepts.

MVP stories:

- Plan-first builder view.
- Natural language "add a step" command.
- Block recommendation engine.
- Rich block inspector with examples, test input, mock output.
- Inline preflight errors with "fix with AI".
- Trace replay on the graph.

### 3. ChatGPT/Claude-Class Chat

Goal: make Soulacy pleasant enough to live in.

Add:

- Project/workspace context selector.
- File uploads and attachments with previews.
- Artifact side panel for generated files, tables, charts, code, reports.
- Inline tool call cards with expandable stdout/stderr.
- Conversation search.
- Message edit + regenerate variants.
- Branch tree.
- Saved prompts/templates.
- Citations from KB/web/tool outputs.
- Export/share session.
- Better stop/resume/cancel controls.
- Keyboard shortcuts and command palette.

MVP stories:

- Artifact panel tied to run/artifact events.
- File upload ingestion into session context or KB.
- Message edit/regenerate.
- Search across session history.
- Tool result cards with copy/open/download.

### 4. OpenClaw-Class Channels Without Building 25 Adapters

Goal: compete on reach without maintaining dozens of native integrations.

Build:

- Generic webhook inbound channel.
- Generic outgoing webhook/channel adapter.
- MCP server mode exposing Soulacy agents and platform operations as tools. *(implemented via `sy mcp serve`)*
- Official channel sidecar templates for Signal, Matrix, iMessage, Google Chat.
- Channel marketplace via plugin/sidecar registry.
- Guided default outbound bot setup for Telegram/Slack/Discord/WhatsApp.
- Agent-specific channel mappings that are visibly distinct from default scheduled-output delivery.
- Text-safe formatters for channel surfaces that cannot render rich GUI artifacts.

MVP stories:

- `webhook` channel with request mapping templates.
- `channel.send` generic HTTP adapter.
- `sy mcp serve` exposing enabled agents as MCP tools, plus generic `soulacy_chat`, schedule status/list, Workboard task list/run, KB list/search, and ephemeral queue inspect/put/take/clear tools. *(implemented)*
- Sidecar starter kit for a new channel.
- Default Telegram outbound bot. *(implemented)*
- Agent-specific Telegram bot mappings. *(implemented)*
- Channel-safe chart/plaintext formatting for Telegram. *(implemented)*

### 5. Native Companion And Mobile Pairing

Goal: challenge OpenClaw's companion-app narrative.

Do not start with full native mobile apps. Start with a responsive PWA and pairing:

- Installable mobile web app.
- QR pairing from desktop.
- Push notifications.
- Approve/deny tool calls from phone.
- View active runs and schedule history.
- Send chat messages to agents.

MVP stories:

- Pairing token/QR system.
- Mobile-first Chat/Activity/Approvals pages.
- Web push notifications.
- "Approve tool" notification action.

### 6. Browser And Computer Control

Goal: make agents actually do more real-world work.

Add a first-class browser automation sidecar:

- Playwright sidecar with screenshot/click/type/extract tools.
- Browser session viewer in GUI.
- Permission model for domains and actions.
- Recorded traces.
- Reusable browser skills.

MVP stories:

- `browser` sidecar plugin through MCP/Playwright quick-start. *(implemented)*
- MCP process janitor to clean up stray browser/tool child processes. *(implemented)*
- GUI live screenshot + current URL.
- Per-domain approval policy.
- Trace replay and artifact capture.

### 7. Remote Execution Backends

Goal: challenge Hermes' "runs anywhere" story.

Add remote executors:

- Local.
- Docker.
- SSH.
- Modal/RunPod/Daytona-style cloud backends later.

MVP stories:

- Executor abstraction for tools and Studio Python nodes. *(implemented)*
- SSH executor. *(implemented; vault credential helper still future)*
- Docker executor. *(implemented; volume policy still future)*
- Per-agent execution backend selection.

### 8. Assistant Persona And Operating Model

Goal: convert runtime into assistant.

Add an opinionated "Primary Assistant" layer:

- Persona setup.
- Connected accounts checklist.
- Daily heartbeat.
- Proactive suggestions.
- User preferences.
- Personal/team memory.
- Default automation templates.

MVP stories:

- `sy onboard` / GUI onboarding for assistant identity.
- First-run "what should I help with?" wizard.
- Daily check-in agent template.
- Proactive opportunity detector over recent activity and schedules.

### 9. Reliability And Trust Layer

Goal: make Soulacy feel safer than OpenClaw/Hermes.

Add:

- Provider health checks.
- Secret consistency checks.
- Run replay.
- Rollback for agent changes.
- Dry-run mode for tools.
- Policy engine for high-risk actions.
- Better failure notifications.

MVP stories:

- Provider/secrets doctor checks in the GUI and API. *(implemented)*
- `sy doctor` v2 with provider/vault consistency checks.
- Agent version history and rollback. *(implemented)*
- Run replay from action log. *(implemented)*
- Activity-to-Studio run debugging with action-log evidence and verified repair. *(implemented)*
- Studio whole-workflow integrity checks before save/run. *(implemented)*
- Channel delivery diagnostics and guided fixes. *(implemented in Channels and Doctor)*
- Policy prompts for shell/file/network actions.

### 10. Template Gallery And Vertical Packs

Goal: make value obvious in 10 minutes.

Ship high-quality templates:

- Daily stock screener.
- Flight deal finder.
- Meeting minutes.
- Inbox triage.
- Competitor monitor.
- Document compliance auditor.
- Personal finance monitor.
- GitHub issue triage.
- Research brief generator.

MVP stories:

- Template gallery with "Install" and "Try".
- Required secrets checklist.
- Test run with mocks.
- Schedule/channel output setup wizard.

## Priority Roadmap

### Phase 0: Production Launch Hardening

1. First-run guided onboarding that configures provider, default assistant, default outbound channel, and one template.
2. Template install wizard with required secrets, channel destination, mock test, real test, and schedule.
3. End-to-end regression pack for top workflows: weather, stock screener, deal finder, flight finder, Telegram output, Studio repair.
4. Release packaging: daemon/service install, upgrade, rollback, doctor, and log bundle.
5. Public docs: install, first agent, channels, Studio, scheduled output, troubleshooting.
6. Security posture: default policies, channel allowlists, secret redaction, and production checklist.
7. Mobile/PWA companion for approvals, Activity, and simple chat.

### Phase 1: Trust And Product Cohesion

1. Provider/secrets doctor checks. *(implemented)*
2. Chat artifact panel.
3. File upload/session attachments.
4. Template gallery polish.
5. Failure notifications and schedule output QA. *(channel diagnostics implemented; delivery regression pack still future)*

### Phase 2: Learning Loop

1. Post-run memory extraction. *(implemented)*
2. Memory proposal UI. *(implemented)*
3. Skill proposal generation. *(implemented)*
4. Past-session semantic search in planning.
5. Periodic reflection jobs.

### Phase 3: Studio Reframe

1. Intent-first Studio plan view.
2. LLM-generated plan lanes.
3. Manual Python/tool block insertion.
4. Rich testing/mocking/replay.
5. One-click deploy to agent/schedule/channel.

### Phase 4: Reach

1. Generic webhook channel.
2. Mobile PWA pairing and approvals.
3. MCP server mode. *(implemented for agent, schedule, Workboard, KB, and queue tools via `sy mcp serve`)*
4. Browser automation sidecar. *(MCP quick-start implemented)*
5. SSH/Docker execution backends. *(implemented)*

## Strategic Positioning

The best positioning is:

> Soulacy is the local-first agent operating system: declarative, inspectable, schedulable, multi-channel, self-improving, and safe enough to trust with real work.

Do not try to beat OpenClaw by adding 25 channels manually. Beat it with a cleaner extension model, generic channel adapters, and a safer local-first core.

Do not try to beat Hermes by only adding more memory tables. Beat it with a visible learning loop that turns completed tasks into memories, procedures, and skills the user can inspect and approve.

## What Would Make Soulacy Distinct

The defensible product is:

> A local-first, inspectable agent OS for people who need agents to run reliably, on schedules and channels, with visible repair, learning, and governance.

That is different from:

- ChatGPT/Claude: great conversational intelligence, weaker local scheduled operations and channel ownership.
- OpenClaw: excellent personal assistant reach, but less focused on visual workflow repair and auditable agent operations.
- Hermes: excellent self-improvement story, but less focused on nontechnical workflow authoring, channel output, and operator-grade observability.

Soulacy should make three promises repeatedly:

1. **Build it:** describe the automation, let Studio create the agent/workflow, then inspect or edit it.
2. **Run it:** trigger from chat, channel, cron, webhook, or another agent.
3. **Fix and learn:** when it fails, Studio can diagnose the real run, repair the workflow, and turn successful patterns into reviewed learnings.

## External References Checked

- OpenClaw website and GitHub README: positioning around personal assistant, channels, native apps, onboarding, and broad channel support. See <https://openclaw.ai/> and <https://github.com/openclaw/openclaw>.
- OpenClaw docs: setup/onboarding and channel-first assistant story. See <https://docs.openclaw.ai/start/openclaw>.
- Hermes GitHub README: positioning around an agent that grows with the user. See <https://github.com/NousResearch/hermes-agent>.
- Hermes memory docs: persistent memory, session search, background self-improvement review, staged memory/skill writes, and external memory providers. See <https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/memory.md>.
