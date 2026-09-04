# Custom Distributions — Flavored Binaries (Story E12)

A *flavored binary* is a standard single static `soulacy` build with extra
drivers compiled in. No dynamic loading, no plugins directory — the same
security and deployment model as stock, just more drivers in the registries.

How it works: drivers self-register with the SDK factory registries from
`init()` (E10: channels/providers/queues/vectors; E15: reasoning strategies;
E16: plugin DB migrations). Compiling a driver in is therefore exactly one
blank import plus a module requirement — which is what the build tool
automates.

## The build tool

From the repository root:

```sh
go run ./scripts/soulacybuild \
    --with github.com/acme/soulacy-matrix@v1.2.0 \
    -o bin/soulacy-matrix
```

Steps performed:

1. `go get` each `--with` module at the requested version (`latest` when
   omitted) into `go.mod`/`go.sum`.
2. Write `cmd/soulacy/builtins_extra.go` — a generated blank-import file
   linking each module's `init()` registrations into the binary.
3. **Verification gates** (skip with `--skip-verify`): the E11 conformance
   kits (`TestConformance*` for providers and channel adapters, the sidecar
   protocol runner against the reference sidecars) plus
   `TestAllBuiltinsRegistered`, proving the registries are fully populated
   in *this* build.
4. `go build -trimpath -o <out> ./cmd/soulacy` — one static binary.

Repeat `--with` for multiple drivers. Without `--with` it is a plain
verified build.

## End-to-end example: a Matrix channel

Driver module (out-of-tree, owned by its author):

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

The author proves the contract out-of-tree with the E11 kit:

```go
func TestMatrixAdapterConforms(t *testing.T) {
    channeltest.RunAdapterSuite(t, func() channel.Adapter {
        a, _, _ := registry.NewChannel("matrix", map[string]any{
            "homeserver": "https://example.org", "access_token": "t", "agent_id": "a"})
        return a
    })
}
```

Build and configure:

```sh
go run ./scripts/soulacybuild --with github.com/acme/soulacy-matrix@v1.2.0 -o bin/soulacy-matrix
```

```yaml
# config.yaml — resolved through the same registry path as built-ins
channels:
  matrix:
    enabled: true
    homeserver: https://matrix.example.org
    access_token: "..."
    agent_id: assistant
plugins_config:        # optional driver settings (E17)
  matrix:
    sync_timeout: 30s
```

The host wires any unrecognised `channels.<key>` block through the factory
registry under that key (generic loop in `internal/app`), so a flavored
binary's `matrix:` block above just works — `enabled: true` plus whatever
keys the driver's factory documents. Keys without a registered factory warn
and skip; the gateway always boots.

## Notes

- `builtins_extra.go` is kept by default (`--keep=false` removes it after
  the build) — keep it committed in a distribution fork so rebuilds are
  reproducible.
- Built-in drivers live in `scripts/genbuiltins` (regenerate
  `builtins_gen.go` via `go generate ./...`); `soulacybuild` only manages
  the *extra* file, so the two never conflict.
- Promote the script to a `soulacy build` subcommand once the workflow
  settles (tracked under E13 polish).
