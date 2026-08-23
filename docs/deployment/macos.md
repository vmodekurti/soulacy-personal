# macOS Deployment

Run Soulacy as a persistent background service on macOS using `launchd`.

## Install via Homebrew

```bash
brew tap vmodekurti/soulacy
brew install soulacy
```

## Configure

Create your config file:

```bash
mkdir -p ~/Library/Application\ Support/soulacy
cp /usr/local/etc/soulacy/config.example.yaml \
   ~/Library/Application\ Support/soulacy/config.yaml
```

Edit the config with your API keys and settings.

## Run as a launchd service

Create a plist:

```xml title="~/Library/LaunchAgents/com.soulacy.soulacy.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.soulacy.soulacy</string>

  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/soulacy</string>
    <string>serve</string>
    <string>--config</string>
    <string>/Users/YOUR_USERNAME/Library/Application Support/soulacy/config.yaml</string>
  </array>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>

  <key>StandardOutPath</key>
  <string>/usr/local/var/log/soulacy.log</string>

  <key>StandardErrorPath</key>
  <string>/usr/local/var/log/soulacy.error.log</string>
</dict>
</plist>
```

Load and start:

```bash
launchctl load ~/Library/LaunchAgents/com.soulacy.soulacy.plist
launchctl start com.soulacy.soulacy
```

## Manage the service

```bash
# Check status
launchctl list | grep soulacy

# View logs
tail -f /usr/local/var/log/soulacy.log

# Restart
launchctl stop com.soulacy.soulacy
launchctl start com.soulacy.soulacy

# Unload (stop and disable autostart)
launchctl unload ~/Library/LaunchAgents/com.soulacy.soulacy.plist
```

## Deploying from a development checkout

From the repository root:

```bash
./deploy.sh
```

`deploy.sh` builds the GUI and Go binaries, installs `soulacy` and `sy`, syncs runtime files into `~/.soulacy`, and restarts the gateway.

The web GUI **Restart Gateway** button is available after config-changing actions. It starts a replacement process with the same executable and arguments before the old process exits, so it works for both LaunchAgent installs and `deploy.sh` manual/nohup fallback installs.

## Data location

| Item | Default path |
|------|-------------|
| Config | `~/Library/Application Support/soulacy/config.yaml` |
| SQLite DB | `~/Library/Application Support/soulacy/soulacy.db` |
| Logs | `/usr/local/var/log/soulacy*.log` |
| Agents | `~/Library/Application Support/soulacy/agents/` |

## Exposing to the internet (for channel webhooks)

Telegram, Slack, Discord, and WhatsApp webhooks require a public HTTPS URL. Options:

- **ngrok** (dev): `ngrok http 1947` — gives you a temporary public URL
- **Cloudflare Tunnel** (recommended for persistent): zero-config secure tunnel, no port forwarding needed
- **Router port forwarding** + **Let's Encrypt**: forward port 443 to your Mac, use Caddy for automatic TLS

### Cloudflare Tunnel (recommended)

```bash
brew install cloudflared
cloudflared tunnel login
cloudflared tunnel create soulacy
cloudflared tunnel route dns soulacy yourdomain.com
cloudflared tunnel run --url http://localhost:1947 soulacy
```
