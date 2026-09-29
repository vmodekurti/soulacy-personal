# Connectors

Connectors give people one place to discover and set up service integrations without first deciding whether the underlying implementation is MCP, a plugin, or a built-in tool.

A connector is a product definition around an execution adapter. It describes:

- the provider and official API documentation
- supported use cases and individual capabilities
- authentication and secret slots
- local and managed deployment compatibility
- read and write effects
- setup, testing, and agent assignment
- checkout or handoff behavior

The adapter still runs through Soulacy's existing tool systems. Connectors do not add another execution protocol.

## Catalog status

The first catalog includes recipes for:

| Connector | Category | Authentication | Initial boundary |
|---|---|---|---|
| eBay Shopping | Shopping | OAuth 2 client credentials | Search, compare, inspect, then open the provider checkout link |
| Best Buy Products | Shopping | API key | Search products and stores, then open the provider checkout link |
| Etsy Marketplace | Shopping | OAuth 2 authorization code | Search listings and shops, then open the provider listing |
| Ticketmaster Events | Events | API key | Search events and venues, then open Ticketmaster for ticket selection |
| Eventbrite Organizer | Events | OAuth 2 or private token | Read authorized organizer data and open public event links |
| Open Food Facts | Shopping | No key for read access | Read barcode, ingredient, nutrition, and allergen data |

`Recipe ready` means Soulacy knows the supported setup path. It does not mean the service is connected. The connector becomes usable by an agent only after an MCP server or plugin is connected, tested, and granted to that agent.

## Setup flow

1. Open **Connectors** and choose a provider.
2. Follow the official provider link to create an application or API key.
3. Store each named credential under **Secrets**. The catalog returns secret names and status only. It never returns secret values.
4. Connect a reviewed MCP server or plugin.
5. Test the adapter from its setup page.
6. Grant individual tools to selected agents in Studio or the agent editor.

This sequence works on local and managed deployments when the adapter is remote. A local stdio adapter still requires a deployment with a persistent runtime and permission to launch it.

## Shopping and ticket safety

The initial connector recipes are read-only. Agents may search, compare, and retrieve details. Purchase and ticket checkout stays on the provider website.

Future cart, reservation, purchase, cancellation, refund, or account-change capabilities must be declared as write effects. Before a purchase can be approved, Soulacy should show the provider, item or event, total price, fees, quantity, and destination. A generic tool approval is not enough evidence for a purchase.

## Adding a connector

Definitions live in `internal/connectors/catalog.go`. A definition needs:

- a stable lowercase ID
- an official HTTPS documentation URL
- an authentication type and secret names
- at least one capability with an explicit `read` or `write` effect
- supported adapter kinds and deployment targets
- concrete setup steps
- a clear checkout or completion boundary

Add catalog metadata only after verifying that the provider offers a documented integration method. Do not catalog scraped consumer pages, unofficial private APIs, or adapters that require credentials to appear in model-visible arguments.

The connector definition and execution adapter may ship separately. Keep the UI status honest while the adapter is unavailable, and never report an integration as connected solely because its credential exists.

## Connector, MCP, plugin, or skill

| Concept | Purpose |
|---|---|
| Connector | User-facing service identity, setup, credentials, capability effects, and status |
| MCP server | Standard tool protocol for local or remote execution |
| Plugin | Soulacy package that can contribute tools, channels, providers, and UI |
| Skill | Instructions that teach an agent when and how to use available tools |

A complete service integration may include all four. The connector is the entry point people see.
