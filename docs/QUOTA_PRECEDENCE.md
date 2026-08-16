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
