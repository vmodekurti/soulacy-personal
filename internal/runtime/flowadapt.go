package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

const (
	flowRepairTimeout      = 20 * time.Second
	flowRepairSnapshotSize = 12_000
)

// flowadapt.go keeps a flow RUNNING through surprises. When an adaptive node
// fails — or "succeeds" but its output reports an error — because a real tool/API
// returned a shape it didn't expect, the runtime asks the model to produce the
// node's intended output from the ACTUAL input, so downstream nodes get usable
// data instead of the flow aborting. This is the on-the-fly counterpart to the
// post-run repair engine: repair fixes the node's code/template for next time;
// adaptation salvages THIS run. Bounded to one attempt per node, and only for
// shape/format surprises — never auth/network/consent failures, which are real.

// adaptFlowNode attempts an LLM salvage for a failed/soft-failed node. It returns
// (salvagedOutput, true) when it produced usable output, or (_, false) to leave
// the original result untouched. Never itself returns an error — a failed
// salvage just declines.
func (e *Engine) adaptFlowNode(ctx context.Context, msg message.Message, node sdkr.FlowNode, renderedInput string, prevOut json.RawMessage, prevErr error) (json.RawMessage, bool) {
	if e.llmRouter == nil {
		return nil, false
	}
	// Never salvage the OUTPUT of an effectful node: fabricating a success
	// receipt for a send/create/write that did not happen turns a visible
	// failure into silent data loss downstream. Argument repair (which runs
	// BEFORE the tool and re-executes the real thing) remains available to
	// effectful nodes; only output reconstruction is off the table.
	if isEffectfulFlowNode(node) {
		return nil, false
	}
	// What went wrong? A hard error, or a soft error the node reported in output.
	reason := ""
	if prevErr != nil {
		reason = prevErr.Error()
	} else if s := flowSoftError(prevOut); s != "" {
		reason = s
	}
	if reason == "" || !isAdaptableFailure(reason) {
		return nil, false
	}

	def := &agent.Definition{}
	if e.loader != nil {
		if d := e.loader.Get(msg.AgentID); d != nil {
			def = d
		}
	}

	prompt := buildAdaptPrompt(node, renderedInput, reason, prevOut)
	req := llm.CompletionRequest{
		Model:          def.LLM.Model,
		Temperature:    def.LLM.Temperature,
		ResponseFormat: "json",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are a resilient workflow step. The upstream data came back in an unexpected shape. Extract/produce this step's intended output from the ACTUAL input. Reply with ONLY the JSON value this step should output — no prose, no code fences."},
			{Role: "user", Content: prompt},
		},
	}
	if e.sink != nil {
		e.sink.Emit(message.Event{
			Type: "flow.adapt", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"node": node.ID, "reason": truncateReason(reason)},
			Timestamp: time.Now().UTC(),
		})
	}
	resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, req)
	if err != nil || resp == nil {
		return nil, false
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return nil, false
	}
	// Prefer valid JSON; otherwise wrap as a JSON string so downstream vars stay
	// well-typed. Reject a salvage that still reports an error.
	if json.Valid([]byte(out)) {
		raw := json.RawMessage(out)
		if flowSoftError(raw) != "" {
			return nil, false
		}
		return raw, true
	}
	b, _ := json.Marshal(out)
	return b, true
}

