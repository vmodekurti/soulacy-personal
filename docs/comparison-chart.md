# Choosing an agent platform

Soulacy overlaps with agent frameworks, visual automation products, and personal
assistant gateways, but these products solve different layers of the stack. This
page compares their operating models rather than declaring a universal winner.

**Research date:** September 10, 2026. Claims below are linked to first-party
documentation or repositories. Product capabilities and packaging change, so
follow the links before making a purchasing or architecture decision.

## At a glance

| Product | Primary shape | Authoring model | Deployment and operations | Human oversight and security | Mobile and channel surface |
| --- | --- | --- | --- | --- | --- |
| **Soulacy** | Self-hosted agent runtime and control plane | Versionable `SOUL.yaml` plus Studio | One Go binary or container with GUI, runs, schedules, logs, diagnostics, and delivery | Confirmation approvals, capability policy, prompt-injection scanning, intent gate, audit, and production-readiness checks | Native iPhone companion plus HTTP, webhook, Telegram, Slack, Discord, WhatsApp, email, Teams, and Google Chat |
| **LangGraph** | Low-level orchestration framework/runtime | Python or JavaScript graphs and functions | Run in your application; LangSmith adds managed, hybrid, or self-hosted deployment and operations | Durable checkpoints and configurable interrupts support approve, edit, reject, and custom review flows | Client and channel experiences are composed by the application team |
| **CrewAI** | Multi-agent framework with Crews and Flows | Python; Crew Studio adds a visual/low-code surface in CrewAI AMP | Self-managed Python application or CrewAI AMP deployment and observability | Guardrails, event/state handling, and human-feedback patterns are framework/platform features | Integrations and end-user clients are selected by the application team |
| **OpenAI Agents SDK** | Code-first agent SDK | Agents, tools, handoffs, guardrails, sessions, and structured outputs in application code | Ships inside your application; built-in tracing records model, tool, handoff, and guardrail events | Input, output, and tool guardrails can block or replace calls; the application supplies its review UX | Client and channel experiences are application-defined |
| **Claude Agent SDK / Managed Agents** | Code SDK plus a managed agent execution service | Code for Agent SDK; versioned model, prompt, tool, MCP, and skill configuration for Managed Agents | Agent SDK is process-operated; Managed Agents supports Anthropic-managed or self-hosted workers | Permission policies support allow/ask decisions and tool-confirmation events; custom-tool enforcement remains the application's responsibility | Client and channel experiences are application-defined |
| **Dify** | Visual LLM application platform | Visual agents, Chatflows, Workflows, knowledge, and plugins | Dify Cloud or a multi-service self-hosted Docker Compose stack; includes logs and monitoring surfaces | Workflow Human Input nodes can pause for review; moderation and provider controls are configurable | Published web apps, APIs, and embedding are first-class surfaces |
| **n8n** | Visual workflow automation platform with AI nodes | Node canvas, expressions, code nodes, and AI Agent nodes | n8n Cloud or self-hosting through npm, Docker, Compose, and cloud guides; execution history and debugging are built in | Selected AI tools can require human approval through a configured review channel | Delivery is assembled from workflow integrations and front ends |
| **Flowise** | Visual platform for agents and LLM workflows | Assistant, Chatflow, and Agentflow builders | Self-hosted or deployed across common cloud/container platforms; tracing, analytics, and evaluations are built in | Agentflow checkpoints support durable human input and per-tool approval | APIs, SDKs, and an embedded chatbot are documented product surfaces |
| **OpenClaw** | Self-hosted personal-assistant gateway | CLI/onboarding, gateway configuration, skills, plugins, and Control UI | A long-running Node.js gateway/daemon owns sessions, auth, channels, and state | Pairing, allowlists, tool policy, sandboxing, exec approvals, and `openclaw security audit`; its documented trust model is one trusted boundary per gateway | Broad chat-channel catalog plus official iOS and Android companion nodes |

## What the categories mean

### Frameworks: LangGraph, CrewAI, and the model-vendor SDKs

