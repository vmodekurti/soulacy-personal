package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/pkg/agent"
)

// PATCH preserves the complete saved definition, including nested advanced
// settings. It then uses the normal update handler for validation and approvals.
func (s *Server) handlePatchAgent(c *fiber.Ctx) error {
	existing := s.loader.Get(c.Params("id"))
	if existing == nil {
		return s.errMsg(c, 404, "agent not found")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(c.Body(), &fields); err != nil || fields == nil {
		return s.errMsg(c, 400, "body must be an agent settings object")
	}
	var partial agent.Definition
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&partial); err != nil {
		return s.errJSON(c, 400, err)
	}
	if _, ok := fields["id"]; ok {
		return s.errMsg(c, 400, "agent id cannot be changed")
	}
	saved, err := json.Marshal(existing)
	if err != nil {
		return s.errJSON(c, 500, err)
	}
	var target map[string]any
	var patch map[string]any
	if err := json.Unmarshal(saved, &target); err != nil {
		return s.errJSON(c, 500, err)
	}
	if err := json.Unmarshal(c.Body(), &patch); err != nil {
		return s.errJSON(c, 400, err)
	}
	mergeAgentPatch(target, patch)
	merged, err := json.Marshal(target)
	if err != nil {
		return s.errJSON(c, 500, err)
	}
	c.Request().SetBody(merged)
	return s.handleUpdateAgent(c)
}
func mergeAgentPatch(target, patch map[string]any) {
	for key, value := range patch {
		if nested, ok := value.(map[string]any); ok {
			current, ok := target[key].(map[string]any)
			if !ok {
				current = map[string]any{}
			}
			mergeAgentPatch(current, nested)
			target[key] = current
		} else {
			target[key] = value
		}
	}
}
