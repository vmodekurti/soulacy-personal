# WhatsApp

Connect agents to WhatsApp using the WhatsApp Business Platform (Meta Cloud API).

Soulacy has two distinct WhatsApp paths:

- `channels.whatsapp` — official Meta WhatsApp Business Cloud API. Use this for
  production/customer traffic.
- `channels.whatsapp_web` — experimental WhatsApp Web linked-device sidecar.
  This uses QR pairing through Baileys and is not an official WhatsApp Business
  API integration. Use only for personal/local automation.

## Prerequisites

- A **Meta Business Account**
- A **WhatsApp Business Account** linked to it
- A phone number approved for WhatsApp Business API

> **Note:** WhatsApp Business API is not free — it is billed per conversation. See [Meta's pricing](https://developers.facebook.com/docs/whatsapp/pricing).

## Setup

### 1. Create a Meta App

1. Go to [developers.facebook.com](https://developers.facebook.com) → **My Apps** → **Create App**
2. Select **Business** app type
3. Add the **WhatsApp** product

### 2. Configure the phone number

In the WhatsApp section of your app, add and verify your business phone number.

### 3. Get credentials

- **Access Token** — permanent token from Meta Business Suite
- **Phone Number ID** — the ID of your WhatsApp phone number
- **Webhook Verify Token** — any secret string you choose

### 4. Configure Soulacy

```yaml title="config.yaml"
channels:
  whatsapp:
    enabled: true
    access_token: "EAA..."
    phone_number_id: "123456789"
    verify_token: "my-webhook-secret"
    app_secret: "meta-app-secret"    # required for webhook signature verification
    agent_id: assistant
```

### 5. Register the webhook

In your Meta app under **WhatsApp → Configuration**, set:

- **Callback URL**: `https://yourdomain.com/channels/whatsapp/webhook`
- **Verify Token**: the same string as `verify_token` above
- Subscribe to: `messages`

---

## Features

| Feature | Support |
|---------|---------|
| Text messages | ✅ |
| Images | ✅ (passed to vision agents) |
| Documents | ✅ (text extracted) |
| Audio | 🔜 Planned |
| Rich formatting | ❌ (WhatsApp is plain text only) |
| Templates | 🔜 Planned |

---

## Limitations

- WhatsApp does not support markdown, bold, or links in outgoing messages — responses are sent as plain text
- The business phone number cannot receive regular WhatsApp messages once it's on the API
- 24-hour conversation window applies: you can only reply within 24 hours of the last user message (or use approved message templates)

## Experimental WhatsApp Web QR setup

`channels.whatsapp_web` links a normal WhatsApp account as a WhatsApp Web linked
device. It starts `scripts/whatsapp-web-sidecar.mjs`, which expects Baileys to be
installed in the runtime environment:

```bash
npm install @whiskeysockets/baileys
```

```yaml title="config.yaml"
channels:
  whatsapp_web:
    enabled: true
    command: node
    args:
      - scripts/whatsapp-web-sidecar.mjs
    session_dir: /Users/you/.soulacy/whatsapp-web
    account_id: default
    agent_id: assistant
    trigger_phrase: "!soulacy"
    ignore_groups: true
    allowed_chat_ids: ""
    allowed_sender_ids: ""
```

Open **Channels**, select an agent, and click **Generate QR code**. Scan the QR
using WhatsApp -> Linked Devices. The authenticated session is stored under
`session_dir/account_id`.

For safety, WhatsApp Web does not treat every personal WhatsApp message as an
agent prompt. By default, messages are ignored unless they start with
`!soulacy`, and group chats are ignored. Set `ignore_groups: false` only when the
agent is intentionally allowed to participate in groups. Use `allowed_chat_ids`
or `allowed_sender_ids` to restrict activation to specific WhatsApp JIDs.

This connector is intentionally separate from `channels.whatsapp`. WhatsApp Web
automation can lose sessions and may violate WhatsApp expectations for automated
use. For durable business/customer messaging, use the official Cloud API channel.
