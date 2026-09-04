# Agent Package Versioning — Design Memo (Story 7)

**Status:** design memo. Six open questions were resolved by the user on 2026-07-14; this revision bakes those decisions in and is the reference for Buckets 7A/B/C implementation.
**Date:** 2026-07-14 (rev 2, decisions applied)
**Cohort:** C (design finalized), 7A implementation starts alongside this revision.

## 0. Decisions applied (from user, 2026-07-14)

The six §9 questions from rev 1 have been answered:

1. **Calendar versioning** — `YYYY.MM.DD[.PATCH]`, not semver.
2. **Config-driven trust store** — under `packages.trusted_keys` in `soulacy.yaml`, not a local `~/.soulacy/trusted-keys.json`.
3. **Single registry** — only `soulacy/registry`; no multi-registry priority ordering.
4. **v1 packages break at a cutoff** — warn now, error after `2027-06-01` (see §5.1 for full deprecation timeline).
5. **Snapshot pruning ships with 7C**, not deferred beyond it.
6. **Third-party publishing enabled** — with namespacing, publisher signing at push time, and an official-vs-community trust distinction (see §4.5).

Everything in this memo assumes those decisions. The rev 1 alternative options have been removed; §9 (open questions) is now empty because everything is decided.

---

## 1. Executive summary

Soulacy has three "package"-ish surfaces today, at different maturity levels:

1. **Agent-side rollback** (SHIPPED). `.agent-history/<id>/<UTC-timestamp>.yaml` snapshot on every `Upsert`/`Delete`/rollback; gateway list/read/restore endpoints; GUI history modal.
2. **`.soulacy-agent.json` export / import** (SHIPPED, partial). Self-contained JSON package; schema-validate → integrity check → agent-validate → capability-requirement report → write. **Does not refuse** on missing providers, channels, or secrets.
3. **`sy pull <url-or-id>`** (PARTIAL, thinnest surface). Raw `SOUL.yaml` fetch; no version pinning, no diff, no rollback, no changelog, no required-secret prompt, no local cache, no fingerprint, no signature.

Story 7's ambition — "operators can pin a version, see a changelog, roll back an installed package, and re-import a version they've already vetted without re-approving every side-effect" — is entirely about surface #3 with a parallel package-side rollback that mirrors surface #1. Surface #2 is the natural transport for versioned distribution once #3 grows up.

