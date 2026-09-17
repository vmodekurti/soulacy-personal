// Package agentsave holds the gates every agent must pass before it reaches
// disk, whoever asked for it.
//
// There were three ways an agent got written, with three different sets of
// guarantees. Studio's save validated, refused to overwrite a protected
// built-in, and blocked a privileged agent being put on a channel without
// explicit consent. The conversational builder validated and nothing else.
// Genie's create_monitor did neither: it cloned Genie, inherited every tool
// including the one that makes more monitors, and wrote straight to disk.
//
// Which gate you got therefore depended on which door you came through, and
// the weakest door was the one a model could open unattended. This package is
// the single answer to "may this be saved", so the question is asked the same
// way every time.
//
// It deliberately does not decide whether the agent should be *enabled*, or
// whether its schedule should be armed. Those are context, not safety: Studio
// stages a new agent disabled so a person reviews it, while the front door
// enables it precisely so the person can watch it run once. Both are right,
// and folding them in here would force one of them to be wrong.
package agentsave

import (
	"context"
	"strings"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/tier"
	"github.com/soulacy/soulacy/pkg/agent"
)

// ConsentItem is one thing the user is being asked to accept.
type ConsentItem struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Finding is a validation error that blocks the save.
type Finding struct {
	Field   string `json:"field,omitempty"`
	Problem string `json:"problem"`
	Fix     string `json:"fix,omitempty"`
}

// Decision is the answer. Allowed is true only when nothing blocks.
type Decision struct {
	Allowed bool

	// Refused is set when the agent may never be written at all — currently
	// only an attempt to overwrite a protected built-in.
	Refused string

	// Blockers are validation errors. An agent whose tools point at files that
	// do not exist is not a saveable agent, however it was authored.
	Blockers []Finding

	// RequiresConsent means a privileged-tier agent would be reachable on a
	// channel. That is a decision for a person, not for whatever produced the
	// definition, so it is never inferred from the caller's intent.
	RequiresConsent bool
	ConsentItems    []ConsentItem
}

// Options carries what the gates need from the caller.
type Options struct {
	// Validate configures agentvalidate. The zero value still validates, just
	// without provider/model knowledge.
	Validate agentvalidate.Options

	// Peer resolves a named agent, so tier classification can follow
	// delegation. Nil is allowed: an unresolvable peer simply cannot raise the
	// tier, which is the same position Studio is in for a fresh draft.
	Peer func(string) *agent.Definition

	// AcceptPrivilegedExposure records that a person has already said yes.
	AcceptPrivilegedExposure bool
}

// Gate answers whether this definition may be written.
func Gate(ctx context.Context, def *agent.Definition, opts Options) Decision {
	_ = ctx // reserved: validation may take a context in future

	if def == nil {
		return Decision{Refused: "no agent definition"}
	}
	id := strings.TrimSpace(def.ID)
	if id == "" {
		return Decision{Blockers: []Finding{{Field: "id", Problem: "an agent needs an id"}}}
	}

	// A protected built-in is never overwritten, whatever else is true. This
	// is checked first because it is a refusal, not a finding to fix.
	if id == runtime.SystemAgentID || id == runtime.GenieAgentID {
		return Decision{Refused: "agent " + id + " is a protected built-in and cannot be overwritten"}
	}

	d := Decision{}

	report := agentvalidate.Definition(def, def.SourcePath, opts.Validate, agentvalidate.Report{})
	for _, f := range report.Findings {
		if f.Severity != agentvalidate.Error {
			continue
		}
		d.Blockers = append(d.Blockers, Finding{Field: f.Field, Problem: f.Message, Fix: f.Suggestion})
	}

	// The same rule Studio applies: privileged tier plus a channel is an
	// exposure someone has to accept. Studio evaluates a draft, this evaluates
	// a definition, and both end at tier.Explain.
	exp := tier.Explain(def, opts.Peer)
	if exp.Tier == tier.Privileged && len(def.Channels) > 0 {
		reason := strings.Join(exp.Reasons, "; ")
		for _, ch := range def.Channels {
			d.ConsentItems = append(d.ConsentItems, ConsentItem{Kind: "channel", Name: ch, Reason: reason})
		}
		d.RequiresConsent = !opts.AcceptPrivilegedExposure
	}

	d.Allowed = d.Refused == "" && len(d.Blockers) == 0 && !d.RequiresConsent
	return d
}
