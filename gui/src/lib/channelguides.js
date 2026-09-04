// channelguides.js — inline setup documentation for the Channels page.
// Each guide renders inside the channel's configuration modal so users can
// configure a channel end-to-end without leaving the GUI.
//
// Shape: { intro, steps: string[] (markdown-lite: **bold** and `code`),
//          fields: {key: hint}, test, warning? }

export const channelGuides = {
  telegram: {
    intro: 'Connect a Telegram bot in about two minutes. You need a free bot token from Telegram\'s BotFather.',
    steps: [
      'Open Telegram and message **@BotFather**, then send `/newbot`.',
      'Pick a display name and a unique username ending in `bot` (e.g. `my_soulacy_bot`).',
      'BotFather replies with a **bot token** like `110201543:AAHdqTcv…` — paste it into **Bot token** below.',
      'Set **Default agent ID** to the agent that should answer (pick from your Agents page).',
      'Save, restart the gateway, then open your bot in Telegram and send `!soulacy hello`.',
    ],
    fields: {
      token: 'From @BotFather after /newbot. Treat it like a password — anyone with it controls the bot.',
      allowed_chat_ids: 'Find a chat ID by messaging @userinfobot, or forward a group message to @getidsbot.',
      ignore_groups: 'For group chats also disable BotFather privacy mode: /setprivacy → Disable, so the bot sees messages.',
    },
    test: 'DM your bot: `!soulacy hello` — the reply should arrive in a few seconds (watch Activity for message.in).',
  },

  discord: {
    intro: 'Create a Discord application with a bot user, invite it to your server, and paste its token here.',
    steps: [
      'Go to **discord.com/developers/applications** → **New Application**.',
      'Open the **Bot** tab → **Reset Token** → copy the token into **Bot token** below. Soulacy accepts either the raw token or `Bot <token>`.',
      'Still on the Bot tab, enable **MESSAGE CONTENT INTENT** (required — without it the bot receives empty messages).',
      'Open **OAuth2 → URL Generator**: scope `bot`; permissions **View Channels**, **Send Messages**, and **Read Message History**. Open the generated URL and invite the bot to your server.',
      'Set **Default agent ID**, save, restart the gateway.',
    ],
    fields: {
      token: 'Developer Portal → your app → Bot → Reset Token. Pasting the raw token is recommended; Soulacy adds the REST Bot prefix automatically.',
      allowed_chat_ids: 'Enable Developer Mode (User Settings → Advanced), then right-click a channel → Copy Channel ID.',
      guild_id: 'Right-click your server icon → Copy Server ID (needs Developer Mode).',
    },
    test: 'In an allowed channel type `!soulacy hello`. If nothing happens, re-check MESSAGE CONTENT INTENT.',
  },

  slack: {
    intro: 'Soulacy uses Slack Socket Mode — no public URL needed. You\'ll create one app and copy two tokens: a bot token (xoxb-) and an app token (xapp-).',
    steps: [
      'Go to **api.slack.com/apps** → **Create New App** → *From scratch*, pick your workspace.',
      '**Socket Mode** (left sidebar) → enable it. When prompted, create an app-level token with the `connections:write` scope — this is your **App token** (`xapp-…`).',
      '**OAuth & Permissions** → under *Bot Token Scopes* add: `chat:write`, `app_mentions:read`, `channels:history`, `groups:history`, `im:history`, `im:write`.',
      '**Event Subscriptions** → enable, and under *Subscribe to bot events* add: `message.im`, `message.channels`, `app_mention`.',
      '**Install App** (left sidebar) → *Install to Workspace* → copy the **Bot User OAuth Token** (`xoxb-…`) into **Bot token** below.',
      'In Slack, invite the bot to a channel with `/invite @YourBot`, set **Default agent ID**, save, restart.',
    ],
    fields: {
      bot_token: 'OAuth & Permissions → Bot User OAuth Token (starts with xoxb-). Re-install the app after changing scopes.',
      app_token: 'Basic Information → App-Level Tokens (starts with xapp-, scope connections:write).',
      allowed_chat_ids: 'Channel ID is in the channel\'s details panel (starts with C…), or copy from its URL.',
    },
    test: 'DM the bot (or post in an invited channel): `!soulacy hello`.',
  },

  whatsapp: {
    intro: 'Official Meta WhatsApp Business Cloud API. Requires a Meta developer app and a publicly reachable gateway (Meta delivers messages via webhook).',
    steps: [
      'Go to **developers.facebook.com** → *My Apps* → **Create App** → type *Business*.',
      'Add the **WhatsApp** product. The *API Setup* page gives you a test number, a temporary **Access token**, and the **Phone number ID** — copy both below.',
      'Choose any random string as your **Verify token** (you invent it; Meta echoes it back during verification).',
      'Copy **App secret** from *App settings → Basic* — Soulacy uses it to verify webhook signatures.',
      'Expose your gateway publicly (reverse proxy or a tunnel like `ngrok http 18789`), then in *WhatsApp → Configuration* set the **Callback URL** to `https://YOUR-HOST/channels/whatsapp/webhook` with your Verify token, and subscribe to the **messages** webhook field.',
      'Save here, restart the gateway, and complete Meta\'s webhook verification.',
    ],
    fields: {
      access_token: 'The API Setup token expires in 24h — for production create a System User token (Business Settings → System Users) with whatsapp_business_messaging permission.',
      verify_token: 'Any string you choose; must match what you enter in Meta\'s webhook configuration.',
      app_secret: 'App settings → Basic → App Secret (click Show).',
      allowed_user_ids: 'Sender phone numbers in international format without +, e.g. 14085551234.',
    },
    test: 'Send a WhatsApp message to your Meta test number: `!soulacy hello`. Meta\'s test numbers only message phone numbers you\'ve added as recipients in API Setup.',
    warning: 'The gateway must be reachable from the internet over HTTPS for webhooks — localhost-only installs need a tunnel.',
  },

  whatsapp_web: {
    intro: 'Links your personal WhatsApp as a companion device via QR code — no Meta account or public URL needed. Powered by an unofficial library (Baileys); WhatsApp may log the device out occasionally.',
    steps: [
      'Make sure **Node.js** is installed (`node --version`) — the bridge runs as a Node sidecar; its dependency is installed automatically on first pairing.',
      'Pick the **Agent** that should answer, keep the trigger phrase (default `!soulacy`), and click **Generate QR code**.',
      'On your phone: **WhatsApp → Settings → Linked Devices → Link a Device**, and scan the QR shown here.',
      'Wait for the status to flip to *connected* — pairing persists across restarts.',
      'Test from the **Message yourself** chat or have anyone message you starting with the trigger phrase.',
    ],
    fields: {
      trigger_phrase: 'Your safety net: only messages starting with this run the agent — everything else in your chats is ignored.',
      session_dir: 'Holds the device keys. Delete it to force a fresh QR pairing.',
      args: 'Managed automatically — the sidecar script is materialised from the binary and kept up to date.',
    },
    test: 'In WhatsApp open “Message yourself” and send `!soulacy hello` — a typing indicator appears while the agent thinks.',
    warning: 'Unofficial automation is against WhatsApp\'s ToS and carries a small account-ban risk. Use a number you can afford to lose, keep the trigger phrase set, and prefer the official Cloud API for production.',
  },

  http: {
    intro: 'Always on — every agent is reachable over the REST API the moment it loads. Nothing to configure.',
    steps: [
      'Find your API key in **Config → Server** (empty key = open local access).',
      'POST a message to any agent and read the reply from the JSON response.',
    ],
    fields: {},
    test: 'curl -X POST http://localhost:18789/api/v1/agents/YOUR-AGENT/chat -H "Authorization: Bearer YOUR-KEY" -H "Content-Type: application/json" -d \'{"message":"hello"}\'',
  },

  email: {
    intro: 'Outbound SMTP delivery — your agents can send email through any mailbox that accepts SMTP (Gmail app-passwords, Office 365, Fastmail, self-hosted Postfix, transactional providers like Postmark/SES/SendGrid). Inbound email is out of scope; use a webhook if you need reply parsing.',
    steps: [
      'Pick a mailbox that can send on the agent\'s behalf. For Gmail create an **App password** (Account → Security → 2-Step Verification → App passwords) — regular passwords no longer work.',
      'Set **SMTP host** (e.g. `smtp.gmail.com`) and **Port** (`587` for STARTTLS, `465` for implicit TLS). Leave **TLS** blank to auto-pick from the port.',
      'Set **Username** to the login mailbox and paste the app password into **Password**. Store secrets in the vault rather than inline when possible.',
      'Set **From** to the address emails should appear from (e.g. `"Soulacy <bot@example.com>"`). If blank, the username is used.',
      'Set **Default recipient** to a safe fallback address — used whenever a scheduled/agent-triggered send has no explicit `to`. Set **Default subject** for the same reason.',
      'Save, restart the gateway, then use the **Test delivery** button below — Soulacy sends a plain-text probe from the configured mailbox to the default recipient.',
    ],
    fields: {
      host: 'SMTP hostname of your outbound provider — usually `smtp.<provider>.com`.',
      port: 'TCP port. Use 587 for STARTTLS (recommended) or 465 for implicit TLS.',
      username: 'Login for SMTP AUTH — typically the full mailbox address.',
      password: 'SMTP password or provider app-password. Prefer storing in the credential vault so it doesn\'t sit in plain YAML.',
      from: 'From header (any RFC-5322 form, including `"Display Name <addr@host>"`). Defaults to username when blank.',
      default_output_to: 'Fallback recipient for scheduled or notify-on-failure sends that don\'t specify `to` — set this so silent drops don\'t happen.',
      subject: 'Default Subject line when a message doesn\'t provide `metadata.subject`.',
      tls: 'Transport mode: `starttls` (upgrade on 587), `implicit` (SMTPS on 465), or `none` (unencrypted — refused if credentials are set).',
      timeout_seconds: 'Connect + I/O deadline per send. Defaults to 20s; raise it for slow providers.',
    },
    test: 'Save the channel, then click **Test delivery** — a probe email lands in your default recipient inbox within a few seconds if credentials are right.',
    warning: 'Free Gmail imposes low daily send limits and may throttle test bursts. For production volume use a transactional provider (Postmark, Amazon SES, SendGrid, Mailgun) and a domain you\'ve verified.',
  },

  teams: {
    intro: 'Outbound-only — posts to a Microsoft Teams channel using an **Incoming Webhook** or **Workflow POST URL**. No public URL, no Azure app, no consent flow: Teams gives you a URL, you paste it here, agents post to it. Interactive Teams bots (mentions, replies) are a separate project.',
    steps: [
      'Open the Teams channel you want the agent to post in → **… → Connectors → Incoming Webhook → Configure**. Give it a name, optionally upload an icon, click **Create**, then copy the generated URL.',
      'Newer Teams tenants surface this as **Workflows → "Post to a channel when a webhook request is received"**. Either flavour of URL works — Soulacy just POSTs `{"text": ...}` to it.',
      'Paste the URL into **Webhook URL** below.',
      'Optionally set **Title** to a short bold prefix (e.g. the agent name) that appears above every message.',
      'Save, restart the gateway, then click **Test delivery** — a probe message appears in the target Teams channel within seconds.',
    ],
    fields: {
      webhook_url: 'The Incoming Webhook or Workflow URL from the Teams channel. Aliases `url` and `default_output_to` accept the same value for legacy configs.',
      title: 'Optional bold header prepended to every message — useful when several agents share one channel.',
      timeout_seconds: 'HTTP timeout per POST. Defaults to 10s; raise if your egress path is slow.',
    },
    test: 'Click **Test delivery** — a probe post lands in the Teams channel. To override the destination on a specific send, put an https URL in `msg.ThreadID` or `msg.Metadata["to"]` and it wins over the configured webhook.',
    warning: 'Anyone with the webhook URL can post to that channel. Treat it as a secret and rotate via Teams if it leaks.',
  },

  google_chat: {
    intro: 'Outbound-only — posts to a Google Chat space using an **Incoming Webhook**. No OAuth, no Google Workspace app, no public gateway URL: the space gives you a URL, you paste it here.',
    steps: [
      'Open the Google Chat space → **space name → Apps & integrations → Add webhooks → Add webhook**. Name it (this shows as the message sender), optionally set an avatar, click **Save**, then copy the URL.',
      'Paste the URL into **Webhook URL** below.',
      'Optionally set **Prefix** — a short string prepended to every message body.',
      'Save, restart the gateway, then use **Test delivery** to confirm the webhook is reachable.',
    ],
    fields: {
      webhook_url: 'The Incoming Webhook URL from the Google Chat space. Aliases `url` and `default_output_to` accept the same value for legacy configs.',
      prefix: 'Optional string prepended to every message body — useful when several agents share one space.',
      timeout_seconds: 'HTTP timeout per POST. Defaults to 10s.',
    },
    test: 'Click **Test delivery** — a probe message appears in the target Google Chat space. Per-message override: an https URL in `msg.ThreadID` or `msg.Metadata["to"]` replaces the configured webhook for that one send.',
    warning: 'Anyone with the webhook URL can post as this sender in the space. Rotate via Google Chat if it leaks — Soulacy has no way to invalidate it.',
  },
};

/** guideFor returns the setup guide for a channel id (null when none). */
export function guideFor(id) {
  return channelGuides[id] || null;
}

/** renderInline converts the guide's **bold** and `code` markers to HTML
 *  (escaping everything else) for safe {@html} rendering. */
export function renderInline(text) {
  const escaped = String(text || '')
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
  return escaped
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/`([^`]+)`/g, '<code>$1</code>');
}
