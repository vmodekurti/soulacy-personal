package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/autopilot"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func (s *Server) SetAutopilotStore(store *autopilot.Store) { s.autopilotStore = store }

func (s *Server) registerAutopilotRoutes(api fiber.Router) {
	read := s.rbacMW(rbac.ResourceAgents, rbac.ActionRead)
	write := s.rbacMW(rbac.ResourceAgents, rbac.ActionWrite)
	api.Get("/autopilot/summary", read, s.handleAutopilotSummary)
	api.Get("/autopilot/proofs", read, s.handleAutopilotProofs)
	api.Get("/autopilot/proofs/:id", read, s.handleAutopilotProof)
	api.Get("/autopilot/reliability", read, s.handleAutopilotReliability)
	api.Get("/autopilot/proposals", read, s.handleAutopilotProposals)
	api.Post("/autopilot/proposals/:id/verify", write, s.handleAutopilotVerifyProposal)
	api.Post("/autopilot/proposals/:id/accept", write, s.handleAutopilotDecideProposal)
	api.Post("/autopilot/proposals/:id/reject", write, s.handleAutopilotDecideProposal)
	api.Get("/autopilot/deployments", read, s.handleAutopilotDeployments)
	api.Post("/autopilot/deployments", write, s.handleAutopilotCreateDeployment)
	api.Post("/autopilot/deployments/:id/simulate", write, s.handleAutopilotTestDeployment)
	api.Post("/autopilot/deployments/:id/run", write, s.handleAutopilotTestDeployment)
	api.Post("/autopilot/deployments/:id/canary", write, s.handleAutopilotPromoteDeployment)
	api.Post("/autopilot/deployments/:id/promote", write, s.handleAutopilotPromoteDeployment)
	api.Post("/autopilot/deployments/:id/rollback", write, s.handleAutopilotRollbackDeployment)
	api.Post("/autopilot/agents/:id/freeze", write, s.handleAutopilotFreeze)
	api.Get("/autopilot/goals", read, s.handleAutopilotGoals)
	api.Post("/autopilot/goals", write, s.handleAutopilotCreateGoal)
	api.Get("/autopilot/goals/:id", read, s.handleAutopilotGoal)
	api.Post("/autopilot/goals/:id/run", write, s.handleAutopilotRunGoal)
	api.Post("/autopilot/goals/:id/cancel", write, s.handleAutopilotCancelGoal)
}

func (s *Server) autopilotOwner(c *fiber.Ctx) (string, error) {
	if s.autopilotStore == nil {
		return "", fiber.NewError(503, "Autopilot storage is unavailable")
	}
	_, owner, err := mobileIdentity(c)
	return owner, err
}

func (s *Server) autopilotCanAccess(c *fiber.Ctx, id, action string) bool {
	claims := auth.ClaimsFromCtx(c)
	if claims == nil {
		return true
	}
	if !claims.AllowsResource(rbac.ResourceAgents) {
		return false
	}
	if s.rbacManager != nil {
		ok, err := s.rbacManager.CanAccessAgentResource(claims.Role, id, rbac.ResourceAgents, action)
		return err == nil && ok
	}
	return rbac.HasPermission(claims.Role, rbac.ResourceAgents, action)
}

func (s *Server) autopilotError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, autopilot.ErrNotFound):
		return s.errMsg(c, 404, "Autopilot record not found")
	case errors.Is(err, autopilot.ErrInvalid):
		return s.errMsg(c, 400, err.Error())
	case errors.Is(err, autopilot.ErrConflict), errors.Is(err, autopilot.ErrFrozen):
		return s.errMsg(c, 409, err.Error())
	default:
		return s.errMsg(c, 500, "Autopilot operation failed")
	}
}

// Fiber preserves escaped route parameters. IDs are opaque database keys, not
// filesystem paths; decode exactly once to match web/mobile path encoding.
func autopilotPathID(c *fiber.Ctx) (string, error) {
	id, err := url.PathUnescape(c.Params("id"))
	if err != nil || id == "" || len(id) > 512 {
		return "", fiber.NewError(400, "Invalid Autopilot identifier")
	}
	return id, nil
}

