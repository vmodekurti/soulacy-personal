# Release an agent only after checking it

**Useful for:** changing a report agent without routing all future work to an
untested version. Autopilot keeps a versioned definition, run receipts, explicit
checks, and promotion gates. It does not independently establish business truth.

**Before starting:** use a separate test agent with no write tools, a working
model, and access to **Autopilot**. Finish the
[notes-to-action-plan exercise](notes-to-action-plan.md) first. Simulations still
use inference and may cost money.

## 1. Make the acceptance criterion concrete

For a notes assistant, start with a deliberately modest claim:

> Produce an action plan from supplied notes and include an Open questions section.

The output marker checks formatting only. The agent could include that heading
and still invent every task. You will review the answer manually as well.

## 2. Save a draft, without changing live routing

Open **Autopilot → Releases → Design a mission**. Enter:

| Field | Example |
|---|---|
| Agent | Your dedicated notes test agent |
| Version label | `notes-format-v2` |
| Mission outcome | `Summarize supplied notes, preserve unknowns, and include open questions.` |
| Required output text | `Open questions` |
| Inference budget (USD) | `0.25` as an illustrative ceiling, not a cost estimate |
| Maximum duration | `2m` |
| Restrict tools to an exact allowlist | Checked; leave the list empty to block every tool |

Choose **Save immutable draft** and confirm. The saved definition is a snapshot.
Editing the ordinary agent afterward does not rewrite that snapshot; make a
new candidate when the instructions need to change.

## 3. Simulate and inspect the proof

In **Test input**, paste the fictional project notes from the first exercise.
Choose **Simulate** on this version. Tool execution is disabled, not mocked into
real success. Open the returned proof and inspect checks, outcome, duration,
and output. A simulation does not count as a verified production sample.

If the marker is missing or the model fails, correct the agent definition and
create another immutable draft. Do not weaken a meaningful check just to get
a green label.

## 4. Run the candidate with real execution limits

Choose **Run candidate**. This is a real run with the candidate's policy; on
an agent with tools, those tools can execute. Our example denies all tools.
Check that the proof is a non-simulation, successful, verified run and that
its revision matches your intended draft.

Also test missing owners, contradictory deadlines, and empty notes. Read each
answer. Output markers cannot substitute for these quality checks.

## 5. Decide whether to promote

**Canary 10%** changes routing for this owner's runs to this agent. It requires
a passing simulation and at least one successful verified real sample. Do not
press it while other people rely on the test agent.

**Promote stable** requires the canary stage and five successful verified real
samples. Collect varied representative samples—not five identical requests
solely to satisfy the counter. The gateway is the authority on gate status and
may reject promotion; read the stated reasons.

Promoting an agent version does **not** deploy a new gateway binary, website,
or iOS app. Those are separate releases.

## 6. Plan recovery before live use

If there is a previous stable version, inspect its identity before using the
release rollback action. Rollback changes which version handles future runs;
it does not reverse emails, file changes, or other past effects. For a first
release without a previous stable version, do not assume rollback has a target.
Keep the exercise isolated and disable the test agent if it should stop running.

| Test | Expected boundary |
|---|---|
| Promote before evidence exists | The server refuses and explains unmet gates |
| Candidate tries a denied tool | No execution; the failure is recorded |
| Model times out or budget is exhausted | No fabricated completion; inspect the failure |
| Proof says business outcome is unknown | Treat it as unknown; formatting success does not make it true |
| A run is interrupted | Review its uncertain/incomplete state before repeating side effects |

For richer YAML checks and bounded task teams, see [Verified Autopilot](../using/autopilot.md).
