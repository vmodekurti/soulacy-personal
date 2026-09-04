# Telegram

Connect your agents to Telegram using the Bot API.

## Setup

### 1. Create a bot

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts
3. Copy the token: `1234567890:AAH...`

### 2. Configure Soulacy

```yaml title="config.yaml"
channels:
  telegram:
    enabled: true
    token: "1234567890:AAH..."
    agent_id: assistant
    allowed_user_ids: []   # optional allowlist of Telegram user IDs
```

Telegram currently uses long polling by default, so it works on a laptop or private server without a public HTTPS URL.

### 3. Allow specific Telegram users

Set `allowed_user_ids` to restrict who can talk to the bot:

```yaml
channels:
  telegram:
    enabled: true
    token: "..."
    agent_id: assistant
    allowed_user_ids: [123456789, 987654321]
```

---

## Multiple Telegram bots

Use `bots:` when you want one Telegram bot per agent:

```yaml
channels:
  telegram:
    enabled: true
    bots:
      - token: "BOT_TOKEN_1"
        agent_id: assistant
        allowed_user_ids: [123456789]
      - token: "BOT_TOKEN_2"
        agent_id: financial-agent
        allowed_user_ids: [123456789]
```

This registers two adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `telegram` | `assistant` |
| `telegram-financial-agent` | `financial-agent` |

You can configure this from the GUI: **Channels → Telegram → Edit → Bot mappings**.

---

## Default Outbound vs Interactive Bots

Telegram can be used in two ways:

| Mode | Use it for | Configure |
| --- | --- | --- |
| Default outbound sender | Cron reports, one-off `channel.send`, non-interactive delivery | Top-level token, `default_output_to`, and usually `outbound_only: true` |
| Interactive agent bot | A Telegram bot that receives messages and invokes one agent | A bot mapping with `agent_id`, token, and allowlists |

If the channel card says `OUTBOUND-ONLY`, that is expected for the default
sender. It means the bot can send reports, but it will not poll inbound Telegram
messages. Add a bot mapping when you want a user to chat with an agent.

For interactive bot mappings, restart the gateway after saving, then send
`/start` to that exact bot. If the bot is used in a group, set
`ignore_groups: false`, add an allowlist if needed, and mention the bot or use
the configured trigger phrase.

---

## Send Scheduled Output To Telegram

For cron agents that only need to post results, configure Telegram as
`outbound_only`. In this mode Soulacy registers the bot for sends, but does not
poll Telegram for inbound messages and does not expose the agent to Telegram
users.

```yaml title="config.yaml"
channels:
  telegram:
    enabled: true
    token: "1234567890:AAH..."
    outbound_only: true
```

Then point the cron agent's `schedule.output` at that adapter and set `to` to
the Telegram chat, user, group, or channel id:

```yaml title="agents/daily-brief/SOUL.yaml"
id: daily-brief
name: Daily Brief
trigger: cron
schedule:
  cron: "0 7 * * *"
  output:
    channel: telegram
    to: "123456789"
    bot_name: "Daily Brief Bot"
    template: "{reply}"
```

For a Telegram channel, add the bot as an administrator and use the channel id
or public handle, for example `@your_channel_name`, as `schedule.output.to`.

### Troubleshooting Telegram Delivery

Open **Channels -> Telegram** and read **Delivery checks**:

- `Default outbound bot token is missing` means the top-level scheduled-output
  sender has no token saved.
- `Adapter is not registered` usually means settings were saved but the gateway
  has not been restarted yet.
- `Adapter is not connected` means the token, network, or Telegram Bot API
  connection failed.
- `No default output destination is configured` is fine for agents that set
  `schedule.output.to`, but cron agents without their own destination need a
  channel-level `default_output_to`.
- A dedicated bot mapping must either select an `agent_id` for interactive use
  or be marked `outbound_only` for send-only use.
- `chat not found` usually means `default_output_to` / `schedule.output.to` is
  wrong, the bot is not in the target chat/channel, or a private chat has not
  sent `/start` to that bot yet.
- If a mapped bot is listed as connected but never receives prompts, confirm the
  agent has `trigger: channel` and includes the mapping adapter ID or
  `telegram` in its `channels:` list.

You can also call `GET /api/v1/doctor`; its `channels` section returns the
same diagnostics for automated production checks.

---

## Features

| Feature | Support |
|---------|---------|
| Text messages | ✅ |
| Photos / images | ✅ (passed to vision-capable agents) |
| Documents | ✅ (text extracted and passed as context) |
| Voice messages | 🔜 Planned |
| Inline keyboards | ✅ (agent can emit buttons) |
| Groups & channels | ✅ (mention the bot to trigger) |
| Commands | ✅ |

---

## Example: minimal Telegram bot

```yaml title="config.yaml"
server:
  api_key: "sy_..."
llm:
  default_provider: openai
  providers:
    openai:
      api_key: "sk-..."
storage:
  type: sqlite
  path: ./soulacy.db
channels:
  telegram:
    enabled: true
    token: "1234567890:AAH..."
    agent_id: assistant
agent_dirs:
  - ./agents
```

```yaml title="agents/assistant/SOUL.yaml"
id: assistant
name: Assistant
trigger: channel
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: You are a helpful assistant on Telegram.
channels:
  - telegram
```

Start with `soulacy serve --config config.yaml` and message your bot on Telegram.
