# Multi-User Soulacy: User Stories and Acceptance Criteria

Status: proposed implementation backlog  
Target first milestone: **Team Preview**  
Default deployment mode: **Personal**

This backlog defines the product and engineering work required to make a
Soulacy deployment safely usable by more than one person. It is ordered by
dependency, not by UI surface. Authentication alone is not considered
multi-tenancy: the release gate is end-to-end workspace isolation across the
API, runtime, storage, events, files, background jobs, and administrative
surfaces.

## Product invariants

These invariants apply to every story:

1. Every deployment has at least one organization, workspace, user, and
   membership, including Personal deployments.
2. Every workspace-owned durable resource has an immutable `workspace_id`.
3. A client may request a workspace, but only the server may authorize and
   establish the active workspace context.
4. User-supplied IDs and channel sender IDs are never authentication inputs.
5. Team and Scale modes fail closed when identity or workspace context is
   absent. Only explicitly public health/readiness endpoints are exempt.
6. The GUI and `sy` CLI use the same versioned API and authorization rules.
7. Personal mode remains zero-dependency and backwards compatible.
8. Cross-workspace access returns `404` where revealing object existence would
   create an enumeration oracle; otherwise it returns `403`.
9. Cache, queue, vector, filesystem, metric, trace, and event keys include the
   workspace boundary.
10. Platform administration and workspace ownership are distinct authorities.

## Milestones

| Milestone | Outcome | Stories |
|---|---|---|
| M1 — Tenant kernel | Personal mode runs on the tenant-aware core | MU-001–MU-005 |
| M2 — Team identity | Multiple users can securely join and use a workspace | MU-006–MU-011 |
| M3 — Data isolation | All material data and extension surfaces are workspace-scoped | MU-012–MU-019 |
| M4 — Execution plane | Runs, schedules, approvals, and budgets are durable and isolated | MU-020–MU-025 |
| M5 — Team Preview | GUI/CLI collaboration and production operations are usable | MU-026–MU-032 |
| M6 — Scale | Multiple gateways and workers operate correctly under failure | MU-033–MU-037 |

---

## Epic A — Deployment modes and tenant kernel

### MU-001 — Choose the operating mode during deployment

**User story:** As an operator, I want to select Personal, Team, or Scale mode
during setup so that Soulacy provisions safe defaults for my intended usage.

**Acceptance criteria:**

- Setup presents `personal`, `team`, and `scale`, with Personal selected by
  default and clear infrastructure requirements for each option.
- The choice is persisted as `deployment.mode`; unknown values fail validation.
- Team mode refuses to start without authentication, a stable signing key,
  PostgreSQL, and an isolation-capable executor.
- Scale mode additionally refuses to start without durable distributed jobs and
  shared artifact storage.
- Unsafe overrides require an explicit named acknowledgement and produce a
  persistent readiness warning; authentication and tenant isolation cannot be
  overridden.
- `sy doctor` reports the active mode, unmet requirements, and remediation.
- Existing configurations with no mode continue as Personal.

### MU-002 — Bootstrap an implicit personal tenant

**User story:** As an existing single-user operator, I want the tenant-aware
upgrade to preserve my current agents and configuration without changing how I
use Soulacy.

**Acceptance criteria:**

- On first tenant-aware startup, Soulacy creates one organization, workspace,
  local user, and owner membership using stable generated IDs.
- Existing agents, conversations, memory, schedules, secrets, knowledge, costs,
  and Studio state are assigned to the implicit workspace exactly once.
- The migration is transactional or resumable and records its schema version.
- Re-running startup does not duplicate tenants or resources.
- Before applying changes, `sy workspace migrate --plan` reports affected stores
  and creates no mutations.
- A failed migration leaves the old deployment usable or provides an automatic
  rollback with a clear recovery command.

### MU-003 — Establish a mandatory workspace request context

**User story:** As a security engineer, I want every request and background
operation to carry an authenticated workspace context so that tenant filters
cannot be accidentally omitted.

**Acceptance criteria:**

- A single immutable context type carries subject, organization, workspace,
  membership, role, scopes, credential ID, and request ID.
- Team and Scale API handlers cannot call workspace-owned repositories without
  this context; repository interfaces require it explicitly.
