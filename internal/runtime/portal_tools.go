package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PortalAccess is supplied only by an authenticated interactive gateway request.
// The implementation retains the request credential privately and rechecks the
// portal's own authorization on every call. It is never serialized to a model.
type PortalAccess interface {
	PortalActions(context.Context, string) (any, error)
	PortalAction(context.Context, string, string, map[string]any, map[string]any) (any, error)
}
type portalAccessKey struct{}
type portalAccessValue struct{ access PortalAccess }

func WithPortalAccess(ctx context.Context, access PortalAccess) context.Context {
	return context.WithValue(ctx, portalAccessKey{}, portalAccessValue{access})
}
func portalAccess(ctx context.Context) PortalAccess {
	value, _ := ctx.Value(portalAccessKey{}).(portalAccessValue)
	return value.access
}

const geniePortalInstructions = `Soulacy Portal actions: use portal_actions to discover the live catalog, portal_read to inspect saved agents, runs, outputs, podcast links, providers and settings, and portal_update to make requested changes. Use these tools directly rather than delegating portal administration to a peer or sending the user to Studio. Core operational settings are read-only to you: authentication, permissions, networking, storage, global runtime limits, services, shared integrations, Genie/System or privileged agents, and providers in use. Do not attempt to change these through another tool, browser, builder or delegated agent. Direct the user to the Portal for those changes. You can edit ordinary agent settings and add providers without changing the active setup. Your portal access is bounded by these protections and the signed-in user's access; the portal rechecks their permissions on every action. For job results, resolve the agent, search runs.list (q, status, after, before, run_id, limit, offset), and return the actual saved output or audio URL with the run's date and status. Paginate when necessary; distinguish no matching results in the scanned history from proof that a job never ran. Never invent a link or run success. For agent edits, read agents.get then send only requested fields to agents.update, including only changed nested settings. For providers, inspect providers.list, save metadata with providers.save, then verify the saved provider (registered means available for selection, not assigned or in use; false means setup is incomplete); ask the user to enter any required API key directly in Providers, never in chat. Ordinary requested reversible edits can proceed. Use portal_confirm only for catalog actions marked confirm; it presents the exact action for approval. Existing portal approvals and secure sign-in remain authoritative. Never claim a save or provider is working until returned evidence supports it. Credentials, approval decisions, file uploads and other interactive-only operations must be completed in the portal's secure page. Portal tools do not grant shell or host-file access. If portal tools are unavailable for this conversation, state that and direct the user to signed-in Genie chat.`

func ensureGeniePortalInstructions(prompt string) string {
	prompt = strings.ReplaceAll(prompt, "You operate as an operator, never as a deployment administrator. You cannot change gateway configuration, restart or upgrade the service, access host credentials, run shell commands, write host files, or bypass confirmations. If work requires an unavailable or administrative capability, explain the exact boundary and ask an administrator to perform that step.", "Use authenticated portal tools for ordinary administration within the user's permissions. Core operational settings remain read-only. You cannot access host credentials, run shell commands, write host files, or bypass confirmations.")
	prompt = strings.ReplaceAll(prompt, "Studio is where they go to change what already exists, or to approve an agent that would be reachable on a channel.", "Use portal tools to change existing agents; Studio remains available for visual editing and approvals.")
	if !strings.Contains(prompt, geniePortalInstructions) {
		prompt += "\n\n" + geniePortalInstructions
	}
	return prompt
}

func (e *Engine) buildPortalBuiltins() []BuiltinTool {
	result := []BuiltinTool{{Name: "connect_agensi", Gate: "agensi", Description: "Start Soulacy's secure Agensi browser sign-in. Return the authorization_url to the user and wait for them to sign in and click Allow. Never request an account token or API key.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Handler: func(ctx context.Context, _ map[string]any) (string, error) {
		if portalAccess(ctx) == nil {
			return "", fmt.Errorf("agensi connection requires signed-in Soulacy chat")
		}
		v, err := portalAccess(ctx).PortalAction(ctx, "update", "skills.agensi_connect", nil, nil)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(v)
		return string(b), err
	}}, {Name: "portal_actions", Gate: "portal", Description: "Discover Soulacy Portal actions and their exact parameters. Search by topic such as runs podcast agents settings providers schedule knowledge channels skills or updates. Call before using an unfamiliar action. Credentials stay in the secure Portal.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}, Handler: func(ctx context.Context, args map[string]any) (string, error) {
		if ActiveAgentFromContext(ctx) != GenieAgentID || portalAccess(ctx) == nil {
			return "", fmt.Errorf("portal access requires signed-in Genie chat")
		}
		v, err := portalAccess(ctx).PortalActions(ctx, argString(args, "query"))
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(v)
		return string(b), err
	}}}
	for _, mode := range []string{"read", "update", "confirm"} {
		mode := mode
		description := map[string]string{"read": "Read saved Soulacy Portal data using an action returned by portal_actions. Returns actual results; never assume an execution succeeded.", "update": "Perform an explicitly requested ordinary Portal change using a discovered action. For agents.update pass ONLY changed fields in body; nested fields are merged and all other settings preserved. Never pass passwords, API keys or approval acknowledgements.", "confirm": "Perform a consequential Portal action after the built-in approval prompt. Supply the exact discovered action, target params and body to review. Required for deletion, execution/delivery, installation and model downloads."}[mode]
		result = append(result, BuiltinTool{Name: "portal_" + mode, Gate: "portal", Description: description, Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}, "params": map[string]any{"type": "object", "description": "Named path and query parameters from portal_actions; never URLs or headers."}, "body": map[string]any{"type": "object", "description": "JSON fields documented by the action. No credentials."}}, "required": []string{"action"}}, Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if ActiveAgentFromContext(ctx) != GenieAgentID || portalAccess(ctx) == nil {
				return "", fmt.Errorf("portal access requires signed-in Genie chat")
			}
			params, _ := args["params"].(map[string]any)
			body, _ := args["body"].(map[string]any)
			v, err := portalAccess(ctx).PortalAction(ctx, mode, argString(args, "action"), params, body)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(v)
			return string(b), err
		}})
	}
	return result
}
