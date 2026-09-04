# Commercial source boundary

Soulacy Commercial consumes the complete public Personal product and adds
private Teams and Scale capabilities. Git ancestry currently carries the
public dependency downstream; `.personal-base` pins the exact incorporated
revision. This is vendored-source composition, not a public mirror of the
private repository.

## Ownership

| Surface | Owner | License |
| --- | --- | --- |
| Personal runtime, Studio, chat, local storage, extension contracts | `soulacy-personal` | Apache-2.0 |
| Workspace membership, RBAC, tenancy, entitlements, billing | Teams | Proprietary |
| SSO/SCIM, compliance reporting, distributed execution and HA | Scale | Proprietary |
| Stable edition interfaces and security contracts | Personal first | Apache-2.0 |

Commercial source is deliberately additive. Shared fixes and contract changes
must merge into Personal first and arrive here through the synchronization PR.
Private implementations must never be copied, cherry-picked, or pushed in the
opposite direction.

`dependencies/editions.json` enumerates the extracted Teams and Scale module
roots. Repository-boundary CI proves that every declared private module exists
in Commercial and is absent from the pinned Personal Git tree. New private
module roots must be added to that inventory in the same change that creates
them.

## Extraction rule

New commercial functionality belongs behind interfaces owned by Personal and
is wired only from a commercial composition root. Existing mixed wiring is
paid down opportunistically whenever it is touched: extract the narrow public
port in Personal, sync it here, then move the private implementation behind
that port. Avoid a flag-only boundary; authorization and entitlements enforce
runtime access, while repository and module ownership protect source.

The CI boundary verifies repository visibility, anonymous Personal
availability, dependency ancestry, declared public contracts, and all three
deployment profiles. A Commercial release additionally records both the
Commercial commit and pinned Personal commit in `release-manifest.json`.