// isEffectfulFlowNode reports whether a node's action plausibly causes an
// external side effect (delivery, mutation, execution) — the nodes whose
// output must never be reconstructed by a model, because the reconstruction
// would claim work happened that didn't. Heuristic on kind and tool name,
// deliberately conservative in the safe direction: declining salvage merely
// restores the pre-adaptation behavior (the real failure stays visible).
//   - agent nodes: a peer agent can do anything, including deliver messages —
//     always treated as effectful (matches FlowNode.Adaptive's documented
//     scope of tool/python/llm nodes).
//   - llm nodes: pure transforms, never effectful.
//   - inline python: effectful exactly when its classified capabilities
//     escalate beyond ReadOnly (system/network).
//   - tools: name-marker match (send/create/write/…), mirroring the string
//     heuristics used across the flow-heal paths until the tier classifier
//     (internal/tier) is plumbed into the engine here.
func isEffectfulFlowNode(node sdkr.FlowNode) bool {
	switch node.Kind {
	case sdkr.FlowNodeAgent:
		return true
	case sdkr.FlowNodeLLM:
		return false
	case sdkr.FlowNodePython:
		if node.Code != "" {
			for _, r := range node.Requires {
				if r == "system" || r == "network" {
					return true
				}
			}
			return false
		}
	}
	name := strings.ToLower(node.Tool)
	if name == "" {
		return false
	}
	for _, marker := range []string{
		"send", "create", "write", "delete", "remove", "update", "insert",
		"post", "upload", "publish", "push", "notify", "exec", "shell",
		"install", "schedule", "deploy", "commit",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// repairFlowEdgePredicate decides a routing edge whose If predicate could not
// be rendered from the live flow vars. The model receives the same redacted,
// bounded view of the upstream values RepairInput uses, plus the predicate's
// template as intent, and must return a strict {"take": bool} verdict — it
// decides WHETHER the edge is taken, never what the data is. ok=false leaves
// the original render error to abort the walk, exactly as before.
func (e *Engine) repairFlowEdgePredicate(ctx context.Context, msg message.Message, edge sdkr.FlowEdge, renderErr error, vars map[string]any) (take bool, ok bool) {
	if e.llmRouter == nil || renderErr == nil || !isAdaptableFailure(renderErr.Error()) {
		return false, false
	}
	def := e.flowRepairDefinition(msg.AgentID)

	var prompt strings.Builder
	prompt.WriteString("Decide one workflow routing predicate for the CURRENT run.\n")
	prompt.WriteString("Edge: " + edge.From + " → " + edge.To + "\n")
	prompt.WriteString("The predicate template failed to render: " + truncateRunes2(edge.If, 2000) + "\n")
	prompt.WriteString("Render error: " + truncateReason(renderErr.Error()) + "\n")
	prompt.WriteString("Redacted live upstream values (actual shapes and bounded samples):\n")
	prompt.WriteString(flowVarsSnapshot(vars))
	prompt.WriteString("\nApply the predicate's INTENT to the ACTUAL live values and decide whether this edge should be taken. Return ONLY {\"take\": true} or {\"take\": false} — no prose, no invented data.")

	repairCtx, cancel := context.WithTimeout(ctx, flowRepairTimeout)
	defer cancel()
	req := llm.CompletionRequest{
		Model:          def.LLM.Model,
		Temperature:    0,
		MaxTokens:      256,
		ResponseFormat: "json",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You repair a single workflow routing decision. Follow the supplied live data exactly. Return only the JSON verdict requested. Never bypass or weaken security, consent, authentication, or policy controls."},
			{Role: "user", Content: prompt.String()},
		},
	}
	healNode := sdkr.FlowNode{ID: edge.From}
	e.emitFlowHeal(msg, healNode, "edge_predicate", "attempted")
	resp, err := e.llmRouter.Complete(repairCtx, def.LLM.Provider, req)
	if err != nil || resp == nil {
		e.emitFlowHeal(msg, healNode, "edge_predicate", "declined")
		return false, false
	}
	take, ok = parseFlowPredicateVerdict(resp.Content)
	if !ok {
		e.emitFlowHeal(msg, healNode, "edge_predicate", "invalid")
		return false, false
	}
	e.emitFlowHeal(msg, healNode, "edge_predicate", "recovered")
	return take, true
}

// parseFlowPredicateVerdict extracts a strict {"take": bool} verdict from a
// model reply. Anything else — missing key, non-bool, prose — is invalid, so a
// hedging reply can never steer routing.
func parseFlowPredicateVerdict(content string) (take bool, ok bool) {
	candidate := strings.TrimSpace(content)
	if extracted, extractedOK := extractLLMJSON(candidate); extractedOK {
		candidate = extracted
	}
	var v struct {
		Take *bool `json:"take"`
	}
	if err := json.Unmarshal([]byte(candidate), &v); err != nil || v.Take == nil {
		return false, false
	}
	return *v.Take, true
}

// repairFlowRenderedInput repairs a shape-drift failure that happened before a
// node could execute. It returns concrete input for this visit only; the saved
// graph remains unchanged and Studio can separately propose a durable repair
// from the resulting trace.
func (e *Engine) repairFlowRenderedInput(ctx context.Context, msg message.Message, node sdkr.FlowNode, inputTemplate string, renderErr error, vars map[string]any) (string, bool) {
	if e.llmRouter == nil || renderErr == nil || !isAdaptableFailure(renderErr.Error()) {
		return "", false
	}
	def := e.flowRepairDefinition(msg.AgentID)
	schema := e.flowNodeToolSchema(def, msg.Channel, node)

	var prompt strings.Builder
	prompt.WriteString("Repair one workflow node input for the CURRENT run.\n")
	prompt.WriteString("Node: " + node.ID + " (kind: " + node.Kind + ")\n")
	if node.Tool != "" {
		prompt.WriteString("Tool: " + node.Tool + "\n")
	}
	if purpose := flowNodePurpose(node); purpose != "" {
		prompt.WriteString("Purpose: " + purpose + "\n")
	}
	prompt.WriteString("The input template failed: " + truncateRunes2(inputTemplate, 2500) + "\n")
	prompt.WriteString("Render error: " + truncateReason(renderErr.Error()) + "\n")
	if schema != nil {
		if b, err := json.Marshal(schema.Parameters); err == nil {
			prompt.WriteString("Target tool JSON schema: " + truncateRunes2(string(b), 5000) + "\n")
		}
	}
	prompt.WriteString("Redacted live upstream values (actual shapes and bounded samples):\n")
	prompt.WriteString(flowVarsSnapshot(vars))
	prompt.WriteString("\nReturn ONLY the final concrete JSON input for this node. Do not return a template, explanation, markdown, credentials, or invented identifiers.")

	return e.completeFlowInputRepair(ctx, msg, def, node, "template_shape", prompt.String())
}

// repairFlowToolInput corrects malformed or schema-invalid arguments and lets
// the runtime retry the real tool once. It is intentionally limited to argument
// contract errors; authorization, consent, network, timeout, and provider
// failures are never sent through this path.
func (e *Engine) repairFlowToolInput(ctx context.Context, msg message.Message, node sdkr.FlowNode, renderedInput string, toolErr error) (string, bool) {
	if e.llmRouter == nil || toolErr == nil || !isRepairableToolArgumentFailure(toolErr.Error()) || !isAdaptableFailure(toolErr.Error()) {
		return "", false
	}
	def := e.flowRepairDefinition(msg.AgentID)
	schema := e.flowNodeToolSchema(def, msg.Channel, node)
	if schema == nil {
		return "", false
	}
	schemaJSON, _ := json.Marshal(schema.Parameters)

	var prompt strings.Builder
	prompt.WriteString("Correct the arguments for one failed workflow tool call, then the runtime will retry the REAL tool once.\n")
	prompt.WriteString("Node: " + node.ID + "\nTool: " + schema.Name + "\n")
	if purpose := flowNodePurpose(node); purpose != "" {
		prompt.WriteString("Purpose: " + purpose + "\n")
	}
	prompt.WriteString("Tool JSON schema: " + truncateRunes2(string(schemaJSON), 6000) + "\n")
	prompt.WriteString("Rejected arguments: " + truncateRunes2(renderedInput, 6000) + "\n")
	prompt.WriteString("Validation error: " + truncateReason(toolErr.Error()) + "\n")
	prompt.WriteString("Return ONLY a JSON object containing corrected arguments. Preserve valid values. Do not invent credentials, authorization, destinations, file paths, or resource identifiers.")

	return e.completeFlowInputRepair(ctx, msg, def, node, "tool_arguments", prompt.String())
}

func (e *Engine) completeFlowInputRepair(ctx context.Context, msg message.Message, def *agent.Definition, node sdkr.FlowNode, repairKind, prompt string) (string, bool) {
	repairCtx, cancel := context.WithTimeout(ctx, flowRepairTimeout)
	defer cancel()
	req := llm.CompletionRequest{
		Model:          def.LLM.Model,
		Temperature:    0,
		MaxTokens:      2048,
		ResponseFormat: "json",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You repair a single workflow input contract. Follow the supplied live data and tool schema exactly. Return only the concrete JSON value requested. Never bypass or weaken security, consent, authentication, or policy controls."},
			{Role: "user", Content: prompt},
		},
	}
	e.emitFlowHeal(msg, node, repairKind, "attempted")
	resp, err := e.llmRouter.Complete(repairCtx, def.LLM.Provider, req)
	if err != nil || resp == nil {
		e.emitFlowHeal(msg, node, repairKind, "declined")
		return "", false
	}
	repaired, ok := parseFlowRepairResponse(node, resp.Content)
	if !ok {
		e.emitFlowHeal(msg, node, repairKind, "invalid")
		return "", false
	}
	e.emitFlowHeal(msg, node, repairKind, "recovered")
	return repaired, true
}

