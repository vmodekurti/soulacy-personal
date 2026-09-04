# Generic Webhooks

Soulacy supports two different webhook patterns:

| Pattern | Use it when | Configuration |
| --- | --- | --- |
| Inbound webhook trigger | An external system sends JSON to Soulacy and expects an agent response | Agent `trigger: webhook` plus the agent's `webhook:` mapping |
| Outbound webhook delivery | Soulacy should send cron reports, alerts, or `channel.send` output to an HTTP endpoint | `channels.webhook` in `config.yaml` |

These are intentionally separate. Inbound webhook triggers decide which agent
runs. Outbound webhook delivery decides where Soulacy sends a finished message.

Use generic webhooks when another product can send JSON but Soulacy does not
need a dedicated native adapter yet.

## Agent configuration

```yaml title="agents/github-triage/SOUL.yaml"
id: github-triage
name: GitHub Triage
trigger: webhook

webhook:
  text_path: issue.title
  user_id_path: sender.id
  username_path: sender.login
  thread_id_path: issue.number
  include_raw: true

system_prompt: >
  Triage inbound GitHub issues. Summarize the problem, identify missing
  reproduction details, and recommend labels.
```

The endpoint is:

```bash
curl -X POST http://localhost:18789/api/v1/webhooks/github-triage \
  -H "Authorization: Bearer $SOULACY_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"issue":{"number":42,"title":"Crash on launch"},"sender":{"id":7,"login":"vasu"}}'
```

Soulacy returns the agent reply synchronously:

```json
{
  "reply": "The issue is a launch crash report...",
  "session_id": "webhook-github-triage-42",
  "thread_id": "42",
  "user_id": "7"
}
```

## Mapping fields

Paths use dot notation. A leading `$` or `$.` is allowed.

| Field | Purpose |
|-------|---------|
| `text_path` | Message text sent to the agent. |
| `user_id_path` | Stable sender identifier. |
| `username_path` | Human-readable sender name. |
| `thread_id_path` | Conversation, issue, alert, or event thread. |
| `session_id_path` | Explicit Soulacy session id. |
| `include_raw` | Attach the compact original JSON payload as run metadata. |

If `text_path` is empty, Soulacy tries common fields such as `text`,
`message`, `body`, `content`, `issue.title`, and `alert.title`. If none are
present, it sends the compact JSON payload as the prompt.

## GUI

Open **Agents**, set **Trigger** to `webhook`, then fill the **Webhook request
mapping** fields. The editor shows the endpoint path for the selected agent.

## Outbound webhook delivery

Use `channels.webhook` when Soulacy should send a completed result to another
system: Zapier, Make, n8n, an internal API, an incident webhook, or any service
that accepts HTTP.

```yaml title="config.yaml"
channels:
  webhook:
    enabled: true
    url: "https://example.com/hooks/soulacy"
    method: POST
    headers: |
      Authorization: Bearer YOUR_SHARED_SECRET
    secret: "hmac-signing-secret"
```

Then point a scheduled agent at it:

```yaml title="agents/daily-brief/SOUL.yaml"
trigger: cron
schedule:
  cron: "0 7 * * *"
  output:
    channel: webhook
```

Or send from an agent/tool step:

```json
{
  "channel": "webhook",
  "text": "Daily report is ready."
}
```

For webhooks, `to` is optional because the configured `url` is the destination.
If you do pass `to` and it is an absolute `http` or `https` URL, Soulacy uses it
as a per-message override without changing the saved channel configuration.

When `secret` is set, Soulacy signs the request body with HMAC-SHA256 and sends
`X-Soulacy-Timestamp` and `X-Soulacy-Signature` headers so the receiver can
reject forged or replayed requests.
