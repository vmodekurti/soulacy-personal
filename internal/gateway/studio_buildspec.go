package gateway

// studio_buildspec.go — ST-01 "Intent To Build Spec" over HTTP.
//
// The extraction and the diff have existed (and been unit-tested) in
// internal/studio/buildspec.go since the story landed, but nothing called them,
// so the screen the story describes could not be built: a user still typed a
// paragraph, pressed Generate, and got a graph. When that graph was wrong they
// could not tell whether Studio misunderstood the intent or built the
// misunderstanding correctly — two different bugs with two different fixes.
//
// This endpoint puts the BuildSpec between the two, and — when the client sends
// the previous intent — returns the visible change summary ST-01 requires, so a
// refinement that only reworded the prose says so instead of implying progress.

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/studio"
)

// studioBuildSpecRequest is the POST /api/v1/studio/build-spec body.
type studioBuildSpecRequest struct {
	Intent string `json:"intent"`
	// PreviousIntent is the intent this one refines, when there was one. Optional:
	// the first pass has nothing to diff against.
	PreviousIntent string `json:"previous_intent,omitempty"`
}

// studioBuildSpecResponse embeds the spec so the client reads
// trigger/schedule/inputs/stages/outputs/delivery/integrations/security/questions
// at the top level, and adds the three things a reviewer needs that the spec
// itself does not carry: whether it is buildable, which questions block, and
// what a refinement actually changed.
type studioBuildSpecResponse struct {
	studio.BuildSpec
	// Ready reports that nothing blocks generation. Mirrors BuildSpec.Ready so
	// the client never has to re-derive it from the question list and get a
	// different answer than the server would.
	Ready bool `json:"ready"`
	// Blockers is the subset of Questions that prevents generation. Always
	// present (possibly empty) so a client can bind to it unconditionally.
	Blockers []studio.SpecQuestion `json:"blockers"`
	// Diff / MateriallyDifferent are populated only when previous_intent was
	// supplied. MateriallyDifferent is deliberately NOT omitempty: "we compared
	// and nothing structural changed" is the answer the story cares about most,
	// and an absent field would read as "not compared".
	Diff                []studio.SpecChange `json:"diff,omitempty"`
	MateriallyDifferent bool                `json:"materially_different"`
	// Compared says whether a previous intent was supplied at all, so `false`
	// above cannot be misread as "the refinement did nothing".
	Compared bool `json:"compared"`
	// Recommendation is the strategy Studio would pick for this intent right now.
	// The Describe panel has a Strategy row and previously had nothing to put in
	// it, so it read "not specified" for every prompt ever typed — the one row
	// that was never once correct. AdviseStrategy is deterministic and needs no
	// model call, so the answer is available at exactly the moment the panel asks.
	Recommendation *studio.StrategyAdvice `json:"recommendation,omitempty"`
}

// handleStudioBuildSpec implements POST /api/v1/studio/build-spec. Pure and
// deterministic — no model call — so the same intent always yields the same
// spec and a user can learn what Studio keys on.
func (s *Server) handleStudioBuildSpec(c *fiber.Ctx) error {
	var req studioBuildSpecRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid request body: "+err.Error())
	}

	// Grounded against what this workspace actually has installed, so the panel
	// can report "you named the trvl MCP tool and we have it" rather than
	// "capabilities: not specified" for a prompt that named one. Still pure and
	// deterministic — reading the catalogue is a local lookup, not a model call.
	//
	// groundCatalog is REQUIRED, not belt-and-braces: studioCatalogSnapshot fills
	// in agents, builtin tools and providers, but MCP servers and skills are
	// populated only here. Without it the spec panel would go on reporting
	// "not specified" for every MCP server the user named.
	cat := s.studioCatalogSnapshot()
	s.groundCatalog(&cat)

	// An empty intent is NOT a 400: ExtractBuildSpec answers it with the blocking
	// question "What should this agent do?", which is the useful response for a
	// screen the user is still typing into.
	spec := studio.ExtractBuildSpecFrom(req.Intent, cat)
	res := studioBuildSpecResponse{
		BuildSpec: spec,
		Ready:     spec.Ready(),
		Blockers:  spec.Blockers(),
	}
	if res.Blockers == nil {
		res.Blockers = []studio.SpecQuestion{}
	}
	// Only once there is something to advise ON. Advising on an empty intent
	// would put a confident "Auto" against a blank prompt.
	if strings.TrimSpace(req.Intent) != "" {
		advice := studio.AdviseStrategy(req.Intent, cat, "", false)
		res.Recommendation = &advice
	}
	if strings.TrimSpace(req.PreviousIntent) != "" {
		previous := studio.ExtractBuildSpecFrom(req.PreviousIntent, cat)
		res.Compared = true
		res.Diff = studio.DiffSpecs(previous, spec)
		res.MateriallyDifferent = studio.MateriallyDifferent(previous, spec)
	}
	return c.JSON(res)
}
