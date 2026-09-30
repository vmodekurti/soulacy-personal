# Connectors

Connectors give people one place to discover and set up service integrations without first deciding whether the underlying implementation is MCP, a plugin, or a built-in tool.

A connector is a product definition around an execution adapter. It describes:

- the provider and its public website or official documentation
- supported use cases and individual capabilities
- whether provider authentication is needed
- local and managed deployment compatibility
- read and write effects
- setup, testing, and agent assignment
- recommended skills that teach agents how to use compatible tools
- checkout or handoff behavior

The adapter still runs through Soulacy's existing tool systems. Connectors do not add another execution protocol.

## Catalog status

The first catalog includes recipes for:

| Connector | Category | Recommended skill | Initial boundary |
|---|---|---|---|
| eBay Shopping | Shopping | `shopping-research` | Search, compare, inspect, then open the provider checkout link |
| Best Buy Products | Shopping | `shopping-research` | Search products and stores, then open the provider checkout link |
| Etsy Marketplace | Shopping | `shopping-research` | Search listings and shops, then open the provider listing |
| Ticketmaster Events | Events | `event-finder` | Search events and venues, then open Ticketmaster for ticket selection |
| Eventbrite Events | Events | `event-finder` | Search public events and open registration links |
| Open Food Facts | Shopping | `food-product-check` | Read barcode, ingredient, nutrition, and allergen data |

`Recipe ready` means Soulacy knows the supported access path. The initial catalog uses public provider pages and public data, so these connectors do not require provider accounts, developer applications, or API keys. A reviewed MCP server or plugin remains an optional way to add structured or account-specific capabilities.

## Setup flow

1. Open **Connectors** and choose a provider.
2. Grant `fetch_url` to the selected agent. `web_search` is optional for broader discovery. Open Food Facts barcode lookup needs only `fetch_url`.
3. Assign the recommended skill so the agent knows how to search, verify, compare, and explain missing data.
4. Ask the agent to search the provider, or give it a public product, event, or barcode URL.
5. Optionally connect a reviewed MCP server or plugin when a task needs structured API data or account-specific capabilities.
6. If optional account access is added, store its credentials under **Secrets**, test the adapter, and grant only the individual tools the agent needs.

Public web access works on local and managed deployments. A local stdio adapter still requires a deployment with a persistent runtime and permission to launch it.

## Shopping and ticket safety

The initial connector recipes are read-only. Agents may search, compare, and retrieve details. Purchase and ticket checkout stays on the provider website.

Future cart, reservation, purchase, cancellation, refund, or account-change capabilities must be declared as write effects. Before a purchase can be approved, Soulacy should show the provider, item or event, total price, fees, quantity, and destination. A generic tool approval is not enough evidence for a purchase.

## Adding a connector

Definitions live in `internal/connectors/catalog.go`. A definition needs:

- a stable lowercase ID
- an official HTTPS provider or documentation URL
- an explicit authentication requirement of `none`, `optional`, or `required`
- at least one capability with an explicit `read` or `write` effect
- supported adapter kinds and deployment targets
- concrete setup steps
- a clear checkout or completion boundary

Prefer public provider pages and documented public endpoints for read-only discovery. Respect provider terms, rate limits, robots policy, and access failures. Never use unofficial private APIs or put credentials in model-visible arguments.

The connector definition and execution adapter may ship separately. Keep the UI status honest while the adapter is unavailable, and never report an integration as connected solely because its credential exists.

## Connector skills

Soulacy ships three outcome skills for the initial catalog:

- `shopping-research` compares products across available shopping tools and keeps checkout on the provider website.
- `event-finder` searches connected event tools, deduplicates results, and compares dates, locations, availability, and known costs.
- `food-product-check` explains barcode, ingredient, nutrition, allergen, and label data while preserving uncertainty.

The skills do not provide network access or credentials. They use only web and MCP tools already granted to the agent. Missing MCP tools do not block public research, and the skills do not request provider credentials unless the person asks for an account-specific capability.

## Connector, MCP, plugin, or skill

| Concept | Purpose |
|---|---|
| Connector | User-facing service identity, public access path, optional account setup, capability effects, and status |
| MCP server | Standard tool protocol for local or remote execution |
| Plugin | Soulacy package that can contribute tools, channels, providers, and UI |
| Skill | Instructions that teach an agent when and how to use available tools |

A complete service integration may include all four. The connector is the entry point people see.
