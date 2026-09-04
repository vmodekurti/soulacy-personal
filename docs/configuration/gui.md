# Web GUI

Soulacy includes a built-in web GUI served by the gateway. Open it at the same host and port as the API, for example:

```text
http://localhost:18789
```

If `server.api_key` is set, use the key button in the sidebar to enter a bearer token.

## Dashboard

The Dashboard shows gateway health, enabled agents, and the live WebSocket event stream.

The live event log includes filter presets:

| Filter | Shows |
|--------|-------|
| All | Every event in the current browser session |
| Errors | Error events and payloads containing error text |
| Tools | Tool call and tool result events |
| LLM | LLM request/result events |
| Messages | Inbound and outbound message events |

Filters only change the view. The raw session event buffer is preserved until you click **Clear** or reload the page.

## Channels

The Channels page configures platform adapters and shows live adapter-to-agent mappings.

Each channel card includes:

- connection state
- enabled/configured state
- masked credential fields
- **Agent mappings**, which show bot name, adapter ID, agent ID, and connection state

Telegram, Slack, and Discord support **Bot mappings** in the edit modal. Each row records a friendly bot name, creates one adapter, and routes that bot to one agent.

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

The GUI lists installed agent IDs in the mapping dropdown, so you do not need to type them manually. Secret values are masked; leaving a secret input blank keeps the existing secret.

After saving channel settings, click **Restart Gateway** from the banner to recreate channel adapters.

## Schedule

The Schedule page lists cron agents, their next run, and the configured output bot. Edit a cron agent to choose a bot mapping and destination ID for scheduled delivery.

```yaml title="SOUL.yaml"
trigger: cron
schedule:
  cron: "0 8 * * *"
  run_missed_on_startup: true
  missed_startup_window: 24h
  output:
    channel: telegram-financial-agent
    to: "123456789"
    bot_name: "Finance Bot"
```

When the cron fires, Soulacy runs the agent internally and sends the final reply through `schedule.output.channel` to `schedule.output.to`. The bot name is shown in the GUI for readability; the adapter ID is the routing key.

If `run_missed_on_startup` is enabled, Soulacy replays the latest missed cron
fire after a restart when the scheduled time occurred during downtime and is
still inside `missed_startup_window`.

## Agents

The Agents page edits `SOUL.yaml` definitions and includes a built-in Playground.

Use **Validate** before saving or deploying an agent. Validation checks common configuration failures such as unknown providers, missing models, incompatible model/provider choices, unsafe provider allowlists, and missing routing fields.

The Playground supports one-run parameter overrides:

| Override | Effect |
|----------|--------|
| Provider | Temporarily run against another configured provider |
| Model | Temporarily run against another model |
| Temperature | Override sampling temperature |
| Max tokens | Override output budget |
| Max turns | Override tool/LLM loop count |
| Tool choice | Force or disable first-turn tool use |

Playground overrides are sent as request metadata. They do not mutate the saved agent file.

## Flow

The Flow page visualizes an agent as a graph and lets you edit key nodes from the inspector:

- trigger type, cron, and channels
- system prompt
- memory read/write scopes
- LLM provider, model, temperature, max tokens, max turns, and tool choice
- output channels
- tool name, description, and Python file

Click **Save Flow** to persist edits through the agent API.

## Configuration, Providers, and MCP

Several GUI pages write to `config.yaml`. Some changes can be reflected immediately in the UI, but the gateway must restart before the runtime reconnects adapters or reloads provider registrations.

Pages that can show **Restart Gateway**:

- Configuration
- Providers
- Channels
- MCP Servers

The restart action calls:

```http
POST /api/v1/admin/restart
```

The gateway starts a replacement process using the same executable and arguments, then exits. This works for `deploy.sh` manual/nohup installs as well as supervised deployments.
