// Package autopilot provides Soulacy's durable, operator-governed autonomy
// domain: deterministic mission checks, run proofs, learning proposals,
// deployments, and multi-agent goals.
package autopilot

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

var (
	ErrNotFound  = errors.New("autopilot: not found")
	ErrInvalid   = errors.New("autopilot: invalid input")
	ErrConflict  = errors.New("autopilot: conflict")
	ErrFrozen    = errors.New("autopilot: agent is frozen")
	ErrIntegrity = errors.New("autopilot: proof integrity check failed")
)

type CheckStatus string

const (
	CheckPass    CheckStatus = "pass"
	CheckFail    CheckStatus = "fail"
	CheckUnknown CheckStatus = "unknown"
)

// CheckResult is a deterministic check result included in a proof. Unknown is
// a first-class result: an absent cost or duration is not equivalent to zero.
type CheckResult struct {
	ID          string                 `json:"id"`
	Type        agent.MissionCheckType `json:"type"`
	Description string                 `json:"description,omitempty"`
	Status      CheckStatus            `json:"status"`
	Expected    string                 `json:"expected,omitempty"`
	Actual      string                 `json:"actual,omitempty"`
	Detail      string                 `json:"detail,omitempty"`
}

type ToolUse struct {
	Name   string `json:"name"`
	CallID string `json:"call_id,omitempty"`
	Status string `json:"status,omitempty"`
}

type Evidence struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	URI         string    `json:"uri,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type ExternalChange struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Summary    string `json:"summary"`
	Reversible bool   `json:"reversible"`
}

// Observation is the trusted runtime data used to evaluate MissionContract.
// Pointer metrics intentionally preserve the distinction between unmeasured
// and a measured zero.
type Observation struct {
	Output   string
	Tools    []ToolUse
	Duration *time.Duration
	CostUSD  *float64
}

type MissionEvaluation struct {
	Verification CheckStatus   `json:"verification"`
	Checks       []CheckResult `json:"checks"`
}

type ProofOutcome string

const (
	ProofSucceeded ProofOutcome = "succeeded"
	ProofFailed    ProofOutcome = "failed"
	ProofCancelled ProofOutcome = "cancelled"
)

type RunClaimStatus string

const (
	RunClaimClaimed   RunClaimStatus = "claimed"
	RunClaimUncertain RunClaimStatus = "uncertain"
	RunClaimFinalized RunClaimStatus = "finalized"
)

type RunClaim struct {
	Subject   string         `json:"subject"`
	RunID     string         `json:"run_id"`
	AgentID   string         `json:"agent_id"`
	Status    RunClaimStatus `json:"status"`
	ProofID   string         `json:"proof_id,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// ProofInput is the one-shot input to SaveProof. SaveProof assigns missing IDs
// and timestamps, derives Verification from Checks, and computes ProofHash.
type ProofInput struct {
	ID              string
	Subject         string
	MissionID       string
	RunID           string
	AgentID         string
	SessionID       string
	Outcome         ProofOutcome
	Simulation      bool
	RevisionHash    string
	Checks          []CheckResult
	Evidence        []Evidence
	Tools           []ToolUse
	ExternalChanges []ExternalChange
	CostUSD         *float64
	DurationMS      *int64
	CompletedAt     time.Time
}

// ProofRecord is immutable once inserted. ProofHash is a sha256:<lowercase hex>
// integrity checksum over every other field. It detects corruption but is not
// a signature or security attestation against an attacker who can rewrite the
// database.
type ProofRecord struct {
	ID              string           `json:"id"`
	Subject         string           `json:"subject"`
	MissionID       string           `json:"mission_id,omitempty"`
	RunID           string           `json:"run_id"`
	AgentID         string           `json:"agent_id"`
	SessionID       string           `json:"session_id"`
	Outcome         ProofOutcome     `json:"outcome"`
	Simulation      bool             `json:"simulation"`
	Verification    CheckStatus      `json:"verification"`
	RevisionHash    string           `json:"revision_hash,omitempty"`
	Checks          []CheckResult    `json:"checks"`
	Evidence        []Evidence       `json:"evidence"`
	Tools           []ToolUse        `json:"tools"`
	ExternalChanges []ExternalChange `json:"external_changes"`
	CostUSD         *float64         `json:"cost_usd,omitempty"`
	DurationMS      *int64           `json:"duration_ms,omitempty"`
	CompletedAt     time.Time        `json:"completed_at"`
	ProofHash       string           `json:"proof_hash"`
}

