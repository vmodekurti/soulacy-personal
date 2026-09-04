package gateway

// studio_planview.go — ST-03 "Plain-Language Plan View" (and ST-06's join
// policy) over HTTP.
//
// The canvas is the right view for someone debugging a wire and the wrong view
// for someone deciding whether the workflow does what they asked. A user who
// cannot read a graph cannot approve one, and approving-without-reading is how
// an agent reaches production doing something nobody intended. BuildPlanView
// has projected the graph into that readable form for a while; nothing served
// it, so the only view on offer stayed the graph.
//
// The projection is derived from the draft on every call rather than stored, so
// Plan / Canvas / SOUL.yaml cannot drift apart.

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/studio"
)

// studioPlanViewResponse re-declares Warnings WITHOUT omitempty. An absent
// "warnings" key reads as "not checked", which is exactly the wrong impression
// for the field that carries "this plan produces a result and delivers it
// nowhere" — the reviewer needs to see an explicit empty list.
type studioPlanViewResponse struct {
	studio.PlanView
	Warnings []string `json:"warnings"`
}

// handleStudioPlanView implements POST /api/v1/studio/plan-view. It takes the
// same {"workflow": <draft>} body as /studio/preflight, /studio/contract and
// /studio/validate so the canvas posts one shape everywhere.
//
// Read-only and pure: nothing is persisted and the draft is not mutated.
func (s *Server) handleStudioPlanView(c *fiber.Ctx) error {
	var req studioPreflightRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	pv := studio.BuildPlanView(req.Workflow)
	// Normalise the collections so the client can iterate unconditionally.
	if pv.Work == nil {
		pv.Work = []studio.PlanStage{}
	}
	if pv.Delivery == nil {
		pv.Delivery = []studio.PlanStage{}
	}
	warnings := pv.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return c.JSON(studioPlanViewResponse{PlanView: pv, Warnings: warnings})
}
