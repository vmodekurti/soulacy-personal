package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/autopilot"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type autopilotRuntime struct {
	store  *autopilot.Store
	cost   func(context.Context, string) (*float64, error)
	mu     sync.Mutex
	active map[string]managedRun
}
type managedRun struct {
	subject, agent string
	cancel         context.CancelFunc
}
type autopilotRunKey struct{}
type autopilotVersionKey struct{}
type managedRunContext struct {
	definition *agent.Definition
	parent     *managedRunContext
	simulation bool
}
type autopilotVersion struct {
	id, agentID string
	simulation  bool
}

// SetAutopilot is called before serving traffic. A nil store deliberately
// disables execution rather than silently bypassing saved freezes or releases.
func (e *Engine) SetAutopilot(store *autopilot.Store, cost func(context.Context, string) (*float64, error)) {
	e.autopilot = &autopilotRuntime{store: store, cost: cost, active: map[string]managedRun{}}
}

// WithAutopilotVersion selects an authorized immutable version for a test run.
// This is a trusted context input, never read from a user's message metadata.
func WithAutopilotVersion(ctx context.Context, agentID, versionID string, simulation bool) context.Context {
	return context.WithValue(ctx, autopilotVersionKey{}, autopilotVersion{versionID, agentID, simulation})
}

func autopilotSubject(ctx context.Context) string {
	if p, ok := PrincipalFromContext(ctx); ok && strings.TrimSpace(p.Subject) != "" {
		return p.Subject
	}
	return "admin"
}

func (e *Engine) definitionForContext(ctx context.Context, id string) *agent.Definition {
	for scope, _ := ctx.Value(autopilotRunKey{}).(*managedRunContext); scope != nil; scope = scope.parent {
		if scope.definition.ID == id {
			return scope.definition
		}
	}
	return e.loader.Get(id)
}

func autopilotManaged(ctx context.Context) bool {
	scope, _ := ctx.Value(autopilotRunKey{}).(*managedRunContext)
	return scope != nil
}

func missionToolAllowed(ctx context.Context, name string) error {
	for scope, _ := ctx.Value(autopilotRunKey{}).(*managedRunContext); scope != nil; scope = scope.parent {
		if m := scope.definition.Mission; m != nil && m.Limits.AllowedTools != nil && !slices.Contains(*m.Limits.AllowedTools, name) {
			return fmt.Errorf("mission does not allow tool %q", name)
		}
	}
	return ctx.Err()
}

func missionSimulation(ctx context.Context) bool {
	scope, _ := ctx.Value(autopilotRunKey{}).(*managedRunContext)
	return scope != nil && scope.simulation
}

func (e *Engine) CancelAutopilotAgent(subject, agentID string) int {
	if e.autopilot == nil {
		return 0
	}
	e.autopilot.mu.Lock()
	defer e.autopilot.mu.Unlock()
	count := 0
	for _, run := range e.autopilot.active {
		if run.subject == subject && run.agent == agentID {
			run.cancel()
			count++
		}
	}
	return count
}

// AutopilotPersistenceTimeout is a short shutdown-safe database deadline,
// capped by the operator's configured tool timeout hierarchy.
func (e *Engine) AutopilotPersistenceTimeout() time.Duration {
	const maximum = 5 * time.Second
	configured := e.effectiveToolTimeout(context.Background())
	if configured <= 0 {
		return maximum
	}
	return min(configured, maximum)
}

