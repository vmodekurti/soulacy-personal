package gateway

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/autopilot"
	"github.com/soulacy/soulacy/internal/rbac"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/pkg/message"
)

func (s *Server) handleAutopilotDeployments(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	versions, err := s.autopilotStore.ListDeploymentVersions(c.UserContext(), owner, c.Query("agent_id"), 100)
	if err != nil {
		return s.autopilotError(c, err)
	}
	states, err := s.autopilotStore.ListDeploymentStatuses(c.UserContext(), owner)
	if err != nil {
		return s.autopilotError(c, err)
	}
	visible := []autopilot.DeploymentVersion{}
	for _, v := range versions {
		if s.autopilotCanAccess(c, v.AgentID, rbac.ActionRead) {
			visible = append(visible, v)
		}
	}
	visibleStates := []autopilot.DeploymentStatus{}
	for _, v := range states {
		if (c.Query("agent_id") == "" || c.Query("agent_id") == v.AgentID) && s.autopilotCanAccess(c, v.AgentID, rbac.ActionRead) {
			visibleStates = append(visibleStates, v)
		}
	}
	return c.JSON(fiber.Map{"deployments": visible, "states": visibleStates})
}

func (s *Server) handleAutopilotPromoteDeployment(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	v, err := s.autopilotStore.GetDeploymentVersion(c.UserContext(), owner, c.Params("id"))
	if err != nil {
		return s.autopilotError(c, err)
	}
	if !s.autopilotCanAccess(c, v.AgentID, rbac.ActionWrite) {
		return s.errMsg(c, 403, "agent write access required")
	}
	var body struct {
		TrafficPercent int                       `json:"traffic_percent"`
		Gates          *autopilot.PromotionGates `json:"gates"`
	}
	if len(c.Body()) > 0 && c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid promotion gates")
	}
	channel := autopilot.ChannelCanary
	if strings.HasSuffix(c.Path(), "/promote") {
		channel = autopilot.ChannelStable
	}
	one := 1.0
	gates := autopilot.PromotionGates{MinSamples: 1, MinSuccessRate: &one, MinVerificationRate: &one}
	if channel == autopilot.ChannelStable {
		gates.MinSamples = 5
	}
	if body.Gates != nil {
		gates = *body.Gates
	}
	if gates.MinSamples < 1 || gates.MinSuccessRate == nil || gates.MinVerificationRate == nil {
		return s.errMsg(c, 400, "live promotion requires a sample count and both success and verification gates")
	}
	state, evaluation, err := s.autopilotStore.PromoteDeployment(c.UserContext(), autopilot.PromotionRequest{Subject: owner, AgentID: v.AgentID, VersionID: v.ID, ToChannel: channel, TrafficPercent: body.TrafficPercent, Gates: gates})
	if err != nil {
		return c.Status(409).JSON(fiber.Map{"error": err.Error(), "gates": evaluation})
	}
	return c.JSON(fiber.Map{"state": state, "gates": evaluation})
}

func (s *Server) handleAutopilotRollbackDeployment(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	v, err := s.autopilotStore.GetDeploymentVersion(c.UserContext(), owner, c.Params("id"))
	if err != nil {
		return s.autopilotError(c, err)
	}
	if !s.autopilotCanAccess(c, v.AgentID, rbac.ActionWrite) {
		return s.errMsg(c, 403, "agent write access required")
	}
	var body struct {
		Channel autopilot.DeploymentChannel `json:"channel"`
		Reason  string                      `json:"reason"`
	}
	if c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid rollback request")
	}
	if body.Channel == "" {
		body.Channel = autopilot.ChannelStable
	}
	if body.Channel != autopilot.ChannelStable && body.Channel != autopilot.ChannelCanary {
		return s.errMsg(c, 400, "rollback channel must be stable or canary")
	}
	status, err := s.autopilotStore.DeploymentStatus(c.UserContext(), owner, v.AgentID)
	if err != nil {
		return s.autopilotError(c, err)
	}
	current := ""
	for _, ch := range status.Channels {
		if ch.Channel == body.Channel {
			current = ch.CurrentVersionID
		}
	}
	if current != v.ID {
		return s.errMsg(c, 409, "selected version is not current; refresh before rolling back")
	}
	state, err := s.autopilotStore.RollbackDeployment(c.UserContext(), autopilot.RollbackRequest{Subject: owner, AgentID: v.AgentID, Channel: body.Channel, Reason: body.Reason, ExpectedVersionID: v.ID})
	if err != nil {
		return s.autopilotError(c, err)
	}
	return c.JSON(state)
}

func (s *Server) handleAutopilotFreeze(c *fiber.Ctx) error {
	owner, err := s.autopilotOwner(c)
	if err != nil {
		return err
	}
	id := c.Params("id")
	if !s.autopilotCanAccess(c, id, rbac.ActionWrite) {
		return s.errMsg(c, 403, "agent write access required")
	}
	if s.loader.Get(id) == nil {
		return s.errMsg(c, 404, "agent not found")
	}
	var body struct {
		Frozen *bool  `json:"frozen"`
		Reason string `json:"reason"`
	}
	if c.BodyParser(&body) != nil || body.Frozen == nil {
		return s.errMsg(c, 400, "frozen boolean is required")
	}
	state, err := s.autopilotStore.SetDeploymentFrozen(c.UserContext(), owner, id, *body.Frozen, body.Reason)
	if err != nil {
		return s.autopilotError(c, err)
	}
	cancelled := 0
	if *body.Frozen {
		cancelled = s.engine.CancelAutopilotAgent(owner, id)
	}
	return c.JSON(fiber.Map{"state": state, "cancelled_runs": cancelled})
}

func (s *Server) autopilotConfirmContext(ctx context.Context, owner string, msg message.Message) context.Context {
	return runtime.WithConfirmSender(ctx, func(req runtime.ConfirmRequest) <-chan bool {
		ch := s.engine.Broker().RegisterRequestForPrincipal(req, msg.AgentID, msg.SessionID, owner)
		if s.hub != nil {
			s.hub.Emit(message.Event{Type: "tool_confirm", AgentID: msg.AgentID, SessionID: msg.SessionID, Payload: req, Timestamp: time.Now().UTC()})
		}
		return ch
	})
}
