// toolrun.go — re-run a single tool with explicit arguments.
//
// This backs the "Retry" action on an individual tool-call card in Chat: it
// re-executes exactly one tool with the same arguments it was originally called
// with, and returns the fresh output/error/duration — without re-running the
// whole LLM turn.
//
// It reuses Engine.RunTool, the same standalone (def-free) primitive the Studio
// real-run verifier uses. Note this executes against the global builtin/MCP tool
// registry and does not re-apply per-agent allowlists or confirmation gates, so
// it is gated behind the same chat action permission as sending a message.

package gateway

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// handleRunTool executes a single named tool with the given arguments.
func (s *Server) handleRunTool(c *fiber.Ctx) error {
	if s.engine == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the runtime engine is unavailable")
	}
	var req struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	req.Tool = strings.TrimSpace(req.Tool)
	if req.Tool == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "tool is required")
	}
	argsJSON := "{}"
	if len(req.Args) > 0 && strings.TrimSpace(string(req.Args)) != "null" {
		argsJSON = string(req.Args)
	}

	start := time.Now()
	out, err := s.engine.RunTool(c.UserContext(), req.Tool, argsJSON)
	durMS := time.Since(start).Milliseconds()

	if err != nil {
		// A tool that ran and failed is not an HTTP error — the caller wants the
		// failure surfaced on the card, so return 200 with ok:false.
		return c.JSON(fiber.Map{
			"ok":          false,
			"tool":        req.Tool,
			"error":       err.Error(),
			"duration_ms": durMS,
		})
	}
	return c.JSON(fiber.Map{
		"ok":          true,
		"tool":        req.Tool,
		"output":      json.RawMessage(out),
		"duration_ms": durMS,
	})
}