func parseFlowRepairResponse(node sdkr.FlowNode, content string) (string, bool) {
	candidate := strings.TrimSpace(content)
	if extracted, ok := extractLLMJSON(candidate); ok {
		candidate = extracted
	}
	if !json.Valid([]byte(candidate)) {
		return "", false
	}
	if node.Kind == sdkr.FlowNodeTool || node.Kind == sdkr.FlowNodeAgent || node.Tool != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(candidate), &args); err != nil || args == nil {
			return "", false
		}
		canonical, err := json.Marshal(args)
		return string(canonical), err == nil
	}
	var value any
	if err := json.Unmarshal([]byte(candidate), &value); err != nil {
		return "", false
	}
	if text, ok := value.(string); ok {
		return text, true
	}
	canonical, err := json.Marshal(value)
	return string(canonical), err == nil
}

func (e *Engine) flowRepairDefinition(agentID string) *agent.Definition {
	if e.loader != nil {
		if def := e.loader.Get(agentID); def != nil {
			return def
		}
	}
	return &agent.Definition{}
}

func (e *Engine) flowNodeToolSchema(def *agent.Definition, channel string, node sdkr.FlowNode) *llm.ToolSchema {
	name := node.Tool
	if node.Kind == sdkr.FlowNodeAgent {
		name = "agent__" + node.Agent
	}
	if name == "" {
		return nil
	}
	for _, schema := range e.allToolSchemas(def, channel) {
		if schema.Name == name {
			copy := schema
			return &copy
		}
	}
	return nil
}

