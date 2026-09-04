package agent

// outcome.go — the persisted business-outcome contract (P0-4).
//
// These types deliberately live in pkg/agent rather than internal/studio: the
// contract has to be readable by the RUNTIME, and Studio's package is a build
// tool the runtime must not depend on. Studio authors the contract; the engine
// enforces it; SOUL.yaml carries it between them.
//
// The shape mirrors internal/studio.Assertion so a Studio-authored assertion
// round-trips without translation, and so a hand-written SOUL.yaml can declare
// one without opening Studio at all.

// OutcomeAssertion is one checkable claim about what a run must achieve.
//
//	target — "result" for the run's final output, or a flow node id
//	op     — exists | not_empty | contains | equals | count_gte | count_eq |
//	         has_field | field_equals | delivered | destination | artifact
//	value  — the operator's argument (a count, a dotted field path, a channel,
//	         a destination id, an artifact type); empty where not needed
type OutcomeAssertion struct {
	Target string `yaml:"target" json:"target"`
	Op     string `yaml:"op" json:"op"`
	Value  string `yaml:"value,omitempty" json:"value,omitempty"`
	// Describe is the plain-language claim this assertion encodes ("three
	// sources were added to the notebook"). Surfaced verbatim when the
	// assertion fails, so an operator reading a failure notification is told
	// what the agent was supposed to do rather than being handed an operator
	// name and a JSON path.
	Describe string `yaml:"describe,omitempty" json:"describe,omitempty"`
}

// OutcomeContract is the set of assertions a run must satisfy, plus how the
// runtime should treat a run that doesn't.
type OutcomeContract struct {
	// Assertions are evaluated against the completed run. Empty = no contract.
	Assertions []OutcomeAssertion `yaml:"assertions,omitempty" json:"assertions,omitempty"`

	// Enforce controls what an unmet contract DOES at run time:
	//
	//	"report" (default) — the run is marked degraded and the outcome is
	//	                     recorded, but delivery still happens. The safe
	//	                     default: adding a contract to an existing agent
	//	                     must not silently start dropping its output.
	//	"fail"             — the run is treated as failed. Scheduled delivery
	//	                     is suppressed for an unmet contract, so an empty
	//	                     brief never reaches the channel looking finished.
	Enforce string `yaml:"enforce,omitempty" json:"enforce,omitempty"`

	// Goal is the user's stated objective the assertions were derived from.
	// Kept so a failure can be explained in the user's own words, and so a
	// regeneration can check the assertions still match the intent.
	Goal string `yaml:"goal,omitempty" json:"goal,omitempty"`
}

// Outcome enforcement modes.
const (
	EnforceReport = "report"
	EnforceFail   = "fail"
)

// EnforcementMode returns the effective enforcement mode, defaulting to
// "report" so an agent that gains a contract never changes delivery behaviour
// until an operator opts in.
func (c *OutcomeContract) EnforcementMode() string {
	if c == nil || c.Enforce != EnforceFail {
		return EnforceReport
	}
	return EnforceFail
}

// HasAssertions reports whether this contract actually constrains anything.
func (c *OutcomeContract) HasAssertions() bool {
	return c != nil && len(c.Assertions) > 0
}
