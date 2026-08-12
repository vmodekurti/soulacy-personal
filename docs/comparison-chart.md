# Choosing Soulacy

Soulacy is a self-hosted runtime and control plane for YAML-defined agents. It
overlaps with automation builders and agent frameworks, but it is not trying to
replace every one of them. Use this page to decide whether its operating model
fits your project.

## Product-shape comparison

| Need | Soulacy | Visual automation platforms | Agent-framework libraries |
| --- | --- | --- | --- |
| Primary artifact | One `SOUL.yaml` per agent | A visual workflow/project export | Application code and graph/state definitions |
| Typical operator | Developer or operator who wants UI plus versionable config | Automation builder working primarily in a canvas | Software team building a custom agent application |
| Deployment | One binary or container; local/VPS friendly | Usually a hosted service or multi-service container deployment | Your application deployment |
| Built-in operations | GUI, chat, channels, schedules, logs, runs, memory, skills, and plugins | Product-specific workflow operations | Supplied by the application team or adjacent services |
| Execution style | Tool-calling agents by default; fixed workflows are experimental | Explicit node graphs | Code-defined loops, graphs, and state machines |
| Provider choice | Registered local or cloud providers selected per agent | Product-specific integrations | Whatever the application integrates |
| Best fit | Shipping and operating multiple self-hosted agents without building a control plane | Business-process automation centered on connectors and canvases | Deeply custom agent behavior embedded in a software product |

This is a comparison of product shapes, not a universal feature score. n8n,
Flowise, Dify, LangGraph, and similar projects evolve independently; consult
their official documentation before making a purchasing or architecture
decision.

## Choose Soulacy when

- You want agents represented as reviewable YAML rather than only in a database
  or application code.
- You need the runtime and its GUI to work on a laptop, VPS, or private network.
- You want channels, scheduling, provider registration, runs, approvals,
  memory, skills, MCP, and diagnostics in the same distribution.
- You prefer conversational/tool-calling agents, with explicit workflows only
  when a fixed graph is truly required.
- You need local models and cloud providers to coexist.

## Choose a visual automation platform when

- The canvas is the main authoring experience for the people building flows.
- Your work is primarily deterministic connector-to-connector business
  automation.
- A hosted control plane or a larger container stack is acceptable.
- Your organization already operates the platform and its connector ecosystem.

Examples and official documentation:

- [n8n self-hosting](https://docs.n8n.io/hosting/)
- [Flowise deployment](https://docs.flowiseai.com/configuration/deployment)
- [Dify self-hosting](https://docs.dify.ai/getting-started/install-self-hosted)

## Choose an agent framework when

- The agent is a component inside a larger application you are writing.
- You need code-level control over state, persistence, retry, graph execution,
  or distributed orchestration.
- Your team is prepared to build the product UI, authentication, channels,
  operations, and deployment around the framework.

For example, [LangGraph](https://docs.langchain.com/oss/python/langgraph/overview)
focuses on low-level orchestration and durable execution for stateful agents.

## Where Soulacy is deliberately conservative

- It is not a hosted SaaS.
- It is not a general-purpose low-code integration catalog.
- Studio does not generate fixed workflows by default. Workflow generation is
  experimental in v0.1.8 and requires explicit user opt-in.
- It does not make every model equally reliable. Provider/model compatibility,
  tool-calling quality, context limits, and latency still matter.
- Self-hosting does not remove the operator's responsibility for host security,
  backups, credential rotation, and access control.

## Evaluate with a real task

Before adopting any platform, implement one representative task end to end:

1. Register the actual model provider you intend to use.
2. Configure the real trigger and delivery destination.
3. Exercise the least and most privileged tool calls.
4. Test failure behavior: bad credentials, unavailable provider, invalid
   destination, timeout, and restart.
5. Verify that logs and artifacts are sufficient to diagnose the run.
6. Confirm backup, upgrade, and rollback on the intended deployment host.

Start with the [Quick Start](getting-started/quickstart.md), then use
[Studio](using/studio.md) to build the representative agent.
