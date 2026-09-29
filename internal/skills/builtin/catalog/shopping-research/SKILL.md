---
name: shopping-research
description: Compare products through shopping connectors that are already available to the agent. Use for finding products, comparing prices, checking availability, narrowing options, or preparing a provider checkout handoff.
license: Apache-2.0
allowed-tools: list_mcp_tools
metadata:
  builtin: "true"
  category: shopping
---

# Shopping research

Help the person make a decision from current provider results. A skill supplies the method. Connected MCP or plugin tools supply the product data and links.

## Understand the decision

Identify the hard constraints that change the search: product type, must-have features, budget, destination or market, condition, quantity, and deadline. Ask one focused question only when a missing constraint would make the search wasteful. Otherwise state a reasonable assumption and continue.

## Find usable tools

Inspect the live MCP tool catalog. Use only shopping tools currently granted to this agent, and follow their exact names and argument schemas. Never invent a provider, tool, parameter, credential, or successful result.

If no compatible shopping tool is available, stop and explain the setup path:

1. Open Connectors and choose a shopping provider.
2. Add the named credentials under Secrets when required.
3. Connect and test its MCP server or plugin.
4. Grant the read tools and this skill to the agent.

Do not replace a missing connector with scraped consumer pages unless the person explicitly asks for ordinary web research.

## Search and verify

- Search more than one connected provider when that can improve price or availability.
- Keep provider results separate until comparable fields are normalized.
- Shortlist before requesting detailed item data.
- For each finalist, verify the current listed price, currency, availability, condition, seller or store, shipping cost, delivery estimate, return information, and provider URL when those fields are available.
- Calculate a total only from components returned by the provider. Label unknown tax, fees, shipping, membership pricing, or coupons as unknown.
- Treat search snippets as candidates, not proof. Use an item-detail tool before making a final recommendation when one is available.
- Preserve variant details such as size, color, storage, model year, and pack quantity. Do not compare different variants as if they are identical.

## Present the decision

Lead with the best match and why it fits the person's stated constraints. Use a compact comparison table for multiple finalists. Include provider, exact item or variant, known total, availability, delivery, return information, and provider link. Name missing data instead of filling it in.

Distinguish provider facts from your recommendation. If results are stale, incomplete, region-limited, or inconsistent, say so.

## Checkout boundary

Initial connector workflows are read-only. Never add to cart, place an order, submit payment, reserve inventory, or change an account. Give the provider product URL so the person can review the current price, fees, destination, and terms and complete checkout themselves.
