---
name: shopping-research
description: Compare products through public provider pages and optional shopping adapters. Use for finding products, comparing prices, checking availability, narrowing options, or preparing a provider checkout handoff.
license: Apache-2.0
allowed-tools: web_search fetch_url list_mcp_tools
metadata:
  builtin: "true"
  category: shopping
---

# Shopping research

Help the person make a decision from current provider results. Public shopping pages are the default path and do not require a provider account or API key. Connected MCP or plugin tools are optional sources of structured data.

## Understand the decision

Identify the hard constraints that change the search: product type, must-have features, budget, destination or market, condition, quantity, and deadline. Ask one focused question only when a missing constraint would make the search wasteful. Otherwise state a reasonable assumption and continue.

## Find usable sources

Use `fetch_url` with a public provider search or listing URL. Use `web_search` for broader discovery when it is available. Search with the provider domain when the person names a store or marketplace. Cite only URLs returned or fetched in this run.

Also inspect the live MCP tool catalog. A compatible shopping tool may provide cleaner structured fields, but it is optional. Use only tools currently granted to this agent and follow their exact names and argument schemas. Never invent a provider, tool, parameter, credential, or successful result.

If no compatible MCP tool is available, continue with public web results. Do not ask for a provider API key merely because an adapter is absent. If `web_search` is unavailable, use a public provider search URL directly. If `fetch_url` is unavailable, explain that URL retrieval must be granted. If a public page blocks retrieval, state which page could not be verified and offer the provider link for the person to open.

Account-specific data such as order history, member pricing, saved items, messages, or seller operations may require a separately authorized connection. Explain that boundary only when the requested task needs it.

## Search and verify

- Search more than one provider when that can improve price or availability.
- Keep provider results separate until comparable fields are normalized.
- Shortlist before requesting detailed item data.
- For each finalist, verify the current listed price, currency, availability, condition, seller or store, shipping cost, delivery estimate, return information, and provider URL when those fields are available.
- Calculate a total only from components returned by the provider. Label unknown tax, fees, shipping, membership pricing, or coupons as unknown.
- Treat search snippets as candidates, not proof. Fetch the public product page or use an item-detail tool before making a final recommendation.
- Preserve variant details such as size, color, storage, model year, and pack quantity. Do not compare different variants as if they are identical.

## Present the decision

Lead with the best match and why it fits the person's stated constraints. Use a compact comparison table for multiple finalists. Include provider, exact item or variant, known total, availability, delivery, return information, and provider link. Name missing data instead of filling it in.

Distinguish provider facts from your recommendation. If results are stale, incomplete, region-limited, or inconsistent, say so.

## Checkout boundary

Initial connector workflows are read-only. Never add to cart, place an order, submit payment, reserve inventory, or change an account. Give the provider product URL so the person can review the current price, fees, destination, and terms and complete checkout themselves.