**Third-party publishing (decision #6) means one more surface** — a publisher-side signing tool + a package-namespacing convention (`vasu/my-agent` vs `soulacy/hackernews-digest`) — treated in §4.5.

---

## 2. What already exists — inventory with file:line

*(unchanged from rev 1; retained verbatim so the memo remains self-sufficient)*

### 2.1 Agent-side rollback (SHIPPED)

**HTTP surface** (`internal/gateway/api.go`):

- `GET /api/v1/agents/:id/versions` → `handleListAgentVersions` (`api.go:381-391`) — RBAC `agents:read`.
- `GET /api/v1/agents/:id/versions/:version` → `handleGetAgentVersion` (`api.go:393-403`) — RBAC `agents:read`. Returns `{agent_id, version, yaml}` (raw YAML text).
- `POST /api/v1/agents/:id/rollback` → `handleRollbackAgent` (`api.go:405-435`) — RBAC `agents:write`. Body `{version}`. Blocks protected system agent.

**Loader** (`internal/runtime/loader.go`):

- `type AgentVersion` (`loader.go:40-46`): `{ID, AgentID, Path, CreatedAt, Bytes}`.
- `AgentVersions(id)` (`loader.go:485-518`) walks `<agent_dir>/.agent-history/<id>/*.yaml`, sorts newest-first.
- `ReadAgentVersion(id, version)` (`loader.go:521-546`) reads raw bytes; refuses path traversal.
- `RestoreAgentVersion(dir, id, version)` (`loader.go:549-570`) reads, forces `def.ID = id`, calls `Upsert` (which snapshots the current version first).
- `snapshotPath` (`loader.go:572-593`) — filename `time.Now().UTC().Format("20060102T150405.000000000Z") + ".yaml"`. **No pruning, no retention policy, no size cap** — every save/delete/rollback adds a file forever. (Fixed in Bucket 7C per decision #5.)

**Frontend** (`gui/src/pages/Agents.svelte`): history modal at 2774-2822; `openHistory`, `selectVersion`, `rollbackVersion`; read-only YAML display; no diff.

**On-disk shape**:
```
<agent_dir>/
  <id>/SOUL.yaml
  .agent-history/<id>/
    20260714T093012.123456789Z.yaml
```

### 2.2 `.soulacy-agent.json` export / import (SHIPPED, partial)

Handlers registered in `internal/gateway/server.go:652-656`:
- `POST /api/v1/agents/package/inspect` → `handleInspectAgentPackage`
- `POST /api/v1/agents/package/import` → `handleImportAgentPackage`
- `GET /api/v1/agents/:id/package` → `handleGetAgentPackage`

Types (`internal/gateway/agent_package.go`):
- `agentPackageResponse` (line 72-79): `{SchemaVersion="soulacy.agent.package/v1", ExportedAt, Manifest, SOULYAML, Files, Integrity}`.
- `agentPackageManifest` (line 39-62): 22 fields including `Version string` (line 42, straight pass-through of `agent.Definition.Version`).
- `agentPackageIntegrity` (line 64-70): `{Algorithm="sha256", SHA256, Signature, PublicKey, Verified}` — hash computed with `Integrity` zeroed. ed25519 signature optional.

**Critical gap for versioning:** `Requirements` statuses are surfaced but do **not** block import. Missing providers, missing secrets, missing peers, and conflicting agent ids all pass through unless the operator manually reads the report.

### 2.3 `sy pull` CLI (PARTIAL — the thinnest surface)

**Command** (`cmd/sy/pull.go`): `sy pull <url-or-id> [--dir <path>] [--force]`.

Three source shapes (`pull.go:76-183`):
1. Full URL — `fetchURL` 15 s timeout.
2. `owner/repo` shorthand — hardcoded `https://raw.githubusercontent.com/<ref>/main/SOUL.yaml`.
3. Plain ID — fetches `registryManifestURL` (`pull.go:50`), linear scan.

Validation: UTF-8 + must contain `id:` (`pull.go:132-138`). Writes flat to `<outputDir>/<agentID>.yaml`. Only prompt: overwrite Y/N.

**Missing subcommands** (confirmed via `cmd/sy/` grep): `--version`, `rollback`, `changelog`, `list`, `refresh`, `trust`.

**Registry storage**: remote `https://raw.githubusercontent.com/soulacy/registry/main/index.json` — one-shot HTTP GET, no ETag / cache-control. **No local cache.**

### 2.4-2.7

*(unchanged from rev 1)* — `internal/pkgregistry` engine (used by `sy skill install`, not `sy pull`); `internal/plugininstall` fingerprint pattern (plugins only, agent packages have no equivalent); `internal/templates.RequiredSecrets` (used by templates + evals, not by agent-package import); `pkg/agent/types.go:288` `Version string` on Definition (free-form).

---

## 3. What's missing at the package level

| # | Story 7 AC | Present today? | Gap |
|---|---|---|---|
| 1 | Version-pinned import (`sy pull <id>@<v>`) | No | No CLI, no registry-side version list; git provider hardcodes `HEAD`. |
| 2 | Changelog display before install | No | No changelog storage, no CLI/GUI surface. |
| 3 | Required-secret prompt at install-time | No | `agentPackageRequirement` says `"verify"` for every secret; nothing gates on it. |
| 4 | Required-provider / channel / MCP / tool validation before install | Partial | `inspectAgentPackage` produces the report; `handleImportAgentPackage` does not enforce it. |
| 5 | Package-side rollback (`sy pull rollback <id>`) | No | Agent-side rollback ships; package-import history isn't tracked distinctly. |
| 6 | Version history per package | No | Snapshots are per-agent (§2.1) but keyed by save-time, not by package version / origin. |
| 7 | Package origin metadata visible in the GUI | Partial | `agentPackageManifest.Source` exists on export but isn't stored on the imported agent. |
| 8 | Signed-package trust chain (author identity, not just integrity) | Partial | Optional ed25519 signature verifies content integrity; author identity isn't attested against a trust store. |

Third-party publishing (decision #6) adds a ninth:

| 9 | Publisher-side push tooling with namespacing + signing | No | No `sy package publish`; no namespacing convention on `slug`; no publisher key infrastructure. |

---

## 4. Proposed data model

### 4.1 Calendar versioning (decision #1)

**Format:** `YYYY.MM.DD[.PATCH]`. Examples: `2026.07.14`, `2026.07.14.1`, `2026.07.14.42`.

**Regex** (used for validation across CLI + gateway + registry): `^(\d{4})\.(\d{2})\.(\d{2})(?:\.(\d+))?$`.

**Semantics:**
- `YYYY.MM.DD` is the release date in UTC. Dates in the future are rejected at publish time (registry-side check).
- `.PATCH` is optional; when omitted, treated as `.0` for ordering. Same-day iterations increment `.PATCH` sequentially — publishing a second package on the same UTC day means bumping to `.1`.
- Ordering is lexicographic on the parsed 4-tuple `(year, month, day, patch)` with all four as integers. `2026.07.14` < `2026.07.14.1` < `2026.07.14.10` < `2026.07.15`.
- No leading zeros are trimmed for display (`2026.07.14`, not `2026.7.14`) so lexicographic-on-string matches lexicographic-on-integer.
- Pre-release / build metadata suffixes (`-rc.1`, `+build.42`) are **not** supported. Ship or don't. If an author needs an early build, publish with today's `.PATCH` (they'll fix forward tomorrow if needed).
- Version equality is exact — `2026.07.14 == 2026.07.14`, not `2026.07.14 == 2026.07.14.0`. The `.PATCH == 0` normalization is only for ordering, not equality (so an operator can pin exactly the unpatched release).

**Resolution rules** (§4.4):
- `sy pull <id>` (no version) → highest published version.
- `sy pull <id>@YYYY.MM.DD` → exact match on the un-patched release.
- `sy pull <id>@YYYY.MM.DD.PATCH` → exact match on the patched release.
- `sy pull <id>@YYYY.MM` → highest release in that month.
- `sy pull <id>@YYYY` → highest release in that year.
- `sy pull <id>@latest` → same as no version.
- **No range syntax.** Calendar versioning intentionally doesn't get `^` / `~` / `>=`; the human reads a date, and if they want "latest in the last week" they use `sy pull <id>` and check `sy pull changelog <id>` first.

### 4.2 The versioned package on disk (source-of-truth format)

Extend `.soulacy-agent.json` schema to v2:

```json
{
  "schema_version": "soulacy.agent.package/v2",
  "manifest": {
    "package_id": "soulacy/hackernews-digest",   // NEW: namespaced (see §4.5)
    "agent_id": "hackernews-digest",
    "version": "2026.07.14",                     // NEW: calendar version, required
    "prior_version": "2026.07.10.1",             // NEW: optional chain
    "released_at": "2026-07-14T09:00:00Z",       // NEW: RFC3339
    "changelog": "Fixed a bug where...",          // NEW
    "publisher": {                                // NEW: identity of the signer (§4.5)
      "id": "soulacy",
      "display_name": "Soulacy Core",
      "signature_key": "ed25519:AB12...",
      "trust_level": "official"
    },
    "requires": {                                 // NEW: hard requirements, gated at install
      "soulacy_version": ">=2026.06.01",         // date-form soulacy build cutoff
      "providers": [{"id": "ollama", "reason": "used by the summarizer node"}],
      "channels": [{"id": "telegram", "reason": "daily digest destination"}],
      "secrets": [
        {"key": "telegram.bot_token", "label": "Telegram bot token",
         "reason": "Required to deliver the daily digest.",
         "kind": "channel_secret", "provider": "telegram"}
      ],
      "mcp_servers": [],
      "peer_agents": []
    },
    /* … existing fields … */
  },
  "soul_yaml": "…",
  "files": [ /* … */ ],
  "integrity": { /* … existing sha256 + optional signature … */ }
}
```

Design rules:
- `version` is REQUIRED and MUST match the calendar-versioning regex in v2. v1 packages are still accepted (with a deprecation warning) until the cutoff in §5.1.
- `package_id` is NAMESPACED as `<publisher>/<package>` (decision #6, see §4.5). Publishers cannot publish under a namespace they don't own.
- `prior_version` forms a linked list so the changelog display can offer "since your last install" instead of the full history.
- `publisher.signature_key` is the ed25519 pubkey the signature MUST verify against.
- `publisher.trust_level` is `"official"` or `"community"` — see §4.5.
- `requires.soulacy_version` uses a comparison prefix (`>=` / `<=` / `>` / `<` / `==`) plus a calendar version. Only one comparator per constraint; no ranges.
- `requires.secrets` mirrors `internal/templates/templates.RequiredSecret` deliberately so the wizard code path is reusable.

### 4.3 The versioned package on the installed side

New sidecar file next to the imported agent (mirrors `.soulacy-install.json` for plugins):

```
<agent_dir>/<id>/
  SOUL.yaml
  .soulacy-package.json              # NEW — origin + version tracking
  <supporting-files>
.agent-history/<id>/                 # unchanged — per-save snapshots
.package-history/<id>/               # NEW — per-import snapshots
  2026.07.10.1_20260710T090000Z.package.json
  2026.07.10_20260710T081500Z.package.json
```

`.soulacy-package.json` schema:
```json
{
  "package_id": "soulacy/hackernews-digest",
  "installed_version": "2026.07.14",
  "installed_from": "https://raw.githubusercontent.com/soulacy/registry/main/packages/hackernews-digest/2026.07.14.package.json",
  "installed_at": "2026-07-14T15:23:00Z",
  "approved_by": "vasu@example.com",
  "approved_fingerprint": "sha256:...",
  "publisher": { /* copy of manifest.publisher */ },
  "signature_verified": true,
  "trust_level_at_install": "official"  // recorded so a later publisher-level change surfaces on next pull
}
```

`.package-history/<id>/` snapshots the full `.package.json` at each import so `sy pull rollback <id>` can pin to any prior version without re-fetching from the registry.

### 4.4 Version resolution rules

See §4.1 for the version syntax. Client-side resolution:

- The registry serves a per-package index at `https://raw.githubusercontent.com/soulacy/registry/main/packages/<id>/index.json`.
- Index shape: JSON array of `{version, released_at, package_url, changelog_url, deprecated?, publisher, trust_level}` records — ordered newest-first.
- Client caches locally at `~/.soulacy/registry/packages/<id>/index.json` with a 6-hour TTL by default (configurable via `packages.cache_ttl`).
- `sy pull refresh <id>` (new) forces a re-fetch of one; `sy pull refresh --all` re-fetches every cached index.
- Cache invalidation is TTL-only in 7B; a signed manifest-of-manifests with ETag support is a post-7C follow-up.

Git-URL sources (shape 1) resolve `HEAD` unless a `#tag`/`#branch`/`#sha` suffix is present (new in 7B): `sy pull https://github.com/x/y#2026.07.14`.

### 4.5 Third-party publishing — namespacing + signing + trust levels (decision #6)

**Namespacing:**
- `package_id` is required to be `<publisher>/<package>` where `<publisher>` matches `^[a-z0-9-]{2,32}$` and `<package>` matches `^[a-z0-9][a-z0-9-]{0,62}$`.
- The `soulacy/` namespace is reserved and can only be signed by keys the trust store marks as `"official"`.
- Non-official publishers use their own namespace (e.g. `vasu/`, `acme-corp/`). Registry-side enforcement: publisher key MUST match the `packages/<publisher>/keys.json` list at push time. This keeps supply-chain safe without a central identity provider.

**Publisher signing at push time:**
- New CLI: `sy package publish <path-to-package.json> --key <ed25519-private-key-file>`.
- The publish tool:
  1. Validates the manifest (schema + calendar version + namespace ownership).
  2. Computes `integrity.sha256` over the package with `integrity` zeroed.
  3. Signs the sha256 with the ed25519 private key; writes `integrity.signature`.
  4. POSTs to the registry (URL TBD in 7B — the registry is currently git-backed, so "publish" is a PR against `soulacy/registry` in 7B; a first-class registry service is post-7C).
- Signing is REQUIRED for third-party publishing. Signing is REQUIRED for `soulacy/` namespace. `--allow-unsigned` on `sy pull` accepts unsigned packages for local development; the GUI shows an "Unsigned — local dev only" chip when applied.

**Trust levels — `official` vs `community`:**
- `publisher.trust_level` on the manifest is set by the registry at index time based on the key's entry in `packages/publishers.json` (a registry-controlled file listing publisher IDs → keys → trust level).
- `"official"` = signed by a key listed under `soulacy/` in the registry's publishers file.
- `"community"` = signed by any other registry-approved publisher key.
- `"untrusted"` (implicit) = signature verifies but the key isn't in the registry's publishers file. `sy pull` requires `--allow-untrusted` and the GUI shows a red-badged "Untrusted publisher" warning.

**Config-driven trust augmentation (decision #2, see §4.6):**
- The operator's local `soulacy.yaml` can trust additional publisher keys not in the registry (private / internal publishers). Registry-declared official/community keys always apply; local additions only expand the trust set for that workspace.

### 4.6 Config-driven trust store (decision #2)

Under `soulacy.yaml`:

```yaml
packages:
  cache_ttl: "6h"                    # optional; default 6h
  allow_unsigned: false              # optional; default false. When true, sy pull accepts unsigned packages without --allow-unsigned.
  trusted_keys:
    # Registry keys always apply; entries here only ADD trust (they cannot
    # override a key the registry has revoked).
    "acme-corp":                     # publisher id
      display_name: "ACME Corp Automation Team"
      trust_level: "community"       # or "official" for internal-only "official" imports (rare)
      keys:
        - "ed25519:CD34..."          # one or more keys; hex or base64 accepted
        - "ed25519:EF56..."
    "vasu":
      display_name: "Vasu (personal)"
      trust_level: "community"
      keys:
        - "ed25519:AB12..."
```

Shape matches existing security config idioms in `soulacy.yaml` (see `credentials:` and `providers:` blocks — nested map under a top-level key, string sub-keys, mapstructure-friendly). Backing struct in `internal/config/config.go`:

```go
type PackagesConfig struct {
    CacheTTL       string                            `mapstructure:"cache_ttl"`
    AllowUnsigned  bool                              `mapstructure:"allow_unsigned"`
    TrustedKeys    map[string]TrustedPublisherConfig `mapstructure:"trusted_keys"`
}

type TrustedPublisherConfig struct {
    DisplayName string   `mapstructure:"display_name"`
    TrustLevel  string   `mapstructure:"trust_level"` // "official" | "community"
    Keys        []string `mapstructure:"keys"`
}
```

Runtime resolution order (highest wins):
1. Registry-revoked key → refuse regardless of local config.
2. Registry-declared key (official / community) → its trust level.
3. Local `packages.trusted_keys` key → its declared trust level.
4. Otherwise → `untrusted` (blocked unless `--allow-untrusted`).

### 4.7 Package-level permission fingerprint

```go
// PackageFingerprint hashes the material an operator explicitly approves when
// they install a specific version. Reordering fields is a no-op; adding a
// required secret, adding a privileged capability, or widening tool_choice
// changes the hash.
type PackageFingerprint struct {
    Providers       []string  // sorted
    Channels        []string  // sorted
    RequiredSecrets []string  // sorted; from manifest.requires.secrets[].key
    Capabilities    []string  // sorted; e.g. ["system"] when agent grants shell
    MCPServers      []string  // sorted
    PublisherID     string    // sorted after everything else so a publisher rebrand triggers re-approval
    TrustLevel      string    // "official" | "community" — moving from official → community requires re-approval
}
```

Re-import of same version → same fingerprint → no re-approval. Re-import of a NEW version whose fingerprint matches → no re-approval. New version with expanded fingerprint OR trust-level downgrade → re-approval modal.

### 4.8 Snapshot pruning (decision #5, shipped in 7C)

Retention policy for both `.agent-history/<id>/` and `.package-history/<id>/`:

- **Time-based:** keep the last 30 days of snapshots unconditionally.
- **Count-based cap:** keep at most 50 snapshots per agent/package, regardless of age. On the 51st snapshot, delete the oldest.
- **Pinned snapshots exempt:** operators can pin any snapshot to protect from pruning. Pinning is stored as a sidecar `<snapshot>.pinned` marker file. GUI has a Pin/Unpin button in the history modal.
- Pruning is triggered on write (each `Upsert`/`Delete`/import runs a prune pass on the affected agent's history dir); no background daemon.
- Both thresholds are configurable under `packages.snapshot_retention` in `soulacy.yaml`:
  ```yaml
  packages:
    snapshot_retention:
      max_age: "30d"
      max_count: 50
  ```
- Rollback restores from `.package-history/`; if the requested version has been pruned, `sy pull rollback <id> --to <v>` fetches the version from the registry (using the cached index) and re-imports. The command surfaces this fallback in its output.

---

## 5. Backwards compatibility & migration

### 5.1 v1 packages — concrete deprecation timeline (decision #4)

Two-phase deprecation with a hard cutoff:

**Phase 1 — Warn (7A ships, immediately):**
- `sy pull` accepts v1 packages but prints a WARN line: `warning: package uses deprecated schema "soulacy.agent.package/v1"; v1 packages will be refused after 2027-06-01 — ask the publisher to re-publish as v2.`
- GUI import modal shows a yellow banner with the same text and a link to `docs/packaging.md#v1-deprecation`.
- The gateway records a `package.v1_import_warning` event in the action log so ops teams can inventory who's still importing v1.
- Existing installed v1 packages continue to work indefinitely — this deprecation is about NEW imports.

**Phase 2 — Refuse (2027-06-01, exactly):**
- `sy pull` errors on v1: `error: package uses schema "soulacy.agent.package/v1" which is not supported after 2027-06-01. Ask the publisher to re-publish as v2 (see docs/packaging.md).`
- Gateway `POST /api/v1/agents/package/import` returns 400 with the same message.
- Escape hatch: `sy pull --allow-v1` bypasses the check for the local dev case. The GUI has no equivalent — the CLI is the only escape, and its help text warns "for local dev only, do not use in production".

**Cutoff rationale:** 2027-06-01 is ~10.5 months from the memo date (2026-07-14). Long enough for ecosystem publishers to migrate; short enough that we don't carry v1-import code past 12 months. Chosen as a DATE rather than a version because the ecosystem tracks calendar time better than Soulacy release numbers, per decision #1.

**Where the warning surfaces:**
- CLI: `sy pull`, `sy pull inspect` (new in 7B) — stderr, WARN prefix.
- GUI: import modal — yellow banner above the "Import" button; the button label changes to "Import (deprecated schema)".
- Docs: `docs/packaging.md` gets a dedicated `#v1-deprecation` anchor with migration steps.
- Action log: `package.v1_import_warning` event with `{package_id, source, imported_by, imported_at}`.

### 5.2 Existing `sy pull` calls continue to work

- Shape 1 (full URL) and shape 2 (owner/repo) — write to `<outputDir>/<id>.yaml`, unchanged.
- When the fetched file is a `.package.json` (v2 wrapper), the new codepath activates.
- Bare `SOUL.yaml` files fall through to the classic write-flat behavior with a WARN: `warning: fetched a bare SOUL.yaml (not a v2 package); no version, no changelog, no rollback will be tracked.`

### 5.3 Existing agent-history snapshots

Untouched. `.agent-history/` remains the per-save log; `.package-history/` is additive. GUI history modal gets a segmented control (Save history vs. Package history) in 7C.

---

## 6. New CLI surface (proposed)

| Command | Description | Bucket |
|---|---|---|
| `sy pull <id>` | Install the highest available version (unchanged default). | existed |
| `sy pull <id>@<v>` | Version-pin using calendar versioning (§4.1 resolution). | 7B |
| `sy pull inspect <id>[@<v>]` | Show manifest + requires + publisher + trust level without installing. | 7B |
| `sy pull rollback <id>` | Roll to the previously-installed version from `.package-history/`. | 7C |
| `sy pull rollback <id> --to <v>` | Roll to a specific prior version; fetches from registry if pruned. | 7C |
| `sy pull list` | List installed packages with version, publisher, trust level, origin. | 7B |
| `sy pull changelog <id>` | Print the changelog between installed version and highest available. | 7B |
| `sy pull refresh <id>` | Force re-fetch of the registry index for one package. | 7B |
| `sy pull refresh --all` | Force re-fetch all cached indexes. | 7B |
| `sy pull trust --list` | List trusted publishers (registry + local config combined). | 7B |
| `sy pull --allow-unsigned <src>` | Accept an unsigned package (dev only). | 7A |
| `sy pull --allow-untrusted <src>` | Accept a signed package whose publisher is neither registry-declared nor locally trusted. | 7C |
| `sy pull --allow-v1 <src>` | Accept a v1 package after the 2027-06-01 cutoff. | 7A (warn) → hard-gates in 7A |
| `sy package publish <path>.package.json --key <keyfile>` | Sign and prepare a package for registry submission (PR to `soulacy/registry` in 7B; direct push post-7C). | 7B (basic) → post-7C (registry push) |
| `sy package validate <path>.package.json` | Local dry-run of publish validation (schema + calendar version + namespace + signature). | 7A |

Every subcommand supports `--dir <path>` and `--json` for scripting. `--yes` for non-interactive CI (never required; default is prompt).

---

## 7. New GUI surface (proposed)

- **Agents page** — each row gains a small badge showing publisher + trust level (green for official, blue for community, red for untrusted, gray for unsigned).
- **Agent details** — new **Package** tab alongside History, showing:
  - Installed version + prior version (chain).
  - Origin URL, publisher, trust level, signature-verified flag.
  - Approved fingerprint (last 12 chars).
  - **Update available** callout when the registry has a newer version.
  - **Rollback** button (7C) — opens a picker over `.package-history/<id>/`.
- **Import package modal** (extends existing `showPackageImport` at `Agents.svelte:2414`):
  - **Publisher & trust** row at the top with publisher display name + trust badge.
  - **Version** row showing the pinned version + a `sy pull` command an operator can paste to reproduce.
  - **Required secrets** section (7A): each secret shows label + reason + vault status; missing secrets block Import.
  - **Requirements** section (7A): providers/channels/peers/skills status; missing status blocks Import unless `Acknowledge missing` is checked.
  - **Permission diff** section (7C): when re-importing an upgrade with a changed fingerprint, shows before/after providers/channels/capabilities + trust level; requires the operator to check "I understand the new permissions".
  - **Changelog** section (7B) pulled from the registry.
- **Config page** — new **Packages** section shows the parsed `packages.trusted_keys` for review, with copy-config buttons that render the exact YAML the operator would paste. Editing is deliberately CLI-only (avoid encoding public keys in a form field).

---

## 8. Implementation plan bucketed by scope

Three shippable buckets, self-contained. Each can ship on its own.

### Bucket 7A — Data model + install-time secret gate (S, ~2 sessions)

Additive-only, no CLI changes beyond `sy pull --allow-unsigned` and `sy package validate`. Ship:
- v2 manifest with required `Version` + `Requires` + `Publisher` blocks (calendar-versioning validation).
- `agentPackageManifest.PackageID` becomes required in v2 and MUST be namespaced (`<publisher>/<package>` per §4.5).
- `handleImportAgentPackage` writes `.soulacy-package.json` alongside `SOUL.yaml` on import.
- `handleInspectAgentPackage` populates `Requirements.status = "missing"` for absent secrets via `secrets.New(s.CredentialVault()).Catalog(...)`.
- `handleImportAgentPackage` REFUSES import when any `Requires` entry is missing unless request body carries `"acknowledge_missing": true`.
- GUI import modal displays the requirements list and disables Import until all `"missing"` entries are resolved OR the operator explicitly acks.
- v1 deprecation warning path per §5.1 phase 1.
- New `sy package validate <path>` command runs the schema + calendar version + namespace regex + signature (if present) check locally.
- `PackagesConfig` struct in `internal/config/config.go` with `CacheTTL`, `AllowUnsigned`, and empty `TrustedKeys` map — the last two are read but only USED by 7B/7C; scaffolded in 7A so config migrations don't churn.

Docs: new `docs/packaging.md` with the v1 deprecation section already populated.

**Explicit non-goals for 7A:** no version resolution, no rollback, no fingerprint, no trust store enforcement, no snapshot pruning.

### Bucket 7B — Version resolution + registry index + publisher signing (M, ~4 sessions)

Builds on 7A. Ship:
- `sy pull <id>@<v>` with calendar-versioning resolution.
- `sy pull inspect`, `sy pull list`, `sy pull changelog`, `sy pull refresh`, `sy pull refresh --all`, `sy pull trust --list`.
- Registry per-package index format at `packages/<id>/index.json`.
- Local cache under `~/.soulacy/registry/packages/<id>/index.json` with 6-hour TTL (configurable via `packages.cache_ttl`).
- `sy package publish <path> --key <keyfile>` — signs the package and outputs a ready-to-PR file for `soulacy/registry`. Post-7C: direct push to the registry service.
- Publisher key registry file `packages/publishers.json` in `soulacy/registry` — used by the client to resolve `trust_level` at pull time.
- GUI "Update available" callout on the new Package tab.

**Explicit non-goals for 7B:** no rollback, no permission fingerprint, no snapshot pruning.

### Bucket 7C — Rollback + fingerprint + trust chain + pruning (M, ~4 sessions)

Builds on 7B. Ship:
- `.package-history/<id>/<version>_<timestamp>.package.json` snapshot on every import.
- `sy pull rollback <id>` and `sy pull rollback <id> --to <v>`.
- `PackageFingerprint` computation on install; re-import compares against the operator's last-approved fingerprint; mismatch triggers a re-approval modal that shows the permission diff.
- Config-driven `packages.trusted_keys` enforcement (§4.6) — local additions expand trust; `sy pull --allow-untrusted` for the pass-through case.
- **Snapshot pruning** per §4.8: 30-day + 50-count retention on both `.agent-history/` and `.package-history/`; sidecar `.pinned` markers exempt; configurable via `packages.snapshot_retention` in config.
- GUI: Package tab gains a Rollback button; import modal renders the permission diff when applicable; History modal gains a Pin/Unpin button.
- Docs: `docs/troubleshooting/common-failures.md` gains a "Package trust and pinning" section.

---

## 9. Open questions — none remaining

All six §9 questions from rev 1 have been decided by the user on 2026-07-14. Implementation of Bucket 7A starts immediately, with 7B/7C on the same cohort's runway if the session has budget.

---

## 10. Files this design will touch (implementation checklist)

Cross-referenced to buckets 7A/B/C.

- `sdk/pkgregistry/pkgregistry.go` — add calendar-version parsing helper. (7A)
- `internal/gateway/agent_package.go` — v2 schema, `Requires` enforcement, `.soulacy-package.json` sidecar, `Publisher` block. (7A)
- `internal/plugininstall/meta.go` — reference for the fingerprint pattern (separate `PackageFingerprint` type). (7C)
- `internal/pkgregistry/` — v2 client speaking the new per-package index format. (7B)
- `internal/pkgregistry/publisher.go` (new) — publisher key registry client. (7B)
- `internal/config/config.go` — `PackagesConfig` struct with `TrustedKeys`, `CacheTTL`, `AllowUnsigned`, `SnapshotRetention`. (7A scaffold, 7C enforce)
- `cmd/sy/pull.go` — add `@<v>`, `inspect`, `rollback`, `list`, `changelog`, `refresh`, `trust`, `--allow-*` flags. (7B, 7C)
- `cmd/sy/package.go` — new file for `sy package publish|validate`. (7A validate, 7B publish)
- `internal/runtime/loader.go` — `.package-history/` snapshotting alongside `.agent-history/`; helpers `PackageVersions(id)`, `ReadPackageVersion(id, v)`, `RestorePackageVersion(id, v)`; snapshot pruning. (7C)
- `gui/src/pages/Agents.svelte` — new Package tab; import modal Requires + Diff sections; History Pin/Unpin. (7A, 7C)
- `gui/src/lib/api.js` — new `api.agents.package.*` bindings. (7A → 7C)
- `docs/packaging.md` — new; canonical reference. (7A)
- `docs/troubleshooting/common-failures.md` — "Package trust and pinning" section. (7C)

---

## 11. Explicitly NOT proposed

- **Semver dependency solving across packages.** Packages that depend on other packages will be flagged by the existing requirements report; there's no solver, no lockfile, no transitive resolution.
- **Automatic version updates.** No background poller, no "Update all". `sy pull refresh` is opt-in.
- **First-class registry service.** The design continues to piggyback on `raw.githubusercontent.com/soulacy/registry`. `sy package publish` produces a ready-to-PR file in 7B; direct-push registry service is post-7C.
- **Cross-workspace package sharing.** Each Soulacy workspace maintains its own installed-package state and trust store. No shared team registry mode.
- **Signature revocation via CRL.** Revocation is done by removing the key from `packages/publishers.json` in the registry — a client that has already installed the package keeps it (installed state doesn't re-verify signatures at runtime; only at pull time). A full CRL / OCSP-style revocation is post-7C.

---

## 12. Summary of the ask (rev 2)

- Six §9 questions are answered. Implementation starts immediately.
- **Bucket 7A** ships the smallest first cohort: v2 schema, install-time secret gate, `.soulacy-package.json` sidecar, `sy package validate`, v1 deprecation warning.
- **Bucket 7B** adds calendar-version resolution, per-package registry index, local cache, publisher signing, `sy pull` browse subcommands.
- **Bucket 7C** adds rollback, permission fingerprint, config-driven trust enforcement, snapshot pruning, and the GUI Rollback/Permission-diff/Pin surfaces.
- **After 7C** — Story 7's ACs are all met and Cohort E production go-live becomes the natural next scope.
