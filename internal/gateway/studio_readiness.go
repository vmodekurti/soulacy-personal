package gateway

// studio_readiness.go — POST /api/v1/studio/readiness (ST-07).
//
// One call replacing the client-side stitching of /studio/compile +
// /studio/security_review + /studio/plan. That stitching had a specific,
// invisible failure mode: if the security call errored, the GUI dropped that
// section and still computed `ok` from the two that succeeded — so a draft
// could be reported ready on the strength of a review that never ran.
//
// studio.Readiness closes that by making "could not evaluate" a first-class
// outcome: any section the caller could not gather state for is reported
// Unknown and forces OK=false. The handler's job is therefore to be HONEST
// about what it could and could not collect, rather than to silently omit.

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/studio"
	"github.com/soulacy/soulacy/pkg/agent"
)

// studioReadinessRequest is the POST body. Only the draft is required.
type studioReadinessRequest struct {
	Workflow studio.Draft `json:"workflow"`
	// Catalog lets the client pass the catalog it already has, avoiding a
	// second snapshot. Omitted = the server takes its own.
	Catalog *studio.Catalog `json:"catalog,omitempty"`
	// ConsentAccepted mirrors the save path's acknowledgement: a draft needing
	// privileged-exposure consent is correctly NOT ready until it is granted,
	// because the save will refuse it.
	ConsentAccepted bool `json:"acceptPrivilegedExposure,omitempty"`
}

// handleStudioReadiness implements POST /api/v1/studio/readiness.
func (s *Server) handleStudioReadiness(c *fiber.Ctx) error {
	var req studioReadinessRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid body: "+err.Error())
	}

	// Sections we genuinely could not evaluate. Recorded rather than skipped:
	// the whole point of this endpoint is that a missing section is visible.
	unavailable := map[string]string{}

	cat := studio.Catalog{}
	if req.Catalog != nil {
		cat = *req.Catalog
	} else {
		cat = s.studioCatalogSnapshot()
	}
	s.groundCatalog(&cat)

	// A catalog with no tools AND no MCP servers is not a workspace with nothing
	// installed — every workspace has builtins — so it means the snapshot did not
	// come back. Judging a draft's tool references against that would manufacture
	// confident false blockers ("web_search does not exist"), which is worse than
	// admitting we could not look.
	if len(cat.Tools) == 0 && len(cat.MCP) == 0 {
		const why = "the tool and channel catalog came back empty, so tool references could not be checked"
		unavailable[studio.ReadinessSectionPreflight] = why
		unavailable[studio.ReadinessSectionContract] = why
	}

	var def *agent.Definition
	if id := strings.TrimSpace(req.Workflow.ID); id != "" && s.loader != nil {
		def = s.loader.Get(id)
	}

	rep := studio.Readiness(studio.ReadinessInput{
		Draft:             req.Workflow,
		Catalog:           cat,
		Preflight:         s.preflightInput(c, cat),
		Definition:        def,
		IntentGateDefault: s.workspaceIntentGateDefault(),
		ConsentAccepted:   req.ConsentAccepted,
		Unavailable:       unavailable,
	})

	// 200 regardless of the verdict: "not ready" is a successful answer to the
	// question asked. Only a malformed request is a client error.
	return c.JSON(rep)
}
