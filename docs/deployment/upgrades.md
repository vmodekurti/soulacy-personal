# Upgrades & Reinstall

Soulacy is built so that upgrades are boring: schemas only move forward,
API contracts are pinned by tests, and a broken plugin can never take the
gateway down. This page covers the standard upgrade flow, what guarantees
back it, and the (destructive) full-reinstall escape hatch.

## Identify how Soulacy was installed

```bash
soulacy --version
sy version
command -v soulacy
command -v sy
```

A tagged release such as `0.1.8` can be compared with the release manifest. A
source build may identify itself by a commit such as `8c2c66f`; the updater
cannot reliably determine whether that commit is newer or older than a semantic
release and reports **versions are not comparable**. In that case, install the
desired tagged release explicitly rather than overriding the comparison.

## Release installation: standard upgrade

```bash
sy update check
sy update install --dry-run
sudo sy update install --yes
```

Use `sudo` only when the binaries are root-owned under `/usr/local/bin`. Restart
the same service that normally runs the gateway:

!!! tip "Root-owned binaries with config outside root's home"
    `sudo` may not load your user's or service's `config.yaml`. Pass the release
    manifest explicitly:

    ```bash
    MANIFEST=https://github.com/vmodekurti/soulacy-personal/releases/latest/download/release-manifest.json
    sudo sy update install --manifest "$MANIFEST" --dry-run
    sudo sy update install --manifest "$MANIFEST" --yes
    ```

```bash
# system service on a VPS
sudo systemctl restart soulacy

# user service installed by sy daemon
systemctl --user restart soulacy
```

Verify the version and startup logs after restarting.

## Source checkout: rebuild upgrade

```bash
cd ~/path/to/soulacy
git pull

# Rebuild GUI + binaries
(cd gui && npm install && npm run build)
make all

# Restart the gateway (foreground example)
lsof -ti :18789 | xargs kill 2>/dev/null || true
./bin/soulacy
```

On macOS checkouts, `build-and-restart.command` wraps the rebuild +
restart steps. Under launchd/systemd, replace the last two steps with
your service manager's restart.

Do not mix a source checkout upgrade with `sy update install` unless you intend
to replace the source-built binaries with release binaries. Stores upgrade
their own schemas at boot; there is no separate migration command.

Source builds now require **Go 1.26.6 or newer**. Binary release installs do
not require a local Go toolchain.

## Verify a systemd service uses the intended config

An interactive `soulacy serve` runs as your shell user; a system service often
runs as the dedicated `soulacy` user with home `/var/lib/soulacy`. Those two
processes do not automatically read the same `config.yaml`.

Inspect the service identity and environment:

```bash
sudo systemctl show soulacy -p User -p Group -p ExecStart --no-pager
sudo systemctl show soulacy -p Environment --value \
  | tr ' ' '\n' \
  | grep -E '^(HOME|SOULACY_CONFIG_PATH|SOULACY_WORKSPACE)='
```

For a system service, keep configuration in a service-readable location and
make the path explicit in a systemd override:

```ini
[Service]
Environment=SOULACY_CONFIG_PATH=/etc/soulacy/config.yaml
Environment=SOULACY_WORKSPACE=/var/lib/soulacy
```

Then reload and verify:

```bash
sudo install -d -o soulacy -g soulacy /etc/soulacy /var/lib/soulacy
sudo chown root:soulacy /etc/soulacy/config.yaml
sudo chmod 640 /etc/soulacy/config.yaml
sudo systemctl daemon-reload
sudo systemctl restart soulacy
sudo journalctl -u soulacy -n 100 --no-pager
```

Do not solve this by making a credential-bearing configuration world-readable.

!!! note "Agent packages: v1 deprecation timeline"
    If you install agent packages via `sy pull` / **Agents → Import**, note
    that the legacy v1 package schema is deprecated and refused after the
    **2027-06-01** cutoff. v1 imports today still succeed with a warning; move
    packages you own to v2 (namespaced ids like `owner/name`, calendar
    versioning `YYYY.MM.DD`, and typed `requires`) before then. Full details
    in [Packaging](../packaging.md).

## Why upgrades are safe

Three guard layers protect the normal path:

### 1. Database schema versioning — additive only, never down

- Every SQLite store (workboard, costs, rbac, apikeys, dlq, checkpoints,
  memory archive, action log, knowledge) records its schema version in a
  shared `soulacy_schema_version` table at boot.
