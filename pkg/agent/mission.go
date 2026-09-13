package agent

// MissionCheckType identifies one deterministic run acceptance check.
//
// Business outcomes are deliberately represented by MissionCheckClaim rather
// than being smuggled into an output substring check. The runtime can record
// the claim, but it cannot mark it as satisfied without an external verifier.
type MissionCheckType string

const (
	MissionCheckOutputContains    MissionCheckType = "output_contains"
	MissionCheckOutputNotContains MissionCheckType = "output_not_contains"
	MissionCheckOutputRegex       MissionCheckType = "output_regex"
	MissionCheckRequiredTool      MissionCheckType = "required_tool"
	MissionCheckMaxDuration       MissionCheckType = "max_duration"
	MissionCheckMaxCost           MissionCheckType = "max_cost"
	MissionCheckAllowedTools      MissionCheckType = "allowed_tools"
	MissionCheckClaim             MissionCheckType = "business_outcome"
)

// MissionCheck is one typed acceptance condition in a MissionContract.
// Only the field associated with Type is used:
//
//   - output_contains, output_not_contains, output_regex: Value
//   - required_tool: Tool
//   - max_duration: Duration (Go duration syntax, for example "2m")
//   - max_cost: CostUSD
//   - business_outcome: Claim
//
// ID is optional for hand-authored SOUL.yaml. Evaluators assign a stable
// positional id ("check-1", "check-2", ...) when it is absent.
type MissionCheck struct {
	ID          string           `yaml:"id,omitempty" json:"id,omitempty"`
	Type        MissionCheckType `yaml:"type" json:"type"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	Value       string           `yaml:"value,omitempty" json:"value,omitempty"`
	Tool        string           `yaml:"tool,omitempty" json:"tool,omitempty"`
	Duration    string           `yaml:"duration,omitempty" json:"duration,omitempty"`
	CostUSD     *float64         `yaml:"cost_usd,omitempty" json:"cost_usd,omitempty"`
	Claim       string           `yaml:"claim,omitempty" json:"claim,omitempty"`
}

// MissionContract declares what a run is meant to achieve and the checks that
// can be applied without asking a model to grade its own work. It complements
// OutcomeContract: OutcomeContract describes workflow-node business outputs;
// MissionContract adds run-wide output, tool, duration, cost, and externally
// verifiable business claims used by Autopilot proof records.
type MissionContract struct {
	ID         string         `yaml:"id,omitempty" json:"id,omitempty"`
	Goal       string         `yaml:"goal,omitempty" json:"goal,omitempty"`
	Acceptance []MissionCheck `yaml:"acceptance,omitempty" json:"acceptance,omitempty"`
	Limits     MissionLimits  `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// MissionLimits are hard runtime governors as well as proof checks. A nil
// AllowedTools pointer preserves the agent's existing tool policy; a pointer
// to an empty slice explicitly denies every tool. Wildcards are intentionally
// unsupported because an autonomy boundary should enumerate its authority.
type MissionLimits struct {
	MaxCostUSD   *float64  `yaml:"max_cost_usd,omitempty" json:"max_cost_usd,omitempty"`
	MaxDuration  string    `yaml:"max_duration,omitempty" json:"max_duration,omitempty"`
	AllowedTools *[]string `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
}

// HasChecks reports whether this contract constrains or documents a run.
func (m *MissionContract) HasChecks() bool {
	return m != nil && (len(m.Acceptance) > 0 || m.Limits.MaxCostUSD != nil ||
		m.Limits.MaxDuration != "" || m.Limits.AllowedTools != nil)
}
