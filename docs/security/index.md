# Security Overview

Soulacy is self-hosted, but self-hosted does not automatically mean safe. The
operator still controls the host, provider accounts, channel bots, credentials,
and which capabilities each agent receives. Soulacy supplies several enforcement
layers so those decisions are visible and reviewable.

## Security model at a glance

| Layer | What it protects | Where to configure or inspect it |
| --- | --- | --- |
| Server authentication | The GUI and `/api/v1` endpoints | `server.api_key`, `auth`, and [Auth configuration](../configuration/auth.md) |
| Secret storage | Provider keys, channel tokens, and plugin credentials | **Secrets**, **Providers**, and the encrypted workspace vault |
| Capability scope | Which tools, skills, peers, files, and MCP calls an agent may use | Agent `SOUL.yaml`, Studio security review, and [Agent tools](../agents/tools.md) |
| Intent gate | Whether an agent may perform a requested action | Agent `security.intent_gate` and [Security posture](../configuration/security.md) |
| Confirmation gate | High-impact tool calls that require operator approval | Agent `confirm_tools` and the Chat approval dialog |
| Python sandbox | Resource and process limits for agent-local Python tools | `runtime.sandbox` and [Tool sandbox](sandbox.md) |
| Untrusted-content handling | Prompt-injection signals in fetched or external material | Security Doctor, run findings, and audit records |
| Audit trail | What ran, who approved it, and what failed | **Activity**, **Logs**, and [Audit records](audit.md) |

## Recommended production baseline

1. Bind the gateway to `127.0.0.1` and expose it through an authenticated TLS
   reverse proxy. Do not expose port `18789` directly to the internet.
2. Set a strong `server.api_key`; never put it in screenshots, shell history,
   agent prompts, or committed YAML.
3. Give every agent the smallest useful tool allowlist. Avoid wildcard tools or
   MCP servers unless you have reviewed the server behind them.
4. Keep write, delivery, shell, and destructive tools in `confirm_tools` when a
   human is expected to supervise the run.
5. Use a separate channel bot and provider key for production when the provider
   supports scoped credentials.
6. Run `sy doctor` after installation and after changes to providers, channels,
   storage, or the service account.
7. Back up the workspace and test restore before upgrading a production host.

## Interactive replies versus outbound delivery

An ordinary Chat or inbound-channel response is returned through the channel
that started the conversation. It does **not** need `channel.send` merely to
reply. Add `channel.send` only when the agent must initiate a separate outbound
message, deliver scheduled output, or send somewhere other than the active
conversation.

When `channel.send` appears in `confirm_tools`, Soulacy opens an **Action
Required** dialog before that outbound write. This is expected enforcement, not
the assistant displaying its answer in a modal. If the intended behavior is a
normal conversational reply, remove the unnecessary outbound tool from the
agent rather than disabling confirmations globally.

## What to review before enabling an agent

- Provider and model are registered and tested on **Providers**.
- Trigger matches the intended lifecycle: conversational, manual, schedule, or
  webhook.
- Delivery channel and destination are explicit for unattended runs.
- Tool, skill, MCP, file, and knowledge access match the agent's job.
- `confirm_tools` covers the write actions a person should approve.
- Studio shows no security or readiness blockers.
- A dry run and one realistic live test have succeeded.

## Incident response

If an agent performs or attempts an unexpected action:

1. Disable the agent or stop the gateway.
2. Revoke affected provider, bot, webhook, or API credentials.
3. Preserve the workspace logs and generate `sy support bundle`.
4. Review Activity, tool-call approvals, and audit records for the session.
5. Narrow the agent's tools and intent policy before re-enabling it.

Report security vulnerabilities privately using the instructions in the
[repository security policy](https://github.com/vmodekurti/soulacy/security/policy).

## Continue reading

- [Security posture configuration](../configuration/security.md)
- [Tool sandbox](sandbox.md)
- [Audit and incident records](audit.md)
- [Plugin security model](../extend/plugin-security.md)
- [Safety introspection](../extend/safety.md)
