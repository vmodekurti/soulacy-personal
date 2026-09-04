# Discord

Connect agents to Discord using the Discord Gateway (WebSocket).

## Setup

### 1. Create a Discord application

1. Go to [discord.com/developers/applications](https://discord.com/developers/applications)
2. Click **New Application**, give it a name
3. Navigate to **Bot** → **Add Bot**
4. Copy the **Token**. Soulacy accepts either the raw token or `Bot <token>`.

### 2. Set bot permissions

Under **OAuth2 → URL Generator**, select:

- Scopes: `bot`, `applications.commands`
- Bot Permissions: `Send Messages`, `Read Message History`, `View Channels`

### 3. Configure intents

Under **Bot → Privileged Gateway Intents**, enable:

- **Message Content Intent** (required to read message text)

### 4. Invite the bot to your server

Use the generated OAuth2 URL to add the bot to your server.

### 5. Configure Soulacy

```yaml title="config.yaml"
channels:
  discord:
    enabled: true
    token: "MTI3..."    # raw bot token; "Bot MTI3..." also works
    agent_id: assistant
    guild_id: ""        # optional: restrict to one guild/server
```

---

## Multiple Discord bots

Use `bots:` when you want separate Discord bot credentials mapped to separate agents:

```yaml title="config.yaml"
channels:
  discord:
    enabled: true
    bots:
      - token: "BOT_TOKEN_1"
        agent_id: assistant
        guild_id: ""
      - token: "BOT_TOKEN_2"
        agent_id: moderator-agent
        guild_id: ""
```

This registers two adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `discord` | `assistant` |
| `discord-moderator-agent` | `moderator-agent` |

You can configure this from the GUI: **Channels → Discord → Edit → Bot mappings**.

---

## Default Outbound vs Interactive Bots

Discord can be used in two ways:

| Mode | Use it for | Configure |
| --- | --- | --- |
| Default outbound sender | Cron reports, one-off `channel.send`, non-interactive delivery | Top-level token plus `default_output_to` |
| Interactive agent bot | DMs and server messages routed to one agent | A bot mapping with `agent_id`, token, guild/channel allowlists, and intents |

The bot can be online while still unable to read or post in a specific server
channel. Confirm the bot has `View Channels`, `Send Messages`, and `Read Message
History` in the destination. For inbound messages, **Message Content Intent** is
required unless the bot is only handling slash commands.

For interactive mappings, the target agent must have `trigger: channel` and a
matching `channels:` entry. In DMs, messages are processed directly. In server
channels, mention the bot or use the configured trigger phrase.

## Troubleshooting Discord Delivery

Open **Channels -> Discord** and read **Delivery checks**:

- `missing access`, `unknown channel`, or `missing permissions` means the bot is
  not in the server/channel or lacks the needed channel permissions.
- `unauthorized` means the token is invalid or revoked.
- No inbound messages usually means **Message Content Intent** is off, the bot
  is not mentioned in a server channel, or the agent is not mapped to the bot.
- Long reports are split automatically to respect Discord's message length
  limit.

Use the channel card's delivery test before relying on scheduled agents.

---

## Features

| Feature | Support |
|---------|---------|
| Direct messages | ✅ |
| Server channel messages | ✅ (mention the bot) |
| Thread replies | ✅ |
| Embeds | ✅ |
| Slash commands | 🔜 Planned |
| Attachments | ✅ (images passed to vision agents) |

---

## Tips

- The bot only responds when directly mentioned (`@BotName`) in a server channel to avoid noise
- In DMs, all messages are processed without needing a mention
- Discord has a 2000-character message limit; long responses are automatically split across multiple messages
