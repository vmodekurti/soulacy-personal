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
| M3 — Data isolation | MU-012–019 | MU-012 ✓ MU-013 ✓ MU-014 ✓ MU-019 ✓; MU-015 partial; MU-016–018 not started |
| — event spine + stores | (cross-cutting) | Events, action log, learning, Studio traces, workboard, conversation history ✓ |
| M4 — Execution plane | MU-020–025 | Not started |
| M5 — Team Preview | MU-026–032 | Not started |
| M6 — Scale | MU-033–037 | Not started |

## Isolation progress

`ownership.MultiUserBlockers()` is the machine-readable list of stores that are
declared workspace-owned but not yet isolated.

- At the start of this work: **57 blockers**
- Now: **21 blockers**

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

### The SDK is extended additively, never modified

`sdk/storage.MemoryBackend` is documented as frozen per major version, so it
was not touched. `memory.Entry` gained `WorkspaceID` (append-only, omitempty)
and a **new** `WorkspaceMemoryBackend` interface sits beside the frozen one. A
backend that does not implement it keeps working; a caller needing isolation
type-asserts and fails closed.

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
3. **MU-025 cannot close yet, and the reason is not effort.** Three of its
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

4. **DLQ, checkpoints, session resources, `rbac_agent_grants`,
   `studio/deployrecord.go`, `agentmemory`, `api_keys`** — all still
   `personal-only`; each needs the same treatment as the stores above.

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
