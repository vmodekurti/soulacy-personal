# Soulacy

**One binary. YAML agents. Runs anywhere — no cloud required.**

!!! info "Documentation channel"
    These pages track the current `main` branch and may describe capabilities
    newer than the latest tagged binary. Check `sy version`, review
    [Recent platform updates](recent-updates.md), and use the
    [upgrade guide](deployment/upgrades.md) before changing a production host.

Soulacy is a self-hosted AI agent runtime. Write an agent in a single YAML file, point it at any LLM (Ollama, OpenAI, Anthropic, Gemini, or anything OpenAI-compatible), and run it from a laptop, a $5 VPS, or a Raspberry Pi — with a full web GUI, chat, voice, scheduling, memory, skills, and plugins built into the one binary.

Think of it as Ollama — but for agents.

## Build it. Run it. Fix and learn.

- **Build it** — describe what you want in plain English in [Studio](using/studio.md),
  or start from a [template](template-guides/index.md). Soulacy recommends an
  agent strategy, lets you make the trigger and destination authoritative, and
  checks the result before you save. Fixed-graph workflow generation is an
  explicit experimental option in v0.1.8.
- **Run it** — deploy to Telegram, Slack, Discord, WhatsApp, HTTP, or a schedule.
  One binary, no cloud required.
- **Fix and learn** — when a run fails, **Debug in Studio** explains it plainly and
  proposes a fix you can preview; successful repairs become regression tests,
  explicit 👍/👎 feedback improves workflow-pattern ranking, and the
  [learning loop](studio-learning-memory.md) shows what Soulacy has learned.

```bash
# install, set up, talk to your first agent — under five minutes
curl -fsSL https://soulacy.io/install.sh | bash
sy setup
sy chat --agent assistant "What can you do?"
```

[Get started :material-rocket-launch:](getting-started/quickstart.md){ .md-button .md-button--primary }
[Tour the GUI :material-monitor:](getting-started/gui-tour.md){ .md-button }

---

## What you can build

<div class="grid cards" markdown>

-   :material-file-document-edit: **Agents from one YAML file**

    ---

    Identity, LLM, tools, memory, schedule — one `SOUL.yaml` per agent. Edit in the GUI or your editor; changes hot-reload.

    [:octicons-arrow-right-24: SOUL.yaml reference](agents/soul-yaml.md)

-   :material-chat-processing: **Chat with branching & voice**

    ---

    Fork a conversation from any message, watch reasoning steps live, see per-reply token costs, or hold a realtime voice conversation.

    [:octicons-arrow-right-24: Chat](using/chat.md) · [Voice](using/voice.md)

-   :material-message-flash: **Every channel**

    ---

    Telegram, Slack, Discord, WhatsApp, HTTP out of the box — or any platform via a sidecar process in the language of your choice.

    [:octicons-arrow-right-24: Channels](channels/index.md)

-   :material-puzzle: **Skills & plugins, safely**

    ---

    Install skills from skills.sh, GitHub, or your own registry. Every install runs a security pipeline; plugins are sandboxed, default-deny principals.

    [:octicons-arrow-right-24: Installing skills](extend/installing-skills.md) · [Skill sources](extend/skill-sources.md)

-   :material-brain: **Memory that learns**

    ---

    Session/agent/global memory scopes, persistent native sqlite-vec retrieval,
    and versioned procedural rulebooks — with locks, diffs, rollback, and
    reviewable human feedback.

    [:octicons-arrow-right-24: Memory & rulebooks](using/memory.md)

-   :material-graph: **Workflows & flow graphs**

    ---

    Linear steps or cyclic graphs with conditional edges and bounded loops — checkpointed, crash-resumable, rendered live on the Flow page.

    [:octicons-arrow-right-24: Flow graphs](agents/flows.md)