type ProofFilter struct {
	AgentID      string
	RunID        string
	RevisionHash string
	Outcome      ProofOutcome
	Verification CheckStatus
	Simulation   *bool
	Limit        int
}

// ReliabilitySummary contains raw counts as well as rates. Rates are nil when
// their denominator is unknown/empty, and point to 0 when zero was observed.
type ReliabilitySummary struct {
	Subject                  string   `json:"subject"`
	AgentID                  string   `json:"agent_id"`
	RevisionHash             string   `json:"revision_hash,omitempty"`
	SampleCount              int64    `json:"sample_count"`
	SuccessCount             int64    `json:"success_count"`
	VerifiedCount            int64    `json:"verified_count"`
	VerificationFailedCount  int64    `json:"verification_failed_count"`
	VerificationUnknownCount int64    `json:"verification_unknown_count"`
	SuccessRate              *float64 `json:"success_rate,omitempty"`
	VerificationRate         *float64 `json:"verification_rate,omitempty"`
	Score                    *float64 `json:"score,omitempty"`
}

type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "pending"
	ProposalAccepted ProposalStatus = "accepted"
	ProposalRejected ProposalStatus = "rejected"
)

type ProposalVerification struct {
	Status     CheckStatus `json:"status"`
	RunID      string      `json:"run_id,omitempty"`
	Detail     string      `json:"detail,omitempty"`
	VerifiedAt *time.Time  `json:"verified_at,omitempty"`
}

type ProposalDraft struct {
	ID             string
	Subject        string
	AgentID        string
	SourceProofID  string
	FailureSummary string
	CandidateCheck agent.MissionCheck
	RulePatch      string
}

