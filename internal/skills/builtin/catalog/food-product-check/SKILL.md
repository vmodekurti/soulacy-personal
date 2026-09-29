---
name: food-product-check
description: Explain packaged-food barcode, ingredient, nutrition, allergen, and label data through a connected food product tool. Use for product checks, ingredient comparisons, dietary filters, and barcode questions.
license: Apache-2.0
allowed-tools: list_mcp_tools
metadata:
  builtin: "true"
  category: shopping
---

# Food product check

Explain the provider record without overstating incomplete community or manufacturer data.

## Identify the product

Prefer an exact barcode. When searching by name, use brand, package size, country, and variant to avoid mixing similar products. Confirm that the returned barcode or identifying fields match the item the person means.

## Find a usable tool

Inspect the live MCP tool catalog. Use only food product tools currently granted to this agent and follow the exact tool names and argument schemas shown there. Never invent a product record or treat a failed lookup as a negative ingredient claim.

If no compatible tool is available, stop and explain the setup path:

1. Open Connectors and select Open Food Facts or another reviewed food provider.
2. Connect and test its MCP server or plugin.
3. Grant its read tools and this skill to the agent.

## Read the record carefully

- Separate fields reported by the provider from your interpretation.
- Report serving size and per-100-unit values with their units. Do not compare nutrition values that use different bases without converting them explicitly.
- Quote or closely preserve the ingredient and allergen fields when accuracy matters.
- Distinguish explicit presence, explicit absence, possible traces, and missing data.
- Treat dietary labels, nutrition grades, processing scores, and environmental scores as provider data. Explain the basis when it is available.
- Compare products only after aligning serving basis, package variant, and market.
- State the provider and retrieval date when freshness matters.

## Safety and uncertainty

Never infer that a product is safe for an allergy because an allergen field is empty or a lookup failed. For a severe allergy, tell the person to verify the current package label and manufacturer guidance. Note when the record is community-contributed, incomplete, old, or inconsistent with another field.

## Answer format

Lead with the requested answer. Then show the exact evidence that supports it, followed by a short uncertainty line. For comparisons, use a compact table with matching units and package variants. Include the provider product link when returned.
