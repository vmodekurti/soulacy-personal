package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
		{Name: "mobile.invoke", Description: "Request an enabled command on a paired phone. Returns a queued receipt, not a completed action. Camera and photo commands require visible interaction on the phone. Use mobile.command_status to read the result. canvas.present draws a native card: params {title, components:[{type:text,markdown}|{type:checklist,title,items:[{label,done}]}|{type:form,id,fields:[{name,label,kind:text|number|choice|toggle|date,options,required}],submit}|{type:chart,title,kind:bar|line,labels,series:[{label,values}]}|{type:metric,label,value,unit,trend}]}. A form keeps the command running until the person submits; the result then carries answers keyed by field name and checklist states. Set expires_in_seconds (up to 900) when waiting on a person.", Gate: "mobile", Parameters: map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]any{"type": "string"}, "command": map[string]any{"type": "string", "enum": mobile.SupportedNodeCommands}, "params": map[string]any{"type": "object"}, "expires_in_seconds": map[string]any{"type": "integer", "minimum": 30, "maximum": 900}}, "required": []string{"device_id", "command"}}, Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
			cmd := mobile.NodeCommand{DeviceID: argString(args, "device_id"), Command: argString(args, "command"), Params: raw}
			if secs, ok := args["expires_in_seconds"].(float64); ok && secs >= 30 && secs <= 900 {
				cmd.ExpiresAt = time.Now().UTC().Add(time.Duration(secs) * time.Second)
			}
			v, err := s.EnqueueNodeCommand(ctx, "personal", owner, cmd)
			return encode(v, err)
		}},
		{Name: "mobile.command_status", Description: "Read the canonical status and result of a requested phone action. Pass wait_seconds (up to 25) to wait for the phone to finish before answering; the phone claims commands on a short poll, so an immediate read usually still says queued. Running after a lost connection may be uncertain; never repeat an action just because its result has not arrived.", Gate: "mobile", Parameters: map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]any{"type": "string"}, "command_id": map[string]any{"type": "string"}, "wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 25}}, "required": []string{"device_id", "command_id"}}, Handler: func(ctx context.Context, args map[string]any) (string, error) {
			s, owner, err := base(ctx)
			if err != nil {
				return "", err
			}
			wait := time.Duration(0)
			if secs, ok := args["wait_seconds"].(float64); ok && secs > 0 {
				wait = time.Duration(min(secs, 25)) * time.Second
			}
			v, err := waitForNodeCommand(ctx, s, owner, argString(args, "device_id"), argString(args, "command_id"), wait)
			return encode(v, err)
		}},
	}
}

// waitForNodeCommand reads a phone command and, when wait is positive, keeps
// re-reading until it reaches a terminal state or the wait elapses. The
// phone claims and answers commands on a poll of a few seconds; without
// this an agent's immediate read almost always says "queued" and it gives
// up on a result that arrives moments later.
func waitForNodeCommand(ctx context.Context, s *mobile.Store, owner, deviceID, commandID string, wait time.Duration) (mobile.NodeCommand, error) {
	deadline := time.Now().Add(wait)
	for {
		cmd, err := s.NodeCommand(ctx, "personal", owner, deviceID, commandID)
		if err != nil {
			return cmd, err
		}
		switch cmd.Status {
		case "completed", "failed", "declined", "expired":
			return cmd, nil
		}
		if wait <= 0 || time.Now().After(deadline) {
			return cmd, nil
		}
		select {
		case <-ctx.Done():
			return cmd, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