- The server verifies requested workspace membership before creating context.
- Runtime calls, scheduled jobs, channel messages, approvals, learning jobs,
  and webhooks receive a service or user principal plus workspace context.
- No handler trusts `workspace_id`, `user_id`, or role from a JSON body, query
  parameter, message payload, or unsigned header.
- Static analysis or an architectural test enumerates routes and fails when a
  protected route lacks authentication, workspace resolution, or authorization.

### MU-004 — Create organization, workspace, and membership persistence

**User story:** As an administrator, I want durable organizations, workspaces,
users, and memberships so that access survives restarts and works across nodes.

**Acceptance criteria:**

- PostgreSQL tables exist for organizations, workspaces, users, identities,
  memberships, invitations, service accounts, and credentials.
- IDs are opaque, immutable, non-sequential public identifiers.
- Email and external identity uniqueness rules are explicit and case-safe.
- A user may hold different roles in different workspaces.
- Membership suspension and deletion take effect on the next authorized request.
- All mutations record actor, request ID, timestamp, and before/after audit data.
- Database constraints prevent memberships from referencing unrelated
  organizations or workspaces.

### MU-005 — Inventory and enforce tenant ownership in every store

**User story:** As a maintainer, I want an authoritative resource inventory so
that no legacy SQLite, JSON, file, or in-memory store escapes tenant scoping.

**Acceptance criteria:**

- The inventory covers agents, definitions, sessions, messages, events, memory,
  costs, credentials, secrets, schedules, approvals, knowledge, vectors,
  artifacts, workboard, Studio drafts/traces/learning, skills, MCP, plugins,
  registries, channels, webhooks, shares, API keys, and audit records.
- Every item is classified as platform-global, organization-owned,
  workspace-owned, user-private, or ephemeral.
- Every workspace-owned repository requires `workspace_id` and uses composite
  uniqueness where human names or agent IDs can collide.
- CI fails if a new durable table or repository is introduced without an
  ownership classification and isolation test.
- The inventory documents retention, export, deletion, and backup behavior.

---

## Epic B — Identity, membership, and authorization

### MU-006 — Sign in interactively with OIDC

**User story:** As a team member, I want to sign in through my identity provider
so that I do not share a server API key with other users.

**Acceptance criteria:**

- The GUI uses Authorization Code flow with PKCE, state, and nonce validation.
- The CLI uses browser-assisted PKCE with a loopback callback and a documented
  device-flow fallback where supported.
- Issuer, audience, algorithm, signature, expiry, and nonce are validated.
- Accounts are linked only by a verified provider subject, never by an
  unverified email claim alone.
- Access tokens are short-lived; refresh tokens rotate and reuse is detected.
- Logout revokes the local session and invalidates or removes refresh material.
- Authentication failures do not disclose whether an account exists.

### MU-007 — Invite and manage workspace members

**User story:** As a workspace owner, I want to invite, suspend, change the role
of, and remove members so that I can control team access.

**Acceptance criteria:**

- Owners can create single-use, expiring invitations for a specific workspace
  and role.
- Invitations cannot grant a role higher than the inviter may administer.
- Accepting an invitation is idempotent and binds the authenticated user.
- Role changes and suspensions affect new API calls, streams, approvals, and job
  claims without requiring a gateway restart.
- Removing the last owner is rejected until ownership is transferred.
- Suspended or removed members cannot refresh a workspace session.
- Every membership action is auditable and visible to workspace owners.

### MU-008 — Enforce workspace-scoped roles and permissions

**User story:** As a workspace owner, I want role-based permissions evaluated in
the active workspace so that a role in one workspace grants nothing in another.

**Acceptance criteria:**

- Initial roles are `owner`, `admin`, `developer`, `operator`, and `viewer`, with
  a documented permission matrix.
- The effective role is loaded from active membership, not accepted from a JWT
  role claim as final authority.
- Platform operators do not automatically receive workspace data access; any
  support access is explicit, time-bound, approved, and audited.
- Object-level agent grants are evaluated after workspace membership and token
  scopes, and can only narrow access unless an owner explicitly grants access.
