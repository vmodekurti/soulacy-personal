# Agent Package Format

Soulacy exports and imports agents as self-contained `.soulacy-agent.json`
packages — a redacted `SOUL.yaml`, a machine-readable manifest listing every
provider / channel / skill / peer / secret the agent depends on, integrity
metadata, and any supporting files (Python tools, evals, prompts, samples).

This page is the reference for the format and the CLI. For the design
rationale (why calendar versioning, why a config-driven trust store, why
namespaced package ids), see `docs/PACKAGE_VERSIONING_DESIGN.md`.

## Schema versions

| Schema | Status | Notes |
|---|---|---|
| `soulacy.agent.package/v1` | **Deprecated (import path warns)** | Legacy shape shipped with earlier Soulacy releases. Missing calendar versioning, namespaced ids, and the `requires` block. |
| `soulacy.agent.package/v2` | **Current** | Required calendar version (`YYYY.MM.DD[.PATCH]`), namespaced `package_id`, optional `publisher` block, and a hard-requirements `requires` block that gates imports. |

### v1 deprecation timeline

- **Now → 2027-05-31:** v1 packages import with a WARN on the CLI (stderr,
  `warning: package uses deprecated schema ...`) and a yellow banner in the
  GUI import modal.
- **2027-06-01 onwards:** v1 imports are refused with a hard 400 from the
  gateway. `sy pull --allow-v1` remains as a local-dev escape hatch.

The cutoff is a date, not a version, so operators track calendar time rather
than Soulacy release numbers.

## Calendar versioning

Format: `YYYY.MM.DD` with an optional `.PATCH` suffix.

Examples: `2026.07.14`, `2026.07.14.1`, `2026.07.14.42`.

Regex: `^(\d{4})\.(\d{2})\.(\d{2})(?:\.(\d+))?$`.

**Rules:**
- Zero-padded month and day are required (`2026.07.14`, not `2026.7.14`).
- `.PATCH` is optional; publishers use it for same-day iterations. When
  omitted, treated as `.0` for ordering.
- Ordering is lexicographic on the parsed 4-tuple `(year, month, day, patch)`
  as integers. `2026.07.14 < 2026.07.14.1 < 2026.07.14.10 < 2026.07.15`.
- **No pre-release / build metadata suffixes.** Ship or don't; use tomorrow's
  `.PATCH` to fix forward.

## Namespaced package ids

`package_id` is required in v2 and must be `<publisher>/<package>`:

- **Publisher:** 2-32 lowercase alphanumeric + hyphen characters.
- **Package:** 1-63 lowercase alphanumeric + hyphen characters, starting with alnum.

Examples: `soulacy/hackernews-digest`, `vasu/my-agent`, `acme-corp/production-2`.

The `soulacy/` namespace is reserved and can only be signed by keys listed as
`official` in the registry's publisher directory. Third-party publishers pick
their own namespace and register a key at push time (bucket 7B).

## `sy package` commands (available today)

- `sy package export <agent-id> [--out FILE] [--signing-key-file FILE]` — export a saved agent.
- `sy package inspect <package.json>` — hit the gateway to preview requirements against your workspace.
- `sy package import <package.json> [--overwrite] [--enable] [--id NEW-ID] [--acknowledge-missing]` — import.
- `sy package validate <package.json>` — **new in 7A** — local-only structural check (schema, calendar version, namespaced id). No gateway hit; useful for publishers.

## Requirements gate (7A)

When a v2 package declares a `requires` block, the import handler classifies
each declared secret / provider / channel / peer agent against the target
workspace:

| Status | Meaning |
|---|---|
| `available` | Ready to use immediately. |
| `configured` | Present in config, not confirmed live. |
| `built_in` | Always available (e.g. the `http` channel). |
| `declared` | Bundled with the package (e.g. MCP server metadata). |
| `packaged` | File is inline in the package payload. |
| `missing` | Nothing on this workspace satisfies it. |

Importing a v2 package with any `missing` requirement returns 409 unless the
request carries `acknowledge_missing: true`. The CLI exposes this as
`--acknowledge-missing`; the GUI import modal shows the missing entries in a
red banner with an "I understand — import anyway" checkbox.

v1 packages don't have a `requires` block, so nothing gates them structurally
(beyond the existing schema + SOUL.yaml validation).

## `.soulacy-package.json` sidecar

Every v2 import (and every v1 import, with mostly-empty fields) writes a
`.soulacy-package.json` file next to `SOUL.yaml`:

```json
{
  "schema_version": "soulacy.agent.package/v2",
  "package_id": "soulacy/hackernews-digest",
  "installed_version": "2026.07.14",
  "installed_at": "2026-07-14T15:23:00Z",
  "publisher": { "id": "soulacy", "trust_level": "official" },
  "signature_verified": true,
  "acknowledged_missing": false
}
```

Bucket 7C uses this sidecar to populate the Package tab on Agent details and
to drive the rollback picker.

## Trust and signing (Buckets 7B/7C, not yet shipped)

The design memo lays out the config-driven trust store under
`packages.trusted_keys` in `soulacy.yaml`; the config scaffold is in place
today, but the runtime enforcement lands with 7C. Publisher signing at push
time (`sy package publish --key ...`) lands with 7B alongside the registry
index format.
