package gateway

// studio_capabilities.go — ST-09 "see what this model can actually do" over
// HTTP.
//
// The capability registry (internal/studio/capability.go) replaced the old
// "does the model name contain gpt-4" heuristic, and every strategy decision
// now consults it — but it was only ever read from inside the advisor, so an
// operator confronted with "Soulacy selected Workflow" had no way to see the
// profile that produced that answer. Worst case, the honest verdict ("this
// model has never been profiled, so we assume the safest behaviour") was
// indistinguishable from "we profiled it and it is weak".
//
//	GET /api/v1/studio/model-capabilities              — the whole registry
//	GET /api/v1/studio/model-capabilities?model=<id>   — one model card
//
// Read-only: nothing here records a probe or mutates the registry.

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/studio"
)

// studioModelCard is one registry row plus the derived verdicts. The raw
// numbers alone do not tell an operator whether ReAct will work; the whole
// point of the profile is the conclusion drawn from it, so the conclusion — and
// the reason it went that way — ships with the card rather than being
// recomputed (differently) by every client.
type studioModelCard struct {
	studio.Capabilities
	// RecommendedMode is the safest execution mode for this model.
	RecommendedMode string `json:"recommended_mode"`
	// SupportsReact / SupportsPlanExecute are the viability verdicts, with
	// ReactWhyNot / PlanExecuteWhyNot carrying the plain-language reason when
	// false. A bare "false" is an assertion the operator cannot argue with.
	SupportsReact       bool   `json:"supports_react"`
	ReactWhyNot         string `json:"react_why_not,omitempty"`
	SupportsPlanExecute bool   `json:"supports_plan_execute"`
	PlanExecuteWhyNot   string `json:"plan_execute_why_not,omitempty"`
}

func studioCardFor(c studio.Capabilities) studioModelCard {
	card := studioModelCard{Capabilities: c, RecommendedMode: c.RecommendedMode()}
	card.SupportsReact, card.ReactWhyNot = c.SupportsReAct()
	card.SupportsPlanExecute, card.PlanExecuteWhyNot = c.SupportsPlanExecute()
	return card
}

// handleStudioModelCapabilities implements GET /api/v1/studio/model-capabilities.
//
// With no query it returns the full registry. With ?model=<id> (and optional
// ?provider=<id>, or a "provider/model" value) it returns the single resolved
// card — including for a model that is NOT in the registry, where the
// conservative Unknown() profile is the answer, not a 404. Reporting "not
// found" would push the client into inventing its own default, and the whole
// reason this registry exists is that the previous default was optimistic.
func (s *Server) handleStudioModelCapabilities(c *fiber.Ctx) error {
	model := strings.TrimSpace(c.Query("model"))
	provider := strings.TrimSpace(c.Query("provider"))

	if model == "" {
		if provider != "" {
			return s.errMsg(c, fiber.StatusBadRequest, "provider requires a model; omit both to list the whole registry")
		}
		all := studio.AllCapabilities()
		cards := make([]studioModelCard, 0, len(all))
		for _, profile := range all {
			cards = append(cards, studioCardFor(profile))
		}
		// Echo the thresholds the verdicts were taken against so a card's
		// "supports_react: false" can be checked rather than trusted.
		return c.JSON(fiber.Map{
			"models": cards,
			"count":  len(cards),
			"thresholds": fiber.Map{
				"react_min_arg_accuracy":            studio.ReActMinArgAccuracy,
				"react_min_plan_reliability":        studio.ReActMinPlanReliability,
				"plan_execute_min_plan_reliability": studio.PlanExecuteMinPlanReliability,
				"min_context_for_planning":          studio.MinContextForPlanning,
			},
		})
	}

	// "anthropic/claude-sonnet-4-6" in one field is how model ids are usually
	// pasted, so accept it rather than requiring the caller to split it.
	if provider == "" {
		if i := strings.Index(model, "/"); i > 0 {
			provider, model = model[:i], model[i+1:]
		}
	}
	return c.JSON(studioCardFor(studio.LookupCapabilities(provider, model)))
}
