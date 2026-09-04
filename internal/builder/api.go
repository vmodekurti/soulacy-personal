package builder

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// APIHandler handles gap analysis REST endpoints.
type APIHandler struct {
	analyzer *GapAnalyzer
	log      *zap.Logger
}

// NewAPIHandler creates an APIHandler backed by the given GapAnalyzer.
func NewAPIHandler(a *GapAnalyzer, log *zap.Logger) *APIHandler {
	return &APIHandler{analyzer: a, log: log}
}

// HandleAnalyze handles POST /api/v1/builder/analyze
// Request:  {"capabilities": ["web search", "send telegram", "read PDF"]}
// Response: {"gaps": [...CapabilityGap...], "count": N}
func (h *APIHandler) HandleAnalyze(c *fiber.Ctx) error {
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body: " + err.Error(),
		})
	}
	if len(body.Capabilities) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "capabilities must be a non-empty array",
		})
	}

	gaps := h.analyzer.Analyze(body.Capabilities)
	if gaps == nil {
		gaps = []CapabilityGap{}
	}

	h.log.Debug("gap analysis complete",
		zap.Int("capabilities", len(body.Capabilities)),
		zap.Int("gaps", len(gaps)),
	)

	return c.JSON(fiber.Map{
		"gaps":  gaps,
		"count": len(gaps),
	})
}

// HandleResolve handles POST /api/v1/builder/resolve
// Request:  {"gap_index": 0, "suggestion_index": 0}
// Response: {"mcp_ref": {...}, "next_steps": "Run: npx ..."}
// (Does not install anything — returns the install command for the user to run.)
func (h *APIHandler) HandleResolve(c *fiber.Ctx) error {
	var body struct {
		Capabilities    []string `json:"capabilities"`
		GapIndex        int      `json:"gap_index"`
		SuggestionIndex int      `json:"suggestion_index"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid request body: " + err.Error(),
		})
	}
	if len(body.Capabilities) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "capabilities must be provided to resolve a gap",
		})
	}

	gaps := h.analyzer.Analyze(body.Capabilities)

	if body.GapIndex < 0 || body.GapIndex >= len(gaps) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("gap_index %d out of range (have %d gaps)", body.GapIndex, len(gaps)),
		})
	}

	gap := gaps[body.GapIndex]
	if body.SuggestionIndex < 0 || body.SuggestionIndex >= len(gap.Suggestions) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("suggestion_index %d out of range (have %d suggestions for gap %q)",
				body.SuggestionIndex, len(gap.Suggestions), gap.Required),
		})
	}

	ref := gap.Suggestions[body.SuggestionIndex]

	nextSteps := ""
	if ref.InstallCmd != "" {
		nextSteps = "Run: " + ref.InstallCmd
	}

	return c.JSON(fiber.Map{
		"mcp_ref":    ref,
		"next_steps": nextSteps,
	})
}
