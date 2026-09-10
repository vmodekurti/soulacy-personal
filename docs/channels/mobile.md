# Soulacy Mobile

Soulacy Mobile is an always-on outbound channel for the native iOS app. Delivery first writes the complete result to the workspace's durable SQLite inbox. APNs is only a discreet wake-up signal, so a missing or temporarily failing push service never loses an agent result.

## Send a scheduled result

```yaml
schedule:
  cron: "0 8 * * *"
  output:
    channel: mobile
    to: all
```

Destinations are `all`, `user:<authenticated-subject>`, or `device:<installation-id>`. The channel test endpoint defaults to `all` when no destination is supplied.

The iOS app registers its installation through `/api/v1/mobile/devices`, reads `/api/v1/mobile/deliveries`, and maintains read receipts per device. The System agent is filtered at the API boundary and is never returned to iOS.

## Native alerts

Durable inbox delivery needs no additional configuration. A Personal gateway can wake a device directly through APNs. Mount the downloaded Apple `.p8` key read-only into the container, then set:

```bash
SOULACY_MOBILE_APNS_TEAM_ID=YOUR_APPLE_TEAM_ID
SOULACY_MOBILE_APNS_KEY_ID=YOUR_APNS_KEY_ID
SOULACY_MOBILE_APNS_PRIVATE_KEY_FILE=/run/secrets/soulacy-apns-key.p8
```

Never commit the `.p8` key or paste it into `.env`. The key file should be readable only by the service account. Direct APNs supports both sandbox and production device tokens and is the recommended Personal-edition configuration.

Alternatively, configure a trusted APNs relay:

```text
SOULACY_MOBILE_PUSH_RELAY_URL=https://push.example.com
SOULACY_MOBILE_PUSH_RELAY_TOKEN=replace-with-a-secret
```

The gateway posts a token, APNs environment, bundle ID, delivery ID, and a `soulacy://delivery/<id>` deep link to `POST <relay>/v1/push`. Notification text is intentionally generic; the sensitive output is fetched from the authenticated gateway after the user opens the app.
