# Understand model preparation

Before a run, Soulacy examines the selected provider/model and uses available
capability metadata to choose a compatible working approach. It adapts the
**method**, not the promise you asked the agent to fulfill.

## Inspect it before running

1. Open the agent in **Agents** (web or iPhone).
2. Save any model/configuration edits first.
3. Open **Model preparation** and choose **Refresh preparation**.
4. Read the provider/model, strategy, evidence source, warnings, and any blocking
   reason. Expand **Capabilities and working approach** on the web.

The preview reads saved configuration. Metadata may be cached for up to five
minutes; refreshing the panel is not a fresh model benchmark.

## Interpret the result

| What you see | Meaning | Your next step |
|---|---|---|
| Native tools supported | Provider metadata reports tool-call support | Test the task; support does not guarantee correct tool arguments |
| Tools known unsupported, automatic strategy | A guarded JSON/ReAct approach may be used | Test with read-only tools before considering writes |
| Capabilities unknown/not reported | The provider did not supply usable metadata | Do not assume incapable or highly capable; run a small representative test |
| Input/output/context ceiling | Reported/configured capacity, when available | Keep the task and history within it; absence is not unlimited capacity |
| Blocking reason | A requirement is incompatible with known capability/configuration | Deliberately choose a compatible model or change the requirement yourself |

It never invents an intelligence score from a model's name, silently drops
required output, grants tools, expands your budget, downloads a model, or
switches to a paid model on its own.

## A useful comparison exercise

Run the [notes exercise](../use-cases/notes-to-action-plan.md) on a model you
already have, keeping the prompt and acceptance checks fixed. Record errors,
duration, and quality. If you deliberately choose a second model, save it,
inspect preparation again, and repeat the same normal and failure cases.

Do not equate a larger context window or reported reasoning support with better
answers. If the unchanged task cannot fit a model's limits, stopping is safer
than silently discarding required context.

The [technical reference](../MODEL_AWARE_EXECUTION.md) covers providers, cache
behavior, explicit strategies, nested budgets, API errors, and SDK integration.