func (e *Engine) emitFlowHeal(msg message.Message, node sdkr.FlowNode, kind, status string) {
	if e.sink == nil {
		return
	}
	e.sink.Emit(message.Event{
		Type:      "flow.heal",
		AgentID:   msg.AgentID,
		SessionID: msg.SessionID,
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"node":   node.ID,
			"kind":   kind,
			"status": status,
		},
	})
}

func flowNodePurpose(node sdkr.FlowNode) string {
	if purpose := strings.TrimSpace(node.Description); purpose != "" {
		return purpose
	}
	return strings.TrimSpace(node.Intent)
}

// isRepairableToolArgumentFailure deliberately recognizes only failures that
// occur before a tool can perform useful work. This prevents an LLM retry from
// duplicating a side effect after a network, provider, or delivery failure.
func isRepairableToolArgumentFailure(reason string) bool {
	r := strings.ToLower(reason)
	if !isAdaptableFailure(r) {
		return false
	}
	for _, marker := range []string{
		"not valid json",
		"invalid json",
		"validation error",
		"field required",
		"missing required",
		"required field",
		"invalid argument",
		"invalid tool input",
		"arguments are missing",
		"argument is missing",
		"must be a string",
		"must be an object",
		"must be an array",
	} {
		if strings.Contains(r, marker) {
			return true
		}
	}
	return false
}