-   :material-calendar-clock: **Scheduling that catches up**

    ---

    Cron agents with missed-run catch-up after downtime, a workboard with tasks, comments, and downloadable run artifacts.

    [:octicons-arrow-right-24: Schedules](using/schedules.md) · [Workboard](using/workboard.md)

-   :material-shield-check: **Observable & governable**

    ---

    Every run emits schema-versioned events: live activity, signed webhooks,
    atomic spend reservations, model allowlists, rate limits, object-scoped
    RBAC, audit logs, and readiness checks.

    [:octicons-arrow-right-24: Events & webhooks](configuration/events.md)

</div>

## Five-minute tour

1. **Install** — one line on [macOS](deployment/macos.md), [Linux](deployment/linux.md), or [Docker](deployment/docker.md), then `sy setup` walks you through providers and channels. → [Installation](getting-started/installation.md)
2. **Meet the GUI** — everything lives at `http://localhost:18789`: Dashboard, Agents, Chat, Workboard, Knowledge, Memory, Skills, Flow, Plugins. → [GUI tour](getting-started/gui-tour.md)
3. **Write an agent** — a complete `SOUL.yaml` walkthrough: prompt, tools, memory, schedule. → [Your first agent](getting-started/first-agent.md)
4. **Give it skills** — `sy registry add https://www.skills.sh/` then `sy skill install anthropics/skills/skill-creator`. → [Skill sources](extend/skill-sources.md)
5. **Put it to work** — bind a Telegram bot, schedule a daily run, or start from a shipped [workflow template](using/templates.md).

## Why Soulacy

| | Soulacy | n8n / Flowise / Dify | LangGraph / AutoGen |
|---|---|---|---|
| **Deploy** | Single binary, zero deps | Docker + Postgres + Redis | Python package |
| **Config** | One YAML file per agent | Visual editor (brittle exports) | Code |
| **Runs on** | Laptop, VPS, Raspberry Pi | Needs a server stack | Dev machine |
| **LLM** | Any — local or cloud | Mostly cloud | Any |
| **No-code** | GUI included in binary | Yes | No |
| **Extensible** | Skills, plugins, sidecars in any language | JS nodes | Python |
| **Security stack** | Untrusted-content envelope, injection scanner, intent gate, Security Doctor — shipped | Third-party plugin | Operator's problem |

## What Soulacy is NOT

Positioning honesty — say what we are, and what we aren't, so you can
self-disqualify quickly if the fit's wrong.

- **Not a hosted SaaS.** No `soulacy.cloud`. Ever. Self-hosted-first is the
  point of the product.
- **Not a LangGraph replacement.** If you need explicit state-machine graphs
  with checkpoints and resumable execution, use LangGraph.
- **Not a personal assistant.** Soulacy runs headless and delivers to
  channels; it doesn't ship a wake-word, a Canvas, or a native mobile app.
  If you want an iMessage / WeChat / Signal / Matrix personal assistant, use
  [OpenClaw](https://openclaw.ai/).
- **Not vendor-locked.** Not tied to Anthropic, OpenAI, Google, or any
  provider. Provider-agnostic via config.
- **Not a low-code node editor for non-developers.** Studio helps, but the
  audience is developers/ops who prefer YAML + Python tools.

## Where to next

- **Users**: [Quick Start](getting-started/quickstart.md) → [GUI Tour](getting-started/gui-tour.md) → [Using Soulacy](using/chat.md)
- **Agent authors**: [SOUL.yaml Reference](agents/soul-yaml.md) → [Tools](agents/tools.md) → [Reasoning](agents/reasoning.md) → [Flow Graphs](agents/flows.md)
- **Operators**: [Configuration](configuration/index.md) → [Events & Webhooks](configuration/events.md) → [Upgrades](deployment/upgrades.md)
- **Extenders**: [Plugins](extend/plugins.md) → [Custom Channels](channels/sidecars.md) → [Custom Distributions](extend/custom-distributions.md) → [Specs](architecture/specs.md)
