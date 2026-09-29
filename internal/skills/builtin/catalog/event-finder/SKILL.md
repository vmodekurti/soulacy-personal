---
name: event-finder
description: Find and compare events through event connectors that are already available to the agent. Use for concerts, sports, conferences, local activities, venue discovery, schedules, and ticket-link handoff.
license: Apache-2.0
allowed-tools: list_mcp_tools
metadata:
  builtin: "true"
  category: events
---

# Event finder

Find events that fit the person's real constraints using current results from connected event tools. Keep ticket selection and checkout with the provider.

## Frame the search

Resolve the constraints that materially change the answer: location or travel radius, date range, event type or performer, party size, budget, age limits, and accessibility needs. Ask one focused question when location or date is missing and cannot be inferred. Otherwise state the assumption.

Use the person's local time zone for the search and display. Show the venue time zone as well when travel or a remote event makes it relevant.

## Find usable tools

Inspect the live MCP tool catalog. Use only event tools currently granted to this agent, with the exact tool names and argument schemas shown there. Never invent a provider, event, ticket availability, price, fee, or tool result.

If no compatible event tool is available, stop and explain the setup path:

1. Open Connectors and choose an event provider.
2. Add the named credential under Secrets.
3. Connect and test its MCP server or plugin.
4. Grant the event read tools and this skill to the agent.

A tool limited to organizer-owned events is not a general public event search. State that limitation rather than treating an empty organizer result as proof that no event exists.

## Search and reconcile

- Search each useful connected provider independently.
- Normalize title, performer or team, start time, venue, city, status, age restriction, accessibility information, price range, currency, fees, and provider URL when available.
- Deduplicate the same event across providers using title or attraction, venue, and start time. Keep provider-specific price and availability fields separate.
- Treat an announced event without ticket inventory as an event match, not as proof that tickets can be bought.
- Label resale, primary inventory, presale, member-only access, and general sale when the provider identifies them.
- Do not calculate a final ticket total unless the provider returned quantity, ticket price, and all applicable fees. Otherwise show the known price range and say that checkout determines the final total.
- Verify finalist details with an event-detail tool when available.

## Present the options

Lead with the strongest matches. For multiple events, use a compact table with date and time, venue and distance if known, ticket status, known price or range, accessibility notes, and provider link. Explain why the leading option fits the requested date, location, and budget.

Name gaps clearly. An empty result from one provider means only that the provider returned no match for that query.

## Ticket boundary

Never reserve seats, add tickets to a cart, submit attendee details, place an order, or change an account. Open the provider event URL so the person can choose seats or ticket classes, review fees and restrictions, and complete checkout themselves.
