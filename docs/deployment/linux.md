# Linux / VPS Deployment

This guide installs Soulacy v0.1.11 as a system-wide systemd service behind
Caddy. It keeps configuration read-only under `/etc/soulacy` and runtime state
under `/var/lib/soulacy`.

For a single-user machine, `sy daemon install` creates a simpler systemd
**user** unit. Use the system service below for a dedicated `soulacy` account,
boot-time startup, and clearer file ownership.

## Prerequisites

- Ubuntu 22.04+, Debian 12+, or another systemd-based distribution.
- Root or sudo access.
- `curl`, `tar`, and `ca-certificates`.
- A domain pointed at the VPS when exposing the GUI remotely.
- An LLM provider reachable from the VPS.

Examples below assume Linux AMD64. Replace `amd64` with `arm64` on an ARM VPS.

## 1. Install a released binary

```bash
cd /tmp
curl -fLO https://github.com/vmodekurti/soulacy/releases/download/v0.1.11/soulacy_v0.1.11_linux_amd64.tar.gz
curl -fLO https://github.com/vmodekurti/soulacy/releases/download/v0.1.11/checksums.sha256
grep 'soulacy_v0.1.11_linux_amd64.tar.gz' checksums.sha256 | sha256sum -c -
tar -xzf soulacy_v0.1.11_linux_amd64.tar.gz
sudo install -m 0755 soulacy sy /usr/local/bin/
```

Verify both binaries:

```bash
soulacy --version
sy version
```

Use a tagged release rather than a source commit for production. A build that
reports only a commit such as `8c2c66f` cannot always be ordered against a
semantic release such as `0.1.8`, so `sy update` may correctly report that the
versions are not comparable.

## 2. Create the service account and directories

```bash
sudo useradd --system \
  --home-dir /var/lib/soulacy \
  --create-home \
  --shell /usr/sbin/nologin \
  soulacy

sudo install -d -o root -g soulacy -m 0750 /etc/soulacy
sudo install -d -o soulacy -g soulacy -m 0750 /var/lib/soulacy/soulspace
```

The account may show `/usr/sbin/nologin` or `/bin/false`; that is normal for a
service account. systemd can still start the binary as that user.

## 3. Create the configuration

Create `/etc/soulacy/config.yaml`:

```yaml title="/etc/soulacy/config.yaml"
server:
  host: 127.0.0.1
  port: 1947
  api_key: "replace-with-a-long-random-value"

llm:
  default_provider: openai
  providers:
    openai:
      api_key: "replace-with-provider-key"

storage:
  backend: sqlite

updates:
  manifest_url: https://github.com/vmodekurti/soulacy/releases/latest/download/release-manifest.json
```

Generate the server key without storing it in shell history:

```bash
openssl rand -hex 32
```

Then protect the file:

```bash
sudo chown root:soulacy /etc/soulacy/config.yaml
sudo chmod 0640 /etc/soulacy/config.yaml
```

!!! warning "The service does not inherit your login environment"
    A foreground `soulacy serve` runs as your login user and normally finds
    `~/.soulacy/soulspace/config.yaml`. The system service runs as `soulacy`,
    whose home is `/var/lib/soulacy`. Make the service config and workspace
    explicit or the two launch methods may read different API keys and agents.

## 4. Create the systemd unit

```ini title="/etc/systemd/system/soulacy.service"
[Unit]
Description=Soulacy gateway
Documentation=https://docs.soulacy.io/deployment/linux/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=soulacy
Group=soulacy
Environment=HOME=/var/lib/soulacy
Environment=SOULACY_CONFIG_PATH=/etc/soulacy/config.yaml
Environment=SOULACY_WORKSPACE=/var/lib/soulacy/soulspace
WorkingDirectory=/var/lib/soulacy/soulspace
ExecStart=/usr/local/bin/soulacy serve
Restart=on-failure
RestartSec=5s

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadOnlyPaths=/etc/soulacy
ReadWritePaths=/var/lib/soulacy

StandardOutput=journal
StandardError=journal
SyslogIdentifier=soulacy

[Install]
WantedBy=multi-user.target
```

`soulacy serve` does not use a `--config` flag. `SOULACY_CONFIG_PATH` is the
supported explicit config selector, and `SOULACY_WORKSPACE` determines where
agents, databases, logs, memory, skills, and secrets live.

Load and start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now soulacy
sudo systemctl status soulacy --no-pager
```

## 5. Prove the service loaded the intended config

Inspect the effective identity and command:

```bash
sudo systemctl show soulacy \
  -p User -p Group -p ExecStart --no-pager