- Missing identity, membership, policy data, or authorization-store errors deny
  access in Team and Scale modes.
- Table-driven tests cover every role/resource/action combination.

### MU-009 — Issue scoped personal and service credentials

**User story:** As a developer, I want scoped credentials for scripts and CI so
that automation does not use my interactive account or a master key.

**Acceptance criteria:**

- Personal access tokens and service-account keys are bound to one organization
  and one or more explicit workspaces.
- Tokens carry resource/action scopes, expiry, issuer, and credential ID.
- Plaintext keys are displayed once and stored only as a strong hash.
- Keys can be rotated and revoked without restarting Soulacy.
- A revoked, expired, suspended, or deleted credential is rejected immediately.
- CLI and audit output identify the service account, not a generic API user.
- Team mode does not accept the static server key for ordinary workspace APIs.

### MU-010 — Switch CLI contexts safely

**User story:** As a CLI user, I want named server and workspace contexts so that
I can operate local, staging, and production deployments without ambiguity.

**Acceptance criteria:**

- `sy context add/list/use/show/delete`, `sy login/logout/whoami`, and
  `sy workspace list/use` are available.
- Context files contain no access tokens, refresh tokens, API keys, or secrets;
  supported OS credential stores hold sensitive values.
- Every command can show server, organization, workspace, user/service account,
  and role with `sy context show`.
- Destructive confirmation names the server and workspace.
- `--json` has stable schemas and does not mix progress text into stdout.
- CI can select context non-interactively with environment-provided credentials
  without placing tokens in command history or process arguments.
- Local mode continues to target the loopback gateway without requiring login.

### MU-011 — Negotiate API and CLI compatibility

**User story:** As an operator, I want the CLI to detect server compatibility so
that upgrades fail clearly instead of corrupting resources.

**Acceptance criteria:**

- The gateway exposes API version, supported features, and minimum/maximum
  compatible CLI versions.
- The CLI performs a capability handshake before unsupported operations.
- Incompatible versions return a typed error and actionable upgrade guidance.
- Additive fields do not break older compatible clients.
- Mutating requests accept an idempotency key and return a request ID.
- Updates use resource versions or ETags and reject stale writes with `409`.

---

## Epic C — Workspace data and extension isolation

### MU-012 — Scope agents, versions, and Studio drafts

**User story:** As a developer, I want my workspace's agents and Studio drafts
isolated from other workspaces while allowing names to overlap.

**Acceptance criteria:**

- Agent identity uniqueness is `(workspace_id, agent_id)`.
- Agent definitions are immutable versions with actor and creation metadata.
- List, load, save, enable, disable, delete, export, import, and hot reload always
  resolve through active workspace context.
- Studio drafts, traces, rules, lessons, preferences, macros, and generation
  proofs are workspace-scoped unless explicitly marked user-private.
- Filesystem watcher events cannot load an agent into another workspace.
- Cross-workspace ID tests return no metadata about the target resource.

### MU-013 — Scope conversations, sessions, and memory durably

**User story:** As a user, I want my conversations and agent memory protected
from other users and workspaces across restarts and gateway replicas.

**Acceptance criteria:**

- Session ownership is persisted with workspace, agent, creator, and visibility;
  authorization does not depend on an in-process map.
- Conversation visibility supports at least `private` and `workspace`.
- Session IDs are opaque and cannot be claimed by a second principal.
- Memory reads and writes include workspace, agent, session, and declared scope.
- Fork, replay, archive, delete, search, export, and feedback enforce the same
  ownership policy as ordinary reads.
- A restart and a second gateway replica preserve identical authorization.
- Cross-tenant tests cover guessed IDs, pagination cursors, search, and exports.

### MU-014 — Isolate knowledge, embeddings, and vector retrieval

**User story:** As a workspace member, I want semantic retrieval restricted to
my workspace so that embeddings never leak another tenant's information.

**Acceptance criteria:**

- Every document, chunk, embedding, collection, ingestion job, and result has a
  server-controlled workspace namespace.
- Vector queries cannot supply or override another namespace.
- Ingestion jobs revalidate workspace and credential status before committing.
- Temporary extraction files are placed under a workspace/run-specific root and
  removed according to retention policy.
