# Quick Start

A working agent, the web GUI, and your first chat — in under five minutes.

Soulacy is a **local-first agent operating system**: Studio for authoring
and self-healing agents, Channels for delivery, Schedule for cron, Learning
for making the same mistake less often, Packaging for versioned installs —
all in one binary, all local by default.

## 1. Install

=== "macOS / Linux"

    ```bash
    curl -fsSL https://soulacy.io/install.sh | bash
    ```

=== "From source"

    ```bash
    git clone https://github.com/vmodekurti/soulacy-personal && cd soulacy
    make all          # builds the GUI + the soulacy and sy binaries into ./bin
    ```

More options (Docker, VPS, launchd service): [Installation](installation.md).

## 2. Run the onboarding wizard

```bash
sy onboard
```

The wizard creates or updates your workspace (`~/.soulacy/soulspace` — the
[soulspace layout](../configuration/workspace.md)), asks which LLM provider to
use, can wire web search and a starter agent, and can configure the release
manifest used by `sy update check`. Use `sy setup` only when you want to write a
fresh config from scratch.

!!! tip "No API key? Use Ollama."
    If you run [Ollama](https://ollama.com) locally, pick it in the wizard and Soulacy works fully offline — no cloud account needed.

## 3. Start the gateway

```bash
soulacy
```

The gateway starts on **http://localhost:18789** with the full web GUI: Dashboard, Studio, Agents, Chat, Workboard, Knowledge, Memory, Skills, integrations, and observability. Take the [GUI tour](gui-tour.md).

## 4. Talk to an agent

=== "GUI"

    Open **http://localhost:18789** → **Chat** → pick your agent → say hello.
    Watch the *Thinking* section show reasoning steps and tool calls live.

=== "CLI"

    ```bash
    sy chat --agent assistant "Summarize the latest AI news in 3 bullets"
    ```

=== "HTTP"

    ```bash
    curl -X POST http://localhost:18789/api/v1/agents/assistant/chat \
      -H "Authorization: Bearer $SOULACY_SERVER_API_KEY" \
      -H "Content-Type: application/json" \
      -d '{"message": "Hello!"}'
    ```

## 5. Make it yours

An agent is one YAML file. Open **Agents** in the GUI (or edit `~/.soulacy/soulspace/agents/assistant/SOUL.yaml`):

```yaml title="SOUL.yaml"
id: assistant
name: Assistant
trigger: channel
llm:
  provider: ollama        # or openai / anthropic / google / any OpenAI-compatible
  model: llama3
system_prompt: |
  You are a helpful, concise assistant.
channels:
  - http
```

Changes hot-reload — no restart. The full schema (tools, memory, reasoning, schedules, workflows) is in the [SOUL.yaml reference](../agents/soul-yaml.md).

## 6. Five things to try next

1. **Generate an agent from plain English** — open **Studio**, describe the
   outcome, explicitly review the trigger and delivery controls beside the
   prompt, then hit **Generate** (Streamed by default streams the pipeline
   phases live below the canvas; the Wizard variant lets you step through
   `clarify_intent → choose_strategy → build_graph → validate → repair`).
   Studio defaults to a native tool-calling agent; generated fixed workflows
   are experimental and require explicit opt-in. Pick a **Runtime intent**
   preset (Fast local / Reliable local / Cloud
   quality) in the Studio model modal to bake sensible timeouts into the
   agent. → [Studio](../using/studio.md)
2. **Give it skills** — add the public skill directory and install one:
   ```bash
   sy registry add https://www.skills.sh/
   sy skill install anthropics/skills/skill-creator
   ```
   Every install passes a [security review](../extend/safety.md) before you consent. → [Skill sources](../extend/skill-sources.md)
3. **Put it on Telegram** — a bot token and two YAML lines. The Channels page
   has inline guided setup cards for Telegram / Slack / Discord / WhatsApp /
   email / Teams / Google Chat, with per-field hints and a Test-delivery
   button that returns a friendly Diagnose reason on failure. → [Channels](../channels/telegram.md)
4. **Schedule it** — run every morning, with automatic catch-up after
   downtime. If the gateway missed a fire, the Automations row shows a
   `⟳ auto-replayed` chip and Activity emits a
   `schedule.missed_run_backfilled` event so you can tell "why did this fire
   at 03:04?" apart from a normal cron. → [Schedules](../using/schedules.md)
5. **Install a versioned package** — `sy pull <owner>/<repo>` or import via
   Agents → Import. The package v2 schema uses namespaced ids
   (`owner/name`), calendar versioning (`2026.7.15`), and an install-time
   secret gate that refuses to import if a required provider / channel /
   secret / MCP server is missing (with an "I understand — import anyway"
   opt-out for local experiments). → [Packaging](../packaging.md)

## Where everything lives

| | |
|---|---|
| Workspace (agents, skills, data) | `~/.soulacy/soulspace/` — [layout](../configuration/workspace.md) |
| Config file | `config.yaml` in the workspace — [reference](../configuration/index.md) |
| GUI | `http://localhost:18789` — [tour](gui-tour.md) |
| CLI | `sy` — [reference](../cli/reference.md) |

## Verify the first run

Do not stop at “the page opened.” Confirm the full path:

```bash
sy version
sy doctor
```

Then check:

1. **Providers** reports at least one tested connection.
2. **Chat** returns a response from the selected agent.
3. **Activity** contains the completed run and no unresolved tool or provider
   error.
4. The selected agent's `SOUL.yaml` contains the provider, model, trigger, and
   channels you intended.

If the gateway runs as a service, execute Doctor with the same user and
environment as that service; see [Linux/VPS deployment](../deployment/linux.md).
