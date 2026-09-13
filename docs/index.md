# Put your first agent to useful work

Soulacy runs agents on a gateway you control. Use the web workspace to create
and supervise them, and the iPhone companion to chat, review work, and approve
supported actions. A model supplies the reasoning; Soulacy supplies tools,
permissions, scheduling, memory, and records of what happened.

Start with a small, read-only task. Make it reliable before giving it more access.

These guides cover **Soulacy Personal**, the open-source, self-hosted edition.
**Soulacy Commercial is SaaS**. See [Personal and Commercial](editions.md) before
following server-installation steps for a hosted account.

[Start here: your first successful run](getting-started/quickstart.md){ .md-button .md-button--primary }
[Pick a worked use case](use-cases/index.md){ .md-button }

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
| Turn messy notes into something useful | [Notes → action plan](use-cases/notes-to-action-plan.md) | Owners, next actions, and explicit unknowns without sending anything |
| Answer questions from your documents | [Grounded handbook answers](use-cases/handbook-answers.md) | A searchable knowledge base and a tested “not found” response |
| Get a briefing on your phone | [A morning briefing](use-cases/morning-brief.md) | Separate checks for generation, scheduled delivery, and notifications |
| Teach an agent your report format | [Teach a preference](use-cases/teach-a-preference.md) | An approved lesson tested in a fresh conversation |
| Review and reverse a record change | [Safe Undo: account handoff](use-cases/safe-undo-handoff.md) | A field-by-field preview, safe apply, and conflict-aware undo |
| Test a revision before live use | [Release a checked agent](use-cases/verified-release.md) | A candidate with explicit checks and a staged promotion decision |

## Four words worth knowing

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
- **Writing YAML:** [first-agent walkthrough](getting-started/first-agent.md) and
  [complete schema](agents/soul-yaml.md).
- **Operating a server:** [configuration](configuration/index.md),
  [cloud setup](deployment/cloud.md), and [upgrades](deployment/upgrades.md).
- **Contributing:** [documentation and validation checklist](contributing/documentation.md).

The Personal edition is self-hosted and Apache-2.0 licensed. Provider access,
hosting, and external services may cost money. The iPhone app needs a reachable
gateway. There is no claim of complete parity with another framework or of
guaranteed, bug-free automation.
