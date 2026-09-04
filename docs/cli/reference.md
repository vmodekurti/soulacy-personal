# CLI Reference

Two binaries:

- **`sy`** — the CLI client. Every GUI action is available here; commands
  talk to a running gateway over its REST API.
- **`soulacy`** — the gateway server itself, plus the build tool and the
  reference package registry.

## Global `sy` flags

| Flag | Default | Description |
|------|---------|-------------|
| `--gateway` | `http://localhost:18789` (or `cli.gateway_url` / `server.port` from config) | Gateway URL |
| `--api-key` | `server.api_key` from config | API key for gateway authentication |
| `--json` | `false` | Output raw JSON |

---

## Getting set up

```bash
sy setup            # interactive wizard: providers, channels, writes config.yaml
sy doctor           # local diagnostics — config, dirs, Python, Ollama, gateway, MCP
sy doctor --json    # machine-readable report
```

`sy doctor` exits nonzero only on hard failures; warnings flag suspicious
but non-fatal configuration (relative `agent_dirs`, non-absolute
`runtime.python_bin`, …).

It also checks the production update path. If `updates.manifest_url` or
`SOULACY_UPDATE_MANIFEST` is configured, the doctor fetches the release manifest
and reports whether this install is current, upgradeable, or unable to reach the
manifest. Missing or unreachable update manifests are warnings so local
development is not blocked, but production workspaces should clear them.

## Managing agents

```bash
sy agent list
sy agent get support-bot
sy agent create --file ./SOUL.yaml
sy agent validate examples/agents/hello-world/SOUL.yaml
sy agent enable support-bot
sy agent disable support-bot
sy agent trigger daily-briefing      # manually fire a scheduled agent
sy agent delete old-bot

sy agent package export support-bot --out support-bot.soulacy-agent.json
sy agent package export support-bot --signing-key-file ./ed25519.hex
sy agent package inspect support-bot.soulacy-agent.json
sy agent package import support-bot.soulacy-agent.json      # imports disabled for review
sy agent package import support-bot.soulacy-agent.json --enable --overwrite
```

`sy agent validate` checks YAML fields, trigger/schedule consistency,
provider and model availability, tool paths, and MCP references — errors
return nonzero, making it CI-friendly.

Agent packages are portable `.soulacy-agent.json` bundles. They include
redacted `SOUL.yaml`, safe local tool files when available, bundled
`evals/`, `prompts/`, and `samples/` files, a setup requirements checklist,
a content checksum, and optional Ed25519 signature metadata. `inspect`
verifies package integrity and shows missing providers, channels, peer
agents, skills, files, knowledge bases, and secrets before anything is
imported.

Pull a definition from a URL or the public registry:

```bash
sy pull my-agent                                   # registry ID
sy pull org/repo                                   # GitHub shorthand (main/SOUL.yaml)
sy pull https://example.com/agents/agent.yaml      # direct URL
sy pull my-agent --dir ~/agents --force            # custom dir, overwrite
```

## Chatting & evaluating

```bash
sy chat --agent support-bot "Summarize today's tickets"
sy chat --agent support-bot --user alice "Hello!"

sy eval --agent my-agent --suite tests/smoke.json        # pass/fail report
sy eval --agent my-agent --suite tests/smoke.json --json
sy eval --agent my-agent --suite evals/golden --tag weather --repeat 3
sy eval --agent my-agent --suite evals/golden --fail-fast
```

Eval suites are JSON:
`{"name": "smoke", "cases": [{"name":"math","input":"2+2?","expected_contains":["4"]}]}`.
A failing case makes the command exit nonzero. Reports include aggregate
pass/fail/skip counts plus latency and token summaries when available.

## Channels

```bash
sy channel list
sy channel status whatsapp_web
sy channel enable telegram
sy channel disable whatsapp_web
sy channel update telegram --set trigger_phrase='!soulacy' --set ignore_groups=true
```

Each adapter also has a first-class namespace with
`status` / `enable` / `disable` / `configure`:

```bash
sy channel telegram configure --token "$TELEGRAM_BOT_TOKEN" --agent assistant
sy channel slack configure --bot-token "$SLACK_BOT_TOKEN" --app-token "$SLACK_APP_TOKEN" --agent assistant
sy channel discord configure --token "$DISCORD_BOT_TOKEN" --agent assistant --guild '1234567890'
sy channel whatsapp configure --phone-number-id "$ID" --access-token "$TOK" \
  --verify-token "$VTOK" --app-secret "$SECRET" --agent assistant
sy channel http status
```

All `configure` commands share the activation-safety flags: `--trigger`
(wake phrase), `--allow-groups`, `--allowed-chats`, `--allowed-users`.

