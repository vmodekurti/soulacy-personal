# Worked use cases

These are end-to-end exercises, not a list of things an agent might someday do.
Each gives prerequisites, sample input, a checkable result, and failure cases.
All sample people, policies, and records are fictional.

Start with **one** read-only example. Add scheduling, memory, or write access
only after its manual version behaves correctly.

| Use case | Requires | Writes outside Soulacy? | Walkthrough |
|---|---|---|---|
| Notes → action plan | One working model | No | [Beginner exercise](notes-to-action-plan.md) |
| Handbook questions with sources | Embedding provider and knowledge base | No | [Grounded answers](handbook-answers.md) |
| Briefing on an iPhone | Paired phone and mobile destination | Delivers a message | [Generation → delivery → push](morning-brief.md) |
| Remember a report format | Learning-enabled agent and reviewer | Saves an approved lesson locally | [Teach, approve, test, disable](teach-a-preference.md) |
| Reassign an account, then undo | Configured conditional-write endpoint | Yes, only reviewed fields | [Account handoff](safe-undo-handoff.md) |
| Promote an agent revision | Autopilot and explicit checks | Changes future routing | [Verified release](verified-release.md) |

## Choose the right mechanism

- **“Use the facts in these documents.”** Use a knowledge base, not a permanent
  personal preference containing the whole handbook.
- **“Use this format in future reports.”** Use an approved Notebook preference.
- **“Run this every morning.”** Use a schedule after a successful manual run.
- **“Show me what will change before writing.”** Use Safe Undo only if your
  integration meets its conditional-write requirements.
- **“Show me evidence before promoting an agent.”** Use mission checks and
  candidate runs. A formatting check does not verify real-world truth.

## Common acceptance checklist

Before using real data, try normal, missing, and contradictory input, a denied
permission, and an unavailable dependency. Confirm the result **and** the run
record. Never retry a possibly completed external write blindly.
The [first-checks guide](../troubleshooting/first-checks.md) explains where to look.
