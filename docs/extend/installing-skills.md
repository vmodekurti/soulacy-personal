# Installing Skills

Install Agent Skills from a local directory, a registry slug, or a git source — every remote install is checksummed, security-scanned, and consented before it touches your agents.

## Quick start

For a repository URL, you can use the unified installer and let Soulacy detect
the package type:

```bash
sy package install https://github.com/user/my-skill --allow-unverified
```

The System agent uses the same path when asked to install a Skill from a URL.
It presents Approve/Deny, runs the existing safety pipeline, hot-loads the
Skill, and skips it when it is already installed.

This does not require unrestricted system tools. Soulacy keeps `shell_exec`,
script execution, and arbitrary file mutation disabled when
`runtime.allow_system_agents` is empty while still exposing this narrowly
scoped, approval-gated installer to the built-in System agent.

```bash
# Local directory (copied into ~/.soulacy/skills/)
sy skill install ./my-skill

# Registry slug (resolved through your configured registries)
sy skill install self-improving-agent

# Git source (works out of the box, no registry config needed)
sy skill install github.com/user/my-skill
```

A skill package is a directory with a `SKILL.md` at its root. After install,
list and inspect skills with:

```bash
sy skill list
sy skill get <name>
```

## Local directory installs

`sy skill install ./my-skill` validates that the directory contains a
`SKILL.md` and copies it into `~/.soulacy/skills/<name>/`. Nothing else
happens — placing files on your own disk is your call. You can equally just
copy a skill directory into `~/.soulacy/skills/` by hand.

## Remote installs: the full flow

When the argument is *not* a local directory, it is treated as a package slug
and resolved through the `registries:` block in `config.yaml` (see
[Skill Sources](skill-sources.md) and [Package Registries](registries.md)).
With no registries configured, a bare git provider is used as fallback, so
`sy skill install github.com/user/my-skill` always works.

Every remote install walks the same pipeline:

1. **Resolve** — registries are queried in priority order with fallback. The
   CLI prints what it found: `Resolved <slug>@<version> via registry "<id>"`,
   the description, the archive sha256, and the signature provenance:

    - `signature: ed25519, verified against registry "<id>"'s signing_key during fetch`
    - `signature: present but UNVERIFIED — set signing_key on registry "<id>" to enforce verification`
    - `signature: none (unsigned package)`

2. **Staged fetch** — the package downloads into a temporary
   `.staging-…` directory under your skills root. Archives are
   sha256-verified before extraction; git sources derive integrity from the
   clone. A package without `SKILL.md` at its root is rejected here. The
   staging directory never survives a failed or aborted install.

3. **Security report** — the [safety introspection pipeline](safety.md) runs
   over the staged files and prints its verdict and findings:

    ```text
    Safety introspection: ✓ passed safety checks
    ```

    or, for example:

    ```text
    Safety introspection: ✗ DANGER — critical findings
      CRITICAL (static) [tool.py:3]: dangerous call: eval() …
    ```

4. **Consent prompt** — the CLI summarises the package (skill heading, tool
   libraries, declared schema migrations, requested capabilities, requested
   credentials) and asks:

    ```text
    Install <slug>@<version>? [y/N]
    ```

5. **Activate & hot-load** — on consent the staging directory moves to
   `~/.soulacy/skills/<name>` and the CLI calls the gateway's
   `POST /api/v1/skills/rescan` so the skill is live immediately. If the
   gateway is unreachable, the skill loads on the next gateway restart.

If a skill with the same name is already installed, the install aborts with
a message telling you to remove the existing directory first.

## Non-interactive installs: `--yes`

```bash
sy skill install github.com/user/my-skill --yes
```

`--yes` (or `-y`) skips the consent prompt for `pass` and `caution` verdicts.

!!! warning "`--yes` never bypasses a danger verdict"
    When the safety report's verdict is **danger**, the CLI always demands an
    interactive confirmation:

    ```text
    ⚠ CRITICAL findings — explicit confirmation required (--yes does not apply).
    ```

    In a non-interactive context (CI, scripts) a danger verdict therefore
    always aborts the install. This is by design — review the findings
    yourself before installing anything flagged critical.

## Unverified installs: `--allow-unverified`

A remote package is **verified** only when it carries an ed25519 signature
*and* the registry that served it has a `signing_key` configured, so the CLI
can check the signature against it during fetch. Anything else cannot be
authenticated:

- an **unsigned** package,
- a registry with **no `signing_key`**, or
- a **raw git source** (`github.com/user/repo`).

By default, such installs are **blocked**:

```text
refusing to install "my-skill": its authenticity cannot be verified
(registry "git" has no signing_key, or the package is unsigned / a raw git
source). An attacker who controls the source or the network could ship
malicious code. Re-run with --allow-unverified to install anyway, or
configure a signing_key on the registry to enforce verification
```

To accept the risk and install anyway, pass `--allow-unverified`:

```bash
sy skill install github.com/user/my-skill --allow-unverified
```

The CLI then prints a loud warning and proceeds:

```text
⚠ WARNING: installing UNVERIFIED package my-skill@HEAD — authenticity could
not be verified (--allow-unverified).
```

`--allow-unverified` only relaxes the authenticity gate. The safety
introspection and the danger-verdict rule (above) still apply.

### Setting up signing to avoid the friction

To make installs verify automatically, configure a registry with the
publisher's ed25519 **public** key as its `signing_key` (hex-encoded) in
`config.yaml`:

```yaml
registries:
  - id: my-registry
    type: http
    base_url: https://registry.example.com
    priority: 10
    signing_key: "3b6a27bc..."   # hex ed25519 public key
```

With a `signing_key` set, the registry refuses unsigned or tampered packages
during fetch, and signed packages install with no `--allow-unverified` needed.
Publishers sign the archive's sha256 checksum with the matching private key.
See [Package Registries](registries.md) for the full registry configuration.

## Installing from the GUI

The **Skills** page in the web GUI offers:

- **⚡ From AgenticSkills** — paste an `agenticskills.io` skill URL; the
  `SKILL.md` is downloaded and hot-loaded with no restart.
- **➕ Skill sources** — review and add registries (skill directories like
  skills.sh, Soulacy registries, git hosts) so slugs resolve for installs.
  See [Skill Sources](skill-sources.md).

Slug-based GUI installs go through the same registry engine and the same
safety pipeline as the CLI — the security report and permission grants are
shown in the approval dialog before anything activates.

## Where skills live

| Path | Purpose |
|---|---|
| `~/.soulacy/skills/<name>/` | install root used by `sy skill install` |
| `SKILL.md` | required at the package root — frontmatter + instructions |

Skills are exposed to agents as an `available_skills` catalog; agents call
`read_skill` to load the full instructions on demand. See
[Skills](../agents/skills.md) for enabling skills per agent.

## Troubleshooting

- **`"<arg>" is not a local directory and no configured registry resolves it`**
  — the slug was not found by any registry. Check `sy registry list`, or use
  an addressed git source (`github.com/user/repo`).
- **`package "<slug>" has no SKILL.md at its root`** — the source is not a
  skill package. Plugins install through the Plugins GUI page instead; see
  [Plugins](plugins.md).
- **`refusing to install … its authenticity cannot be verified`** — the
  package is unsigned, the registry has no `signing_key`, or it's a raw git
  source. Configure a `signing_key` on the registry to verify automatically,
  or re-run with `--allow-unverified` to accept the risk. See
  [Unverified installs](#unverified-installs-allow-unverified).
- **`Gateway rescan failed … the skill loads on the next gateway restart`** —
  the install succeeded; only the hot-load was skipped because the gateway
  was unreachable.
