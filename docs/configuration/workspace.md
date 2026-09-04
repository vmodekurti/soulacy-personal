# Workspace Layout (soulspace)

Everything Soulacy owns lives in **one organized workspace** — the
*soulspace* — instead of files scattered across a flat dot-directory. You
get predictable paths for backups, a single `config.yaml`, and a clean
separation between data, logs, and secrets.

```
~/.soulacy/soulspace/
├── config.yaml        # the one config file
├── agents/            # SOUL.yaml definitions
├── skills/            # installed skills (sy skill install target)
├── plugins/           # installed plugins (+ .staging)
├── templates/         # user template overrides
├── tools/             # shared python tools
├── memory/            # brain memory (episodic / semantic / procedural)
├── data/              # ALL databases: actions.db, archive.db, knowledge.db,
│                      #   plugins.db, rbac.db, costs.db, workboard.db,
│                      #   apikeys.db, dlq.db, history.db, checkpoints.db
├── logs/              # log files
├── audit/             # tool-call audit JSONL
├── secrets/           # credential vault + signing keys (0700)
├── registry/          # packages for `soulacy registry serve`
└── gui/               # optional static GUI override
```

## Inspect your workspace

```bash
sy workspace info
```

```
Workspace: /Users/you/.soulacy/soulspace
Layout:    soulspace

  config     /Users/you/.soulacy/soulspace/config.yaml
  agents     /Users/you/.soulacy/soulspace/agents
  skills     /Users/you/.soulacy/soulspace/skills
  plugins    /Users/you/.soulacy/soulspace/plugins
  ...
```

On a pre-soulspace installation the layout line reads
`legacy (flat ~/.soulacy — run 'sy workspace migrate' to organize)`.

## Resolution order

Soulacy decides where the workspace lives at startup, in this order:

| Priority | Condition | Result |
|----------|-----------|--------|
| 1 | `SOULACY_WORKSPACE` env var is set | That directory, soulspace layout |
| 2 | `~/.soulacy/soulspace` exists | Soulspace layout (fresh install or post-migration) |
| 3 | `~/.soulacy` has legacy content (`config.yaml`, `agents/`, `actions.db`, or `skills`) | **Legacy flat layout** — every path resolves to its historical location, bit-for-bit unchanged |
| 4 | Nothing exists | A fresh `~/.soulacy/soulspace` is created on first run |

!!! note "Legacy installations keep working"
    The legacy auto-detection means pre-soulspace installations need no
    action at all. Databases stay flat in `~/.soulacy/`, and explicitly
    configured paths (`agent_dirs`, `memory.dir`, …) always win over
    workspace defaults — in both layouts.

### Relocating the workspace

```bash
export SOULACY_WORKSPACE=/srv/soulacy
soulacy   # gateway now roots everything at /srv/soulacy
```

The directory is treated as a soulspace layout regardless of its name.

## Migrating a legacy installation

The migration moves a flat `~/.soulacy` into the organized layout. Always
preview first:

```bash
sy workspace migrate --dry-run   # print the full plan, move nothing
```

Then, **with the gateway stopped** (databases move as files):

```bash
sy workspace migrate             # interactive confirm
sy workspace migrate -y          # skip the confirmation prompt
```

What the migration does:

- Moves every known directory and database into the organized layout —
  databases go to `data/`, the credential vault to `secrets/`, and
  WAL/SHM siblings travel with their database.
- Rewrites absolute legacy paths inside `config.yaml` so configured
  locations follow their files. Comments and unknown blocks are preserved
  byte-for-byte.
- Leaves anything it does not recognise exactly where it was, and lists
  it in the plan output so you can move it manually if needed.
- Every move is an atomic rename — soulspace lives inside `~/.soulacy`,
  so source and destination are on the same filesystem by construction.

After migration the resolver picks the soulspace automatically. Restart
the gateway and verify with `sy workspace info`.

!!! warning "Stop the gateway first"
    `sy workspace migrate` moves SQLite databases as files. Running it
    against a live gateway risks corruption. Stop the gateway, migrate,
    then restart.

## Where config.yaml is found

The gateway and CLI search for `config.yaml` in this order:

1. The explicit path in `SOULACY_CONFIG_PATH` (gateway) or `--gateway`
   discovery via `cli.gateway_url` (CLI).
2. The current working directory (project-level config wins for dev).
3. The workspace root (soulspace or legacy).
4. The legacy flat `~/.soulacy` (harmless duplicate when the workspace
   *is* legacy).

Any config key can also be overridden by environment variables with the
`SOULACY_` prefix — dots become underscores:

```bash
export SOULACY_SERVER_API_KEY="sy_..."   # overrides server.api_key
export SOULACY_SERVER_PORT=18789          # overrides server.port
```

## See also

- [Configuration overview](index.md) — every top-level config key
- [Storage & backends](storage.md) — what lives in `data/`
- In-repo spec: [`docs/WORKSPACE.md`](../WORKSPACE.md)
