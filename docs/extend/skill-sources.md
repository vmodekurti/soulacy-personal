# Skill Sources

Point Soulacy at any URL — a skill directory like skills.sh, a Soulacy registry, or a git host — and it identifies what it is, shows you what it found, and adds it as an install source only after you consent.

## Quick start

```bash
sy registry probe https://www.skills.sh/    # review only — changes nothing
sy registry add   https://www.skills.sh/    # review + consent + save to config.yaml
sy registry list                            # configured sources
```

After adding skills.sh, install directly by skills.sh id
(`owner/repo/skill`):

```bash
sy skill install vercel-labs/skills/find-skills
```

## Reviewing a URL: `sy registry probe`

Probing runs entirely client-side (no gateway needed) and never modifies
your config. The report shows:

```text
URL:    https://www.skills.sh/
Kind:   skillssh
Review: …
Audits: third-party security audits available; Soulacy's own introspection still runs on every install
Found:
  - …sample skills/repos discovered…
Suggested config entry: id=skills.sh type=skillssh base_url=https://www.skills.sh priority=50
```

Pass the CLI's global `--json` flag to get the raw probe report instead.

### What detection recognises

| Kind | Detected as | What it means |
|---|---|---|
| `skillssh` | skills.sh-compatible directory API | A skill directory; slugs are skills.sh ids `owner/repo/skill` |
| `http` | Soulacy package registry (`/v1/search` shape) | A registry serving versioned, checksummed packages |
| `git` | known git host | Addressed installs: `sy skill install github.com/owner/repo` |
| `unknown` | plain web page | Not a registry — but the report lists any GitHub repos the page links, each installable directly as a git source |

When a source publishes partner security audits (skills.sh does), the probe
report flags it, and those audits are surfaced again in the install consent
prompt.

!!! warning "External audits never replace local checks"
    Third-party audits are extra signal, not a bypass. Soulacy's own
    [safety introspection pipeline](safety.md) still runs on **every**
    install, regardless of source.

## Adding a source: `sy registry add`

```bash
sy registry add https://www.skills.sh/
```

This probes, prints the same report, then asks:

```text
Add source "skills.sh" (type skillssh, https://www.skills.sh) to config.yaml? [y/N]
```

Flags:

| Flag | Effect |
|---|---|
| `--id <id>` | override the suggested source id |
| `--priority <n>` | resolution priority (lower runs first) |
| `--yes`, `-y` | skip the confirmation prompt |

Saving goes through the gateway API when it is reachable (so the GUI sees
the change immediately); when the gateway is down, the entry is appended
directly to `config.yaml`. Duplicate ids are refused either way.

`sy skill install` picks up new sources immediately; GUI installs use the
new source after a gateway restart.

## End-to-end example: skills.sh

```bash
# 1. Review and add the source
sy registry add https://www.skills.sh/

# 2. Install a skill by its skills.sh id (owner/repo/skill)
sy skill install vercel-labs/skills/find-skills
```

The install resolves through the skills.sh directory API, fetches the
skill's file tree, runs the local safety pipeline, shows any partner
security audit alongside Soulacy's own report at the consent prompt, and
hot-loads the skill on approval.

## Adding sources from the GUI

On the **Skills** page:

1. Click **➕ Skill sources**.
2. The modal lists your configured sources (id, type, URL, and a 🔑 marker
   when auth headers are configured).
3. Paste a URL and click **🔍 Review** — the probe report renders inline:
   the detected kind, a review summary, sample skills/repos, and whether the
   source publishes third-party security audits.
4. If the URL is a recognisable registry, click **➕ Add "…" as a source**.

The GUI uses the gateway endpoints `GET/POST /api/v1/registries` and
`POST /api/v1/registries/probe`; all of them require config-level RBAC.

## What gets written

Adding a source appends one entry to the `registries:` block in
`config.yaml`:

```yaml
registries:
  - id: skills.sh
    type: skillssh
    base_url: https://www.skills.sh
    priority: 50
```

Sources are queried in ascending `priority` order with fallback — the first
registry that resolves a slug wins. For the full config grammar (auth
headers, ed25519 signing keys, running your own registry) see
[Package Registries](registries.md).

!!! warning "Adding a source is a trust decision"
    Anyone who controls a configured source can offer packages under any
    slug it serves. Keep priorities such that your most-trusted registries
    resolve first, pin `signing_key` on `http` registries you operate, and
    rely on the per-install security report — it runs no matter where a
    package came from.
