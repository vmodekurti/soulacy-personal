# Recent platform updates

Soulacy's current `main` branch includes a broad runtime-hardening and learning
upgrade. This page is the short map; each section links to the operational
documentation with configuration and API details.

!!! note "Release versus main"
    Documentation deploys from `main`. A tagged binary may lag these pages.
    Run `sy version` before relying on a feature, and follow the
    [upgrade guide](deployment/upgrades.md) when updating an existing host.

## New: reviewed autonomy and practical guides

Before the earlier memory/runtime work below, the current source also adds:

- **Blue Living Core branding** across the workspace, marketing page, docs,
  favicon, and PWA/notification assets.
- **[Private Learning Notebook](using/learning-notebook.md):** teach a bounded
  lesson, inspect its sources, approve it, and disable it when it stops helping.
- **[Model preparation](using/model-preparation.md):** inspect available model
  capabilities while preserving your chosen goal, permissions, and budget.
- **[Verified Autopilot](using/autopilot.md):** explicit run checks, candidate
  releases, staged promotion, bounded goal teams, and supervised recovery.
- **[Safe Undo](use-cases/safe-undo-handoff.md):** review and reverse supported
  conditional changes without treating arbitrary side effects as reversible.
- **[iPhone setup](getting-started/iphone.md)** and **[published files](using/published-files.md)**
  guides, with clear gateway configuration and permission prerequisites.

Start with the [six worked use cases](use-cases/index.md). Each has sample input,
expected results, edge cases, and recovery instructions. Existing data is not
automatically migrated into a new learning mechanism. “Regression checks” in
Autopilot is distinct from private Notebook lessons.

## Earlier: persistent semantic agent memory

Agent semantic memory now uses Soulacy's embedded **sqlite-vec** backend in
production instead of an in-process vector store. Semantic entries survive a
gateway restart, searches are isolated by agent, and startup fails visibly if
the semantic backend cannot be attached—there is no silent downgrade to
keyword-only behavior.

The effective default is:

```yaml
vector:
  backend: sqlite-vec
  dims: 768
```

Keep `vector.dims` aligned with the configured embedding model. See
[Storage & backends](configuration/storage.md) and
[Agent memory](using/memory.md).

## Human feedback closes the learning loop

Assistant responses in Chat now expose **👍 Helpful** and **👎 Unhelpful**
actions. Feedback is tied to a server-issued completed run, stored durably with
redaction and bounded retention, and used to boost or suppress matching
workflow patterns. Changing a rating updates the existing signal rather than
double-counting it.

Written feedback becomes a **pending** procedural-learning proposal. It never
changes an agent rulebook without review. See [Chat](using/chat.md),
[Studio learning & memory](studio-learning-memory.md), and the
[feedback API](api/agents.md#response-feedback).

## Studio learns structure, fit, and preferences

Studio's learning layer now covers four kinds of evidence:

1. successful multi-tool runs distilled into sanitized workflow patterns;
2. provider/model/strategy success rates used to warn against unreliable fits;
3. repeated manual Goal and Instructions edits distilled into user-scoped
   preferences;
4. semantically retrieved repair lessons stored in sqlite-vec.

Generated drafts remain reviewable. Workflow is still an explicit
**experimental** strategy; Auto remains the general default. Trigger,
destination, provider, and model controls are authoritative and should be
verified in the resulting `SOUL.yaml`. See [Using Studio](using/studio.md).

## LLM usage is admitted before it is spent

All governed inference paths—including Chat, reasoning, Studio, workflows,
repair, synthesis, and embeddings—share one controller. It can:

- enforce provider/model/data-region policy;
- reserve worst-case cost and tokens before a call;
- enforce daily, monthly, per-user, and per-agent budgets;
- clamp output tokens to remaining capacity;
- bound provider concurrency and token throughput;
- open a circuit after repeated provider failures;
- preserve request IDs and retry attempts without double-counting;
- reconcile the local ledger with supported provider billing exports.

Use `costs.enforcement_mode: hard` and `costs.unknown_pricing: block` for a
strict production posture. See [LLM usage and cost controls](LLM_COST_CONTROLS.md)
and [Costs API](api/costs.md).

## Runtime and gateway hardening

The recent security sweep added or strengthened:

- object-scoped RBAC and authenticated session ownership;
- canonical filesystem roots and symlink-safe path resolution;
- redirect-aware SSRF protection for model-controlled HTTP destinations;
- privileged execution isolation and explicit confirmation boundaries;
- credential, log, learning-memory, attachment, and support-bundle redaction;
- bounded retention for action logs, audit records, and learned feedback;
- schema validation and secure file modes for configuration and bundles;
- production security readiness checks for privileged shared-channel exposure.

Start with [Security overview](security/index.md), then configure the
[security posture](configuration/security.md) and inspect
`GET /api/v1/security/readiness` before enabling production agents.

## Toolchain and deployment

Source builds, CI, release workflows, and container builds now use **Go
1.26.6**. Existing binary installations do not need Go installed; source-based
installations and contributors do. The installer compares the full Go
major/minor/patch version and will reject older source-build toolchains.

For an existing VPS, use [Upgrades & reinstall](deployment/upgrades.md), then
run `sy doctor` and confirm the systemd service reads the intended workspace
and configuration path.
