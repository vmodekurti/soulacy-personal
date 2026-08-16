# Learning lineage and cross-tenant rules (MU-025)

## Criterion 4 — deletion lineage

When source evidence is deleted, derived learning follows these rules
(`Store.InvalidateBySession`):

| Proposal state | What happens | Why |
|---|---|---|
| **pending** | deleted outright | it affects nothing yet and nobody should review a rule whose evidence is gone |
| **accepted** | disabled, with `invalidated_reason` and `invalidated_at` in its metadata | it is already influencing behaviour, so it must stop — but silently deleting an active rule loses the record that it once applied |
| **rejected** | left alone | it already affects nothing, and removing it loses the "we considered this and said no" signal that stops the same lesson being re-proposed |

Deleting evidence in one workspace never touches another's: the store is per
workspace, so invalidation can only reach the caller's own file.

**Known limit.** Lineage is tracked by `session_id`. A proposal derived from
evidence that was never a session — a scheduled run's telemetry, say — has no
link to invalidate through. Every proposal the background sweep produces
carries its session, so this affects only hand-authored proposals.

## Criterion 5 — revalidation before commit

A sweep runs every six hours and takes as long as the evidence is wide. It
reads an agent definition, tails thousands of events, builds proposals, and
only then writes. Everything it decided at the top can have stopped being true
by the time it commits.

Two checks run immediately before anything is written:

- **Workspace status.** A `WorkspaceStatusFunc` hook, consulted per workspace.
  An error means "do not commit", and an *unreachable* status source is also an
  error — a skipped sweep is recoverable; a write into a suspended tenant is
  not. One uncommittable workspace skips that workspace, never the sweep: a
  bare `return err` in a loop over tenants lets one suspended customer stop
  learning for everybody.
- **Agent policy.** The definition is **re-read from its own workspace**, not
  re-checked on the pointer the loop captured. Re-checking the same pointer
  tests nothing that was not already true — and the test that proves this had
  to mutate the world from inside the *tailer*, because mutating from the
  status hook fires before the agent list is read and proves only that the loop
  skips absent agents.

**Partial by design.** Workspaces have no lifecycle column on this branch:
`memberships` has active/suspended/deleted, `workspaces` has nothing. Rather
than invent a status to check against, the gate takes a hook an operator — or
MU-032, when workspace lifecycle lands — supplies. Unset means "no status to
check", which is the accurate answer today rather than a pretend one.

## Criteria 2 and 3 — cross-workspace and organization sharing

**Cross-workspace learning is not merely disabled by default; it is
inexpressible.** Every read and write goes through `Stores.For(workspaceID)`,
which returns one tenant's store. There is no aggregate cache and no shared
vector index for learning data to leak through, because there is no API that
takes two workspaces.

`TestNoCrossWorkspaceSharingSurfaceExists` keeps that structural: an exported
function taking two workspace parameters fails the build.

**Organization-wide sharing is explicitly out of scope for Team Preview.**

Criterion 3 asks for "an explicit policy, source attribution, redaction, and
opt-in destination". Building that framework now would be speculative design:
there is no sharing surface, no product decision about what may be shared, and
no destination to opt into. A policy engine for a feature nobody has specified
would be four abstractions guarding nothing, and it would have to be rewritten
the moment the feature was actually designed.

The decision recorded here is: **cross-workspace learning stays inexpressible,
and criterion 3 is deferred rather than satisfied.** The guard above is what
makes that decision enforceable — anyone adding a sharing surface has to
delete a failing test, and the test tells them what the criterion requires
before they do.