WhatsApp Web pairs over QR:

```bash
sy channel whatsapp-web pair --agent assistant            # safe defaults: trigger !soulacy, no groups
sy channel whatsapp-web pair --agent assistant --trigger '!ask' --allow-groups
sy channel whatsapp-web status                            # connection state + QR payload
```

## Skills & registries

For a Git URL, the unified installer detects whether the repository contains a
Skill or MCP server, shows the safety/approval step, installs it persistently,
and registers MCP servers automatically:

```bash
sy package install https://github.com/owner/repository --allow-unverified
```

The built-in **System** agent uses this same installer when you say “Install
the Skill/MCP server from this URL.” Existing installations are reported and
left unchanged.

```bash
sy skill list
sy skill get pdf-tools
sy skill install ./my-skill                      # local directory
sy skill install self-improving-agent            # registry slug
sy skill install github.com/user/my-skill        # git source
sy skill install some-skill --yes                # skip consent prompt
```

Remote installs resolve through the `registries:` config block (falling
back to a bare git provider), run the safety introspection pipeline
(static scan + sandboxed dry-run), show a consent prompt, then hot-load
via the gateway's `/skills/rescan` API.

!!! note "`--yes` never bypasses danger"
    `--yes` skips the routine consent prompt, but a **danger** safety
    verdict always requires an interactive yes.

Manage skill sources:

```bash
sy registry list                                  # configured sources
sy registry probe https://www.skills.sh/          # review what a URL is
sy registry add https://www.skills.sh/            # probe + consent + save
sy registry add https://reg.example.com --id main --priority 10 -y
```

`probe` runs client-side (no gateway needed). `add` saves via the gateway
API when it is reachable, otherwise appends directly to `config.yaml`.

## Memory, schedule & logs

```bash
sy memory list --agent support-bot     # session memory entries
sy schedule list                       # scheduled agent entries
sy logs --follow                       # stream live events
```

## Workspace

```bash
sy workspace info                  # resolved layout (soulspace vs legacy) + every path
sy workspace migrate --dry-run     # print the migration plan, move nothing
sy workspace migrate               # migrate legacy ~/.soulacy → soulspace (confirm; -y to skip)
```

Stop the gateway before `migrate` — databases move as files. See
[Workspace Layout](../configuration/workspace.md).

## Gateway control

```bash
sy server status     # GET /health against the gateway
sy server start      # convenience hint — run the `soulacy` binary for production
sy update check --manifest ./release-manifest.json
sy update install --manifest ./release-manifest.json --dry-run
sy update install --manifest ./release-manifest.json --yes
sy version           # CLI version + resolved gateway URL
```

When `updates.manifest_url` is configured in `config.yaml` or
`SOULACY_UPDATE_MANIFEST`, `sy update check` and `sy update install` use it by
default. `sy launch check` also reports whether that production upgrade path is
configured.

---

## The `soulacy` binary

Running `soulacy` with no subcommand starts the gateway in the
foreground, loading config from `SOULACY_CONFIG_PATH` or the
[workspace](../configuration/workspace.md):

```bash
soulacy                                          # start the gateway
SOULACY_CONFIG_PATH=/etc/soulacy/config.yaml soulacy
```

### `soulacy build`

Build a flavored binary with extra driver modules compiled in
(see [Custom Distributions](../extend/custom-distributions.md)):

```bash
soulacy build --with github.com/acme/soulacy-matrix@v1.2.0 -o bin/soulacy-matrix
```

| Flag | Default | Description |
|------|---------|-------------|
| `--with` | — | Extra driver module, `module[@version]` (repeatable) |
| `-o` | `bin/soulacy` | Output binary path |
| `--skip-verify` | `false` | Skip conformance/registry test gates |
| `--keep` | `true` | Keep the generated `builtins_extra.go` (required for rebuilds) |

### `soulacy registry serve` / `keygen`

Host your own package registry
(see [Package Registries](../extend/registries.md)):

```bash
# Generate a signing keypair (private key written 0600; public key printed)
soulacy registry keygen --out ~/.soulacy/registry-signing.key

# Serve <slug>-<version>.tar.gz archives, signed
soulacy registry serve --dir ./packages --addr 127.0.0.1:18790 \
    --signing-key-file ~/.soulacy/registry-signing.key
```

Consumers put the printed **public** key in their `registries:` entry as
`signing_key` — unsigned or tampered packages are then refused.

| `serve` flag | Default | Description |
|--------------|---------|-------------|
| `--dir` | `packages` | Directory of `<slug>-<version>.tar.gz` archives |
| `--addr` | `127.0.0.1:18790` | Listen address |
| `--signing-key-file` | — | Hex ed25519 private key; when set, every package is signed |
