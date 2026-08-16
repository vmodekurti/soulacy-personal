# Quota and budget precedence (MU-024)

Limits can be defined at six levels. This file is the documented precedence
criterion 1 asks for; `internal/quota/limits.go` is the implementation and
`internal/quota/limits_test.go` is the executable version of everything below.

## The levels, broadest to narrowest

| Level | Entity | Meaning |
|---|---|---|
| `deployment` | the process/cluster | the operator's ceiling regardless of who asks |
| `organization` | one customer | a customer cannot buy more than the deployment has |
| `workspace` | one team within a customer | |
| `principal` | one user or service account | |
| `agent` | one agent definition | |
| `model` | one `provider/model` pair | a statement about the model, not about the caller |

## Precedence is conjunctive, not override

**Every applicable limit is enforced.** The tempting reading is that the
narrowest configured level wins and the broader ones are ignored. Under that
rule, setting a generous per-agent limit would let one agent spend past its
organization's budget — which inverts the point of having an organization
budget at all.

A narrower scope is a **subset** of a broader one, so its limit can only ever
restrict further. It can never license.

```
organization org_a: $0.001/day
agent bot:          $1.000/day
→ effective: $0.001/day, bound by organization:org_a
```

What precedence *does* decide is which **value** applies at a given level when
both a specific entry and that level's default exist:

```
workspace (default): $0.010/day
workspace ws_a:      $0.090/day
→ ws_a gets $0.090; every other workspace gets $0.010
```

So: **conjunctive across levels, most-specific-wins within a level.**

## Zero means "not limited here"

A zero in any dimension means that level does not constrain that dimension. It
does not mean a ceiling of zero. A naive `min()` across levels would make every
unconfigured level forbid everything; `tighter64` keeps only the smallest
*positive* value.

The failure this prevents is subtle: with every level unconfigured for a
dimension the answer is zero either way, so the bug only appears once some
level limits a *different* dimension. `TestALevelLimitingOneDimensionDoesNot
ZeroTheRest` is that case.

## Rejections name the binding scope

`Tightest` returns the scope that contributed each ceiling alongside the
number. "You have $0.00 remaining" is unactionable; "the organization's monthly
budget is exhausted, resets in 9 days" tells somebody what to do — which is
criterion 6, and a caller that kept only the number could not produce it.

## Fair scheduling

Budgets bound *how much*. They say nothing about *who goes next*, so a plain
semaphore lets a tenant with a hundred queued runs starve everyone behind it
without exceeding any budget — it is not spending faster than allowed, only
first.

`internal/quota/fairshare.go` applies max-min fairness by workspace: with N
workspaces competing for C slots, each may hold up to `ceil(C/N)`. One tenant
alone gets everything; capacity nobody wants is never idled. The share rounds
**up** because `floor(4/3) = 1` leaves a slot permanently unused while three
tenants queue for it, and fairness that wastes capacity is not the goal.

The unit of fairness is the **workspace**, not the principal: per-principal
would let a workspace with fifty members take fifty shares from a workspace
with two — the same starvation, one level down.

## Rate limits are keyed by credential and workspace

The agent bucket was keyed `"agent:" + agentID`, and the agent ID is read from
the **request body**. Two consequences, and the second is worse than the first:

- Agent IDs are unique per workspace, not per deployment, so two tenants with a
  `support-bot` already shared one bucket by accident.
- Because the ID came from the body rather than from anything verified, a
  member of one workspace could name **another tenant's** agent and burn its
  rate-limit budget on purpose — a cross-tenant denial of service needing no
  credential beyond a valid session of one's own.

The fix is not to validate the body value. It is to prefix every key with a
workspace nobody can assert: the one on the verified identity. A body field
then selects a bucket *within the caller's own tenant*, where naming your own
agents is exactly what the limiter is for.

Keys are also scoped to the **credential**, not only the subject. A person with
a long-lived API key and the same person in a browser session are two things an
operator may legitimately want bounded separately, and revoking one must not
hand the other a fresh budget.

`bucketUserKey` and `bucketKey` are the only two places a key is constructed.
The recorder, the middleware and the status endpoint all read through them —
four hand-rolled key expressions is how a limiter ends up checking a bucket
nothing fills.

## Configuring it

```yaml
costs:
  # The flat keys still work and are still enforced. Everything below
  # TIGHTENS them; nothing here can raise a ceiling the operator set.
  daily_budget_usd: 100
  quotas:
    deployment:
      daily_usd: 100
    organizations:
      org_acme: { daily_usd: 40 }
    workspaces:
      "": { daily_usd: 5 }          # every workspace, unless overridden
      ws_platform: { daily_usd: 25 }
    principals:
      usr_batch: { daily_tokens: 2000000 }
    agents:
      nightly-crawler: { daily_usd: 2 }
    models:
      "anthropic/expensive-model": { daily_usd: 10 }
```

The empty key at a level is that level's **default**, so "every workspace gets
$5" is expressible alongside per-workspace overrides. An entity present with
all-zero values is dropped rather than becoming a ceiling of zero — leaving a
key in place must not forbid everything for that entity.

A deployment that configures no `quotas` block builds no policy at all, so it
pays for no lookup and behaves byte-identically to before.

## Known gap

Nothing in the gateway currently calls `RecordTokens*`, so both token quotas
are inert: the middleware checks a bucket no production code fills. That
predates this work and is tracked separately — but the key had to be correct
before wiring the recorder would mean anything.