- Search logs and relevance diagnostics redact source text unless authorized.
- Isolation tests run against each supported vector backend.

### MU-015 — Encrypt and authorize workspace secrets

**User story:** As a workspace owner, I want secrets encrypted and selectively
available to agents so that another workspace or compromised worker cannot read
my complete vault.

**Acceptance criteria:**

- Secret names are unique within a workspace, not globally.
- Values use envelope encryption with a versioned per-workspace data key and a
  production KMS-backed wrapping key.
- APIs list metadata but never return plaintext through ordinary read routes.
- A run receives only explicitly referenced secret versions for its workspace.
- Decryption revalidates workspace, agent grant, principal, and purpose.
- Rotation preserves version history without exposing old plaintext.
- Values are redacted from events, logs, traces, errors, prompts, support bundles,
  subprocess arguments, and process environments where avoidable.
- Secret access and failed access are audited.

### MU-016 — Isolate artifacts, attachments, and filesystem tools

**User story:** As a user, I want generated files and attachments isolated so
that paths, links, symlinks, and exports cannot cross workspace boundaries.

**Acceptance criteria:**

- Artifact object keys include workspace and run IDs and are generated by the
  server, not accepted as raw client paths.
- Downloads use short-lived authorized URLs or authenticated streaming.
- Path containment is checked after symlink resolution and before every access.
- Archives reject absolute paths, traversal, device files, links escaping the
  extraction root, and decompression bombs.
- File tools operate only within the run's authorized mounts.
- Deleted or expired artifacts are no longer downloadable, including through
  previously issued URLs after their bounded expiry.

### MU-017 — Govern skills, MCP servers, plugins, and registries

**User story:** As a workspace administrator, I want extensions installed and
granted per workspace so that third-party code cannot become a platform-wide
backdoor.

**Acceptance criteria:**

- Extension inventory, configuration, credentials, approvals, and enablement
  are workspace-scoped; platform-provided extensions are read-only templates.
- URL installation stages content, pins an immutable revision, verifies a
  checksum/signature where available, runs safety inspection, and requires an
  authorized approval before activation.
- Installation permissions are separate from extension-use permissions.
- An extension receives an explicit capability grant and only referenced secret
  handles for its workspace.
- MCP processes execute within workspace isolation and cannot inherit gateway
  credentials or unrestricted host access.
- Updates that add capabilities require renewed approval.
- Revocation prevents new invocations and terminates or drains existing
  processes according to documented policy.

### MU-018 — Isolate channel identities and routing

**User story:** As a workspace administrator, I want external channel accounts
and senders mapped explicitly so that inbound and outbound messages reach only
the intended workspace and agent.

**Acceptance criteria:**

- Each channel connection belongs to exactly one workspace unless a documented
  platform router performs verified account-based dispatch.
- Inbound workspace identity is derived from the authenticated webhook,
  connection, bot, or account—not from message content.
- External sender IDs map to workspace-local identities and are not treated as
  Soulacy authentication principals.
- Outbound sends verify workspace channel ownership at execution time.
- Webhook replay protection and provider signature verification are enabled.
- Ambiguous routing fails closed and records a redacted diagnostic.

### MU-019 — Scope costs, audit, shares, and support data

**User story:** As a workspace owner, I want operational and diagnostic data
isolated so that indirect surfaces do not leak tenant information.

**Acceptance criteria:**

- Costs, audit records, metrics detail, shared links, support bundles, browser
  traces, and run diagnostics include workspace ownership.
- Aggregate platform metrics do not expose tenant labels to unauthorized users.
- Shared links are explicit, revocable, expiring, minimally scoped, and audited.
- Support bundles default to one workspace, redact secrets and personal data,
  and require explicit confirmation before including content bodies.
- Pagination, filters, exports, and aggregate queries cannot omit the workspace
  predicate.

---

## Epic D — Durable and isolated execution

### MU-020 — Submit runs as durable asynchronous jobs

**User story:** As a user, I want long-running agents to survive disconnection
and gateway restarts so that CLI and GUI sessions remain responsive.

**Acceptance criteria:**

- Run creation returns `202` with a durable `run_id` and event cursor.
- The job records workspace, immutable agent version, authenticated actor or
  service principal, policy snapshot, budget reservation, and idempotency key.
