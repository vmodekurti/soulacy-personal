# Custom Distributions

A flavored binary is a standard single static `soulacy` build with extra drivers compiled in — the same security and deployment model as stock, verified by conformance gates before it ships.

## Quick start

From the repository root:

```bash
soulacy build --with github.com/acme/soulacy-matrix@v1.2.0 -o bin/soulacy-matrix
```

(Equivalent: `go run ./scripts/soulacybuild --with … -o …` if you are
working from source without a built `soulacy`.)

The result is one static binary containing the extra driver, with all
registries proven populated before the build is accepted.

## How it works

Drivers self-register with the SDK factory registries from `init()` —
channels, LLM providers, queues, vectors, reasoning strategies, plugin DB
migrations, and package-registry providers all use the same mechanism.
Compiling a driver in is therefore exactly one blank import plus a module
requirement, which is what the build tool automates:

1. `go get` each `--with` module at the requested version (`latest` when
   the `@version` suffix is omitted) into `go.mod`/`go.sum`.
2. Write `cmd/soulacy/builtins_extra.go` — a generated blank-import file
   linking each module's `init()` registrations into the binary.
3. **Verification gates**: the conformance kits (`TestConformance*` for
   providers and channel adapters, the sidecar protocol runner against the
   reference sidecars) plus `TestAllBuiltinsRegistered`, proving the
   registries are fully populated in *this* build.
4. `go build -trimpath -o <out> ./cmd/soulacy` — one static binary.

Flags:

| Flag | Effect |
|---|---|
| `--with module[@version]` | extra driver module to compile in (repeatable) |
| `-o <path>` | output binary path (default `bin/soulacy`) |
| `--skip-verify` | skip the conformance/registry test gates |
| `--keep` | keep `builtins_extra.go` after the build (default true — required for rebuilds) |

Without `--with` it is a plain verified build.

!!! warning "Think twice before `--skip-verify`"
    The gates exist so a flavored binary cannot ship with a driver that
    half-implements its contract. Skipping them is for fast local
    iteration only — never for a binary you hand to others. In a
    distribution fork, keep the generated `builtins_extra.go` committed so
    rebuilds are reproducible.

## Writing a driver: register with the SDK factories

A driver is an ordinary Go module owned by its author. It registers a
factory in `init()`:

```go
// github.com/acme/soulacy-matrix
package matrix

import (
    "github.com/soulacy/soulacy/sdk/channel"
    "github.com/soulacy/soulacy/sdk/registry"
)

func init() {
    registry.MustRegisterChannel("matrix", func(cfg map[string]any) (channel.Adapter, error) {
        return newAdapter(cfg) // reads homeserver, access_token, agent_id …
    })
}
```

The same pattern covers every extension point — for example a custom
package-registry provider:

```go
func init() {
    registry.MustRegisterPkgRegistry("s3", func(cfg map[string]any) (pkgregistry.Provider, error) {
        return newS3Provider(cfg)
    })
}
```

…selected with `type: s3` in a `registries:` entry. Plugin DB schema uses
`storage.MustRegisterMigration` from `sdk/storage`.

Authors prove the contract out-of-tree with the conformance kit:

```go
func TestMatrixAdapterConforms(t *testing.T) {
    channeltest.RunAdapterSuite(t, func() channel.Adapter {
        a, _, _ := registry.NewChannel("matrix", map[string]any{
            "homeserver": "https://example.org", "access_token": "t", "agent_id": "a"})
        return a
    })
}
```

## The Matrix walkthrough, end to end

1. **Author** publishes `github.com/acme/soulacy-matrix` with the
   `init()` registration above and a passing conformance test.
2. **Operator** builds the flavor:

    ```bash
    soulacy build --with github.com/acme/soulacy-matrix@v1.2.0 -o bin/soulacy-matrix
    ```

3. **Configure** — unrecognised `channels.<key>` blocks are wired through
   the factory registry under that key, so the new driver configures
   exactly like a built-in:

    ```yaml
    # config.yaml
    channels:
      matrix:
        enabled: true
        homeserver: https://matrix.example.org
        access_token: "..."
        agent_id: assistant
    plugins_config:        # optional driver settings
      matrix:
        sync_timeout: 30s
    ```

4. **Run** `bin/soulacy-matrix`. Keys without a registered factory warn
   and skip — the gateway always boots, even with a typo'd block.

## Flavored binary vs. plugin — which one?

| | Plugin (installed at runtime) | Flavored binary |
|---|---|---|
| Language | any (sidecar protocol) / Python tools | Go |
| Distribution | directory, archive, git, registry | one static binary |
| Trust model | default-deny principal, install approval | compiled in — full trust, reviewed at build time |
| Best for | third-party additions on a running host | curated distributions, native performance, custom storage/registry backends |

Spec deep-dive: [Custom Distributions](../CUSTOM_DISTRIBUTIONS.md).
