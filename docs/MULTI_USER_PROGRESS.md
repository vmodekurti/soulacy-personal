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
| M3 — Data isolation | MU-012–019 | MU-012 ✓ MU-013 ✓ MU-014 ✓ MU-018 ✓ MU-019 ✓; MU-015 partial; MU-016 partial; MU-017 partial |
| — event spine + stores | (cross-cutting) | Events, action log, learning, Studio traces, workboard, conversation history ✓ |
| — isolation floor | (cross-cutting) | 0 blockers: every declared store is scoped and names a real isolation test |
| M4 — Execution plane | MU-020–025 | MU-020 ✓; MU-021 ✓ (6/7; scalable workers → M6); MU-022 ✓; MU-023 ✓; MU-024 ✓; MU-025 ✓ (4/5; org sharing deferred with a guard, workspace-status half awaits MU-032) |
| M5 — Team Preview | MU-026–032 | MU-026 ✓; MU-027 partial (cancellation criteria 3 and 5 closed); MU-028–032 not started |
| M6 — Scale | MU-033–037 | Not started |

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
   - *Archives* — `internal/plugininstall/archive.go` has traversal refusal and
     a decompression-bomb bound. Symlink and device entries, and the other two
     extraction sites (`internal/updates`, `internal/knowledge/ingest.go`),
     have not been audited against the criterion.

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
`go test ./...`, and the GUI suite (782 tests across 71 files) before landing.

Two flakes were found and fixed along the way rather than tolerated, because
both failed under an unrelated test's name and would have cost someone an
afternoon:

- `TestChatStreamRunStaysCancellableWhileItIsStillRunning` returned while the
  cancelled run was still writing memory files, so `t.TempDir`'s `RemoveAll`
  raced them. It now drains the stream to EOF, which is the deterministic
  signal the run is done.
- `SQLiteHistoryStore.Close` was not idempotent. Closing an already-closed
  channel panics, so a store reached through two shutdown paths took the
  process down instead of returning an error.

One process note for whoever picks this up: run `gofmt -w` on the files you
touched, never on a whole tree. A repo-wide format pass in this branch's
history produced churn in a dozen untouched files — including a comment-list
reflow that degraded a doc comment in `internal/queue/memory` — and it had to
be reverted by hand before each commit.
