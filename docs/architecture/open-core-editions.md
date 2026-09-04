# Soulacy edition architecture

Status: repositories separated; downstream synchronization active.

## Decision

Soulacy uses an open-core, two-repository architecture:

| Repository | License | Product | Dependency direction |
| --- | --- | --- | --- |
| `vmodekurti/soulacy-personal` | Apache-2.0 | A complete, self-hosted Personal product | Depends on no commercial repository |
| `vmodekurti/soulacy-commercial` | Private | Personal plus Teams and Scale distributions | Merges the public upstream; never the reverse |

The GitHub repositories now exist with the required visibility:
[`vmodekurti/soulacy-personal`](https://github.com/vmodekurti/soulacy-personal)
is public and `vmodekurti/soulacy-commercial` is private. The former mixed
repository is private and archived. `.personal-base` and Git ancestry identify
the precise public revision contained by every Commercial commit.

Scale extends Teams; Teams extends Personal. There is no sibling fork for each
edition. A commercial binary is assembled at a composition root by registering
edition descriptors and implementations against the contracts in
`pkg/edition`. The browser follows the same shape: `gui/src/lib/edition.js` is
the contract and `gui/src/distribution/edition.js` is the replaceable
composition root.

The new public repository was created from an audited Personal snapshot rather
than by copying the mixed history. The former mixed repository and the canonical
Commercial repository are private. Commercial history is never pushed into the
public repository.

## GitHub source-availability rule

Personal is the canonical public upstream, not a periodically generated mirror
of a private monorepo. Day-to-day development of Personal lands in the public
repository first. During the extraction period, `soulacy-commercial` merges
public Personal commits and layers Teams/Scale implementation, packaging and
tests on top. `.personal-base` records the included public revision. The target
architecture remains separately versioned public modules plus private adapters.

This gives the desired invariant: losing access to the private repository can
never make Personal source unavailable. Conversely, cloning the public
repository can never reveal a new commercial implementation.

The public repository has a scheduled `Personal source availability` workflow.
It fails if GitHub reports private visibility, verifies an unauthenticated
`git ls-remote`, and runs the edition-boundary check. GitHub branch protection
should require both that workflow and normal CI on `main`; repository visibility
changes should be restricted to organization owners and covered by audit-log
alerts.

The private repository should run the complementary checks:

1. its visibility is `private`;
2. its pinned public-core revision is anonymously cloneable;
3. the recorded public revision is a Git ancestor of Commercial;
4. its build records both public and commercial revisions;
5. upstream updates arrive through automated, reviewable pull requests;
6. Personal, Team and Scale contracts pass before those pull requests merge.

## What remains open

Personal is a useful product rather than a crippled trial. It owns:

- local single-user identity and workspace behavior;
- Studio, agents, chat, memory, knowledge, skills, plugins, MCP and channels;
- local model/provider configuration and secrets;
- SQLite/local storage, schedules, logs and the local execution runtime;
- extension contracts, security boundaries and SDKs required to build on it.

Security contracts stay open even when a commercial implementation uses them.
Workspace context, resource ownership, permission vocabulary and extension
capability declarations are interoperability and audit surfaces, not licensing
switches.

## What moves to the commercial repository

Teams owns organization/workspace membership, invitations, workspace RBAC,
workspace administration, deployment-wide provider governance, PostgreSQL
multi-tenancy, commercial entitlements and billing. Scale adds SSO/SCIM,
auditing and compliance reporting, distributed execution, HA coordination,
enterprise policy and operations integrations.

The first extraction inventory is intentionally explicit:

- `internal/entitlements` and its Stripe integration;
- PostgreSQL tenancy/catalog implementations under `internal/tenancy`;
- workspace membership, invitation and administration HTTP handlers;
- `WorkspaceAccess.svelte`, `WorkspaceLogin.svelte`, `Members.svelte`,
  `WorkspaceAdmin.svelte`, `WorkspaceProviders.svelte` and
  `WorkspaceConfig.svelte`;
- commercial edition registration in `gui/src/editions/commercial.js`;
- Scale readiness/reporting and distributed-runtime implementations.

Several of those currently share files with Personal wiring. They are migration
work, not permission checks that can be hidden behind a feature flag.

## Dependency rules

1. Public contracts never import commercial or `internal` implementation code.
2. Personal builds and tests without access to the commercial repository.
3. Commercial code may import public code; the reverse import is forbidden.
4. Product behavior branches on capabilities, not edition-name strings.
5. RBAC and entitlements are runtime enforcement. Repository and module
   boundaries are the intellectual-property boundary.
6. A missing commercial module produces a valid Personal binary, never a
   partially functional Teams binary.

`make edition-boundary` enforces the rules already expressible in this combined
repository. CI runs it on every change. The check expands as implementations
move behind contracts; known mixed files are not falsely declared separated.

## Extraction sequence

1. **Contracts and composition (complete).** Introduce immutable edition
   descriptors, capability checks and one composition root per runtime.
2. **Service ports.** Move tenancy, authorization, entitlement, audit and
   distributed execution dependencies behind narrow public interfaces. Keep
   Personal adapters in the public repository.
3. **Commercial extraction.** Move Teams/Scale adapters and pages into the
   private module. The private repository builds a distribution that embeds the
   public GUI and registers its additions.
4. **Fresh public snapshot (complete).** Generate an allowlisted snapshot, scan its full
   contents and Git objects for secrets and proprietary material, then create a
   new public repository with a single initial commit.
5. **Independent release proof (active).** CI builds and exercises Personal from the
   public repository alone. Commercial CI pins a public-core version and runs
   Personal, Teams and Scale contract tests.

Do not use subtree filtering to publish the old history. Git merges currently
carry public changes downstream; no script copies private source upstream. The
end state remains ordinary Go/JavaScript module composition with one-way imports.

## Versioning and releases

The public core uses semantic versions. The commercial repository pins a core
version and publishes its own release with a compatibility declaration, for
example `commercial 1.4.x requires core ^1.8`. Breaking contract changes land in
the public core first, with deprecation coverage, before commercial consumers
move. Release artifacts state their edition and source revision at runtime.

## Licensing and trademarks

The public snapshot retains `LICENSE` (Apache-2.0). The commercial repository
gets its own proprietary license before any extracted code is committed there.
Do not add a proprietary header to files that remain in the Apache repository,
and do not rely on entitlement checks to change the license of source code.

The Soulacy name and logos should be governed by a separate trademark policy so
forking remains permitted while product identity is protected. Final license,
contributor agreement and trademark wording require legal review before the
public launch.