sudo systemctl show soulacy -p Environment --value \
  | tr ' ' '\n' \
  | grep -E '^(HOME|SOULACY_CONFIG_PATH|SOULACY_WORKSPACE)='
```

Expected values:

```text
HOME=/var/lib/soulacy
SOULACY_CONFIG_PATH=/etc/soulacy/config.yaml
SOULACY_WORKSPACE=/var/lib/soulacy/soulspace
```

Verify the service user can read the configuration and write the workspace:

```bash
sudo -u soulacy test -r /etc/soulacy/config.yaml && echo 'config readable'
sudo -u soulacy test -w /var/lib/soulacy/soulspace && echo 'workspace writable'
```

Check startup logs:

```bash
sudo journalctl -u soulacy -b --no-pager -n 100
```

Finally, run Doctor with the same environment as the service:

```bash
sudo -u soulacy env \
  HOME=/var/lib/soulacy \
  SOULACY_CONFIG_PATH=/etc/soulacy/config.yaml \
  SOULACY_WORKSPACE=/var/lib/soulacy/soulspace \
  /usr/local/bin/sy doctor
```

If the browser rejects a key that works in a foreground process, do not rotate
keys yet. First compare these paths and environments—the usual cause is two
different `config.yaml` files.

## 6. Put Caddy in front of Soulacy

Install Caddy using its current official instructions, then configure:

```caddyfile title="/etc/caddy/Caddyfile"
soulacy.example.com {
    reverse_proxy 127.0.0.1:1947 {
        header_up X-Real-IP {remote_host}
        flush_interval -1
    }
}
```

```bash
sudo systemctl reload caddy
```

Keep port `1947` closed to the public. Caddy terminates TLS on ports 80/443
and proxies to Soulacy over loopback. Preserve streaming by retaining
`flush_interval -1`.

## 7. Firewall

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

Do not open port `1947` when using the loopback/Caddy configuration.

## Service operations

```bash
# Status and recent logs
sudo systemctl status soulacy --no-pager
sudo journalctl -u soulacy -n 100 --no-pager

# Follow logs
sudo journalctl -u soulacy -f

# Restart after config changes
sudo systemctl restart soulacy

# Validate the unit after editing it
sudo systemd-analyze verify /etc/systemd/system/soulacy.service
```

## Upgrade a release installation

Back up first:

```bash
sudo systemctl stop soulacy
sudo tar -C /var/lib -czf "/root/soulacy-backup-$(date +%F-%H%M%S).tar.gz" soulacy
sudo systemctl start soulacy
```

Then check the release update path:

```bash
UPDATE_MANIFEST=https://github.com/vmodekurti/soulacy/releases/latest/download/release-manifest.json
sy update check --manifest "$UPDATE_MANIFEST"
sudo sy update install --manifest "$UPDATE_MANIFEST" --dry-run
sudo sy update install --manifest "$UPDATE_MANIFEST" --yes
sudo systemctl restart soulacy
```

Passing `--manifest` matters when `sudo` changes `HOME` and does not inherit the
service's config environment. It makes the release source explicit instead of
accidentally reading root's empty workspace.

If the installed build is a source commit and reports “versions are not
comparable,” install the desired tagged release bundle explicitly using step 1.
Do not edit version strings to bypass the safety check.

After every upgrade:

```bash
soulacy --version
sudo systemctl restart soulacy
sudo journalctl -u soulacy -b --no-pager -n 100
```

See [Upgrades and reinstall](upgrades.md) for rollback and migration behavior.

## Troubleshooting matrix

| Symptom | Check | Fix |
| --- | --- | --- |
| API key works in foreground but not systemd | Compare `SOULACY_CONFIG_PATH`, `SOULACY_WORKSPACE`, `HOME`, and service user | Add the explicit Environment entries, reload, and restart |
| `/etc/soulacy/config.yaml` does not exist | `sudo ls -l /etc/soulacy/config.yaml` | Create it or point `SOULACY_CONFIG_PATH` at the real file |
| Service cannot read config | `sudo -u soulacy test -r ...` | Set owner `root:soulacy` and mode `0640` |
| Service cannot create databases/logs | Test workspace write access | `sudo chown -R soulacy:soulacy /var/lib/soulacy` |
| Service uses no agents | Inspect `SOULACY_WORKSPACE` and `agent_dirs` | Put agents under the selected workspace or configure absolute agent directories |
| Browser gets 401 after config change | Service was not restarted or browser retained an old key | Restart, then enter the key from the service's config—not the login user's config |
| Caddy buffers responses | Missing streaming proxy setting | Add `flush_interval -1` and reload Caddy |
| Update says versions are not comparable | Current binary is identified by a commit rather than a release version | Install a tagged release bundle explicitly |
