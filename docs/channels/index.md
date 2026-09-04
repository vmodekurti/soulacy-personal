# Channels Overview

Channels are adapters that connect agents to messaging platforms. Each channel handles platform authentication, inbound message normalization, outbound replies, and live connection status.

## Supported channels

| Channel | Status | Config key |
|---------|--------|-----------|
| [HTTP](http.md) | ✅ Stable | — (always active) |
| [Generic Webhooks](webhook.md) | ✅ Stable | agent `trigger: webhook` |
| [Telegram](telegram.md) | ✅ Stable | `channels.telegram` |
| [Slack](slack.md) | ✅ Stable | `channels.slack` |
| [Discord](discord.md) | ✅ Stable | `channels.discord` |
| [WhatsApp](whatsapp.md) | ✅ Stable | `channels.whatsapp` |
| [Email](email.md) | ✅ Stable | `channels.email` |
| [Microsoft Teams](teams.md) | ✅ Stable outbound | `channels.teams` |
| [Google Chat](google-chat.md) | ✅ Stable outbound | `channels.google_chat` |
| [Outgoing Webhook](webhook.md#outbound-webhook-delivery) | ✅ Stable | `channels.webhook` |

## How channels route to agents

There are three related configuration layers:

1. `config.yaml` chooses which agent a channel adapter routes inbound messages to.
2. The agent's `SOUL.yaml` `channels:` list declares which channels that agent is intended to be reachable on.
3. `default_output_to` gives scheduled or one-off sends a destination when the caller does not provide `to`.

For a simple single-bot channel, `agent_id` is the routing target:

```yaml title="config.yaml"
channels:
  telegram:
    enabled: true
    token: "1234567890:AAH..."
    agent_id: assistant
```

```yaml title="agents/assistant/SOUL.yaml"
id: assistant
trigger: channel
channels:
  - telegram
  - http
```

At runtime, Telegram inbound messages become messages with `channel: telegram` and `agent_id: assistant`. The engine handles the message and the channel registry sends the reply back through the same adapter ID.

Inbound replies and outbound reports are intentionally different:

| Need | Configure | Result |
| --- | --- | --- |
| One default bot for cron/non-interactive reports | Top-level channel token plus `default_output_to`; optionally `outbound_only: true` | Schedules and `channel.send` can send when no per-run destination is supplied |
| One interactive bot for one agent | A bot mapping with `agent_id` and platform credentials | Messages to that bot invoke exactly that agent |
| Multiple interactive bots | Multiple bot mappings, one per agent | Each mapping gets its own adapter ID such as `telegram-weather-agent` |
| One agent to answer in the same chat that triggered it | Agent `trigger: channel` and matching `channels:` entry | Soulacy replies to the inbound chat/thread automatically; no `channel.send` step is required |

The default outbound sender is a fallback for delivery. It is not the same thing
as an interactive bot mapping. Interactive mappings are what make a channel
message invoke an agent.

## Activation safety

Message channels use explicit activation guardrails by default. Enabling a
channel should not make every platform message an agent prompt.

Common safety fields:

```yaml title="config.yaml"
channels:
  telegram:
    trigger_phrase: "!soulacy"
    ignore_groups: true
    allowed_chat_ids: ""
    allowed_user_ids: ""
```

- `trigger_phrase` gates activation. A message must start with this phrase, and
  the phrase is stripped before the agent sees the text.
- `ignore_groups` drops group/server/channel messages unless you explicitly set
  it to `false`.
- `allowed_chat_ids` restricts activation to specific platform destinations.
- `allowed_user_ids` restricts activation to specific senders.

The same model applies to Telegram, Slack, Discord, WhatsApp Cloud API, and
WhatsApp Web. HTTP is always active because callers explicitly invoke its API.

## Multi-bot mappings

Telegram, Slack, and Discord support multiple bot credentials under the same channel type. This is useful when you want separate platform bots mapped to separate agents.

```yaml title="config.yaml"
channels:
  telegram:
    enabled: true
    bots:
      - bot_name: "Assistant Bot"
        token: "BOT_TOKEN_FOR_ASSISTANT"
        agent_id: assistant
        allowed_user_ids: [123456789]
      - bot_name: "Finance Bot"
        token: "BOT_TOKEN_FOR_FINANCE"
        agent_id: financial-agent
        allowed_user_ids: [123456789]
```

The first bot keeps the canonical adapter ID. Additional bots get deterministic adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `telegram` | `assistant` |
| `telegram-financial-agent` | `financial-agent` |

The same pattern applies to Slack and Discord:

- first bot: `slack` or `discord`
- later bots: `slack-<agent_id>` or `discord-<agent_id>`

## Managing mappings in the GUI

Open **Channels** in the web UI:

- **Guided setup cards** walk you through Telegram, Slack, Discord, WhatsApp
  Cloud, WhatsApp Web, email (SMTP), Microsoft Teams, Google Chat, generic
  webhooks, and HTTP with step-by-step instructions, per-field hints keyed
  off the exact adapter config keys, and a Test-delivery button at the
  bottom.
- Channel cards show **Agent mappings**, including adapter ID, agent ID, and connection state.
- Channel cards show **Delivery checks** with failures, warnings, and remedies for missing tokens, missing default destinations, offline adapters, and broken bot mappings.
- Channel cards expose **Test** and **Diagnose** paths for outbound delivery.
  Failed tests return the same structured diagnosis used in Activity and support
  bundles, so `chat not found`, `missing scope`, bad webhook URLs, and rate
  limits are visible before a cron job depends on them. Email adds
  SMTP-specific categories — authentication failed, STARTTLS required,
  relay denied / SPF, recipient rejected, quota, message rejected, TLS
  handshake — so raw `535 5.7.8` / `550 5.1.1` codes don't bubble up as
  "unknown".
- Click **Edit** on Telegram, Slack, or Discord to manage **Bot mappings**.
- Bot mapping rows record a friendly bot name and provide an agent ID dropdown populated from your installed agents.
- After saving channel settings, click **Restart Gateway** from the banner to reconnect adapters.

The same readiness checks are exposed through **Providers -> Doctor** / `GET /api/v1/doctor` so production setups can verify channel delivery before relying on scheduled agents.

## Scheduled output through a bot

Cron agents run internally by default. To publish a scheduled run result to a specific bot, set `schedule.output` in the agent:

```yaml title="agents/daily-finance/SOUL.yaml"
trigger: cron
schedule:
  cron: "0 8 * * *"
  output:
    channel: telegram-financial-agent
    to: "123456789"
    bot_name: "Finance Bot"
```

`channel` is the adapter ID shown in **Channels -> Agent mappings**. `to` is the platform destination ID: Telegram chat ID, Slack channel/user ID, Discord channel ID, WhatsApp recipient number, email address, or an override webhook URL for outbound webhook-style channels such as Teams, Google Chat, and generic outgoing webhooks.

If a scheduled run succeeds but no message appears in the destination, check the
channel card's **Delivery checks** first. The most common causes are:

- The adapter ID in `schedule.output.channel` does not match a registered mapping.
- The adapter is offline because the gateway was not restarted after saving credentials.
- The bot token is missing or invalid.
- `schedule.output.to` or the channel-level default output destination is missing.
- The bot is not allowed to post to the target group/channel.
- A webhook-style channel URL was rotated or deleted in the destination app.

## Generic outbound send

Agents and Studio workflows can deliver to any registered adapter with the
built-in `channel.send` tool:

```yaml title="agents/notifier/SOUL.yaml"
id: notifier
builtins:
  - channel.send
```

Tool input:

```json
{
  "channel": "telegram",
  "to": "123456789",
  "text": "Daily report is ready.",
  "metadata": {
    "source": "daily-report"
  }
}
```

`channel` is the adapter ID shown in **Channels**. `to` is the platform-native
destination: Telegram chat ID, Slack channel/user ID, Discord channel ID,
WhatsApp recipient, email recipient, custom sidecar thread ID, or an override
HTTP URL for the outgoing webhook, Teams, or Google Chat adapters.

If `to` is omitted, Soulacy can only infer it when the run came from an inbound
chat/thread or the selected channel mapping has `default_output_to` configured.
For interactive channel agents, prefer returning the final answer normally. Use
`channel.send` only for out-of-band delivery, such as sending a second copy of a
report to a team channel.

## Multi-channel agents

An agent can be available on multiple channels simultaneously:

```yaml title="agents/assistant/SOUL.yaml"
id: assistant
name: Assistant
trigger: channel
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: You are a helpful assistant.
channels:
  - http
  - telegram
  - slack
  - discord
```

The channel must also be configured in `config.yaml`; the agent-side list alone does not create platform credentials.

## Channel-specific formatting

Each channel adapter handles platform-specific formatting automatically:

- **Telegram** — Markdown → MarkdownV2 escaping, inline buttons
- **Slack** — Markdown → Block Kit, thread replies
- **Discord** — Markdown → Discord markdown, embed cards
- **WhatsApp** — Plain text (WhatsApp does not support rich formatting)
- **HTTP** — Raw text or JSON, caller decides rendering
