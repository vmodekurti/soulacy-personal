---
name: event-finder
description: Find and compare events through public event pages and optional event adapters. Use for concerts, sports, conferences, local activities, venue discovery, schedules, and ticket-link handoff.
license: Apache-2.0
allowed-tools: web_search fetch_url list_mcp_tools
metadata:
  builtin: "true"
  category: events
---

# Event finder

Find events that fit the person's real constraints using current public results. Provider accounts and developer keys are not required for public event discovery. Keep ticket selection and checkout with the provider.

## Frame the search

Resolve the constraints that materially change the answer: location or travel radius, date range, event type or performer, party size, budget, age limits, and accessibility needs. Ask one focused question when location or date is missing and cannot be inferred. Otherwise state the assumption.

Use the person's local time zone for the search and display. Show the venue time zone as well when travel or a remote event makes it relevant.

## Find usable sources

Use `fetch_url` with a public provider search or event URL. Use `web_search` for broader discovery when it is available. Search with the provider domain when the person names an event service. Cite only URLs returned or fetched in this run.

Also inspect the live MCP tool catalog. A compatible event tool may provide cleaner structured fields, but it is optional. Use only tools currently granted to this agent, with the exact tool names and argument schemas shown there. Never invent a provider, event, ticket availability, price, fee, or tool result.

If no compatible MCP tool is available, continue with public web results. Do not ask for a provider API key merely because an adapter is absent. If `web_search` is unavailable, use a public provider search URL directly. If `fetch_url` is unavailable, explain that URL retrieval must be granted. If a public page blocks retrieval, state which detail could not be verified and provide the provider link.

Organizer-owned events, attendee lists, saved tickets, and account changes require separately authorized access. A tool limited to organizer-owned events is not a general public event search. State that limitation rather than treating an empty organizer result as proof that no event exists.

## Search and reconcile

- Search each useful provider independently.
- Normalize title, performer or team, start time, venue, city, status, age restriction, accessibility information, price range, currency, fees, and provider URL when available.
- Deduplicate the same event across providers using title or attraction, venue, and start time. Keep provider-specific price and availability fields separate.
- Treat an announced event without ticket inventory as an event match, not as proof that tickets can be bought.
- Label resale, primary inventory, presale, member-only access, and general sale when the provider identifies them.
- Do not calculate a final ticket total unless the provider returned quantity, ticket price, and all applicable fees. Otherwise show the known price range and say that checkout determines the final total.
- Verify finalist details from the public event page or with an event-detail tool when available.

## Present the options

Lead with the strongest matches. For multiple events, use a compact table with date and time, venue and distance if known, ticket status, known price or range, accessibility notes, and provider link. Explain why the leading option fits the requested date, location, and budget.

Name gaps clearly. An empty result from one provider means only that the provider returned no match for that query.

## Ticket boundary

Never reserve seats, add tickets to a cart, submit attendee details, place an order, or change an account. Open the provider event URL so the person can choose seats or ticket classes, review fees and restrictions, and complete checkout themselves.
