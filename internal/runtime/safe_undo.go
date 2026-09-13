package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/soulacy/soulacy/internal/auth"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/safeundo"
	"github.com/soulacy/soulacy/pkg/message"
)

type safeUndoRuntime struct {
	store         *safeundo.Store
	authenticated bool
}

// Called before serving traffic. Tools only prepare changes; external writes
// require an operator's fresh preview and confirmation through the gateway.
func (e *Engine) SetSafeUndo(store *safeundo.Store, authenticated bool) {
	e.safeUndo.Store(&safeUndoRuntime{store, authenticated})
}

func (e *Engine) buildSafeUndoBuiltins() []BuiltinTool {
	base := func(ctx context.Context) (*safeundo.Store, string, string, error) {
		state := e.safeUndo.Load()
		if state == nil || state.store == nil || !state.authenticated {
			return nil, "", "", fmt.Errorf("Safe Undo is not configured with authentication")
		}
		owner, err := mobileToolOwner(ctx)
		if err != nil {
			return nil, "", "", err
		}
		if p, ok := PrincipalFromContext(ctx); ok {
			claims := &auth.Claims{Scopes: p.Scopes}
			if !claims.Allows("agents", "write") {
				return nil, "", "", safeundo.ErrPermission
			}
		}
		inbound, ok := ctx.Value(inboundMsgKey{}).(message.Message)
		if !ok || inbound.AgentID == "" {
			return nil, "", "", fmt.Errorf("Safe Undo requires a current agent session")
		}
		return state.store, owner, inbound.AgentID, nil
	}
	encode := func(value any, err error) (string, error) {
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(value)
		return string(b), err
	}
	return []BuiltinTool{
		{Name: "safe_undo.resources", Gate: "safe_undo", Description: "List explicitly configured Safe Undo resources for this agent. URLs and credentials are never exposed. Only these conditional-write integrations support Safe Undo.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			store, _, agentID, err := base(ctx)
			if err != nil {
				return "", err
			}
			return encode(store.Resources(agentID), nil)
		}},
		{Name: "safe_undo.prepare", Gate: "safe_undo", Description: "Prepare up to 8 reversible changes as one job. This does NOT apply changes. The user must review and apply the job in Safe Undo on web or iPhone. For json_record use fields (top-level names to new JSON values) and/or remove (field names). For webdav_text use text. Existing resources only; no sends, notifications, creation, deletion or arbitrary tools are covered. Return the job ID to the user and do not claim the work was applied.", Parameters: map[string]any{
			"type": "object", "additionalProperties": false, "properties": map[string]any{
				"title": map[string]any{"type": "string", "maxLength": 200},
				"changes": map[string]any{"type": "array", "minItems": 1, "maxItems": safeundo.MaxActions, "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
					"resource_id": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}, "fields": map[string]any{"type": "object"}, "remove": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				}, "required": []string{"resource_id"}}},
			}, "required": []string{"title", "changes"}}, Handler: func(ctx context.Context, args map[string]any) (string, error) {
			store, owner, agentID, err := base(ctx)
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(args)
			if err != nil || len(raw) > safeundo.MaxJobBytes {
				return "", safeundo.ErrInvalid
			}
			var input safeundo.PrepareRequest
			if safeundo.DecodeRequest(raw, &input) != nil {
				return "", safeundo.ErrInvalid
			}
			job, err := store.Prepare(ctx, owner, agentID, llm.CallMetadataFromContext(ctx).RunID, input, func() error { _, _, _, err := base(ctx); return err })
			if err != nil {
				return "", err
			}
			// The model and general tool logs need the receipt ID, not a copy
			// of every private before/after value. Review is owner-scoped in UI.
			return encode(map[string]any{"job_id": job.ID, "title": job.Title, "status": job.Status, "applied": false, "review_required": true, "change_count": len(job.Actions)}, nil)
		}},
	}
}
