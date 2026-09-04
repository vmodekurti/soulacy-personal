package tour

// narrate.go — one outcome, told from wherever you are standing.
//
// Every page's tour tells the SAME story: how an agent comes to do real work
// without you watching, and how you find out it worked. What changes per page
// is which link of that chain you are looking at, and what changes per install
// is where you actually are in it.
//
// The alternative — a paragraph per page describing its controls — is what
// tooltips already do, badly. Someone who does not know what Queues are for
// does not need Queues defined; they need to know that Queues is where work
// waits between the steps of a thing they are trying to build, and whether
// they need to care yet. Usually they do not, and saying so is the useful part.

import (
	"fmt"
)

// Outcome is the thing the whole platform is for. Every page's story is told
// as a step towards it, so a user always knows why they are looking at this
// screen and not another.
const Outcome = "an agent that does a real job on its own, and tells you it did"

// Beat is one paragraph of the story. Splitting it up lets the UI pace the
// telling instead of dropping a wall of text.
type Beat struct {
	// Heading is optional; a beat without one continues the previous thought.
	Heading string `json:"heading,omitempty"`
	Text    string `json:"text"`
}

// PageTour is the whole narrative for one screen.
type PageTour struct {
	Page string `json:"page"`
	// Chapter is where this page sits in the chain ("brain", "voice", …).
	Chapter string `json:"chapter"`
	// Position is human, not numeric: "step 2 of the chain, and the one you
	// are missing" reads better than "2/8".
	Position string `json:"position"`
	Outcome  string `json:"outcome"`
	Beats    []Beat `json:"beats"`
	// NextAction is a fix-action id from internal/studio's shared vocabulary,
	// so the tour's call to action is the same button the findings use. Empty
	// when the honest next step is simply to read the page.
	NextAction string `json:"nextAction,omitempty"`
	NextLabel  string `json:"nextLabel,omitempty"`
	// Blocked names the earlier link that has to exist first, if any. A tour
	// that explains Automations to someone with no agent is a tour that wastes
	// their time; saying so is more useful than the explanation.
	Blocked string `json:"blocked,omitempty"`
}

// page describes one screen's place in the chain and how to tell its part.
type page struct {
	stage Stage
	// role is the one-line answer to "why does this screen exist".
	role string
	// contribution is what this page gives the outcome, in plain terms.
	contribution string
	// whenEmpty is the story when this page has nothing in it yet.
	whenEmpty func(InstallState) string
	// whenUsed is the story once it does.
	whenUsed func(InstallState) string
	// nextAction, when set, is offered as the call to action.
	nextAction string
	nextLabel  string
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