- Retrying the same idempotency key does not create a duplicate run.
- Job state transitions are explicit, validated, and durable.
- A gateway restart does not lose queued, running, paused, or completed state.
- Users can reconnect, inspect, follow, cancel, and retrieve final results.

### MU-021 — Execute tools in workspace-isolated workers

**User story:** As an operator, I want untrusted tools isolated from the gateway
and other tenants so that one run cannot compromise the service.

**Acceptance criteria:**

- Team mode defaults to container or equivalent isolation; Scale mode supports
  independently scalable workers.
- Each run has a private filesystem, explicit read-only inputs, bounded writable
  scratch space, and no ambient host or cloud credentials.
- CPU, memory, PIDs, open files, disk, wall time, and output size are limited.
- Network egress is denied by default or controlled through explicit policy.
- Secret material is injected narrowly, never written to durable job payloads,
  and removed when the run ends.
- Worker loss causes bounded retry only for retry-safe stages; side-effecting
  calls are not repeated without idempotency proof or approval.
- Isolation escape and noisy-neighbor tests run in CI or a dedicated security
  suite.

### MU-022 — Route approvals to authorized humans

**User story:** As a workspace member, I want privileged actions paused and
routed to eligible approvers so that no other tenant can view or approve them.

**Acceptance criteria:**

- Approval records persist workspace, run, tool, redacted arguments, requester,
  required permission, expiry, and decision actor.
- Only current eligible members may view or decide an approval.
- Membership revocation, policy changes, run cancellation, or expiry invalidate
  pending decisions.
- A decision is single-use and safe under concurrent approvers.
- Approved execution verifies the exact tool and argument fingerprint.
- CLI and GUI receive the same approval state through the API/event stream.

### MU-023 — Run schedules exactly once within a workspace

**User story:** As an operator, I want schedules to work across multiple gateway
instances without duplicate executions.

**Acceptance criteria:**

- Schedules are workspace-scoped and store timezone, next fire time, immutable
  agent version policy, and creator.
- A distributed lease or database claim prevents duplicate firing.
- Enqueue uses a deterministic idempotency key per schedule occurrence.
- Misfire behavior is configurable and bounded; catch-up cannot create an
  unbounded burst.
- Disabling an agent, removing permission, or exhausting budget prevents future
  executions with an auditable reason.
- Failover tests kill the active scheduler during claim and enqueue boundaries.

### MU-024 — Enforce fair quotas and cost budgets

**User story:** As an organization owner, I want budgets and resource quotas so
that one user or agent cannot exhaust capacity or incur uncontrolled costs.

**Acceptance criteria:**

- Limits can be defined at deployment, organization, workspace, user/service
  account, agent, and provider/model levels with documented precedence.
- Run admission atomically reserves estimated budget before provider calls.
- Actual usage reconciles reservations and records cached, reasoning, input, and
  output token costs.
- Concurrency uses fair scheduling rather than global first-come starvation.
- Rate limits are keyed by authenticated credential and workspace, not only IP.
- Users receive remaining budget, reset time, and a typed rejection reason.
- Concurrent requests cannot overspend the same hard budget.

### MU-025 — Make background learning tenant-safe

**User story:** As a workspace owner, I want learning and memory improvements to
use only my workspace's evidence so that strategic knowledge never crosses
tenant boundaries accidentally.

**Acceptance criteria:**

- Distillation, lesson retrieval, strategy-fit telemetry, preference mining,
  feedback, and repair jobs carry immutable workspace context.
- Cross-workspace learning is disabled by default and cannot occur through
  aggregate caches or vector similarity queries.
- Organization-wide sharing requires an explicit policy, source attribution,
  redaction, and opt-in destination.
- Deleting source evidence removes or invalidates derived private learning data
  according to documented lineage rules.
- Background jobs revalidate workspace status and policy before committing.

---

## Epic E — Realtime experience and collaboration

### MU-026 — Stream workspace-isolated events with resume

**User story:** As a GUI or CLI user, I want reliable realtime run updates that
I can resume without seeing another user's events.

**Acceptance criteria:**