- Schema changes run through a shared migration helper: one transaction
  per step, versions ≤ current are skipped, and a failed step rolls back
  cleanly.
- Migrations are **additive-only by default** — `DROP`/`RENAME`
  statements are refused unless a step explicitly opts in as destructive
  (reserved for deprecation cycles).
- Versions **never downgrade**. A database touched by a newer build keeps
  its version; don't point an older binary at it and expect new tables to
  be understood.

### 2. API contract stability

Contract tests pin the `/api/v1` response envelopes that the GUI, CLI,
and plugins depend on (agents, config, providers, skills, the plugin
install preview, the `{"error": …}` envelope, the 401 auth gate).
Additive fields are fine; removing or renaming a pinned key fails the
test suite and demands a deprecation cycle. The
[event stream](../configuration/events.md) has the same rule: schema `1`
is only bumped for breaking changes, with dual-publishing during the
transition.

### 3. Plugin SDK major gate & graceful fallbacks

- Plugin manifests may declare `sdk_major: <n>`. A plugin built against a
  **newer** SDK major than the host is refused at load with an upgrade
  hint — it never crashes the gateway.
- The loader warns-and-skips broken manifests, future schemas,
  capability/credential violations, and stale install approvals. Every
  refused plugin is published as a boot event (`type: error`,
  `stage: plugin-load`) so the Logs GUI explains every silently absent
  plugin.
- Crashed channel sidecars are contained by the supervisor (backoff
  restarts); the gateway keeps serving.

## Back up before major upgrades

All state lives in one place — the workspace:

```bash
# Stop the gateway first so SQLite files are quiescent, then:
tar -czf soulacy-backup-$(date +%F).tar.gz ~/.soulacy
```

That captures `config.yaml`, agents, skills, plugins, all databases
(`data/`), memory, and the credential vault (`secrets/`). Restoring is
untarring it back. Verify what your workspace contains with
`sy workspace info` ([Workspace Layout](../configuration/workspace.md)).

!!! tip "Minimum viable backup"
    If a full archive is too big, the irreplaceable parts are
    `config.yaml`, `agents/`, `data/`, `memory/`, and `secrets/`.

## Full reinstall from scratch

The repo ships `reinstall-from-scratch.command` (macOS) for when you want
a genuinely clean slate:

```bash
./reinstall-from-scratch.command
```

What it does, in order:

1. Prompts for a **typed confirmation** — you must literally type `wipe`.
2. Stops the gateway (port 18789, plus any launchd service).
3. **Deletes `~/.soulacy` entirely — agents, memories, the encrypted
   credential vault, config.yaml. No backup is taken.**
4. Rebuilds the GUI dist and binaries from the checkout (`make all`).
5. Boots once so a fresh soulspace workspace is created, prints
   `sy workspace info`, and offers the `sy setup` wizard.

!!! danger "Destructive — everything under ~/.soulacy is gone"
    This is a wipe, not an upgrade. If there is any chance you'll want
    your agents, conversation history, or vault back, take the backup
    above *first*. For a normal version bump, use the standard upgrade
    flow — never the reinstall script.

## Rollback

Every upgrade run by `install.sh` first **snapshots the outgoing install** —
both the `soulacy` and `sy` binaries and the current `config.yaml` — into
`~/.soulacy/backups/<timestamp>/` before overwriting anything. The five most
recent snapshots are kept (`SOULACY_BACKUP_KEEP` to change; `SOULACY_BACKUP_DIR`
to relocate).

If a new version misbehaves, roll straight back to the previous one:

```bash
./install.sh --rollback
# or from the one-liner:
curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/install.sh | bash -s -- --rollback
```

This restores the previous binaries and their matching `config.yaml`. Your
current config is never destroyed — it's saved next to the restored one as
`config.yaml.pre-rollback.<epoch>` first. Restart the gateway afterward
(`soulacy serve`, or `sy daemon stop && sy daemon start`).

!!! note "Data, not just the binary"
    Rollback swaps the binary and config back. Because schema migrations only
    move forward, if an upgrade migrated your workspace database, pair the
    rollback with the matching workspace backup (see below) for a fully
    consistent previous state.

## See also

- [Workspace Layout](../configuration/workspace.md) — what to back up
- [macOS deployment](macos.md) · [Linux / VPS](linux.md) · [Docker](docker.md)