// Handle owns the full proof boundary, including terminal verification. All
// ingress paths and nested peer runs use the same wrapper.
func (e *Engine) Handle(ctx context.Context, msg message.Message) (reply message.Message, runErr error) {
	if e.autopilot == nil {
		return e.handle(ctx, msg)
	}
	if e.autopilot.store == nil {
		return reply, fmt.Errorf("autopilot storage is unavailable")
	}
	started := time.Now().UTC()
	def := e.loader.Get(msg.AgentID)
	if def == nil {
		return reply, fmt.Errorf("unknown agent %q", msg.AgentID)
	}
	def = def.Clone()
	subject := autopilotSubject(ctx)
	runID := uuid.NewString()
	if msg.ID != "" {
		runID = msg.ID
	}
	if len(runID) > 256 || strings.TrimSpace(runID) != runID {
		return reply, fmt.Errorf("invalid run id")
	}
	if msg.SessionID == "" {
		msg.SessionID = uuid.NewString()
	}
	meta := llm.CallMetadataFromContext(ctx)
	meta.RunID, meta.AgentID, meta.SessionID, meta.Subject = runID, msg.AgentID, msg.SessionID, subject
	ctx = llm.WithCallMetadata(ctx, meta)
	resolution, err := e.autopilot.store.ResolveDefinition(ctx, subject, msg.AgentID, runID)
	if err != nil && !errors.Is(err, autopilot.ErrNotFound) {
		return reply, err
	}
	simulation := missionSimulation(ctx) || dryRunFrom(ctx) || def.DryRun
	if override, ok := ctx.Value(autopilotVersionKey{}).(autopilotVersion); ok && override.agentID == msg.AgentID {
		version, err := e.autopilot.store.GetDeploymentVersion(ctx, subject, override.id)
		if err != nil {
			return reply, err
		}
		if version.AgentID != msg.AgentID {
			return reply, autopilot.ErrNotFound
		}
		resolution.DefinitionJSON = version.DefinitionJSON
		resolution.RevisionHash = version.RevisionHash
		simulation = simulation || override.simulation
	}
	if len(resolution.DefinitionJSON) > 0 {
		source := def.SourcePath
		if err := json.Unmarshal(resolution.DefinitionJSON, &def); err != nil {
			return reply, fmt.Errorf("load deployment definition: %w", err)
		}
		if def == nil || def.ID != msg.AgentID {
			return reply, fmt.Errorf("deployment agent identity does not match")
		}
		def.SourcePath = source
	}
	if err := autopilot.ValidateMission(def.Mission); err != nil {
		return reply, err
	}
	simulation = simulation || def.DryRun
	definitionJSON, err := json.Marshal(def)
	if err != nil {
		return reply, err
	}
	revision := resolution.RevisionHash
	if revision == "" {
		_, revision, err = autopilot.CanonicalDefinition(definitionJSON)
		if err != nil {
			return reply, err
		}
	}
	regressions, err := e.autopilot.store.ListProposals(ctx, subject, autopilot.ProposalFilter{AgentID: msg.AgentID, Status: autopilot.ProposalAccepted, Limit: 500})
	if err != nil {
		return reply, fmt.Errorf("load approved regression checks: %w", err)
	}
	parent, _ := ctx.Value(autopilotRunKey{}).(*managedRunContext)
	ctx = context.WithValue(ctx, autopilotRunKey{}, &managedRunContext{definition: def, parent: parent, simulation: simulation})
	ctx = llm.WithRunCostTracking(ctx)
	if def.Mission != nil {
		if limit := def.Mission.Limits.MaxCostUSD; limit != nil {
			// Zero means no inference spend, including locally unpriced calls.
			micros := int64(*limit * 1e6)
			ctx = llm.WithRunCostBudget(ctx, micros)
		}
	}
	timeout := def.ResolvedRunTimeout(e.effectiveRunTimeout())
	if def.Mission != nil && def.Mission.Limits.MaxDuration != "" {
		d, _ := time.ParseDuration(def.Mission.Limits.MaxDuration)
		if d > 0 && d < timeout {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := e.autopilot.store.ClaimRun(ctx, subject, runID, msg.AgentID); err != nil {
		return reply, err
	}
	activeKey := subject + "\x00" + runID
	e.autopilot.mu.Lock()
	e.autopilot.active[activeKey] = managedRun{subject: subject, agent: msg.AgentID, cancel: cancel}
	e.autopilot.mu.Unlock()
	defer func() { e.autopilot.mu.Lock(); delete(e.autopilot.active, activeKey); e.autopilot.mu.Unlock() }()
	// Close the freeze/admission race before starting any work. A freeze after
	// this check sees the registered cancel function and stops this run.
	if _, err := e.autopilot.store.ResolveDefinition(ctx, subject, msg.AgentID, runID); err != nil && !errors.Is(err, autopilot.ErrNotFound) {
		runErr = err
	}
	var toolsMu sync.Mutex
	tools := []autopilot.ToolUse{}
	toolEvidence := []autopilot.Evidence{}
	previousObserver := toolObserverFrom(ctx)
	ctx = WithToolObserver(ctx, func(call message.ToolCall, result string, failed bool) {
		status := "succeeded"
		if failed {
			status = "failed"
		} else if simulation {
			status = "simulated"
		}
		toolsMu.Lock()
		tools = append(tools, autopilot.ToolUse{Name: call.Name, CallID: call.ID, Status: status})
		resultHash := sha256.Sum256([]byte(result))
		toolEvidence = append(toolEvidence, autopilot.Evidence{ID: fmt.Sprintf("tool-%d", len(tools)), Kind: "tool_receipt", Title: call.Name, Summary: status, ContentHash: "sha256:" + hex.EncodeToString(resultHash[:]), CreatedAt: time.Now().UTC()})
		toolsMu.Unlock()
		if previousObserver != nil {
			previousObserver(call, result, failed)
		}
	})
	if runErr == nil {
		reply, runErr = e.handle(ctx, msg)
	}
	duration := time.Since(started)
	durationMS := duration.Milliseconds()
	var cost *float64
	if e.llmRouter != nil && e.llmRouter.SupportsRunCostBudgets() {
		cost = llm.RunCostObservation(ctx)
	} else if e.autopilot.cost != nil {
		cctx, done := context.WithTimeout(context.WithoutCancel(ctx), e.AutopilotPersistenceTimeout())
		cost, _ = e.autopilot.cost(cctx, runID)
		done()
	}
	toolsMu.Lock()
	observed := append([]autopilot.ToolUse(nil), tools...)
	observedEvidence := append([]autopilot.Evidence(nil), toolEvidence...)
	toolsMu.Unlock()
	output := flattenParts(reply.Parts)
	observation := autopilot.Observation{Output: output, Tools: observed, Duration: &duration, CostUSD: cost}
	evaluation := autopilot.EvaluateMission(def.Mission, observation)
	for _, regression := range regressions {
		check := regression.CandidateCheck
		check.ID = "regression:" + regression.ID
		extra := autopilot.EvaluateMission(&agent.MissionContract{Acceptance: []agent.MissionCheck{check}}, observation)
		evaluation.Checks = append(evaluation.Checks, extra.Checks...)
		if extra.Verification == autopilot.CheckFail {
			evaluation.Verification = autopilot.CheckFail
		}
		if extra.Verification == autopilot.CheckUnknown && evaluation.Verification != autopilot.CheckFail {
			evaluation.Verification = autopilot.CheckUnknown
		}
	}
	for i := range evaluation.Checks {
		evaluation.Checks[i].Actual = truncateAutopilotText(redact.Text(evaluation.Checks[i].Actual), 2048)
	}
	if len(evaluation.Checks) > 0 {
		evaluation.Verification = autopilot.CheckPass
		for _, check := range evaluation.Checks {
			if check.Status == autopilot.CheckFail {
				evaluation.Verification = autopilot.CheckFail
				break
			}
			if check.Status != autopilot.CheckPass {
				evaluation.Verification = autopilot.CheckUnknown
			}
		}
	}
	if runErr == nil && evaluation.Verification == autopilot.CheckFail {
		runErr = fmt.Errorf("mission acceptance checks failed")
	}
	outcome := autopilot.ProofSucceeded
	if runErr != nil {
		outcome = autopilot.ProofFailed
		if errors.Is(runErr, context.Canceled) {
			outcome = autopilot.ProofCancelled
		}
	}
	if reply.Metadata != nil && strings.EqualFold(reply.Metadata[message.MetaReasoningDegraded], "true") {
		outcome = autopilot.ProofFailed
	}
	outputHash := sha256.Sum256([]byte(output))
	evidence := []autopilot.Evidence{{ID: "output", Kind: "output", Title: "Final output", ContentHash: "sha256:" + hex.EncodeToString(outputHash[:]), CreatedAt: time.Now().UTC()}}
	evidence = append(evidence, observedEvidence...)
	if runErr != nil {
		evidence = append(evidence, autopilot.Evidence{ID: "error", Kind: "error", Title: "Run failure", Summary: truncateAutopilotText(redact.Text(runErr.Error()), 2048), CreatedAt: time.Now().UTC()})
	}
	input := autopilot.ProofInput{ID: runID, Subject: subject, RunID: runID, AgentID: msg.AgentID, SessionID: msg.SessionID, Outcome: outcome, RevisionHash: revision, Checks: evaluation.Checks, Evidence: evidence, Tools: observed, CostUSD: cost, DurationMS: &durationMS, Simulation: simulation}
	if def.Mission != nil {
		input.MissionID = def.Mission.ID
	}
	saveCtx, done := context.WithTimeout(context.WithoutCancel(ctx), e.AutopilotPersistenceTimeout())
	proof, proofErr := e.autopilot.store.SaveProof(saveCtx, input)
	if proofErr == nil && !simulation && def.Mission != nil {
		for index, check := range def.Mission.Acceptance {
			if check.ID == "" {
				check.ID = fmt.Sprintf("check-%d", index+1)
			}
			for _, result := range evaluation.Checks {
				if result.ID != check.ID || result.Status != autopilot.CheckFail {
					continue
				}
				_, proposalErr := e.autopilot.store.CreateProposal(saveCtx, autopilot.ProposalDraft{ID: runID + ":" + check.ID, Subject: subject, AgentID: msg.AgentID, SourceProofID: proof.ID, FailureSummary: "Acceptance check failed: " + truncateAutopilotText(redact.Text(check.Description), 500), CandidateCheck: check, RulePatch: "Retain this assertion as a reviewed regression check; no instructions are changed automatically."})
				if proposalErr != nil && !errors.Is(proposalErr, autopilot.ErrConflict) {
					runErr = errors.Join(runErr, fmt.Errorf("persist regression proposal: %w", proposalErr))
				}
			}
		}
	}
	done()
	if proofErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("could not persist run proof: %w", proofErr))
	}
	if reply.Metadata == nil {
		reply.Metadata = map[string]string{}
	}
	if proofErr == nil {
		reply.Metadata["autopilot_proof_id"] = proof.ID
		reply.Metadata["autopilot_verification"] = string(proof.Verification)
	}
	if runErr != nil {
		reply.Metadata[message.MetaReasoningDegraded] = "true"
	}
	if e.sink != nil {
		e.sink.Emit(message.Event{Type: "run.completed", AgentID: msg.AgentID, SessionID: msg.SessionID, Timestamp: time.Now().UTC(), Payload: map[string]any{"run_id": runID, "success": runErr == nil && outcome == autopilot.ProofSucceeded, "outcome": outcome, "proof_id": proof.ID, "verification": evaluation.Verification, "simulation": simulation}})
		if proofErr == nil {
			e.sink.Emit(message.Event{Type: "autopilot.proof", AgentID: msg.AgentID, SessionID: msg.SessionID, Timestamp: proof.CompletedAt, Payload: proof})
		}
	}
	return reply, runErr
}

func truncateAutopilotText(value string, max int) string {
	r := []rune(value)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return value
}