- SSE/WebSocket authentication establishes a workspace-authorized subscription;
  client-provided event filters can only narrow it.
- Every event carries workspace and authorization-relevant ownership metadata
  internally, while public payloads expose only permitted fields.
- Event authorization is performed before serialization and broadcast.
- Clients can resume from a bounded cursor after reconnect.
- Slow consumers are bounded and disconnected without blocking publishers.
- Multiple gateway replicas publish and consume a consistent event stream.
- Cross-user and cross-workspace leakage tests include sessionless events and
  administrative events.

### MU-027 — Keep GUI and CLI responsive during long work

**User story:** As a user, I want immediate acknowledgement and progressive
feedback so that long LLM and tool operations do not make Soulacy feel frozen.

**Acceptance criteria:**

- Run submission acknowledges within 500 ms at p95 under the documented Team
  Preview load, excluding external identity-provider latency.
- Run status reads complete within 300 ms at p95 under the same load.
- The GUI renders queued, running, waiting-for-approval, cancelling, completed,
  and failed states without holding a request open.
- `sy run follow` reconnects with backoff and resumes from its last cursor.
- Cancellation is acknowledged promptly and workers check cancellation between
  bounded operations.
- Provider/tool latency is separately visible from Soulacy queue and processing
  latency.

### MU-028 — Prevent stale writes during collaboration

**User story:** As a developer, I want concurrent edits detected so that one
member cannot silently overwrite another's agent or Studio changes.

**Acceptance criteria:**

- Mutable resources expose a version or ETag.
- Updates with stale versions return `409` plus current metadata; they never
  silently overwrite.
- The GUI offers reload, compare, and explicit overwrite/fork choices.
- Agent deployment references an immutable definition version.
- Audit history identifies both conflicting actors and versions.

### MU-029 — Switch organizations and workspaces in the GUI

**User story:** As a member of multiple teams, I want an obvious workspace
selector so that I always know where an action will occur.

**Acceptance criteria:**

- The active organization and workspace remain visible in the global shell.
- Switching clears workspace-specific caches, subscriptions, drafts, and
  optimistic state before loading the destination.
- Deep links verify access and redirect safely when membership is absent.
- Destructive dialogs display workspace identity.
- Empty, suspended, expired-session, and access-revoked states provide a clear
  recovery path without exposing inaccessible resource names.

### MU-030 — Administer members, credentials, and usage in the GUI

**User story:** As a workspace owner, I want a coherent administration surface
so that I can manage access and costs without using raw APIs.

**Acceptance criteria:**

- Owners can manage invitations, memberships, service accounts, token
  revocation, budgets, quotas, and workspace retention settings.
- UI controls are hidden or disabled for unauthorized users, while the API
  independently enforces every action.
- Secret values and token plaintext are shown only at creation when applicable.
- Usage views identify major consumers without exposing private conversation
  content.
- High-impact actions require reauthentication or an equivalent recent-auth
  check.

### MU-031 — Provide a workspace audit trail

**User story:** As a workspace owner, I want a searchable audit trail so that I
can investigate configuration, access, and execution changes.

**Acceptance criteria:**

- Audit events include actor, effective workspace, action, resource type/ID,
  request ID, timestamp, outcome, credential ID, and redacted change summary.
- Authentication, membership, token, secret, policy, extension, approval,
  schedule, export, deletion, and support-access actions are covered.
- Audit storage is append-oriented and ordinary workspace administrators cannot
  modify existing records.
- Search and export are workspace-scoped, paginated, and themselves audited.
- Retention is configurable within platform minimums.

### MU-032 — Export and delete workspace data

**User story:** As an organization owner, I want controlled export and deletion
so that I can move or remove my data predictably.

**Acceptance criteria:**

- Export enumerates all owned resource classes and produces a versioned manifest
  with checksums.
- Export is asynchronous, access-controlled, expiring, and audited.
- Deletion requires recent authentication, explicit workspace-name confirmation,
  and a configurable recovery window.
- New runs and writes stop when deletion begins.
- Database rows, objects, vectors, secrets, jobs, caches, and derived learning
  data are deleted or tombstoned according to documented retention rules.
- Completion produces a deletion report without secret values.

