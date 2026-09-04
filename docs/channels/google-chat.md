# Google Chat Channel

The Google Chat channel sends outbound Soulacy messages to a Google Chat
incoming webhook URL. It is meant for scheduled reports, alerts, and
non-interactive agent output.

!!! tip "Guided setup in the GUI"
    The GUI's **Channels → Google Chat → Configure** card walks through creating
    an Incoming Webhook in the target space and pasting the URL back in — plus
    a **Test delivery** button. Use this reference page when editing
    `config.yaml` directly.

## Configure

```yaml title="config.yaml"
channels:
  google_chat:
    enabled: true
    webhook_url: "https://chat.googleapis.com/v1/spaces/..."
    prefix: "[Soulacy]"
```

Fields:

- `webhook_url`: required. The Google Chat incoming webhook endpoint.
- `prefix`: optional text prepended to each message.
- `default_output_to`: optional alternate webhook URL for scheduled output.
- `timeout_seconds`: request timeout; defaults to 10.

## Send From An Agent

```json
{
  "channel": "google_chat",
  "text": "Daily run completed."
}
```

If `to` is supplied and is an absolute HTTP URL, Soulacy uses it as a one-off
webhook override. Otherwise it uses `webhook_url`.

## Notes

This adapter is outbound-only. Interactive Google Chat app support requires a
separate inbound app integration because incoming webhooks do not receive user
messages.

## Troubleshooting

Use **Diagnose** on the channel mapping when a send fails. The delivery doctor
recognises expired or revoked webhooks (`bad_token`), missing destinations
(`missing_destination`), webhook 404s (`invalid_destination`), and rate limits
(`rate_limited`) and returns a plain-language fix. See
`internal/channels/deliverydoctor.go` for the full category list.
