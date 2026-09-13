# Safe Undo

Safe Undo lets an agent prepare a small group of external changes, lets you
inspect the exact before/after values, and keeps a durable receipt for reversing
supported changes later. An agent can prepare a job, but **cannot apply or undo
it through a Safe Undo tool**. You confirm each operation on web or iOS.

Web: Autopilot → Safe Undo → choose an agent. iOS: Autopilot → Safe Undo,
or Agents → an agent → Safe Undo. iOS also requires device authentication.

This is a deliberately narrow first release, not universal time travel, not a
claim of being first to invent rollback, and not a promise of zero bugs.

## Supported resources

| Resource | Apply | Undo | Conflict policy |
| --- | --- | --- | --- |
| Existing JSON record | RFC 6902 PATCH of explicitly allowed top-level fields | Patch only the recorded fields back, preserving unrelated fields | Current touched values must match the recorded values; every write also uses strong If-Match |
| Existing UTF-8 text/Markdown document | WebDAV-compatible conditional PUT | Restore the recorded text and preserve its Content-Type | Exact recorded ETag, media type and contents must match; no automatic text merge |

These are configurable HTTP protocol adapters, **not turnkey Google Docs,
Salesforce, Notion, email, or payment integrations**. An endpoint must enforce
the documented conditional-write contract. Existing record fields can be added
or removed; creating or deleting resources is not supported. A nested JSON
object counts as one entire top-level field, not independent nested fields.

Messages, notifications, purchases, payments, shell commands, arbitrary agent
tools and file creation/deletion are outside this feature. Restoring an agent
release still restores configuration only, not external effects.

## Operator setup

Keep the feature off until you have verified the integration in a disposable
test account. Add exact resource URLs to the server configuration:

```yaml
server:
  safe_undo:
    resources:
      - id: customer
        agent_id: account-assistant
        name: Customer record
        kind: json_record
        url: https://records.example.com/customers/123
        token_env: CUSTOMER_SERVICE_TOKEN
        fields: [status, owner]
        conditional_writes: true
      - id: notes
        agent_id: account-assistant
        name: Account notes
        kind: webdav_text
        url: https://documents.example.com/accounts/123.md
        token_env: DOCUMENT_SERVICE_TOKEN
        conditional_writes: true
```

The example domains are placeholders, not services supplied by Soulacy.
Supply tokens through the gateway process environment or your deployment's
secret manager. Never place tokens inside a resource URL or agent prompt.
`token_env` can be omitted for a genuinely public endpoint. No extra arbitrary
headers, query strings, embedded URL credentials, redirects, or automatic
network retries are permitted. HTTPS uses normal certificate verification.
Private hosts require `allow_private_host: true` on the exact resource; network
metadata/link-local destinations remain blocked. `allow_loopback_http: true`
permits literal loopback HTTP addresses only, for isolated tests.

Explicitly enable the two preparation tools in the agent's SOUL.yaml:

```yaml
builtins:
  - safe_undo.resources
  - safe_undo.prepare
```

Wildcards do not opt in. Gateway authentication must be effective. Use an
interactive authenticated operator/admin with current agent access and
`agents:write` (and `agents:read` for the review UI). A legacy mobile `chat`
credential cannot approve external writes; its scopes are not silently widened.
Jobs are private to their authenticated owner and agent, including against the
master key when another credential owns the job. Replacing a managed API key
creates a new owner; do not revoke the only owner credential while unresolved
jobs still need review. Rotate an external integration token in its existing
environment variable instead when only that service token needs replacement.

Changing a resource's target, integration kind, credential variable, network
authority or field allowlist invalidates existing jobs. Changing the value of
the same external token variable permits credential rotation. Resource config
changes take effect on gateway restart. Do not map the same underlying object
under different URL aliases; Soulacy cannot discover server-side URL aliases.

## Required remote contract

`conditional_writes: true` is an **operator attestation**, not a capability
Soulacy can prove from a header. Before enabling it, verify that:

1. GET returns 200, one strong ETag, bounded UTF-8 data, and a supported media
   type. Weak/missing ETags, duplicate JSON keys and malformed data are refused.
2. Each PATCH/PUT atomically compares If-Match with the current representation
   and rejects a mismatch with no effects. Every successful write produces a
   new strong ETag. JSON Patch is atomic as a whole, including its test ops.