func flowVarsSnapshot(vars map[string]any) string {
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	safe := make(map[string]any, len(keys))
	for _, key := range keys {
		safe[key] = summarizeFlowRepairValue(key, vars[key], 0)
	}
	b, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return "{}"
	}
	return truncateRunes2(string(b), flowRepairSnapshotSize)
}

func summarizeFlowRepairValue(key string, value any, depth int) any {
	if isSensitiveFlowRepairKey(key) {
		return "[redacted]"
	}
	if depth >= 4 {
		return fmt.Sprintf("[%T]", value)
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for child := range typed {
			keys = append(keys, child)
		}
		sort.Strings(keys)
		if len(keys) > 40 {
			keys = keys[:40]
		}
		out := make(map[string]any, len(keys))
		for _, child := range keys {
			out[child] = summarizeFlowRepairValue(child, typed[child], depth+1)
		}
		return out
	case []any:
		limit := len(typed)
		if limit > 8 {
			limit = 8
		}
		out := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, summarizeFlowRepairValue(key, typed[i], depth+1))
		}
		return out
	case string:
		text := strings.TrimSpace(typed)
		if len(text) > 0 && (text[0] == '{' || text[0] == '[') {
			var decoded any
			if json.Unmarshal([]byte(text), &decoded) == nil {
				return summarizeFlowRepairValue(key, decoded, depth+1)
			}
		}
		return truncateRunes2(typed, 1200)
	default:
		return typed
	}
}

func isSensitiveFlowRepairKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"password", "passwd", "secret", "token", "api_key", "apikey", "authorization", "cookie", "credential", "private_key"} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}

// buildAdaptPrompt describes the node's job and hands the model the real input.
func buildAdaptPrompt(node sdkr.FlowNode, renderedInput, reason string, prevOut json.RawMessage) string {
	var sb strings.Builder
	sb.WriteString("Workflow step id: " + node.ID + " (kind: " + node.Kind + ")\n")
	if d := strings.TrimSpace(node.Description); d != "" {
		sb.WriteString("What this step is supposed to do: " + d + "\n")
	} else if in := strings.TrimSpace(node.Intent); in != "" {
		sb.WriteString("What this step is supposed to do: " + in + "\n")
	}
	if node.Kind == string(sdkr.FlowNodePython) && strings.TrimSpace(node.Code) != "" {
		sb.WriteString("\nThe step's python code (for intent; it failed on the real input):\n" + node.Code + "\n")
	}
	sb.WriteString("\nIt failed with: " + truncateReason(reason) + "\n")
	sb.WriteString("\nThe ACTUAL input it received (parse THIS real shape — it may be a string, may have headers/prefix before JSON, may nest differently):\n")
	sb.WriteString(truncateRunes2(renderedInput, 6000) + "\n")
	if len(prevOut) > 0 {
		sb.WriteString("\nThe (wrong) output it produced: " + truncateRunes2(string(prevOut), 800) + "\n")
	}
	sb.WriteString("\nReturn ONLY the correct JSON output for this step, derived from the actual input above.")
	return sb.String()
}

// flowSoftError reports an error a node put in its own JSON output (top-level
// error/errors/err string) despite not raising — the soft-failure signal.
func flowSoftError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"error", "errors", "err"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// isAdaptableFailure is true for shape/format surprises and false for real
// failures (auth, rate-limit, network, consent, timeout) that salvage can't fix.
func isAdaptableFailure(reason string) bool {
	r := strings.ToLower(reason)
	for _, s := range []string{
		"unauthorized", "forbidden", "401", "403", "429", "rate limit",
		"no such host", "connection refused", "timeout", "deadline exceeded",
		"consent", "not permitted", "permission",
	} {
		if strings.Contains(r, s) {
			return false
		}
	}
	return true
}

func truncateReason(s string) string { return truncateRunes2(s, 300) }

func truncateRunes2(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
