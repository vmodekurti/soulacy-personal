# Package Registries (Story E19)

Skill and plugin installs resolve through a **pluggable multi-registry
engine** instead of hardcoded endpoints. Operators declare registries in
`config.yaml`; provider construction goes through the SDK factory registry,
so flavored binaries (E12) can ship custom registry providers exactly like
custom channels or LLM providers.

## The contract

`sdk/pkgregistry` (stdlib-only, frozen per SDK major version):

```go
type Provider interface {
    ID() string
    Search(ctx context.Context, query string) ([]Package, error)
    Resolve(ctx context.Context, slug string) (Package, error)  // ErrNotFound → fall through
    Fetch(ctx context.Context, pkg Package, dstDir string) error
}
```

`Package` carries `{slug, version, checksum, signature?, manifest, source,
description, provider}`. `Checksum` (sha256 hex) is REQUIRED for archive
downloads — the HTTP provider refuses unverifiable archives. Git sources
carry no checksum; integrity comes from the clone.

## Configuration

```yaml
registries:
  - id: main
    type: http
    base_url: https://registry.example.com
    priority: 10
    auth_headers:
      Authorization: "Bearer sk_..."
  - id: github
    type: git
    priority: 100
```

Resolution order is ascending `priority` (lower first; ties keep config
order). `Resolve` returns the first hit; `ErrNotFound` AND transport errors
fall through to the next registry, so one broken registry never blocks
installs from the others. `Search` aggregates every registry and dedupes by
slug, keeping the highest-priority result.

## Built-in providers

**http** — an E19 registry speaking:

| Endpoint | Returns |
|---|---|
| `GET {base}/v1/search?q={query}` | `{"packages": [Package…]}` |
| `GET {base}/v1/packages/{slug}` | `Package` with checksum + source (404 = unknown) |
| `GET {pkg.source}` | the archive (tar.gz/zip), sha256-verified, ≤256 MiB |

`auth_headers` are sent verbatim on every request. Extraction goes through
the same hardened path as plugin installs (traversal + decompression-bomb
guards, shared with E13 via `plugininstall.VerifyAndExtract`).

**git** — resolves *addressed* sources: `github.com/user/my-skill`, full
`https://…` URLs, or `git@…` remotes. Plain slugs report `ErrNotFound` so
the engine falls through to real registries. Fetch is the shared shallow
clone (`plugininstall.GitClone`, `.git` stripped, 120 s timeout). Search
always returns nothing — git hosts are not an index.

## Package signatures (ed25519)

Registry operators sign the **raw 32 bytes of each archive's sha256
digest** with an ed25519 private key; `Package.Signature` carries the
signature base64-encoded. Operators publish the 32-byte public key
hex-encoded; consumers pin it per registry:

```yaml
registries:
  - id: main
    type: http
    base_url: https://registry.example.com
    signing_key: "3b6a27bc…"   # hex ed25519 public key
```

With `signing_key` set, EVERY package from that registry must carry a
valid signature — unsigned or tampered packages are refused at fetch,
before extraction. Without it, integrity rests on the sha256 checksum
alone and the CLI marks signatures as UNVERIFIED. Malformed keys fail at
boot, not at first fetch. The reference server (`soulacy registry serve
--signing-key-file`) produces compatible signatures.

## Custom providers

```go
func init() {
    registry.MustRegisterPkgRegistry("s3", func(cfg map[string]any) (pkgregistry.Provider, error) {
        return newS3Provider(cfg)
    })
}
```

Add the package to the flavored-binary build (`soulacybuild --with …`, E12)
and select it with `type: s3` in a `registries:` entry. Unknown types are
warned and skipped at boot — a typo never bricks the gateway.

## Reference server

`internal/registryserver` is the reference implementation of the protocol,
shipped as a subcommand:

```bash
soulacy registry keygen --out ~/.soulacy/registry-signing.key   # prints the public key
soulacy registry serve --dir ./packages --addr 127.0.0.1:18790 \
    --signing-key-file ~/.soulacy/registry-signing.key
```

Layout: a flat directory of `<slug>-<version>.tar.gz` archives (also
.tgz/.zip); optional `<slug>.yaml` sidecars add `description:` metadata.
The newest version per slug (numeric dotted compare) is what Resolve
returns; checksums are computed at index time and every package is signed
when a key is configured. Archive serving is traversal-guarded (only
indexed basenames are reachable). End-to-end conformance with the E19
client — including signature verification — is pinned by
`internal/registryserver/registryserver_test.go`.

## Consumers

- `sy skill install <slug>` (Story E18) — registry resolve → safety audit
  (E20) → consent → verified extract → hot-load.
- The GUI install flow (E13) — uses the same engine for slug-based installs.

## Skill directories (type: skillssh — Story E26)

`skillssh` registries speak the skills.sh directory API
(https://skills.sh/docs/api): `GET /api/v1/skills/search?q=`,
`GET /api/v1/skills/{owner}/{repo}/{skill}` (full file tree inline),
`GET /api/v1/skills/audit/{id}` (partner security audits — surfaced in
the install consent prompt; Soulacy's own E20 introspection still runs on
every install). Slugs are skills.sh ids: `owner/repo/skill`.

```yaml
registries:
  - id: skills.sh
    type: skillssh
    base_url: https://skills.sh
    priority: 50
```

```bash
sy skill install vercel-labs/skills/find-skills
```

## Reviewing new sources (Story E26)

Point the framework at ANY url and it identifies what it is and guides
you to add it:

```bash
sy registry probe https://www.skills.sh/     # review only
sy registry add   https://www.skills.sh/     # review + consent + save
sy registry list
```

Detection: known git hosts → `git` entry; skills.sh API shape →
`skillssh`; E19 `/v1/search` shape → `http`; anything else reports the
GitHub repos the page links (installable directly via
`sy skill install github.com/owner/repo`). The GUI equivalent lives on
the Skills page (➕ Skill sources → paste URL → Review → Add); gateway
endpoints: `GET/POST /api/v1/registries`, `POST /api/v1/registries/probe`
(config RBAC). Saving appends to the `registries:` block —
`sy skill install` picks it up immediately, GUI installs after a restart.
