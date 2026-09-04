# Plugin Manifest Reference

Status: schema 2 (Story E7) · Types: `pkg/plugin` · Loader/wiring:
`internal/plugins`

A plugin is a directory containing `plugin.yaml`. Schema versions:

| `manifest_schema` | Meaning |
|---|---|
| absent / 0 / 1 | Legacy v1: Python tools only; `channels:`/`providers:` are informational string lists. |
| 2 | Full contribution model below. |
| >2 | Unknown future grammar — the plugin is skipped with a warning (never guessed at). |

A v1 manifest that declares v2-only contributions still loads (its tools keep
working); the v2-only parts are skipped with a warning. A v2 manifest with a
malformed contribution is refused with a precise error.

## Full v2 example

```yaml
id: matrix-suite
name: Matrix Suite
version: 1.0.0
manifest_schema: 2

channels:                       # sidecar channels (External Channel Protocol)
  - id: matrix
    agent_id: assistant         # required: agent that receives messages
    sidecar:
      command: node             # required
      args: ["sidecar/matrix.mjs"]

providers:                      # OpenAI-compatible inference endpoints
  - id: local-vllm
    openai_compatible:
      base_url: http://localhost:8000/v1   # required
      api_key_env: VLLM_KEY     # host env var holding the key (optional)
      model: llama-3.3-70b      # default model (optional)

tools:                          # Python tool libraries (unchanged from v1)
  - rooms

skills:                         # agent skill directories (relative to root)
  - skills/moderation

gui:                            # static UI mount (served in E8)
  nav: { label: "Matrix", icon: "💬" }
  static: ui                    # directory must exist

permissions:                    # capabilities — see docs/PLUGIN_CAPABILITIES.md
  - cap: channel.send
    channels: [matrix]

credentials:                    # vault delegation — see docs/PLUGIN_CREDENTIALS.md
  - key: MATRIX_TOKEN
    from: matrix-suite/token
```

## What happens at boot

1. The loader scans `plugin_dirs`, parses and validates each manifest
   (schema gate → v2 contribution checks → capability set → credential
   refs). Any failure refuses that plugin and logs why; other plugins are
   unaffected.
2. Plugin `skills:` directories join the skill loader's search path.
3. `plugins.Wire` registers contributions with the host:
   - each sidecar channel becomes a **supervised** external adapter
     (crash/backoff restarts, sandbox rlimits, per-spawn credential env,
     rotation watch → restart) in the channel registry, started with all
     other channels;
   - each provider is wrapped by the existing OpenAI-compatible
     `llm.Provider` and registered with the router under its `id`;
   - the capability set registers with the enforcer (`internal/caps`).
4. GUI mounts (E8): static assets are served at `/plugins/<id>/ui/` and the
   Svelte shell shows a nav entry per mount, rendering the UI in a sandboxed
   iframe (`allow-scripts allow-forms` — no same-origin). The shell fetches
   a scoped plugin token (`POST /api/v1/plugins/:id/token`) and passes it in
   the iframe URL fragment; the plugin uses it as a bearer token. At the API
   the token authenticates as `plugin:<id>` and is default-denied outside
   the capability route table (docs/PLUGIN_CAPABILITIES.md) — it is never
   the user's API key.

`pkg/plugin.Registry` is the contract behind step 3 — `internal/plugins`
implements it against the live registries, and Go-native plugins get the
same interface via the SDK work (E9+).

## Compatibility rules

- Manifest fields are append-only; schema bumps only for breaking grammar
  changes.
- v1 manifests must keep loading forever (tools-only).
- Capability and credential grammars are versioned with the manifest; see
  their own docs for semantics.
