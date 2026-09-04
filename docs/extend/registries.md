# Package Registries

Skill and plugin installs resolve through a pluggable multi-registry engine you control entirely from `config.yaml` — including running your own registry with one subcommand.

## Quick start

```yaml
# config.yaml
registries:
  - id: main
    type: http
    base_url: https://registry.example.com
    priority: 10
    auth_headers:
      Authorization: "Bearer sk_..."
    signing_key: "3b6a27bc…"   # hex ed25519 public key
  - id: skills.sh
    type: skillssh
    base_url: https://skills.sh
    priority: 50
  - id: github
    type: git
    priority: 100
```

```bash
sy skill install some-skill                        # resolved via main → skills.sh → git
sy skill install vercel-labs/skills/find-skills    # skills.sh id
sy skill install github.com/user/my-skill          # addressed git source
```

With no `registries:` block at all, a bare git provider still resolves
addressed sources, so git installs work out of the box.

## Resolution: priority and fallback

Registries are queried in ascending `priority` (lower first; ties keep
config order). Resolve returns the first hit; *not found* **and** transport
errors both fall through to the next registry, so one broken registry never
blocks installs from the others. Search aggregates every registry and
dedupes by slug, keeping the highest-priority result.

Unknown `type:` values are warned and skipped at boot — a typo never bricks
the gateway.

## Built-in registry types

### `http` — Soulacy package registry

Speaks the registry protocol:

| Endpoint | Returns |
|---|---|
| `GET {base}/v1/search?q={query}` | `{"packages": […]}` |
| `GET {base}/v1/packages/{slug}` | package metadata with checksum + source (404 = unknown) |
| `GET {pkg.source}` | the archive (tar.gz/zip), sha256-verified, ≤ 256 MiB |

`auth_headers` are sent verbatim on every request — use them for bearer
tokens against private registries. A sha256 checksum is **required** for
every archive; unverifiable archives are refused. Extraction goes through
the same hardened path as plugin installs (path-traversal and
decompression-bomb guards).

### `git` — git hosts

Resolves *addressed* sources only: `github.com/user/my-skill`, full
`https://…` URLs, or `git@…` remotes. Plain slugs fall through to real
registries. Fetch is a shallow clone with `.git` stripped (120 s timeout);
integrity comes from the clone, so there is no checksum. Search returns
nothing — git hosts are not an index.

### `skillssh` — skill directories

Speaks the skills.sh directory API: search, full file trees inline, and
partner security audits (`GET /api/v1/skills/audit/{id}`) that are surfaced
in the install consent prompt. Slugs are skills.sh ids
(`owner/repo/skill`). Soulacy's own
[safety introspection](safety.md) still runs on every install.

See [Skill Sources](skill-sources.md) for the guided
`sy registry probe/add` flow to add any of these from a URL.

## Package signatures (ed25519)

Registry operators sign the **raw 32 bytes of each archive's sha256
digest** with an ed25519 private key; packages carry the signature
base64-encoded. Consumers pin the operator's 32-byte public key
(hex-encoded) per registry:

```yaml
registries:
  - id: main
    type: http
    base_url: https://registry.example.com
    signing_key: "3b6a27bc…"
```

!!! warning "Pin `signing_key` on every http registry you can"
    With `signing_key` set, **every** package from that registry must carry
    a valid signature — unsigned or tampered packages are refused at fetch,
    before extraction. Without it, integrity rests on the sha256 checksum
    alone and the CLI marks signatures as `UNVERIFIED` in the install
    output. Malformed keys fail at boot, not at first fetch.

## Running your own registry

The reference server ships as a `soulacy` subcommand and needs no gateway
config:

```bash
soulacy registry keygen --out ~/.soulacy/registry-signing.key   # prints the public key
soulacy registry serve --dir ./packages --addr 127.0.0.1:18790 \
    --signing-key-file ~/.soulacy/registry-signing.key
```

Layout is a flat directory of archives:

```text
packages/
  my-skill-1.0.0.tar.gz      # <slug>-<version>.tar.gz (also .tgz / .zip)
  my-skill-1.1.0.tar.gz
  my-skill.yaml              # optional sidecar: description: metadata
```

- The newest version per slug (numeric dotted compare) is what resolve
  returns.
- Checksums are computed at index time; every package is signed when a key
  is configured.
- Archive serving is traversal-guarded — only indexed basenames are
  reachable.

Hand the printed public key to consumers as the `signing_key` for their
registry entry, then point them at it:

```yaml
registries:
  - id: team
    type: http
    base_url: http://127.0.0.1:18790
    priority: 10
    signing_key: "<public key from keygen>"
```

## Custom registry providers

Flavored binaries can ship additional registry types (S3, IPFS, your
artifact store) by registering a factory with the SDK and selecting it via
`type:` in a `registries:` entry — see
[Custom Distributions](custom-distributions.md). The full provider
contract lives in [the registry spec](../PACKAGE_REGISTRIES.md).
