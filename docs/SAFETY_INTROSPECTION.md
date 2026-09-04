# Pre-Installation Safety Introspection (Story E20)

Every plugin staged through the install API (E13) — and every remote skill
resolved by `sy skill install` (E18) — runs through a three-check pipeline
**before** the operator sees the approval dialog. The pipeline never blocks
silently: each check that cannot run degrades to a visible finding.

## The checks

**1. Static scan** (`introspect.StaticScan`) — every `.py` file is searched
line-by-line for dangerous calls (`eval`/`exec`/`subprocess.*`/`os.system`/
`os.popen`/`__import__`/`ctypes`), suspicious imports (`subprocess`,
`socket`, `ctypes`, base64-decoded payloads), and path-traversal strings
(`../`). Comment lines are skipped. Package documents (`SKILL.md`,
`plugin.yaml`, `README.md`) are additionally checked for blatant
prompt-injection markers ("ignore previous instructions", role
reassignment, concealment instructions).

**2. LLM prompt & code audit** (`introspect.RouterAuditor`) — an internal
auditor agent reads the package documents through the llm router (default
provider) looking for prompt injection and behaviour/manifest mismatches
(e.g. a "weather" skill requesting vault credentials). The model must
answer with a JSON findings array; unknown severities clamp to *warning*.
**Degrades gracefully**: no provider, transport error, or unparseable
response → a single info finding "audit skipped: …" — never a silent gap.

**3. Sandboxed dry-run** (`introspect.DryRun`) — declared startup hooks
(manifest v2 sidecar specs) execute briefly inside the staging directory
under the F1 rlimit `__exec-sandbox` wrapper (when sandboxing is enabled),
with HTTP egress pointed at a dead loopback proxy (`127.0.0.1:9`) so
well-behaved clients cannot phone home (raw-socket escapes are the static
scan's job to flag). Recorded per hook: exit status + runtime (info on
clean exit, warning on crash, info for daemon-style sidecars that simply
outlive the 5 s timeout) and **file writes** detected by snapshot diff.

## The report

```json
{
  "findings": [{"check": "static", "severity": "critical", "file": "tool.py", "line": 3, "message": "dangerous call: eval() …"}],
  "severity": "critical",
  "verdict": "danger"
}
```

`severity` is the max across findings; `verdict` maps it to
`pass` / `caution` / `danger`.

## Where it surfaces

- **GUI approval dialog** — `Preview.security` (E13 extension) renders a
  verdict badge + findings list above the permission grants
  (PluginManager.svelte).
- **CLI consent prompt** — `sy skill install` (E18) prints the same report
  before asking for confirmation.

The pipeline is wired in `internal/app` (`gateway.SetSafetyPipeline`); the
LLM audit activates when at least one provider is registered, the sandboxed
dry-run inherits `runtime.sandbox` limits.
