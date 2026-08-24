// studio_addstep.go — "add one step in natural language" (Epic 3).
//
// The user describes a single new step in plain English; the per-node compiler
// turns it into a concrete block (recommending tool / python / agent), and the
// step is linearly appended to the current workflow and rewired — no full
// regeneration. Returns the updated workflow plus the block kind that was chosen
// so the UI can explain "I added a Python step because…".

package gateway

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/studio"
)

type studioAddStepRequest struct {
	Workflow    studio.Draft `json:"workflow"`
	Instruction string       `json:"instruction"`
	Kind        string       `json:"kind,omitempty"` // optional hint: tool|python|agent|"" (let the model choose)
}

func (s *Server) handleStudioAddStep(c *fiber.Ctx) error {
	var req studioAddStepRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}
	if strings.TrimSpace(req.Instruction) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "instruction is required — describe the step to add")
	}

	// Compile the described step into a single node, grounded in the live catalog.
	compileReq := studio.CompileNodeRequest{
		Intent:   req.Instruction,
		Kind:     req.Kind,
		Catalog:  s.studioCatalogSnapshot(c, s.agents(c)),
		Upstream: upstreamVarsFor(req.Workflow),
	}
	node, err := studio.CompileNode(authorizedRequestContext(c), s.studioLLM(c), compileReq)
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "could not turn that into a step: "+err.Error())
	}

	updated := studio.AppendLinearStep(req.Workflow, node)

	return c.JSON(fiber.Map{
		"workflow":     updated,
		"node":         node,
		"recommended":  node.Kind,
		"step_summary": node.Description,
	})
}

// upstreamVarsFor exposes the output var names already produced by the workflow
// so the compiled step can reference them.
func upstreamVarsFor(d studio.Draft) []studio.UpstreamVar {
	var vars []studio.UpstreamVar
	for _, n := range d.Flow.Nodes {
		name := n.Output
		if name == "" {
			name = n.ID
		}
		vars = append(vars, studio.UpstreamVar{Name: name})
	}
	return vars
}
