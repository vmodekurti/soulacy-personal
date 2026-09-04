# Soulacy Commercial

Private Teams and Scale extensions for the public
[`vmodekurti/soulacy-personal`](https://github.com/vmodekurti/soulacy-personal)
Personal edition.

This repository now preserves the combined implementation while it is split
into a strictly additive commercial module:

- Personal source remains authored, tagged and released from the public
  repository.
- Teams extends Personal; Scale extends Teams.
- Public Personal code never imports this private repository.
- New Teams/Scale implementation and commercial release work happens here.

Personal updates are merged downstream through automated, reviewable pull
requests. `.personal-base` records the exact public revision currently included.
`dependencies/editions.json` declares the Personal → Teams → Scale dependency
graph, required contracts, compatibility commands, and non-automatic update
policy.
See the [repository workflow](docs/architecture/repository-workflow.md) before
changing code shared by Personal and Commercial.

The imported history was briefly reachable on a non-default public branch
before that branch was removed. Do not publish this repository or mirror its
branches to the public remote.

Files imported from Personal retain their Apache-2.0 license. Teams/Scale work
first authored here is governed by [LICENSE-COMMERCIAL](LICENSE-COMMERCIAL).
See the [commercial source boundary](docs/architecture/commercial-source-boundary.md)
for ownership rules. Final product terms require legal review before external
commercial distribution.
