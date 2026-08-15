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
| M3 — Data isolation | MU-012–019 | MU-012 ✓ MU-013 ✓ MU-014 ✓ MU-019 ✓; MU-015 partial; MU-016 partial; MU-017 partial; MU-018 not started |
| — event spine + stores | (cross-cutting) | Events, action log, learning, Studio traces, workboard, conversation history ✓ |
| M4 — Execution plane | MU-020–025 | Not started |
| M5 — Team Preview | MU-026–032 | Not started |
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

## Guards worth keeping

- **`TestRequestScopeIsNeverReadFromADetachedGoroutine`** (AST-based) fails the
  build if `s.agents(c)` or `s.studio(c)` is read inside a `go func()`. Fiber
  recycles the request context when a handler returns, so doing so is a
  use-after-free that surfaces as a nil dereference deep in fasthttp, far from
  the cause. **This guard has already caught two real defects** that no test of
  the feature itself would have found.
- **The ownership catalog discovery scan** now also finds loaders and
  package-level persistence keyed by a `root`/`dir` parameter. That closed a
  blind spot where `internal/studio/library.go` and `rulesstore.go` held
  workspace data that CI had never demanded be classified.

## Known remaining work

Highest-value first, with the reason each matters:

1. **MU-015 part 2** — envelope encryption with a versioned per-workspace data
   key under a production KMS wrapping key; per-run secret version references;
   redaction sweep across events, traces, prompts, and subprocess environments.
2. **MU-016** — artifacts and filesystem tools: server-generated object keys
   including workspace and run, path containment after symlink resolution,
   archive extraction that rejects traversal, escaping links, and decompression
   bombs.
3. **MU-016 cannot close yet either, and the remaining gap is architectural.**
   Four of its six criteria are met — server-generated object keys carrying the
   workspace, authenticated streaming, path containment after `EvalSymlinks`
   (`internal/runtime/filesystem_policy.go`, which fails closed with no roots),
   and expired artifacts becoming undownloadable immediately. What is left:

   - *"File tools operate only within **the run's** authorized mounts"* —
     `SetFilesystemRoots` configures one process-global set. Every tenant's
     runs share it, so a filesystem builtin in workspace A can read a file
     written by workspace B. Fixing it means per-run mounts, which is the same
     change MU-021 (execute tools in workspace-isolated workers) describes.
     Doing it here would be building half of MU-021 in the wrong place.
   - *Archives* — `internal/plugininstall/archive.go` has traversal refusal and
     a decompression-bomb bound. Symlink and device entries, and the other two
     extraction sites (`internal/updates`, `internal/knowledge/ingest.go`),
     have not been audited against the criterion.

4. **MU-025 cannot close yet, and the reason is not effort.** Three of its
   criteria presuppose infrastructure this branch has not built, and inventing
   it to tick the box would be worse than leaving it open:

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
   permissions change stops loading until a human re-approves. What remains is
   criterion 4 (capability grants and referenced secret handles), 5 (MCP
   processes under workspace isolation) and 7 (revocation that drains running
   processes).

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