---

## Epic F — Production and horizontal-scale hardening

### MU-033 — Operate multiple stateless gateway replicas

**User story:** As a platform operator, I want gateway replicas to scale and
fail over without inconsistent authorization or lost state.

**Acceptance criteria:**

- Gateways hold no authoritative session ownership, approval, schedule, run, or
  membership state in process memory.
- Authentication, authorization, revocation, and workspace switching behave
  consistently across replicas.
- Readiness fails when required shared dependencies are unavailable.
- Draining a replica stops new long-lived connections and allows bounded
  completion or reconnection.
- Load-balancer stickiness is not required for correctness.

### MU-034 — Process durable jobs with leases and idempotency

**User story:** As a platform operator, I want workers to recover safely from
crashes so that work is neither lost nor duplicated dangerously.

**Acceptance criteria:**

- Job claims use renewable leases with ownership and expiry.
- A crashed worker's retry is bounded and observable.
- At-least-once delivery is assumed; handlers are idempotent or explicitly mark
  non-retryable side effects.
- Poison jobs enter a workspace-scoped dead-letter queue after a bounded count.
- Operators can inspect, retry, or discard dead-letter jobs with authorization
  and audit records.
- Chaos tests cover crashes before/after claim, provider call, tool side effect,
  event publish, and result commit.

### MU-035 — Back up, restore, and migrate safely

**User story:** As an operator, I want tested backup and migration procedures so
that tenant data survives failures and upgrades.

**Acceptance criteria:**

- PostgreSQL and object storage backups have documented RPO/RTO targets.
- Restore tests verify relational data, objects, vector references, encrypted
  secrets, and schema version consistency.
- Migrations are versioned, forward-safe, observable, and compatible with the
  supported rolling-upgrade window.
- Destructive migrations require an explicit backup gate.
- A single workspace can be exported for recovery without exposing others.

### MU-036 — Observe health without leaking tenant data

**User story:** As a platform operator, I want service-level observability while
preserving workspace privacy.

**Acceptance criteria:**

- Metrics cover request latency, queue wait, worker utilization, provider/tool
  latency, errors, rate limits, budget rejection, and event lag.
- High-cardinality tenant/user/agent IDs are excluded from general metric labels.
- Logs and traces carry internal correlation IDs with access-controlled lookup.
- Prompt, response, tool arguments, secret values, and personal data are absent
  from default logs and traces.
- Workspace-level diagnostics are available only through authorized APIs.
- Alerts distinguish platform failure from one tenant's workload failure.

### MU-037 — Pass multi-tenant security and load release gates

**User story:** As a release owner, I want objective release gates so that Team
or Scale mode cannot be declared production-ready prematurely.

**Acceptance criteria:**

- Automated adversarial tests cover every route, repository, event type, queue,
  cache, vector query, filesystem operation, artifact link, and export for
  cross-tenant access.
- Tests cover membership revocation and role change during active runs, streams,
  approvals, and refresh-token use.
- Load tests demonstrate fair scheduling under a noisy tenant and publish p50,
  p95, and p99 API/queue latency plus error rates.
- Backup restoration and worker/gateway chaos tests pass in a production-like
  environment.
- Threat modeling and an independent security review have no unresolved critical
  or high findings.
- Team and Scale readiness checks are documented and enforced at startup.

---

## Definition of done for every implementation story

A story is complete only when:

- Unit, integration, and negative authorization tests pass.
- At least one test uses two organizations, two workspaces, and colliding human
  resource names to prove isolation.
- API, CLI, GUI, configuration, migration, and operational documentation are
  updated where applicable.
- Audit behavior and secret/PII redaction have been reviewed.
- Metrics and typed errors are sufficient to operate the feature.
- Personal-mode compatibility has been tested.
- No authorization decision depends solely on UI behavior.
- No workspace predicate is left to an optional caller convention.

## Team Preview release boundary

The first Team Preview may be a single Soulacy gateway with PostgreSQL and
isolated local workers. It must include MU-001 through MU-032. Horizontal
replication is not required for that preview, but no Team Preview component may
depend on an architecture that prevents MU-033 through MU-037 from being added
without changing public resource identity or authorization semantics.

