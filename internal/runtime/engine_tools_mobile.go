package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/pkg/message"
)

func mobileToolOwner(ctx context.Context) (string, error) {
	if p, ok := PrincipalFromContext(ctx); ok {
		if p.Subject == "" || (p.Role != "admin" && p.Role != "operator") {
			return "", fmt.Errorf("mobile commands require an authenticated operator")
		}
		return p.Subject, nil
	}
	// Static API-key chat has no JWT principal; the authenticated local owner
	// is the same identity used by /mobile/nodes. Channel messages and schedules
	// cannot select a phone by inventing a user_id.
	if msg, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok && msg.Channel == "http" {
		return "admin", nil
	}
	return "", fmt.Errorf("mobile commands require an authenticated interactive session")
}

func (e *Engine) buildMobileBuiltins() []BuiltinTool {
	base := func(ctx context.Context) (*mobile.Store, string, error) {
		owner, err := mobileToolOwner(ctx)
		if err != nil {
			return nil, "", err
		}
		s := mobile.DefaultStore()
		if s == nil {
			return nil, "", fmt.Errorf("mobile channel is unavailable")
		}
		return s, owner, nil
	}
	encode := func(v any, err error) (string, error) {
		if err != nil {
			return "", err
		}
		b, e := json.Marshal(v)
		return string(b), e
	}
	return []BuiltinTool{
		{Name: "mobile.list_nodes", Description: "List the current operator's paired phones and explicitly enabled commands. Device actions require the Soulacy app in the foreground.", Gate: "mobile", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			s, owner, err := base(ctx)
			if err != nil {
				return "", err
			}
			v, err := s.Nodes(ctx, "personal", owner)
			return encode(v, err)
		}},
		{Name: "mobile.invoke", Description: "Request an enabled command on a paired phone. Returns a queued receipt, not a completed action. Camera and photo commands require visible interaction on the phone. Use mobile.command_status to read the result.", Gate: "mobile", Parameters: map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]any{"type": "string"}, "command": map[string]any{"type": "string", "enum": mobile.SupportedNodeCommands}, "params": map[string]any{"type": "object"}}, "required": []string{"device_id", "command"}}, Handler: func(ctx context.Context, args map[string]any) (string, error) {
			s, owner, err := base(ctx)
			if err != nil {
				return "", err
			}
			params := args["params"]
			if params == nil {
				params = map[string]any{}
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return "", err
			}
			v, err := s.EnqueueNodeCommand(ctx, "personal", owner, mobile.NodeCommand{DeviceID: argString(args, "device_id"), Command: argString(args, "command"), Params: raw})
			return encode(v, err)
		}},
		{Name: "mobile.command_status", Description: "Read the canonical status and result of a requested phone action. Running after a lost connection may be uncertain; never repeat an action just because its result has not arrived.", Gate: "mobile", Parameters: map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]any{"type": "string"}, "command_id": map[string]any{"type": "string"}}, "required": []string{"device_id", "command_id"}}, Handler: func(ctx context.Context, args map[string]any) (string, error) {
			s, owner, err := base(ctx)
			if err != nil {
				return "", err
			}
			v, err := s.NodeCommand(ctx, "personal", owner, argString(args, "device_id"), argString(args, "command_id"))
			return encode(v, err)
		}},
	}
}