3. PUT/PATCH changes only the reviewed representation. It must not trigger
   emails, notifications, charges, counters, downstream workflows or other
   effects that this receipt cannot reverse. Audit/history entries remain.
4. Explicit refusal responses 401, 403, 404, 405, 409, 412, 415 and 422 mean
   nothing was changed. Other responses or transport/read-back failures are
   treated as uncertain, even if the endpoint might actually have succeeded.
5. No proxy strips If-Match, retries mutations, caches stale representations,
   or fabricates ETags. This contract applies end-to-end, including gateways.

Soulacy re-reads after a 200/204 mutation and verifies both a changed version
and the desired values. That cannot establish hidden downstream effects or
repair a server that violates its attested concurrency contract.

## Review and recovery

Prepare captures before/after values without writing externally. A preview
re-reads every affected resource and expires after five minutes. Confirming
checks the whole preview before the first write, then uses If-Match for every
individual write. Permissions and credentials are checked again before each
dispatch. JSON numbers are shown as lossless text, including large integers.

Successful actions are recorded individually. Undo runs in reverse order.
Cross-system changes are **not atomic**: a refusal or lost response stops the
remaining steps. A fresh preview can resume still-pending work after a certain
refusal. Once undo starts, the old job cannot be reapplied.

Repeated confirmation with the most recently consumed preview token returns
the receipt without dispatching again. Older consumed tokens are refused after
a newer operation. Neither web nor iOS queues or automatically retries writes.
If a response is lost, reopen/refresh the receipt before deciding what to do.

A crash, lost acknowledgement, malformed success, or failed read-back leaves
the action needing review. Its overlapping resource fields are quarantined
against other Safe Undo jobs. Inspect the external resource, then request an
**Accept observed state** preview. This shows whether each uncertain action
currently looks applied, not applied, or undone. Confirmation makes no external
writes; it records your acceptance of the observed state, **not proof that the
original request ran**. If the state matches neither side, reconciliation is
blocked and manual investigation is required.

Undo also refuses to pass a later, overlapping applied Safe Undo job; undo the
later job first. JSON cannot distinguish an outside edit that changes a field
and later returns it to the same value (the ABA problem). Text is stricter:
even restored identical bytes with a different ETag conflict, including after
another job changed and restored the document. In that case, use a new reviewed
change against current state, rather than bypassing the conflict guard.

## Storage and limits

Linux/macOS only. One process owns the local SQLite ledger; a second process
cannot open it concurrently. The ledger uses WAL with synchronous FULL,
persists write intent before dispatch, and quarantines interrupted intent on
restart. Keep its directory private on a durable local filesystem, not a
shared/network filesystem. Back up the whole gateway workspace consistently;
losing or reverting the ledger loses trustworthy recovery history. There is
no distributed coordinator across separate ledgers/gateway installations.

Receipts contain private before/after data in the workspace's `safe-undo.db`.
The file is mode 0600, **not encrypted by this feature**; use encrypted disks
and protected backups. List APIs omit contents, detailed responses use
`Cache-Control: no-store`, and resource URLs/tokens are never in tool results.
Only a minimal prepared-job receipt is returned to the model; its proposed
input may still appear in normal chat/tool history. Clients render values as
inert text and discard private review state when identity changes.

Limits: 64 configured resources; 8 changes per job; 32 allowlisted top-level
JSON fields per resource; 64 KiB per canonical JSON resource or UTF-8 document;
2 MiB per stored job; 2,000 jobs and 128 MiB of receipt payload per ledger;
16 confirmation attempts per job. Lists show the newest 100 receipts (older
known IDs remain fetchable). There is no automatic deletion of recovery
history. Hitting a limit stops new work; do not delete the ledger to get past it.
The operation ceiling is 45 seconds or the configured HTTP timeout, whichever
is shorter, and individual remote requests have an eight-second timeout.
Preview/prepare timing can therefore fail on slow resources or large batches.

## Product cleanup

Autopilot's raw device-command/JSON playground was removed. The phone's
explicit Device access settings, permission gates and agent-facing device
capabilities remain. The iOS label “Learning proposals” is now “Regression
checks”: the feature evaluates explicit checks; it does not magically retrain
an agent. Existing saved data, receipts and APIs were preserved.

Standards: [HTTP conditional requests](https://www.rfc-editor.org/rfc/rfc9110.html#name-if-match)
and [JSON Patch](https://www.rfc-editor.org/rfc/rfc6902.html).
