# Personal and Commercial

Soulacy has two delivery models: **Personal is open-source and self-hosted;
Commercial is the SaaS offering.** The old statement that Soulacy would never
be hosted no longer describes the product family.

| | Soulacy Personal | Soulacy Commercial |
|---|---|---|
| Delivery | You run the gateway on your machine or cloud account | SaaS |
| Source | Public Personal repository, Apache-2.0 | Separate private commercial code and commercial terms |
| These installation guides | Apply directly | Do not install a Personal server merely to sign into SaaS |
| Resource footprint on this site | Describes your Personal gateway/profile | Not a promise about SaaS infrastructure, capacity, or service levels |

## Which path should I follow?

Choose **Personal** when you want to operate the gateway yourself. You control
its configuration, backups, updates, provider connections, and network access.
Start with [Quick Start](getting-started/quickstart.md) or
[self-hosting in a cloud account](deployment/cloud.md). Deploying Personal to
AWS/Azure/Railway does not itself turn it into the Commercial SaaS service.

For **Commercial SaaS**, use the access/onboarding instructions provided for
that service. Do not assume Personal's local paths, owner API keys, cloud
templates, or restart instructions apply to a hosted account. This page does
not announce pricing, signup availability, retention terms, or an SLA; use the
Commercial service's published terms and onboarding details for those.

## Scope of this documentation

This site currently documents the public Personal gateway and its companion
client workflows. Commercial extends Personal through a separate codebase;
feature availability, administrative authority, and account boundaries depend
on the edition and deployment. A screen described here is not an entitlement
promise for every hosted account.

Provider data handling still matters in either model. Self-hosting the gateway
does not prevent prompts from reaching a configured cloud model. Review the
applicable service/provider policies before sending sensitive data.
