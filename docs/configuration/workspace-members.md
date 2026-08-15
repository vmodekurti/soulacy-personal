# Workspace members

Team and Scale deployments manage access through workspace memberships. Open
**System → Members** in the GUI to invite a teammate, change a role, suspend
access, remove a member, or review membership history.

## Invite and accept

Workspace owners and administrators can create an invitation for a verified
email address. Each invitation has a role, an expiry between one minute and 30
days, and a cryptographically random token. The plaintext token is shown once;
Soulacy stores only its SHA-256 hash.

Send the generated invitation link through a trusted channel. The recipient
signs in through OIDC with the invited, verified email address and opens the
link or pastes its token into **Accept an invitation**. Acceptance is
single-user, single-use, and idempotent when the same recipient retries it.

## Role administration

Roles form a strict hierarchy:

| Role | May administer |
|------|----------------|
| `owner` | owner, admin, developer, operator, viewer |
| `admin` | admin, developer, operator, viewer |
| `developer` | none |
| `operator` | none |
| `viewer` | none |

An administrator cannot invite, promote, suspend, or remove an owner. No
operation may leave a workspace without an active owner; concurrent owner
changes are serialized in PostgreSQL so two requests cannot bypass that rule.

## Immediate access changes

Membership authority is read from PostgreSQL on each new workspace request.
Role changes, suspension, and removal therefore affect new API calls, streams,
approval actions, and job claims immediately. Existing access tokens do not
preserve a stale workspace role. A suspended or removed user also loses refresh
eligibility; the current refresh-token family is revoked on the next refresh
attempt.

## Audit visibility

Owners can view membership and invitation mutations in **Membership audit**.
Each entry records the actor subject, request ID, action, resource, timestamp,
and before/after state. Invitation bearer tokens are never included in audit
records.

## Platform support access

Soulacy has no hidden platform-operator, support, or super-admin bypass. A
support engineer can see workspace data only after a workspace owner explicitly
invites that person's verified identity as a normal member. Use a short-lived
invitation, assign the least-privileged role, and remove or suspend the member
when the support window ends. Invitation, role, suspension, and removal events
are recorded in the membership audit log.
