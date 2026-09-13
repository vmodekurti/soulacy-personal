# Safe Undo: reassign an account without losing later notes

**The concrete example:** move a fictional account from Maya to Leo and change
its status from `pending` to `active`. If you change your mind, restore only
those two fields while preserving an unrelated note added by someone else.

This example uses an **existing compatible record endpoint**, not a built-in
Salesforce, HubSpot, or arbitrary website connector. If you have not configured
one, you can read the walkthrough but cannot execute it yet.

## 1. Have the operator prepare a disposable resource

Before touching real customer data, the server owner must complete the
[Safe Undo integration checklist](../SAFE_UNDO.md#operator-setup). The service
must support strong version tags and atomic conditional JSON Patch, including
field tests. Simply turning on `conditional_writes` does not add this support
to an incompatible backend.

Use the reference resource ID `customer`, agent `account-assistant`, and allowed
fields `status` and `owner`. The resource URL must identify **one test record**.
Its initial body is:

```json
{"status":"pending","owner":"Maya","notes":"Demo requested"}
```

The operator supplies a narrowly scoped service credential through the server
environment. Never paste that credential into chat. Enable only the preparation
tools `safe_undo.resources` and `safe_undo.prepare` for the agent.

**Checkpoint:** sign in as the intended owner, open **Autopilot → Safe Undo**,
select `account-assistant`, and confirm the supported resource appears. An empty
list or “unavailable” means setup/authorization is incomplete. Do not bypass it
with a generic HTTP or shell tool.

## 2. Ask for a plan, not a write

```text
Using Safe Undo resource customer, prepare this change:
set owner to Leo and status to active. Leave notes and all other fields alone.
Do not apply anything. I want to review the before/after values first.
```

The agent may read and prepare. It cannot confirm Apply or Undo for you.
Open the new receipt in **Autopilot → Safe Undo** (or the agent's Safe Undo
screen on iPhone), then choose **Preview: Apply changes**.

Review this expected diff:

| Field | Before | After |
|---|---|---|
| owner | Maya | Leo |
| status | pending | active |
| notes | No proposed change | No proposed change |

Wrong record, wrong value, or extra field? Stop. Prepare a corrected job rather
than approving a “close enough” plan. Previews expire after five minutes; get a
fresh one if needed.

## 3. Confirm and verify the write

Read the partial-failure acknowledgment, tick the review box, then confirm
**Apply changes**. On iPhone, satisfy the local authentication prompt as well.

Read the receipt **and** independently inspect the record in its source system.
Expected body:

```json
{"status":"active","owner":"Leo","notes":"Demo requested"}
```

A network timeout does not prove nothing happened. If the receipt is uncertain,
stop here and follow the uncertainty procedure below—do not prepare a duplicate.

## 4. Try an unrelated edit, then undo

In the disposable source system, have a person change only `notes` to
`Demo booked for Wednesday`. Keep owner/status unchanged.

Return to the **same job**, choose **Preview: Undo changes**, inspect the diff,
acknowledge it, and confirm **Undo changes**. Expected final record:

```json
{"status":"pending","owner":"Maya","notes":"Demo booked for Wednesday"}
```

The original owner/status return; the later note stays. This is a targeted
conditional reversal, not restoration of an entire old record snapshot.

## 5. Test the stop conditions

Use separate fresh test jobs for these cases:

| What happens after Apply | Expected behavior |
|---|---|
| Someone changes owner from Leo to Priya | Undo detects an overlapping change and refuses to overwrite Priya |
| Only the unrelated notes field changes | Undo can preserve notes while restoring owner/status if the endpoint contract still holds |
| Preview sits open beyond five minutes | Confirmation requires a new preview |
| A write succeeds but its response is lost | Receipt becomes uncertain; automatic replay is not treated as safe |
| One resource in a multi-resource job fails | Inspect each result; the job is not a cross-system atomic transaction |
| An email/notification was triggered downstream | Safe Undo cannot unsend it; the integration should not have hidden irreversible effects |

### When a receipt is uncertain

Inspect the external resource directly with the operator. **Accept observed
state** previews and records what exists now; it does **not** perform a repair,
prove the original request succeeded, or undo a hidden side effect. Confirm it
only after you understand the observation. Do not delete the ledger or blindly
retry to make a warning disappear.

## Where this is a good fit—and where it is not

Good fit: a reviewed top-level field change in a compatible internal record
service, or an existing small UTF-8 document on a conditional WebDAV service.
For text documents, any intervening version change can block Undo; the JSON
field-preservation example above is not a promise of text merging.

Not a fit: sending email, purchases, deleting arbitrary files, arbitrary shell
commands, or a service without atomic conditional writes. Safe Undo cannot
detect every history pattern, such as a field changed away and back again.
See [the full safety contract](../SAFE_UNDO.md) before production use.
