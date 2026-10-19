# Genie portal actions

In signed-in Genie chat, ask for saved run results, podcast links, ordinary agent
changes, or a new model provider. Genie discovers a catalog of portal actions and
uses the same authenticated API handlers as the browser. Both regular chat and
streaming chat support bearer tokens and browser access cookies. The grant exists
only for that conversation request and cannot be passed to peer agents.

Examples:

- “Find the podcast link from yesterday's Daily AI Leadership Podcast job.”
- “Show the latest failed run and explain what failed.”
- “Change my research agent's model to …”
- “Add a new OpenAI-compatible provider at …”

Run search supports text, agent, status, run ID, explicit date bounds, and paging.
Results include the real saved output and failure diagnosis. An event scan that
hits its limit is marked incomplete; an empty page is not proof no older run exists.

Agent edits merge only requested fields into the current complete definition.
Nested fields are preserved, arrays replace, and unknown fields are rejected.
Zero cumulative token, model-call, and turn limits remain unlimited.

## Protected operational settings

Genie cannot change gateway authentication, permissions, networking, storage,
global runtime defaults/budgets, service lifecycle, shared channels/plugins/tool
servers, protected or privileged agents, or providers in use. These restrictions
are enforced before execution, even when the requester is the owner. Provider
references include disabled agents. Providers with stored credentials are also
protected. Add credentials directly through the Providers screen.

Genie can inspect protected settings, edit ordinary agents' names, descriptions,
prompts, model settings, schedules and execution limits, and add a provider
without replacing the active setup. Core settings must be changed directly in the
Portal. The builder cannot overwrite existing agents or grant host administration.
Genie-originated delegated work cannot execute privileged host tools.

Deleting ordinary resources, running a job (which may deliver a result), and
model downloads use the existing confirmation UI. Approval does not override the
protected settings policy. Dry-run mode simulates portal mutations. The original
caller's credential is rechecked on every request; no owner credential is injected.

## Extending the catalog

`internal/gateway/portal_catalog.go` is the reviewed action list, with parameter
and body guidance. `portal_policy.go` separately enforces operational protections.
New HTTP endpoints do not automatically become Genie tools. Keep secret entry,
approval decisions, uploads and interactive sign-in in the portal. Tests verify
catalog routes, caller authorization, cookie/stream chat, output search, partial
updates, provider setup, dry-run classification, and critical-setting denials.
