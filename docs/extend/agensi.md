# Agensi skill catalog

Soulacy can search and use skills from [Agensi](https://www.agensi.io/) without
downloading their instructions into the Soulacy workspace.

## Connect once

1. Open **Skills** in the Soulacy Portal.
2. In **Agensi skill catalog**, select **Connect Agensi**.
3. Sign in on the Agensi page and select **Allow**.
4. Return to Soulacy. The card changes to **Connected**.

Genie and the System agent can start the same browser sign-in for you. They
will show the Agensi authorization link and wait for you to finish. Soulacy
never asks you to paste an Agensi token or API key, and it does not fall back
to anonymous access.

## Find and use a skill

Search from the Agensi card, then select **Use with Genie**, or ask Genie in
plain language. Soulacy calls `get_skill` again for every run so the agent uses
the current version. The skill body stays in memory for that run and is replaced
with a receipt in activity events, learning memory, and conversation history.

Agent Builder applies the same rules when an agent is given Agensi tools.

## Paid skills

Soulacy can show the skill name, price, reason it fits, and the checkout or
top-up link returned by Agensi. A purchase with account credit always opens a
live approval request. Scheduled and unattended agents cannot approve it on
your behalf.

After access is granted, Soulacy loads the skill again with `get_skill` before
using it.

## Connection details

The built-in integration uses Agensi's Streamable HTTP MCP endpoint:

```text
https://mcp.agensi.io/mcp/auth
```

OAuth access and refresh tokens are held in Soulacy's encrypted credential
vault. The configuration file contains only the vault reference and public
OAuth client metadata.
