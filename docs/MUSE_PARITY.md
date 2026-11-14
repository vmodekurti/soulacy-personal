# Soulacy and Meta Muse parity

Baseline reviewed November 5, 2026. This compares the consumer Muse personal
agent with Soulacy Personal at local main `8628936c` plus the reliability patch
`b13bd02` and this branch’s guided goal editor. Soulacy findings are based on
source and product documentation. Meta findings describe publicly documented
behavior. A source match alone does not establish live interoperability or
equal quality.

## Capability and acceptance matrix

| Capability | Meta Muse | Soulacy today | What would establish parity |
| --- | --- | --- | --- |
| Chat and channels | Persistent main chat, side chats, simultaneous requests, web and mobile, and WhatsApp | Web chat sessions, native iPhone surface, Android push support, and a configurable WhatsApp adapter | Start a project in one channel and continue in another under the same identity, including an overlapping task and its approval |
| Goals and plans | Conversational goal decomposition, progress tracking, and ongoing follow-up | [Autopilot goals](using/autopilot.md) have ordered or branching agent tasks, budgets, proofs, progress, and cancellation. The guided editor creates ordered drafts | Ask in chat for a goal, review and edit its generated plan, approve it, then track its progress and see what happens next |
| Background execution | Keeps working after the app closes, on schedules and events, and follows up when appropriate | [Schedules](using/schedules.md), [person triggers](using/person-triggers.md), and server-side goals. Interrupted goal work requires review | Run a goal across a gateway restart and recover from an interrupted external write without duplicate side effects |
| Proactive suggestions | Onboarding tips, an Ideas view, and suggested changes based on observations | Person signals, commitments, configurable triggers, and feed delivery | Surface useful, evidence-linked ideas for an active goal with delivery preferences, dismissal, and suppression of repeats |
| Memory and personalization | Persistent memory with user inspection, editing, and selective forgetting | [Adaptive memory](using/adaptive-memory.md), [person model](using/person-model.md), and a Memory page | Teach a fact in chat, inspect and correct it, forget it, and verify that later sessions across channels stop retrieving it |
| Browser and computer use | Dedicated browser, filesystem, and terminal used for multistep work | Managed Chromium, browser traces, and sandboxed tool execution | Complete representative authenticated browser workflows with user handoff and resumable approval, then verify the receipt |
| Connectors and credentials | Revocable app access, credential entry separated from the agent, and a connector platform | OAuth connections, MCP, permission checks, and secrets. Coverage varies by connector and action | Demonstrate read-only grants, separate write grants, revocation, and credential non-disclosure on each supported provider |
| Approvals and activity | Structured approvals and planned and completed activity history | Approval cards, run proofs, activity and audit records. Required approvals fail closed without a confirmation path | In a background run, present the exact sensitive action, resume only the approved action once, and show the decision and result |
| Isolation and network policy | Per-person Secure VM and a system-separated Sentinel for outbound activity | [Sandbox](security/sandbox.md), network policy, intent and injection controls, and scoped credentials | Independently verify per-user isolation and enforced mediation of browser, terminal, and connector egress, including bypass and cross-user tests |
| Shopping, travel, and payments | Form completion and booking, plus Stripe Link checkout with purchase protections | Browser, connectors, and approvals provide primitives. An equivalent protected checkout was not established | Show the final merchant, items and amount for approval, pay with delegated credentials, and reconcile a real or sandboxed receipt. Provider protections require an eligible integration |
| Artifacts | Chat-linked documents, PDFs, web pages, and interactive outputs | [Published files](using/published-files.md), workboard artifacts, and output contracts | Generate, preview, update, and share representative documents and interactive artifacts with tested permissions |
| Voice and avatar | Interruptible real-time voice and expressive avatars rolling out on mobile, web, and Mac | [Voice chat](using/voice.md) supports local speech and an explicitly configured realtime provider | Test interruption, latency, simultaneous background work, and configurable avatar behavior across supported clients |
| Meta ecosystem | Native Meta surfaces and content, with additional devices announced | Configured channel adapters and independent integrations | Demonstrate equivalent workflows where APIs and account access permit. Native Meta distribution and announced hardware require separate scope and dependencies |
| Model quality | Meta’s hosted Muse model stack | Configurable model providers | Run both products against the same consented task set and measure success, policy adherence, latency, and cost |

## Delivery order

1. Ship the guided goal workflow and validate it against the existing Autopilot
   API. Then add reviewable chat-to-goal drafting and owner-visible progress.
2. Make long-running goals recoverable with durable checkpoints, scoped
   approvals, receipts, and idempotent writes. Test restart and revocation.
3. Exercise browser and connector workflows against the isolation boundary.
   Establish separate outbound enforcement before enabling unattended
   transactions.
4. Extend cross-channel continuity, proactive ideas, artifacts, voice, and
   shopping against the acceptance scenarios above.

Meta announced a user-key Confidential VM for later release. It is tracked
separately from the Secure VM baseline pending confirmed availability. A
percentage requires a fixed set of scenarios, accounts and regions, a
weighting rule, and observed pass results for both products.

## Sources

- [Meta Muse launch and security overview](https://about.fb.com/news/2026/09/introducing-muse-personal-ai-agent/)
- [How Meta designed Muse](https://muse.ai/blog/how-we-designed-muse)
- [Meta Muse voice and avatar rollout](https://about.fb.com/news/2026/11/introducing-muse-ai-voice-expressive-avatars/)
- [Muse connector guidelines](https://platform.muse.ai/docs)
