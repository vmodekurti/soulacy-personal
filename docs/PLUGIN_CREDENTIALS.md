# Plugin Credential Delegation

Status: v1 (Story E6) · Code: `internal/plugins/delegation.go`,
`internal/channels/external` · Manifest types: `pkg/plugin.CredentialRef`

Plugin sidecars never see the gateway's environment or the vault. They
declare the secrets they need in `plugin.yaml`, and the host injects **only
those** at spawn.

## Declaring credentials

```yaml
# plugin.yaml
id: matrix-suite
credentials:
  - key: MATRIX_TOKEN        # env var name inside the sidecar
    from: matrix-suite/token # vault path: <namespace>/<secret-key>
```

Rules (enforced at plugin load; violations refuse the plugin):

- `key` must be an uppercase env-var name (`[A-Z_][A-Z0-9_]*`), unique per
  plugin.
- `from` is `<namespace>/<key>` and the namespace **must equal the plugin's
  own ID**. Cross-namespace references — another plugin's or an agent's
  secrets — are structurally impossible, not just denied.

## Storage namespace

Plugin secrets live in the existing encrypted vault (`internal/credentials`,
AES-256-GCM) under the vault namespace `plugin:<id>` — disjoint from agent
credentials by construction. Operators set them through the normal
credentials API with `agent_id = "plugin:<id>"`.

## Spawn-time injection

`plugins.Delegator.Env(ctx, pluginID, refs)` builds the sidecar's **complete**
environment:

- a minimal whitelisted base (`PATH`, `HOME`, `TMPDIR`, `LANG`, `TZ`, … —
  see `baseEnvAllowlist`), so the gateway's own env (API keys etc.) is
  never inherited;
- plus exactly the declared secrets.

A declared-but-missing secret fails the spawn (retried through the
supervisor's backoff loop) rather than silently starting a sidecar that
can't authenticate. The supervisor (`external.SupervisorConfig.Env`) runs the
resolver **on every spawn**, so each restart picks up current values.

## Rotation → restart

The vault versions secrets (`SQLiteVault.Rotate`). `plugins.WatchCredentials`
polls a SHA-256 fingerprint of the declared secrets (values are hashed,
never retained or logged) and invokes a callback on any change — rotation,
replacement, addition, or removal. The callback wires to
`Supervisor.Restart(reason)`, which stops the running sidecar and lets the
supervision loop respawn it with freshly resolved credentials.

## Secret hygiene

- Secrets are never written to disk by delegation code; the only persistent
  copy is the encrypted vault row.
- Delegation code never logs values; errors name the vault path, not the
  content. Audit/zap output carries counts and key names only.
- Sidecar stderr is logged by the adapter — sidecars must not print their
  own credentials (documented contract, cannot be enforced host-side).

## v1 transport limitations (environment variables)

Env transport is simple and language-agnostic, but:

- visible in `/proc/<pid>/environ` to same-UID processes on shared hosts;
- a process's env is fixed at spawn — rotation requires the restart above;
- children of the sidecar inherit it unless the sidecar is careful.

**v2 option (not implemented):** deliver secrets in a `credentials` frame
after the protocol handshake (`docs/EXTERNAL_CHANNEL_PROTOCOL.md`), letting
sidecars receive rotated values without a restart and keeping secrets out of
the process environment entirely. Demand-gated; the manifest grammar stays
unchanged.
