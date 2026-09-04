# Slack

Connect agents to Slack using the Slack Events API and Socket Mode.

## Setup

### 1. Create a Slack app

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From scratch**
2. Choose a name and workspace

### 2. Configure OAuth scopes

Under **OAuth & Permissions → Bot Token Scopes**, add:

- `app_mentions:read`
- `channels:history`
- `chat:write`
- `files:read` (optional, for document handling)
- `im:history`
- `im:write`

### 3. Enable Socket Mode (recommended)

Under **Socket Mode**, enable it and generate an **App-Level Token** with `connections:write` scope. This avoids needing a public webhook URL.

### 4. Enable Events

Under **Event Subscriptions**, enable and subscribe to:

- `app_mention`
- `message.im`

### 5. Install to workspace and copy tokens

- **Bot Token** (`xoxb-...`) — from OAuth & Permissions
- **App-Level Token** (`xapp-...`) — from Socket Mode (if using socket mode)
- **Signing Secret** — from Basic Information

### 6. Configure Soulacy

```yaml title="config.yaml"
channels:
  slack:
    enabled: true
    bot_token: "xoxb-..."
    app_token: "xapp-..."        # Socket Mode token
    agent_id: assistant
```

For channel messages, invite the app to the Slack channel first:

```text
/invite @YourSoulacyApp
```

Use the Slack channel ID (for example `C0123...`) as `default_output_to` or
`schedule.output.to` for outbound delivery. A visible channel name is convenient
for humans, but the API destination should be the ID.

---

## Multiple Slack bots

Use `bots:` when you want separate Slack bot credentials mapped to separate agents:

```yaml title="config.yaml"
channels:
  slack:
    enabled: true
    bots:
      - bot_token: "xoxb-assistant"
        app_token: "xapp-assistant"
        agent_id: assistant
      - bot_token: "xoxb-support"
        app_token: "xapp-support"
        agent_id: support-agent
```

This registers two adapter IDs:

| Adapter ID | Agent |
|------------|-------|
| `slack` | `assistant` |
| `slack-support-agent` | `support-agent` |

You can configure this from the GUI: **Channels → Slack → Edit → Bot mappings**.

---

## Default Outbound vs Interactive Bots

Slack can be used in two ways:

| Mode | Use it for | Configure |
| --- | --- | --- |
| Default outbound sender | Cron reports, one-off `channel.send`, non-interactive delivery | Top-level `bot_token`, `app_token`, and `default_output_to` |
| Interactive agent bot | DMs and app mentions routed to one agent | A bot mapping with `agent_id`, bot token, app token, and optional allowlists |

Socket Mode being `Live` means Soulacy can connect to Slack. It does not prove
the app can post to every channel. Posting also requires `chat:write`, the app
being installed in the workspace, and the app being invited to private channels
or channels where it is expected to answer.

For interactive mappings, the target agent must have `trigger: channel` and a
matching `channels:` entry. In DMs, messages are processed directly. In channels,
mention the app or use the configured trigger phrase.

## Troubleshooting Slack Delivery

Open **Channels -> Slack** and read **Delivery checks**:

- `missing scope` means the Slack app is missing a required OAuth scope. Add the
  scope, reinstall the app, then restart Soulacy.
- `not_in_channel`, `channel_not_found`, or `unknown_channel` means the app is
  not invited to the destination or the destination ID is wrong.
- `invalid_auth` means the bot token is wrong, revoked, or from another Slack
  app/workspace.
- `adapter disabled` or `restart required` means the gateway has not reconnected
  after a config change.

Use the channel card's delivery test before relying on scheduled agents.

## Features

| Feature | Support |
|---------|---------|
| DM messages | ✅ |
| Channel mentions | ✅ |
| Thread replies | ✅ (agent replies in thread) |
| Block Kit formatting | ✅ |
| File uploads | ✅ (text extracted) |
| Slash commands | 🔜 Planned |
