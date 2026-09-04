# The Soulacy Workspace ("soulspace")

Everything the framework owns lives in ONE organized workspace instead of
files scattered across a flat dot-directory:

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

## Resolution order

1. **`SOULACY_WORKSPACE`** env var — explicit root, soulspace layout.
2. **`~/.soulacy/soulspace`** exists — soulspace layout (fresh install or
   post-migration).
3. **Legacy**: `~/.soulacy` exists with pre-soulspace content
   (config.yaml / agents/ / actions.db / skills) — every path resolves to
   its historical flat location, **bit-for-bit unchanged**. Existing
   installations keep working without any action.
4. Nothing exists — a fresh `~/.soulacy/soulspace` is created on first run.

Explicitly configured paths (config.yaml `agent_dirs`, `memory.dir`, …)
always win over workspace defaults, in both layouts.

`sy workspace info` prints the resolved mode and every path.

## Migrating a legacy installation

```bash
sy workspace migrate --dry-run   # print the full plan, move nothing
# stop the gateway, then:
sy workspace migrate             # interactive confirm (-y to skip)
```

What it does: moves every known directory and database into the organized
layout (databases → `data/`, the credential vault → `secrets/`, WAL/SHM
siblings travel with their database), rewrites absolute legacy paths inside
config.yaml so configured locations follow their files (comments and
unknown blocks preserved byte-for-byte), and leaves anything it doesn't
recognise exactly where it was — listed in the plan output. Same
filesystem by construction (soulspace lives inside `~/.soulacy`), so every
move is an atomic rename. After migration the resolver picks soulspace
automatically; restart the gateway.

**Stop the gateway first** — databases move as files.
