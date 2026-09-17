# An assistant that knows you, on a gateway you control

Soulacy keeps a structured picture of how your days go — your routine, the
people who matter, what you owe and when — and every agent you run reads the
same one. A model supplies the reasoning; Soulacy supplies the picture, the
tools, the permissions, the scheduling, and the record of what happened.

It learns **by asking**. A few questions in the web workspace on your first
day are enough for agents to stop starting from nothing. The iPhone companion
is optional: pair it and allow a sense, and Soulacy keeps up on its own;
skip it and everything still works from what you have told it.

Start with a small, read-only task. Make it reliable before giving it more access.

These guides cover **Soulacy Personal**, the open-source, self-hosted edition.
See [how your Personal setup works](personal.md) to understand the gateway,
model, and companion-client requirements.

[Start here: your first successful run](getting-started/quickstart.md){ .md-button .md-button--primary }
[What it learns about you](using/person-model.md){ .md-button }
[Pick a worked use case](use-cases/index.md){ .md-button }
[See what your iPhone adds](iphone.md){ .md-button }

!!! info "Match these guides to your installation"
    These docs follow the Personal repository's `main` branch. Your installed
    gateway or TestFlight build may be older. Check `sy version` on the server
    and the version in the iPhone app before expecting a new screen. Source
    availability is not evidence that your server has been upgraded.
    See [recent changes](recent-updates.md) and [safe upgrades](deployment/upgrades.md).

## Choose your starting point

| You want to… | Follow this guide | What you will have at the end |
|---|---|---|
| Try Soulacy for the first time | [First successful run](getting-started/quickstart.md) | A model connection and a response you can check yourself |
| Use your gateway from an iPhone | [Connect your iPhone](getting-started/iphone.md) | A paired phone and a verified round-trip chat |
| Let agents use your phone's signals | [Soulacy on iPhone](iphone.md) | Lock-screen approvals, declared device tools, Siri and CarPlay, and a brief built from health, calendar, and Focus |
| Turn messy notes into something useful | [Notes → action plan](use-cases/notes-to-action-plan.md) | Owners, next actions, and explicit unknowns without sending anything |
| Answer questions from your documents | [Grounded handbook answers](use-cases/handbook-answers.md) | A searchable knowledge base and a tested “not found” response |
| Get a briefing on your phone | [A morning briefing](use-cases/morning-brief.md) | Separate checks for generation, scheduled delivery, and notifications |
| Teach an agent your report format | [Teach a preference](use-cases/teach-a-preference.md) | An approved lesson tested in a fresh conversation |
| Review and reverse a record change | [Safe Undo: account handoff](use-cases/safe-undo-handoff.md) | A field-by-field preview, safe apply, and conflict-aware undo |
| Test a revision before live use | [Release a checked agent](use-cases/verified-release.md) | A candidate with explicit checks and a staged promotion decision |

## Five words worth knowing

- **Genie:** the agent you talk to. It answers questions, and builds the agents
  that answer them again later. See [talking to Genie](using/genie.md).
- **Gateway:** the running Soulacy server. Closing the browser does not stop a
  server, but turning off the computer hosting it does.
- **Agent:** saved instructions plus a model, permitted tools, and optional
  memory or schedule. Its definition is a `SOUL.yaml` file.
- **Run:** one attempt to carry out a request. A reply is not automatically proof
  that an external action succeeded.
- **Tool:** an operation an agent can request, such as searching a knowledge
  base. Knowing about a tool does not grant permission to use it.

## What is automatic, and what needs your decision?

| Capability | What Soulacy does | What you still control |
|---|---|---|
| [Model preparation](using/model-preparation.md) | Reads capability metadata and prepares an execution approach | Model, goal, permissions, required output, and budget |
| [Learning Notebook](using/learning-notebook.md) | Proposes and retrieves source-backed lessons | Approval, rejection, disabling, and testing whether a lesson helps |
| [Verified Autopilot](using/autopilot.md) | Records checks and enforces release gates | Meaningful criteria and whether to route live work |
| [Safe Undo](SAFE_UNDO.md) | Tracks reviewed, conditional changes to configured resources | Integration setup, Apply/Undo confirmation, and resolving uncertainty |
| [Published files](using/published-files.md) | Lists and previews a narrow output folder, read-only | Which agent/folder is exposed and which documents belong there |

“Verified” means a run met its configured checks. It does not mean every fact is
true, every model is equally capable, or every external effect can be reversed.
Safe Undo is not a universal undo button for email, purchases, or shell commands.

## Where your information goes

Self-hosted does not automatically mean fully offline. A cloud model receives
the prompts and tool content sent to it; external search and plugins contact
their own services. Use approved providers and non-sensitive sample data while
learning. Keep keys, pairing QR codes, and backups private.
See [security](security/index.md) and [authentication](configuration/auth.md).

## Need a different kind of help?

- **Something failed:** [Find the failing step](troubleshooting/first-checks.md).
- **Building an agent:** ask [Genie](using/genie.md). If you would rather write
  it yourself: [first-agent walkthrough](getting-started/first-agent.md) and
  [complete schema](agents/soul-yaml.md).
- **Working out what your deployment can do:**
  [capabilities and limits](configuration/deployment.md) — particularly on a
  platform where you have no shell.
- **Operating a server:** [configuration](configuration/index.md),
  [cloud setup](deployment/cloud.md), and [upgrades](deployment/upgrades.md).
- **Contributing:** [documentation and validation checklist](contributing/documentation.md).

The Personal edition is self-hosted and Apache-2.0 licensed. Provider access,
hosting, and external services may cost money. The iPhone app needs a reachable
gateway. There is no claim of complete parity with another framework or of
guaranteed, bug-free automation.
