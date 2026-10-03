# Connectors

A connector is a user-created integration built around an outcome. It is not a fixed provider entry and it is not another tool protocol.

Someone can ask for a connector in plain language:

> Compare products across Amazon, eBay, Etsy, and any stores I add later.

Soulacy turns that intent into an editable plan. It suggests relevant websites, lets the person choose the exact set, enables public access, writes an Agent Skill, and offers optional Website Access for account-only capabilities.

## Public access first

Every website starts with public search and retrieval. A provider account, developer application, or API key is not required for public pages.

The generated skill tells agents to:

1. Search and read approved public websites first.
2. Cite the exact pages used.
3. Report blocked or unverifiable content clearly.
4. Use an authenticated website session only when the requested task needs subscribed, personalized, saved, or account-only information.
5. Keep purchases, registrations, bookings, cancellations, messages, and other side effects behind the normal approval policy.

## Optional access per website

Authentication belongs to a website, not to the whole connector. A shopping connector can use public access for every store while adding Website Access only for a store where the person wants member prices, wish lists, or order details.

Choosing **Add sign-in** creates a pending Website Access record with these boundaries:

- one user owns it
- one approved domain is allowed
- browser session state is encrypted in the credential vault
- cookies and tokens never enter the connector record or generated skill
- the API returns status metadata but never returns secret values
- agents receive access only after an explicit grant

The person completes the normal website login in the Soulacy Session Capture companion. Password managers, MFA, CAPTCHA, and passkeys continue to work on the real website.

## Connector composer

The GUI flow has four steps:

1. Describe the desired outcome.
2. Review Soulacy's category and website suggestions.
3. Select suggestions or add any public HTTPS website.
4. Build the connector.

Building creates:

- a persistent connector definition
- a normalized site list
- public and optional advanced capability descriptions
- a generated Agent Skill scoped to those sites
- an audit event

The connector can then be edited, assigned to an agent through its generated skill, or deleted. Deleting a connector removes the generated skill. Saved website sessions remain in Website Access until the person explicitly removes them.

## Genie

Genie has the same constrained composer through three tools:

- `plan_connector` turns a goal into a proposed website set and access plan
- `create_connector` saves the agreed connector and generates its skill
- `list_connectors` reports current connectors and access status

Genie should show the proposed websites before creating a connector unless the person already supplied an exact list. Genie never requests passwords, cookies, browser storage, or API keys in chat. It directs the person to Website Access only when account access is useful.

## Storage model

Connector records contain safe metadata:

- name, intent, and category
- approved websites and domains
- public and advanced capability descriptions
- generated skill name
- optional Website Access connection IDs
- creation and update timestamps

Authentication material remains in the encrypted credential vault. A connection ID is a reference, not a credential.

## Security boundaries

- Only public HTTPS websites on the standard port may be added.
- Loopback, local, link-local, and private network targets are rejected.
- Duplicate domains are rejected within a connector.
- Generated skills contain site URLs and operating rules, never authentication material.
- Website sessions are domain restricted and user scoped.
- Page content is untrusted data and cannot redefine the generated skill or approval policy.
- Write actions continue to use Soulacy's existing tool permissions and approval controls.

## Connector, MCP, plugin, Website Access, or skill

| Concept | Purpose |
|---|---|
| Connector | A user-owned outcome, website set, capability plan, and access status |
| MCP server | A standard protocol for local or remote structured tools |
| Plugin | A package that can add tools, channels, providers, and UI |
| Website Access | An encrypted, domain-restricted browser session for account-only pages |
| Skill | Instructions that teach an agent how to use the connector safely |

A connector can use public web tools alone. MCP and plugins are optional enhancements when a structured tool offers clear value.
