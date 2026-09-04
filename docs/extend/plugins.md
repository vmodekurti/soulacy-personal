# Plugins

Plugins extend Soulacy with new tools, chat channels, LLM providers, skills, and GUI panels — installed from the web GUI with an explicit review of everything they request before anything activates.

## Quick start

1. Open the **Plugins** page in the GUI.
2. Paste a source — a git URL, a sha256-checksummed archive
   (`.tar.gz`/`.zip`), or a local directory path — and click
   **⤓ Clone & review** (or **⤓ Fetch & review**).
3. Review the approval dialog: safety verdict, requested capabilities,
   credentials, schema migrations, sidecar channels, providers.
4. Click **✓ Approve & install**, then restart the gateway to load it.

## What plugins can contribute

A plugin is a directory with a `plugin.yaml` manifest. With manifest
schema 2 it can declare:

| Contribution | What you get |
|---|---|
| `tools:` | Python tool libraries callable by agents |
| `channels:` | sidecar chat channels in any language ([External Channel Protocol](../channels/sidecars.md)) |
| `providers:` | OpenAI-compatible LLM endpoints registered with the router |
| `skills:` | Agent Skill directories joined to the skill search path |
| `gui:` | a static UI panel served at `/plugins/<id>/ui/` in a sandboxed iframe, with its own nav entry |
| `migrations:` | SQLite schema in the dedicated plugin database (`~/.soulacy/plugins.db`) |

## Installing from the GUI

The flow is **stage → approve → restart**:

1. **Stage** — the source is fetched into a `.staging` area and never
   loaded. Archives require a sha256 checksum, verified before extraction;
   git URLs are shallow-cloned with history stripped; extraction is
   hardened against path traversal and decompression bombs.

2. **Approve** — the dialog shows *everything* the manifest requests:

    - **Safety introspection** — verdict badge and findings from the
      [pre-install safety pipeline](safety.md);
    - **Requested capabilities** — with scopes; unscoped grants are flagged
      loudly;
    - **Requested credentials** — which vault secrets the plugin's sidecars
      will receive;
    - **Declared schema migrations** — so you approve schema alongside
      permissions;
    - **Sidecar channels** and **LLM providers** it will register.

    Approving records a *permission fingerprint* — a canonical,
    order-insensitive hash of the approved permissions and credentials —
    in `.soulacy-install.json` next to the plugin.

3. **Load** — at the next gateway restart the loader's install gate admits
   the plugin. Every install response carries the restart note.

!!! warning "Nothing is active until you approve — and approval is precise"
    Staged plugins are never loaded. Approval covers exactly the grants you
    saw. If a later update adds, widens, or re-scopes any permission or
    credential, the fingerprint no longer matches and the plugin **stops
    loading** until you explicitly re-approve it.

## Managing installed plugins

The Plugins page lists every installer-managed plugin with its status,
source, and granted permissions:

- **Enable / Disable** — flips the load gate without touching files
  (`POST /api/v1/plugins/:id/enable|disable`).
- **Re-approve** — appears when an update changed the requested
  permissions; shows as *needs re-approval* and logs
  `plugins: plugin skipped by install state` until you act
  (`POST /api/v1/plugins/:id/reapprove`).
- **Remove** — deletes the plugin from disk (`DELETE /api/v1/plugins/:id`).

All plugin-management routes require config-level RBAC. **Hand-installed
plugins** (directories placed in a plugin dir without install metadata) are
implicitly approved and invisible to the installer — putting files on disk
already required operator access.

## Plugin settings: `plugins_config`

Plugins read their own settings from a `plugins_config:` block in
`config.yaml`, keyed by plugin ID. The shape under each key is owned by the
plugin — the core parser never validates it:

```yaml
plugins_config:
  weather-bot:
    units: metric
    cache_ttl: 15m
```

The **Config** GUI page has a *Plugin settings* editor for these sections.
Secret-looking keys (`token`, `secret`, `password`, `api_key`,
`credential` in the name) are redacted as `***` in the config API, so they
never reach the browser — and the server skips `***` placeholders when
saving, so editing other settings never clobbers real secrets on disk.

## Authoring: manifest v2 overview

```yaml
id: matrix-suite
name: Matrix Suite
version: 1.0.0
manifest_schema: 2

channels:                       # sidecar channels (External Channel Protocol)
  - id: matrix
    agent_id: assistant         # required: agent that receives messages
    sidecar:
      command: node             # required
      args: ["sidecar/matrix.mjs"]

providers:                      # OpenAI-compatible inference endpoints
  - id: local-vllm
    openai_compatible:
      base_url: http://localhost:8000/v1   # required
      api_key_env: VLLM_KEY     # host env var holding the key (optional)
      model: llama-3.3-70b      # default model (optional)

tools:                          # Python tool libraries (unchanged from v1)
  - rooms

skills:                         # agent skill directories (relative to root)
  - skills/moderation

gui:                            # static UI mount, sandboxed iframe
  nav: { label: "Matrix", icon: "💬" }
  static: ui                    # directory must exist

permissions:                    # capabilities — default-deny without them
  - cap: channel.send
    channels: [matrix]

credentials:                    # vault delegation, own namespace only
  - key: MATRIX_TOKEN
    from: matrix-suite/token

migrations:                     # plugin-namespaced SQLite schema
  - name: 001_items
    up_sql: CREATE TABLE plugin_matrix_suite_items (id INTEGER PRIMARY KEY)
```

Rules worth knowing as an author:

- Legacy v1 manifests (no `manifest_schema`, or 1) keep loading forever —
  tools only; v2-only blocks in a v1 manifest are skipped with a warning.
- A v2 manifest with a malformed contribution is refused with a precise
  error; unknown future schemas (`> 2`) are skipped, never guessed at.
- Migration table names must be prefixed `plugin_<id>_`; `ATTACH`,
  `PRAGMA`, and friends are refused; applied steps are checksummed — add a
  new step instead of editing an old one.
- Capabilities and credentials are the heart of the trust model — read
  [Plugin Security Model](plugin-security.md) before publishing.

Full references: [plugin manifest](../PLUGIN_MANIFEST.md),
[capabilities](../PLUGIN_CAPABILITIES.md),
[credentials](../PLUGIN_CREDENTIALS.md),
[migrations](../PLUGIN_MIGRATIONS.md).