func (s *Server) autopilotProofs(c *fiber.Ctx, owner string) ([]autopilot.ProofRecord, error) {
	items, err := s.autopilotStore.ListProofs(c.UserContext(), owner, autopilot.ProofFilter{AgentID: c.Query("agent_id"), RunID: c.Query("run_id"), Limit: c.QueryInt("limit", 100)})
	if err != nil {
		return nil, err
	}
	out := []autopilot.ProofRecord{}
	for _, p := range items {
		if s.autopilotCanAccess(c, p.AgentID, rbac.ActionRead) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Server) handleAutopilotProofs(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	items, err := s.autopilotProofs(c, owner)
	if err != nil {
		return s.autopilotError(c, err)
	}
	return c.JSON(fiber.Map{"proofs": items})
}
func (s *Server) handleAutopilotProof(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	id, err := autopilotPathID(c)
	if err != nil {
		return err
	}
	p, err := s.autopilotStore.GetProof(c.UserContext(), owner, id)
	if err != nil {
		return s.autopilotError(c, err)
	}
	if !s.autopilotCanAccess(c, p.AgentID, rbac.ActionRead) {
		return s.errMsg(c, 404, "proof not found")
	}
	return c.JSON(p)
}

func (s *Server) autopilotReliability(c *fiber.Ctx, owner string) ([]autopilot.ReliabilitySummary, error) {
	items := []autopilot.ReliabilitySummary{}
	for _, a := range s.loader.All() {
		if (c.Query("agent_id") == "" || c.Query("agent_id") == a.ID) && s.autopilotCanAccess(c, a.ID, rbac.ActionRead) {
			v, err := s.autopilotStore.Reliability(c.UserContext(), owner, a.ID, "")
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
	}
	return items, nil
}
func (s *Server) handleAutopilotReliability(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	items, err := s.autopilotReliability(c, owner)
	if err != nil {
		return s.autopilotError(c, err)
	}
	return c.JSON(fiber.Map{"reliability": items})
}

func (s *Server) autopilotProposals(c *fiber.Ctx, owner string) ([]autopilot.LearningProposal, error) {
	items, err := s.autopilotStore.ListProposals(c.UserContext(), owner, autopilot.ProposalFilter{AgentID: c.Query("agent_id"), Status: autopilot.ProposalStatus(c.Query("status")), Limit: 100})
	if err != nil {
		return nil, err
	}
	out := []autopilot.LearningProposal{}
	for _, p := range items {
		if s.autopilotCanAccess(c, p.AgentID, rbac.ActionRead) {
			out = append(out, p)
		}
	}
	return out, nil
}
func (s *Server) handleAutopilotProposals(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	items, err := s.autopilotProposals(c, owner)
	if err != nil {
		return s.autopilotError(c, err)
	}
	return c.JSON(fiber.Map{"proposals": items})
}

func (s *Server) handleAutopilotSummary(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	proofs, err := s.autopilotProofs(c, owner)
	if err != nil {
		return s.autopilotError(c, err)
	}
	reliability, err := s.autopilotReliability(c, owner)
	if err != nil {
		return s.autopilotError(c, err)
	}
	proposals, err := s.autopilotProposals(c, owner)
	if err != nil {
		return s.autopilotError(c, err)
	}
	claims, err := s.autopilotStore.ListUnfinishedRunClaims(c.UserContext(), owner, 100)
	if err != nil {
		return s.autopilotError(c, err)
	}
	visibleClaims := []autopilot.RunClaim{}
	for _, claim := range claims {
		if s.autopilotCanAccess(c, claim.AgentID, rbac.ActionRead) {
			visibleClaims = append(visibleClaims, claim)
		}
	}
	return c.JSON(fiber.Map{"proofs": proofs, "reliability": reliability, "proposals": proposals, "unfinished_runs": visibleClaims, "schema_version": "soulacy.autopilot/v1"})
}

func (s *Server) handleAutopilotDecideProposal(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	id, err := autopilotPathID(c)
	if err != nil {
		return err
	}
	p, err := s.autopilotStore.GetProposal(c.UserContext(), owner, id)
	if err != nil {
		return s.autopilotError(c, err)
	}
	if !s.autopilotCanAccess(c, p.AgentID, rbac.ActionWrite) {
		return s.errMsg(c, 403, "agent write access required")
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if len(c.Body()) > 0 && c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid decision")
	}
	decision := autopilot.ProposalRejected
	if strings.HasSuffix(c.Path(), "/accept") {
		decision = autopilot.ProposalAccepted
	}
	p, err = s.autopilotStore.DecideProposal(c.UserContext(), owner, p.ID, decision, body.Reason)
	if err != nil {
		return s.autopilotError(c, err)
	}
	return c.JSON(p)
}

func proofCheckStatus(p autopilot.ProofRecord, check agent.MissionCheck) autopilot.CheckStatus {
	// Match the recorded assertion's meaning, not a client-submitted verdict.
	expected := autopilot.EvaluateMission(&agent.MissionContract{Acceptance: []agent.MissionCheck{check}}, autopilot.Observation{})
	for _, recorded := range p.Checks {
		if len(expected.Checks) > 0 && recorded.Type == check.Type && recorded.Expected == expected.Checks[0].Expected {
			return recorded.Status
		}
	}
	if check.Type == agent.MissionCheckRequiredTool {
		return autopilot.EvaluateMission(&agent.MissionContract{Acceptance: []agent.MissionCheck{check}}, autopilot.Observation{Tools: p.Tools}).Verification
	}
	return autopilot.CheckUnknown
}

func (s *Server) handleAutopilotVerifyProposal(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	id, err := autopilotPathID(c)
	if err != nil {
		return err
	}
	p, err := s.autopilotStore.GetProposal(c.UserContext(), owner, id)
	if err != nil {
		return s.autopilotError(c, err)
	}
	if !s.autopilotCanAccess(c, p.AgentID, rbac.ActionWrite) {
		return s.errMsg(c, 403, "agent write access required")
	}
	var body struct {
		CandidateProofID string `json:"candidate_proof_id"`
	}
	if c.BodyParser(&body) != nil || body.CandidateProofID == "" {
		return s.errMsg(c, 400, "candidate_proof_id is required; run the candidate version first")
	}
	baseline, err := s.autopilotStore.GetProof(c.UserContext(), owner, p.SourceProofID)
	if err != nil {
		return s.autopilotError(c, err)
	}
	candidate, err := s.autopilotStore.GetProof(c.UserContext(), owner, body.CandidateProofID)
	if err != nil {
		return s.autopilotError(c, err)
	}
	if baseline.AgentID != p.AgentID || candidate.AgentID != p.AgentID || candidate.Simulation || candidate.Outcome != autopilot.ProofSucceeded {
		return s.errMsg(c, 400, "candidate must be a successful real run of the same agent")
	}
	now := time.Now().UTC()
	p, err = s.autopilotStore.SetProposalVerification(c.UserContext(), owner, p.ID, autopilot.ProposalVerification{Status: proofCheckStatus(baseline, p.CandidateCheck), RunID: baseline.RunID, VerifiedAt: &now}, autopilot.ProposalVerification{Status: proofCheckStatus(candidate, p.CandidateCheck), RunID: candidate.RunID, VerifiedAt: &now})
	if err != nil {
		return s.autopilotError(c, err)
	}
	return c.JSON(p)
}

func (s *Server) handleAutopilotCreateDeployment(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	var body struct {
		AgentID    string            `json:"agent_id"`
		Version    string            `json:"version"`
		Definition *agent.Definition `json:"definition"`
	}
	if c.BodyParser(&body) != nil || body.AgentID == "" {
		return s.errMsg(c, 400, "agent_id is required")
	}
	if !s.autopilotCanAccess(c, body.AgentID, rbac.ActionWrite) {
		return s.errMsg(c, 403, "agent write access required")
	}
	if s.loader.IsBuiltin(body.AgentID) {
		return s.errMsg(c, 400, "built-in agents cannot be managed as releases")
	}
	loaded := s.loader.Get(body.AgentID)
	if loaded == nil {
		return s.errMsg(c, 404, "agent not found")
	}
	if body.Definition == nil {
		body.Definition = loaded.Clone()
	}
	if body.Definition.ID != body.AgentID {
		return s.errMsg(c, 400, "definition identity must match agent_id")
	}
	report := agentvalidate.Definition(body.Definition, loaded.SourcePath, agentvalidate.Options{}, agentvalidate.Report{})
	if !report.Valid {
		return c.Status(400).JSON(fiber.Map{"error": "definition validation failed", "report": report})
	}
	raw, err := json.Marshal(body.Definition)
	if err != nil {
		return s.autopilotError(c, err)
	}
	if body.Version == "" {
		body.Version = time.Now().UTC().Format("20060102-150405")
	}
	created, err := s.autopilotStore.CreateDeploymentVersion(c.UserContext(), autopilot.DeploymentVersionInput{Subject: owner, AgentID: body.AgentID, Version: body.Version, DefinitionJSON: raw})
	if err != nil {
		return s.autopilotError(c, err)
	}
	return c.Status(201).JSON(created)
}

func (s *Server) handleAutopilotTestDeployment(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	version, err := s.autopilotStore.GetDeploymentVersion(c.UserContext(), owner, c.Params("id"))
	if err != nil {
		return s.autopilotError(c, err)
	}
	if !s.autopilotCanAccess(c, version.AgentID, rbac.ActionWrite) {
		return s.errMsg(c, 403, "agent write access required")
	}
	var body struct {
		Input string `json:"input"`
	}
	if c.BodyParser(&body) != nil || strings.TrimSpace(body.Input) == "" || len(body.Input) > 65536 {
		return s.errMsg(c, 400, "input is required (up to 64 KiB)")
	}
	ctx := withRequestPrincipal(c, context.Background())
	ctx = runtime.WithPrincipal(ctx, runtime.Principal{Subject: owner, Role: autopilotRole(c), Scopes: autopilotScopes(c)})
	ctx = runtime.WithAutopilotVersion(ctx, version.AgentID, version.ID, strings.HasSuffix(c.Path(), "/simulate"))
	var definition agent.Definition
	if err := json.Unmarshal(version.DefinitionJSON, &definition); err != nil {
		return s.autopilotError(c, err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.resolveRunTimeout(&definition))
	defer cancel()
	runID := c.Get("Idempotency-Key")
	if runID == "" {
		runID = uuid.NewString()
	}
	runID = "autopilot-test-" + runID
	msg := message.Message{ID: runID, AgentID: version.AgentID, SessionID: runID, Channel: "http", Parts: message.Text(body.Input)}
	if err := s.claimSession(c, version.AgentID, msg.SessionID); err != nil {
		return err
	}
	ctx = s.autopilotConfirmContext(ctx, owner, msg)
	reply, runErr := s.engine.Handle(ctx, msg)
	proof, err := s.autopilotStore.GetProof(context.WithoutCancel(ctx), owner, runID)
	if err != nil {
		if runErr != nil {
			return s.autopilotError(c, runErr)
		}
		return s.autopilotError(c, err)
	}
	result := fiber.Map{"proof": proof, "reply": reply}
	if proof.Simulation && runErr == nil && proof.Outcome == autopilot.ProofSucceeded {
		state, _, promotionErr := s.autopilotStore.PromoteDeployment(context.WithoutCancel(ctx), autopilot.PromotionRequest{Subject: owner, AgentID: version.AgentID, VersionID: version.ID, ToChannel: autopilot.ChannelSimulation})
		if promotionErr == nil {
			result["state"] = state
		} else {
			result["promotion_error"] = promotionErr.Error()
		}
	}
	if runErr != nil {
		result["run_error"] = runErr.Error()
	}
	return c.JSON(result)
}

func autopilotRole(c *fiber.Ctx) string {
	if claims := auth.ClaimsFromCtx(c); claims != nil {
		return claims.Role
	}
	return "admin"
}
func autopilotScopes(c *fiber.Ctx) []string {
	if claims := auth.ClaimsFromCtx(c); claims != nil {
		return append([]string(nil), claims.Scopes...)
	}
	return nil
}

// Kept here to make bounded command errors useful to native and web clients.
func autopilotInputError(message string) error {
	return fmt.Errorf("%w: %s", autopilot.ErrInvalid, message)
}
