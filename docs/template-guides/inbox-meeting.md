# Inbox / Meeting Assistant

Triages your inbox (summarize unread, flag what needs a reply) and produces
meeting minutes (decisions, action items, owners) from a transcript or notes.

## Requirements

| Requirement | Needed | Notes |
| --- | --- | --- |
| LLM provider | Yes | A capable summarization model recommended |
| Tools | Email/calendar tool or MCP server | To read inbox/meetings |
| MCP servers | Optional | e.g. a Gmail/Outlook MCP server |
| Secrets | Provider/account tokens | Stored in the vault |
| Channels | Optional | Deliver the digest to Slack/Telegram |
| Schedule | Optional | e.g. a morning inbox summary |

## Install

1. Connect the email/calendar tool or MCP server (see [MCP Servers](../extend/mcp.md)).
2. Review the readiness checklist — it flags a missing MCP server or token.
3. Run the **mock test** on sample content (no real mailbox access).
4. Add tokens, run the **real test** against your account.
5. (Optional) Schedule a morning summary to a channel.
6. Install.

## Verify

Add a small suite modeled on the golden suites, or run your own cases:

```bash
sy eval --agent inbox-assistant --suite evals/golden
```

## Notes

Reading a mailbox is a network/privileged capability — the agent's capability
tier is shown before you bind it to a channel. See
[Policy & Safety](../extend/safety.md) and
[Common failures](../troubleshooting/common-failures.md).