Frameworks give software teams composable primitives and leave substantial
product assembly to the adopter. LangGraph explicitly positions itself as a
low-level runtime for long-running, stateful agents and documents
[durable execution, streaming, memory, and human-in-the-loop](https://docs.langchain.com/oss/python/langgraph/overview).
Its persistence layer supports checkpoints, fault recovery, replay/time travel,
and review; its interrupt model can
[pause and resume a run for approve, edit, or reject decisions](https://docs.langchain.com/oss/python/langchain/human-in-the-loop).
[LangSmith deployment](https://docs.langchain.com/oss/python/langgraph/deploy)
adds managed, hybrid, and self-hosted operational choices.

CrewAI centers on
[Crews and Flows](https://docs.crewai.com/en/concepts/agents) in Python.
Its documentation describes guardrails, memory, knowledge, and observability as
framework capabilities, while
[CrewAI AMP](https://docs-platform.crewai.com/platform/en/introduction) adds managed
deployment, monitoring, and Crew Studio. This makes CrewAI a closer comparison
for code-first multi-agent applications than for a packaged private gateway.

The [OpenAI Agents SDK](https://openai.github.io/openai-agents-python/agents/)
provides agents, tools, handoffs, sessions, and structured outputs. It includes
[input, output, and tool guardrails](https://openai.github.io/openai-agents-python/guardrails/)
and [built-in tracing](https://openai.github.io/openai-agents-python/tracing/),
but the surrounding application, deployment, end-user UI, and channel adapters
remain product decisions for the adopter.

Anthropic now documents two related shapes. The Claude Agent SDK is operated as
part of an application, while
[Managed Agents can use Anthropic-managed or self-hosted workers](https://platform.claude.com/docs/en/managed-agents/reference).
Managed Agent versions capture the
[model, prompt, tools, MCP servers, and skills](https://platform.claude.com/docs/en/managed-agents/agent-setup),
and [permission policies](https://platform.claude.com/docs/en/managed-agents/permission-policies)
can always allow or ask before supported tool use. Anthropic explicitly notes
that applications remain responsible for enforcing permissions around their
custom tools.

### Visual platforms: Dify, n8n, and Flowise

Dify is a broad visual platform rather than only a workflow library. Its
[open-source repository](https://github.com/langgenius/dify) lists visual
workflow building, agents, RAG, model management, observability, APIs, and
plugins. It offers cloud hosting and a
[Docker Compose self-hosting path](https://docs.dify.ai/en/self-host/deploy/quick-start/docker-compose).
Current Dify documentation also includes
[workflow history and logs](https://docs.dify.ai/en/self-host/use-dify/debug/history-and-logs)
and a [Human Input node](https://docs.dify.ai/en/self-host/use-dify/nodes/human-input).

n8n is primarily a general workflow-automation product with AI integrated into
its node canvas. Official documentation describes
[Cloud and self-hosted options](https://docs.n8n.io/choose-how-to-use-n8n.md),
[npm, Docker, Compose, and cloud-provider installation paths](https://docs.n8n.io/deploy/host-n8n/install-options.md),
an [AI Agent node](https://docs.n8n.io/integrations/builtin/cluster-nodes/root-nodes/n8n-nodes-langchain.agent.md),
[human approval for selected AI tools](https://docs.n8n.io/build/integrate-ai/ai-examples/human-in-the-loop-for-tools.md),
and [execution debugging](https://docs.n8n.io/build/understand-workflows/understand-executions/debug-executions.md).

Flowise focuses on visual generative-AI construction. Its
[official overview](https://docs.flowiseai.com/) lists Assistant, Chatflow, and
Agentflow builders alongside tracing, analytics, evaluations, human-in-the-loop,
APIs, SDKs, and an embedded chatbot. Agentflow V2 documents
[checkpointed human input and tool approval](https://docs.flowiseai.com/using-flowise/agentflowv2),
and Flowise provides several
[deployment targets](https://docs.flowiseai.com/configuration/deployment).

### Personal-assistant gateway: OpenClaw

OpenClaw is the closest product-shape comparison in this list. It runs a
self-hosted gateway that owns sessions, authentication, channels, and state;
its [getting-started guide](https://docs.openclaw.ai/getting-started) installs a
Node.js gateway/daemon and opens a browser Control UI.

It has a substantial first-party channel and device story:

- The [channel catalog](https://docs.openclaw.ai/channels) includes Telegram,
  Slack, Discord, WhatsApp, Google Chat, iMessage, SMS, WebChat, and more.
- The [iOS companion](https://docs.openclaw.ai/platforms/ios) connects to a
  gateway, exposes opt-in device capabilities, caches recent conversations, and
  supports durable offline message queuing.
- The [Android companion](https://docs.openclaw.ai/platforms/android) supports
  gateway pairing and review of pending command approvals.

OpenClaw also documents security controls rather than leaving them implicit:
[pairing and allowlists, tool policies, and sandboxing](https://docs.openclaw.ai/security),
[exec approvals](https://docs.openclaw.ai/tools/exec-approvals), and a
[structured security audit](https://docs.openclaw.ai/gateway/security/running-the-audit).
Its documentation is equally explicit about the boundary: OpenClaw assumes one
trusted operator or team per gateway and
[does not claim hostile multi-tenant isolation](https://docs.openclaw.ai/gateway/security/trust-model).

## Where Soulacy is different

Soulacy packages a self-hosted runtime, versionable agent definition, browser
control plane, and native iPhone operations surface together. The goal is to
operate multiple private agents without first building the deployment console,
run history, schedules, approvals, delivery adapters, and production diagnostics
around a framework.

The differentiators documented in this project are:

- A reviewable `SOUL.yaml` remains the canonical agent artifact, with
  [Studio](using/studio.md) providing a guided authoring surface.
- The [security stack](security/index.md) combines capability scope,
  confirmation approvals, untrusted-content handling, prompt-injection signals,
  an intent gate, audit records, SSRF controls, and launch-readiness checks.
- The [channel runtime](channels/index.md) includes HTTP, webhooks, Telegram,
  Slack, Discord, WhatsApp, email, Teams, and Google Chat, with mapping and
  delivery diagnostics in the same control plane.
- [Soulacy for iPhone](channels/mobile.md) is an operator and delivery client: it can
  connect to a remote gateway, chat, review approvals, watch runs, inspect
  activity, and receive mobile-channel output.
- The distribution can run as a single binary or container on a laptop, private
  host, or VPS; operators still own TLS, host security, backups, credential
  rotation, and network policy.

OpenClaw is stronger when the desired product is a personal assistant spanning a
large catalog of chat networks and device-node capabilities. Visual platforms are
stronger when a canvas and connector ecosystem are the center of the job.
Frameworks are stronger when agent behavior is embedded deeply in a custom
software product. Soulacy is aimed at teams that want a private, multi-agent
operations layer with versionable configuration and conservative production
checks.

## How to evaluate any option

Run one representative task end to end before adopting a platform:

1. Use the actual model provider, tool, trigger, and delivery destination.
2. Exercise both least-privileged and high-impact actions.
3. Test a human-review pause and confirm the run resumes after restart.
4. Test bad credentials, unavailable providers, timeouts, duplicate delivery,
   and an unreachable channel.
5. Confirm what is retained in logs, traces, chat history, and audit records.
6. Verify backup, restore, upgrade, and rollback on the intended host.
7. Test the real phone or chat client over the same network path users will use.

For Soulacy, start with the [Quick Start](getting-started/quickstart.md), then use
[Studio](using/studio.md) to build that representative agent.

## Primary sources

- **Soulacy:** [security](security/index.md), [channels](channels/index.md),
  [Studio](using/studio.md), [iPhone app](channels/mobile.md)
- **LangGraph / LangSmith:** [overview](https://docs.langchain.com/oss/python/langgraph/overview),
  [human-in-the-loop](https://docs.langchain.com/oss/python/langchain/human-in-the-loop),
  [deployment](https://docs.langchain.com/oss/python/langgraph/deploy)
- **CrewAI:** [agents](https://docs.crewai.com/en/concepts/agents),
  [CrewAI AMP](https://docs-platform.crewai.com/platform/en/introduction)
- **OpenAI:** [Agents SDK](https://openai.github.io/openai-agents-python/agents/),
  [guardrails](https://openai.github.io/openai-agents-python/guardrails/),
  [tracing](https://openai.github.io/openai-agents-python/tracing/)
- **Anthropic:** [Managed Agents reference](https://platform.claude.com/docs/en/managed-agents/reference),
  [agent setup](https://platform.claude.com/docs/en/managed-agents/agent-setup),
  [permission policies](https://platform.claude.com/docs/en/managed-agents/permission-policies)
- **Dify:** [repository](https://github.com/langgenius/dify),
  [self-hosting](https://docs.dify.ai/en/self-host/deploy/quick-start/docker-compose),
  [history and logs](https://docs.dify.ai/en/self-host/use-dify/debug/history-and-logs)
- **n8n:** [deployment choices](https://docs.n8n.io/choose-how-to-use-n8n.md),
  [AI Agent node](https://docs.n8n.io/integrations/builtin/cluster-nodes/root-nodes/n8n-nodes-langchain.agent.md),
  [human approval](https://docs.n8n.io/build/integrate-ai/ai-examples/human-in-the-loop-for-tools.md)
- **Flowise:** [overview](https://docs.flowiseai.com/),
  [Agentflow V2](https://docs.flowiseai.com/using-flowise/agentflowv2),
  [deployment](https://docs.flowiseai.com/configuration/deployment)
- **OpenClaw:** [getting started](https://docs.openclaw.ai/getting-started),
  [channels](https://docs.openclaw.ai/channels),
  [iOS](https://docs.openclaw.ai/platforms/ios),
  [security](https://docs.openclaw.ai/security),
  [trust model](https://docs.openclaw.ai/gateway/security/trust-model)
