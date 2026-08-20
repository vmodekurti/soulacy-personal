// Package quota resolves "may this run spend this" across the six levels a
// deployment can define limits at, and shares scarce concurrency fairly
// between tenants (MU-024).
//
// WHAT WAS MISSING. internal/costs already reserves budget atomically before a
// provider call, reconciles actual usage, and returns a typed rejection with
// remaining capacity and a reset time. What it had no concept of was a
// WORKSPACE. Its limits were global, per-user and per-agent, so a deployment's
// daily budget was a single pool every tenant drew from: one workspace's
// runaway agent exhausted it, and every other tenant's next call was refused
// with an accurate message about a budget they had not spent. "One user or
// agent cannot exhaust capacity" was true within a workspace and false between
// them, which is the only direction that matters once there is more than one.
//
// Provider concurrency had the same shape — one global semaphore, first-come —
// so a tenant submitting a hundred runs starved everyone behind them without
// exceeding any budget at all.
package quota

import (
	"fmt"
	"sort"
	"strings"
)

// Level is a scope a limit can be defined at. Ordered from broadest to
// narrowest; the order of these constants IS the documented precedence.
type Level int

const (
	// LevelDeployment is the whole process/cluster: the operator's ceiling on
	// what the deployment can spend regardless of who is asking.
	LevelDeployment Level = iota
	// LevelOrganization is one customer. Below deployment because a customer
	// cannot buy more than the deployment has.
	LevelOrganization
	// LevelWorkspace is one team within a customer.
	LevelWorkspace
	// LevelPrincipal is one user or service account.
	LevelPrincipal
	// LevelAgent is one agent definition.
	LevelAgent
	// LevelModel is one provider/model pair, the narrowest thing a limit can
	// name. Last because a limit on "expensive-model" is a statement about
	// that model, not about who is calling it.
	LevelModel
)

var levelNames = map[Level]string{
	LevelDeployment:   "deployment",
	LevelOrganization: "organization",
	LevelWorkspace:    "workspace",
	LevelPrincipal:    "principal",
	LevelAgent:        "agent",
	LevelModel:        "model",
}

func (l Level) String() string {
	if name, ok := levelNames[l]; ok {
		return name
	}
	return fmt.Sprintf("level(%d)", int(l))
}

// Levels in precedence order, broadest first.
func Levels() []Level {
	return []Level{LevelDeployment, LevelOrganization, LevelWorkspace, LevelPrincipal, LevelAgent, LevelModel}
}

// Limit is one configured ceiling. A zero field means "not limited at this
// level", which is different from zero: an explicit zero would forbid
// everything, and the zero value of a struct must not do that.
type Limit struct {
	// DailyMicros and MonthlyMicros cap spend in USD micros.
	DailyMicros   int64
	MonthlyMicros int64
	// DailyTokens caps token throughput over a rolling 24h window.
	DailyTokens int64
	// Concurrency caps simultaneous in-flight provider calls.
	Concurrency int
}

// IsZero reports whether this limit constrains nothing.
func (l Limit) IsZero() bool {
	return l.DailyMicros <= 0 && l.MonthlyMicros <= 0 && l.DailyTokens <= 0 && l.Concurrency <= 0
}

// Scope identifies the entity a limit applies to at one level.
type Scope struct {
	Level Level
	// ID is the entity — an organization id, a workspace id, a subject, an
	// agent id, or "provider/model". Empty means the level's default, which
	// applies to every entity at that level with no specific entry.
	ID string
}

func (s Scope) String() string {
	if s.ID == "" {
		return s.Level.String() + ":*"
	}
	return s.Level.String() + ":" + s.ID
}

// Policy is the configured set of limits.
type Policy struct {
	limits map[Scope]Limit
}

// Entries returns a copy of the policy's scoped limits.
//
// A COPY, not the map. A caller composing a new policy on top of this one —
// per-workspace limits stored at runtime, say — must not be able to mutate the
// operator's configured policy by holding a reference to its internals, which
// is what returning the map itself would permit. The one legitimate use is
// building a derived policy, and that only needs to read.
func (p *Policy) Entries() map[Scope]Limit {
	if p == nil {
		return nil
	}
	out := make(map[Scope]Limit, len(p.limits))
	for scope, limit := range p.limits {
		out[scope] = limit
	}
	return out
}

// NewPolicy builds a policy from configured limits.
func NewPolicy(limits map[Scope]Limit) *Policy {
	out := &Policy{limits: make(map[Scope]Limit, len(limits))}
	for scope, limit := range limits {
		scope.ID = strings.TrimSpace(scope.ID)
		if limit.IsZero() {
			continue
		}
		out.limits[scope] = limit
	}
	return out
}

// Subject is who is asking, at every level at once.
type Subject struct {
	OrganizationID string
	WorkspaceID    string
	PrincipalID    string
	AgentID        string
	Provider       string
	Model          string
}

