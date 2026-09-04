# Safety Introspection

Every plugin staged through the install API and every remote skill resolved by `sy skill install` runs through a three-check safety pipeline *before* you are asked to approve anything — and a check that cannot run degrades to a visible finding, never a silent gap.

## Tool risk tiers

Every tool is classified into one of five risk tiers so you can see a tool's blast
radius before an agent that uses it is bound to a channel:

| Tier | Meaning | Examples |
| --- | --- | --- |
| `safe` | Reads only, no side effects | `read_file`, `kb_search`, `list_dir` |
| `write` | Changes local files or state | `write_file`, `kb_write`, `queue_put` |
| `network` | Reaches external services | `fetch_url`, `http_request`, `web_search`, MCP tools |
| `privileged` | Installs software / broad system config | `install_library` |
| `shell_system` | Runs arbitrary commands or code | `shell_exec`, `run_script`, `python_eval` |

The tier appears on tools in the Studio palette and in the `/tool-catalog` API
(`risk` field). The top two tiers (`privileged`, `shell_system`) are treated as
high-risk and require confirmation before running unless the agent explicitly
allows them (see `confirm_tools` in the SOUL.yaml reference).

When you approve or deny a gated tool, the decision — **who approved what, at
which risk tier** — is recorded in the action log (a `tool.approval` event) so
the Activity view shows an accountable trail.

## Quick look

Install something and the report appears automatically:

```bash
sy skill install github.com/user/my-skill
```

```text
Safety introspection: ⚠ caution — review findings
  WARNING (static) [tools/fetch.py:12]: suspicious import: socket
  INFO (audit): audit skipped: no LLM provider configured
Install my-skill@1.2.0? [y/N]
```

No command or setting is needed to enable it — the pipeline runs on every
install, regardless of which registry or source the package came from.

## The three checks

### 1. Static scan

Every `.py` file is searched line-by-line (comments skipped) for:

- dangerous calls — `eval`/`exec`/`subprocess.*`/`os.system`/`os.popen`/
  `__import__`/`ctypes`;
- suspicious imports — `subprocess`, `socket`, `ctypes`, base64-decoded
  payloads;
- path-traversal strings (`../`).

Package documents (`SKILL.md`, `plugin.yaml`, `README.md`) are additionally
checked for blatant prompt-injection markers: "ignore previous
instructions", role reassignment, concealment instructions.

### 2. LLM prompt & code audit

An internal auditor agent reads the package documents through the LLM
router (default provider), looking for prompt injection and
behaviour/manifest mismatches — e.g. a "weather" skill requesting vault
credentials. The model answers with a structured findings list; unknown
severities clamp to *warning*.

This check **degrades gracefully**: no provider configured, a transport
error, or an unparseable response produces a single info finding
(`audit skipped: …`) instead of blocking the install or silently passing.
The CLI runs without a router, so CLI installs always report the LLM audit
as skipped — the static scan and dry-run still run in full.

### 3. Sandboxed dry-run

Declared startup hooks (manifest v2 sidecar commands) execute briefly
inside the staging directory under the rlimit sandbox wrapper (when
sandboxing is enabled), with HTTP egress pointed at a dead loopback proxy
(`127.0.0.1:9`) so well-behaved clients cannot phone home. Recorded per
hook:

- exit status and runtime — info on a clean exit, warning on a crash, info
  for daemon-style sidecars that simply outlive the 5 s timeout;
- **file writes**, detected by a before/after snapshot diff of the staging
  directory.

Raw-socket escapes are deliberately the static scan's job to flag — the
dry-run's dead proxy only blackholes HTTP-proxy-respecting clients.

## Reading the report

The report's `severity` is the maximum across findings and maps to a
verdict:

| Verdict | CLI badge | Meaning |
|---|---|---|
| `pass` | `✓ passed safety checks` | no findings above info |
| `caution` | `⚠ caution — review findings` | warnings present — read them |
| `danger` | `✗ DANGER — critical findings` | at least one critical finding |

What to do with findings:

- **pass** — install normally. The pipeline is heuristic, not a guarantee;
  the [capability model](plugin-security.md) is what actually contains a
  plugin at runtime.
- **caution** — read each warning. A `socket` import in a chat-channel
  sidecar is expected; the same import in a "markdown formatter" skill is
  not. Check that the findings match what the package claims to do.
- **danger** — stop. Critical findings (e.g. `eval()` on fetched data,
  prompt-injection text in `SKILL.md`) mean you should inspect the staged
  source yourself or walk away.

!!! warning "Danger verdicts always require an interactive yes"
    `sy skill install --yes` skips the consent prompt for pass/caution
    verdicts only. A **danger** verdict always demands an explicit
    interactive confirmation — in scripts and CI it aborts the install.
    There is no flag that bypasses it.

## Where the report appears

- **CLI** — `sy skill install` prints the verdict badge and every finding
  (severity, check, file:line, message) immediately before the consent
  prompt.
- **GUI** — the plugin approval modal renders a *Safety introspection*
  verdict badge and the findings list **above** the requested permission
  grants, so you read the risk before the asks. The same report ships in
  the `security` field of the staging API response
  (`POST /api/v1/plugins/install`).

Sources that publish partner security audits (e.g. skills.sh) have those
audits surfaced at consent as additional signal — Soulacy's own pipeline
still runs on every install.

## Limits worth knowing

- The static scan covers Python and package documents; it does not execute
  code or analyse compiled artifacts.
- The LLM audit is advisory and skippable by circumstance — treat
  `audit skipped` findings as "one fewer check ran", not as a pass.
- The dry-run observes only declared startup hooks for a few seconds.
- None of this replaces the install-time permission review or the runtime
  default-deny enforcement — it is a pre-filter, the trust decisions remain
  yours.