// LearningProposal is a candidate only. Accepting it records an operator
// decision; this package intentionally has no method that writes a rulebook.
type LearningProposal struct {
	ID             string               `json:"id"`
	Subject        string               `json:"subject"`
	AgentID        string               `json:"agent_id"`
	SourceProofID  string               `json:"source_proof_id"`
	FailureSummary string               `json:"failure_summary"`
	CandidateCheck agent.MissionCheck   `json:"candidate_check"`
	RulePatch      string               `json:"rule_patch,omitempty"`
	Baseline       ProposalVerification `json:"baseline"`
	Candidate      ProposalVerification `json:"candidate"`
	Status         ProposalStatus       `json:"status"`
	DecisionReason string               `json:"decision_reason,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
	ReviewedAt     *time.Time           `json:"reviewed_at,omitempty"`
}

type ProposalFilter struct {
	AgentID string
	Status  ProposalStatus
	Limit   int
}

type DeploymentChannel string

const (
	ChannelDraft      DeploymentChannel = "draft"
	ChannelSimulation DeploymentChannel = "simulation"
	ChannelCanary     DeploymentChannel = "canary"
	ChannelStable     DeploymentChannel = "stable"
)

type DeploymentVersionInput struct {
	ID             string
	Subject        string
	AgentID        string
	Version        string
	DefinitionJSON json.RawMessage
}

// DeploymentVersion is append-only; channel pointers live separately.
type DeploymentVersion struct {
	ID             string          `json:"id"`
	Subject        string          `json:"subject"`
	AgentID        string          `json:"agent_id"`
	Version        string          `json:"version"`
	RevisionHash   string          `json:"revision_hash"`
	DefinitionJSON json.RawMessage `json:"definition_json"`
	CreatedAt      time.Time       `json:"created_at"`
}

type PromotionGates struct {
	MinSamples          int64    `json:"min_samples,omitempty"`
	MinSuccessRate      *float64 `json:"min_success_rate,omitempty"`
	MinVerificationRate *float64 `json:"min_verification_rate,omitempty"`
}

type GateEvaluation struct {
	Passed      bool               `json:"passed"`
	Reasons     []string           `json:"reasons"`
	Reliability ReliabilitySummary `json:"reliability"`
}

type DeploymentChannelState struct {
	Channel           DeploymentChannel `json:"channel"`
	CurrentVersionID  string            `json:"current_version_id,omitempty"`
	PreviousVersionID string            `json:"previous_version_id,omitempty"`
	TrafficPercent    int               `json:"traffic_percent"`
	Gates             PromotionGates    `json:"gates"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

type DeploymentStatus struct {
	Subject      string                   `json:"subject"`
	AgentID      string                   `json:"agent_id"`
	Frozen       bool                     `json:"frozen"`
	FreezeReason string                   `json:"freeze_reason,omitempty"`
	Channels     []DeploymentChannelState `json:"channels"`
}

type PromotionRequest struct {
	Subject        string
	AgentID        string
	VersionID      string
	ToChannel      DeploymentChannel
	TrafficPercent int
	Gates          PromotionGates
}

type RollbackRequest struct {
	Subject string
	AgentID string
	Channel DeploymentChannel
	Reason  string
	// ExpectedVersionID prevents a stale operator view from rolling back a
	// newer release. Checked inside the same transaction as the swap.
	ExpectedVersionID string
}

type DeploymentResolution struct {
	VersionID      string            `json:"version_id"`
	RevisionHash   string            `json:"revision_hash"`
	Channel        DeploymentChannel `json:"channel"`
	DefinitionJSON json.RawMessage   `json:"definition_json"`
}

type GoalStatus string

const (
	GoalStatusDraft GoalStatus = "draft"
	GoalRunning     GoalStatus = "running"
	GoalSucceeded   GoalStatus = "succeeded"
	GoalFailed      GoalStatus = "failed"
	GoalCancelled   GoalStatus = "cancelled"
)

type GoalTaskStatus string

const (
	GoalTaskPending   GoalTaskStatus = "pending"
	GoalTaskRunning   GoalTaskStatus = "running"
	GoalTaskSucceeded GoalTaskStatus = "succeeded"
	GoalTaskFailed    GoalTaskStatus = "failed"
	GoalTaskBlocked   GoalTaskStatus = "blocked"
	GoalTaskCancelled GoalTaskStatus = "cancelled"
)

// ExecutionBudget is required for goals and their tasks. MaxCostUSD may be
// zero to prohibit spend exactly; MaxDurationMS must be positive. Every
// executable unit is therefore finite by construction.
type ExecutionBudget struct {
	MaxCostUSD    float64 `json:"max_cost_usd"`
	MaxDurationMS int64   `json:"max_duration_ms"`
}

type GoalTaskDraft struct {
	ID        string
	Title     string
	Prompt    string
	AgentID   string
	DependsOn []string
	Budget    ExecutionBudget
}

type GoalDraft struct {
	ID        string
	Subject   string
	Title     string
	Objective string
	Budget    ExecutionBudget
	Tasks     []GoalTaskDraft
}

type GoalTask struct {
	ID         string          `json:"id"`
	GoalID     string          `json:"goal_id"`
	Title      string          `json:"title"`
	Prompt     string          `json:"prompt"`
	AgentID    string          `json:"agent_id"`
	Status     GoalTaskStatus  `json:"status"`
	DependsOn  []string        `json:"depends_on"`
	Budget     ExecutionBudget `json:"budget"`
	RunID      string          `json:"run_id,omitempty"`
	ProofID    string          `json:"proof_id,omitempty"`
	Result     string          `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CostUSD    *float64        `json:"cost_usd,omitempty"`
	DurationMS *int64          `json:"duration_ms,omitempty"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

type Goal struct {
	ID              string          `json:"id"`
	Subject         string          `json:"subject"`
	Title           string          `json:"title"`
	Objective       string          `json:"objective"`
	Status          GoalStatus      `json:"status"`
	Error           string          `json:"error,omitempty"`
	Budget          ExecutionBudget `json:"budget"`
	CostUSD         float64         `json:"cost_usd"`
	ReservedCostUSD float64         `json:"reserved_cost_usd"`
	DurationMS      int64           `json:"duration_ms"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	Tasks           []GoalTask      `json:"tasks"`
}

type GoalFilter struct {
	Status GoalStatus
	Limit  int
}

type FinishGoalTaskRequest struct {
	Subject    string
	GoalID     string
	TaskID     string
	Status     GoalTaskStatus
	ProofID    string
	Result     string
	Error      string
	CostUSD    *float64
	DurationMS *int64
}