// modelID is how a provider/model pair is named as a scope.
func (s Subject) modelID() string {
	provider := strings.TrimSpace(s.Provider)
	model := strings.TrimSpace(s.Model)
	if provider == "" && model == "" {
		return ""
	}
	return provider + "/" + model
}

func (s Subject) idAt(level Level) string {
	switch level {
	case LevelOrganization:
		return strings.TrimSpace(s.OrganizationID)
	case LevelWorkspace:
		return strings.TrimSpace(s.WorkspaceID)
	case LevelPrincipal:
		return strings.TrimSpace(s.PrincipalID)
	case LevelAgent:
		return strings.TrimSpace(s.AgentID)
	case LevelModel:
		return s.modelID()
	default:
		return ""
	}
}

// Applicable is one limit that applies to a subject, with the scope it came
// from so a rejection can name it.
type Applicable struct {
	Scope Scope
	Limit Limit
}

// Resolve returns every limit that applies to a subject, broadest first.
//
// EVERY APPLICABLE LIMIT IS ENFORCED — this is the part that is easy to get
// wrong, and getting it wrong is what made the old global budget bypassable.
// The tempting reading of "precedence" is that the narrowest configured level
// wins and the others are ignored. Under that rule, setting a generous
// per-agent limit would let one agent spend past the organization's budget,
// which inverts the whole point: a narrower scope is a SUBSET of a broader
// one, so its limit can only ever be a further restriction, never a licence.
//
// What precedence actually decides is which VALUE applies at a given level
// when both a specific entry and that level's default exist. A workspace with
// its own entry uses it; one without falls back to the workspace default.
//
// So: conjunctive across levels, most-specific-wins within a level.
func (p *Policy) Resolve(subject Subject) []Applicable {
	if p == nil {
		return nil
	}
	out := make([]Applicable, 0, len(Levels()))
	for _, level := range Levels() {
		id := subject.idAt(level)
		// Most specific first: an entry naming this entity beats the level's
		// catch-all default.
		if id != "" {
			if limit, ok := p.limits[Scope{Level: level, ID: id}]; ok {
				out = append(out, Applicable{Scope: Scope{Level: level, ID: id}, Limit: limit})
				continue
			}
		}
		if limit, ok := p.limits[Scope{Level: level}]; ok {
			out = append(out, Applicable{Scope: Scope{Level: level}, Limit: limit})
		}
	}
	return out
}

// Tightest returns the most restrictive value of each dimension across every
// applicable limit, and which scope contributed it.
//
// The scope matters as much as the number. "You have $0.00 remaining" is
// unactionable; "the organization's monthly budget is exhausted, resets in 9
// days" tells somebody what to do, and MU-024 criterion 6 asks for exactly
// that. A caller that only kept the number could not produce it.
type Tightest struct {
	DailyMicros   int64
	DailyScope    Scope
	MonthlyMicros int64
	MonthlyScope  Scope
	DailyTokens   int64
	TokenScope    Scope
	Concurrency   int
	ConcScope     Scope
}

// Tightest collapses the applicable limits into one effective ceiling per
// dimension.
func (p *Policy) Tightest(subject Subject) Tightest {
	var out Tightest
	for _, applicable := range p.Resolve(subject) {
		out.DailyMicros, out.DailyScope = tighter64(out.DailyMicros, out.DailyScope, applicable.Limit.DailyMicros, applicable.Scope)
		out.MonthlyMicros, out.MonthlyScope = tighter64(out.MonthlyMicros, out.MonthlyScope, applicable.Limit.MonthlyMicros, applicable.Scope)
		out.DailyTokens, out.TokenScope = tighter64(out.DailyTokens, out.TokenScope, applicable.Limit.DailyTokens, applicable.Scope)
		concurrency, scope := tighter64(int64(out.Concurrency), out.ConcScope, int64(applicable.Limit.Concurrency), applicable.Scope)
		out.Concurrency, out.ConcScope = int(concurrency), scope
	}
	return out
}

// tighter64 keeps the smaller POSITIVE value. Zero means unlimited, so it
// never wins — treating it as "smallest" would make an unconfigured level
// forbid everything, which is the failure mode a naive min() produces.
func tighter64(current int64, currentScope Scope, candidate int64, candidateScope Scope) (int64, Scope) {
	if candidate <= 0 {
		return current, currentScope
	}
	if current <= 0 || candidate < current {
		return candidate, candidateScope
	}
	return current, currentScope
}

// Describe renders the resolved precedence for one subject, for the docs and
// for an operator asking "why was I refused".
func (p *Policy) Describe(subject Subject) string {
	applicable := p.Resolve(subject)
	if len(applicable) == 0 {
		return "no limits apply"
	}
	sort.SliceStable(applicable, func(i, j int) bool { return applicable[i].Scope.Level < applicable[j].Scope.Level })
	parts := make([]string, 0, len(applicable))
	for _, a := range applicable {
		parts = append(parts, a.Scope.String())
	}
	return "all of: " + strings.Join(parts, ", ")
}
