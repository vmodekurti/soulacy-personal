// flow.go — Story E25: the graph form of workflows, compiled onto the
// existing WorkflowExecutor + CheckpointStore.
//
// Checkpoint keys are visit-indexed ("<node>#<visit>") so bounded cycles
// persist each iteration separately; resume restores completed visits in
// walk order and recomputes edge decisions from the restored vars — the
// deterministic predicates take the same path, so execution continues
// exactly where the crash happened.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/studio/consent"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"go.uber.org/zap"
)

// runFlow executes the spec's graph form for one trigger message.
type triggerInput map[string]any

const flowTraceFieldMaxBytes = 32 * 1024

func (t triggerInput) String() string {
	if text, ok := t["text"].(string); ok {
		return text
	}
	return ""
}

func (w *WorkflowExecutor) runFlow(ctx context.Context, msg message.Message, runID string) (json.RawMessage, error) {
	spec := w.spec.FlowSpec()
	g, err := reasoning.CompileFlow(*spec)
	if err != nil {
		return nil, fmt.Errorf("workflow graph: %w", err)
	}
	agentID := msg.AgentID

	// Record this run in the agent's run history at start, tagged with its trigger
	// source (the channel: telegram/http/schedule/…), so the history lists EVERY
	// run — scheduled or on-demand — even one that fails before any block records.
	w.engine.TagFlowRun(agentID, runID, msg.Channel)

	// Conversation continuity: pull a compact transcript of recent turns for
	// this session so the flow's entry agent can resolve follow-up references
	// ("its price", "that company") without the user restating context. Empty
	// on the first turn. Exposed to every node as {{.history}} and auto-prepended
	// to the entry agent's message below.
	history := w.engine.flowHistoryTranscript(msg.SessionID, msg.AgentID, flowHistoryMaxMsgs)
	entryID := spec.Entry
	if entryID == "" {
		if len(spec.Nodes) > 0 {
			entryID = spec.Nodes[0].ID
		}
	}

	// The user's inbound text is exposed under several aliases because
	// Studio-generated input templates variously reference {{ .trigger.text }},
	// {{ .trigger.message }}, or {{ .trigger.input }}. Aliasing them all to the
	// same value prevents an entry node from receiving null just because the
	// generated template picked a different (equally reasonable) field name.
	inputText := flattenParts(msg.Parts)
	vars := map[string]interface{}{
		"trigger": triggerInput{
			"text":    inputText,
			"message": inputText,
			"input":   inputText,
		},
		"history": history,
	}

	// Synchronous in-process tap so a caller like Studio "Run live" can collect
	// EVERY node's result during this run (not just tool calls).
	nodeObs := flowNodeObserverFrom(ctx)

	hooks := reasoning.FlowHooks{}
	// Per-block run trace (Story S0.3 Phase 1): record every executed block's
	// input/output/duration/error and stream it as a flow.node event so the GUI
	// can render a legible run trace. Independent of the checkpoint store.
	// Untrimmed node outputs, kept for outcome-contract evaluation (P0-4). The
	// trace copy is truncated for display; an assertion counting items in a
	// large result must see the real value, not "[truncated]".
	//
	// hookMu guards every piece of run-local state these hooks touch: a
	// kind=parallel node (and a parallel for_each) calls them from several branch
	// goroutines at once, so an unguarded append would drop or corrupt records.
	var hookMu sync.Mutex
	var outcomeTrace []reasoning.FlowNodeRun
	hooks.Observe = func(rec reasoning.FlowNodeRun) {
		hookMu.Lock()
		outcomeTrace = append(outcomeTrace, rec)
		hookMu.Unlock()
		displayRec := trimFlowNodeRun(rec)
		if nodeObs != nil {
			nodeObs(displayRec)
		}
		w.engine.recordFlowNode(agentID, runID, displayRec)
		if w.engine.sink == nil {
			return
		}
		w.engine.sink.Emit(message.Event{
			Type:      "flow.node",
			AgentID:   agentID,
			SessionID: msg.SessionID,
			Timestamp: time.Now().UTC(),
			Payload: map[string]any{
				"runId":         runID,
				"visitKey":      displayRec.VisitKey,
				"nodeId":        displayRec.NodeID,
				"kind":          displayRec.Kind,
				"input":         displayRec.Input,
				"output":        displayRec.Output,
				"error":         displayRec.Error,
				"durationMs":    displayRec.DurationMS,
				"wiredPorts":    displayRec.WiredPorts,
				"adapted":       displayRec.Adapted,
				"branchId":      displayRec.BranchID,
				"parallelGroup": displayRec.ParallelGroup,
			},
		})
	}
	if w.store != nil {
		hooks.Restore = func(visitKey string) (json.RawMessage, bool) {
			cp, gerr := w.store.Get(ctx, agentID, runID, visitKey)
			if gerr != nil || cp.Status != CheckpointCompleted {
				return nil, false
			}
			w.log.Debug("flow visit already completed, restoring",
				zap.String("run_id", runID), zap.String("visit", visitKey))
			return cp.State, true
		}
		hooks.Started = func(visitKey string, node sdkr.FlowNode) {
			_ = w.store.Upsert(ctx, Checkpoint{
				AgentID: agentID, RunID: runID, StepID: visitKey,
				Status: CheckpointInProgress, UpdatedAt: time.Now().UTC(),
			})
			if w.engine.sink != nil {
				w.engine.sink.Emit(message.Event{
					Type:      "flow.node.started",
					AgentID:   agentID,
					SessionID: msg.SessionID,
					Timestamp: time.Now().UTC(),
					Payload: map[string]any{
						"runId":    runID,
						"visitKey": visitKey,
						"nodeId":   node.ID,
						"kind":     node.Kind,
						"status":   "running",
					},
				})
			}
		}
		hooks.Completed = func(visitKey string, state json.RawMessage) {
			_ = w.store.Upsert(ctx, Checkpoint{
				AgentID: agentID, RunID: runID, StepID: visitKey,
				Status: CheckpointCompleted, State: state, UpdatedAt: time.Now().UTC(),
			})
		}
		hooks.Failed = func(visitKey string, ferr error) {
			_ = w.store.Upsert(ctx, Checkpoint{
				AgentID: agentID, RunID: runID, StepID: visitKey,
				Status: CheckpointFailed, UpdatedAt: time.Now().UTC(),
			})
		}
	}

	// Runtime healing happens before a malformed template can abort the graph.
	// The model receives a redacted, bounded view of the actual upstream values
	// and the target tool schema, then must return concrete JSON. This repair is
	// visit-local and cannot rewrite the deployed workflow.
	ctx, markAdapted := reasoning.WithAdaptedTracker(ctx)
	hooks.RepairInput = func(repairCtx context.Context, node sdkr.FlowNode, inputTemplate string, renderErr error, liveVars map[string]any) (string, bool) {
		if !(node.Adaptive || w.engine.adaptiveNodes) {
			return "", false
		}
		repaired, ok := w.engine.repairFlowRenderedInput(repairCtx, msg, node, inputTemplate, renderErr, liveVars)
		if ok {
			markAdapted(node.ID)
		}
		return repaired, ok
	}

	// Edge predicates address fields into node outputs, so a routing decision
	// can hit the same shape drift RepairInput heals for node inputs. When a
	// predicate fails to render on an adaptive flow, the model DECIDES the edge
	// from the redacted live values instead of the walk aborting. Bounded to one
	// attempt per edge per run; the compiled workflow is never mutated.
	predicateAttempted := map[string]bool{}
	hooks.RepairPredicate = func(repairCtx context.Context, edge sdkr.FlowEdge, renderErr error, liveVars map[string]any) (bool, bool) {
		from := g.Node(edge.From)
		if !(from.Adaptive || w.engine.adaptiveNodes) {
			return false, false
		}
		key := edge.From + "→" + edge.To
		hookMu.Lock()
		attempted := predicateAttempted[key]
		predicateAttempted[key] = true
		hookMu.Unlock()
		if attempted {
			return false, false
		}
		take, ok := w.engine.repairFlowEdgePredicate(repairCtx, msg, edge, renderErr, liveVars)
		if ok {
			markAdapted(edge.From)
		}
		return take, ok
	}

	// lastSentText captures the human-readable content the workflow last handed to
	// channel.send. When the flow's final result is a bare delivery RECEIPT
	// (channel.send returns {ok,channel,to}, not content), the interactive reply
	// falls back to this text so the user sees the actual message — not the receipt.
	var lastSentText string

	execNode := func(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
		// Per-node timeout — the framework wraps EVERY block with its own budget.
		// Precedence: an explicit FlowNode.Timeout (the inspector "timeout" field)
		// wins; else a wait/timeout argument the node declares (max_wait, timeout_s);
		// else a generous default for external MCP tools; else the global default.
		// The value carries on the context so the tool/python call uses it as its
		// budget, AND a hard deadline wraps the WHOLE block (any kind — tool, python,
		// agent) as a backstop, so a developer can always cap a slow block from the
		// node itself without touching global config.
		if d := nodeExecTimeout(node, renderedInput); d > 0 {
			ctx = WithToolTimeout(ctx, d)
			// The inner tool/python timeout is exactly d and fires first with a clear
			// "exceeded the … timeout" message; this outer deadline (small grace) is
			// the catch-all that bounds any non-tool work in the block too.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d+5*time.Second)
			defer cancel()
		}
		// Studio "Custom Python" node with inline code: execute it in the
		// sandboxed Python executor. The rendered input is delivered as the
		// run(inputs) argument. (A python node that instead references a deployed
		// tool — Code empty, Tool set — falls through to the RunTool path.)
		if node.Kind == sdkr.FlowNodePython && node.Code != "" {
			// Fail-closed per-case consent (§13): refuse to run code beyond the
			// ReadOnly guardrails unless the node carries a matching, valid
			// consent stamp and the operator ceiling permits it.
			def := w.engine.definitionForContext(ctx, msg.AgentID)
			if err := missionToolAllowed(ctx, "inline_python"); err != nil {
				return nil, err
			}
			if missionSimulation(ctx) {
				return json.RawMessage(`{"simulated":true,"tool":"inline_python"}`), nil
			}
			if cerr := consent.Authorize(node, w.engine.IsSystemAgentAllowed(def)); cerr != nil {
				return nil, cerr
			}
			return w.engine.RunInlinePython(ctx, node.Code, []byte(renderedInput))
		}
		var def *agent.Definition
		if w.engine.loader != nil {
			def = w.engine.definitionForContext(ctx, msg.AgentID)
		}
		if def == nil {
			def = &agent.Definition{}
		}
		if node.Kind == sdkr.FlowNodeLLM {
			return w.engine.runFlowLLMNode(ctx, def, msg, node, renderedInput)
		}
		tool := node.Tool
		if node.Kind == sdkr.FlowNodeAgent {
			tool = "agent__" + node.Agent
		}

		tc := message.ToolCall{
			ID:        "flow-" + node.ID,
			Name:      tool,
			Arguments: map[string]any{},
		}
		if renderedInput != "" {
			if node.Kind == sdkr.FlowNodeAgent {
				if strings.HasPrefix(strings.TrimSpace(renderedInput), "{") {
					if args, perr := parseToolArgs(renderedInput); perr == nil {
						tc.Arguments = args
					} else {
						tc.Arguments["message"] = renderedInput
					}
				} else {
					tc.Arguments["message"] = renderedInput
				}
			} else {
				args, perr := parseToolArgs(renderedInput)
				if perr != nil {
					return nil, fmt.Errorf("workflow: tool args for %q not valid JSON: %w", tool, perr)
				}
				tc.Arguments = args
			}
		}

		// Entry agent gets the conversation history prepended to its message so
		// follow-ups resolve against prior turns. Only the entry node — downstream
		// agents act on this turn's extracted intent, not the raw transcript.
		if node.Kind == sdkr.FlowNodeAgent && node.ID == entryID && history != "" {
			base, _ := tc.Arguments["message"].(string)
			if base == "" {
				base = flattenParts(msg.Parts)
			}
			tc.Arguments["message"] = "Conversation so far (context for resolving follow-up references; " +
				"do not answer it directly):\n" + history + "\n\nUser's new message: " + base
		}

		// Remember the content routed to a delivery tool so a receipt-terminated
		// workflow can still return the message to an interactive caller.
		if tool == "channel.send" {
			if t := argStringFirst(tc.Arguments, "text", "message", "body", "content"); t != "" && t != "<no value>" {
				hookMu.Lock()
				lastSentText = t
				hookMu.Unlock()
			}
		}

		outStr, rerr := w.engine.runTool(ctx, def, msg.SessionID, tc)
		if rerr != nil {
			return nil, rerr
		}
		outBytes := []byte(outStr)

		// Auto-poll async work: when this is a status/poll node (e.g. a NotebookLM
		// studio_status / research_status) and its result is still "in progress",
		// re-poll on an interval until it reaches a terminal state or the poll
		// budget runs out — so the developer never has to hand-wire a poll loop,
		// a sleep node, or a max_wait the tool may not even accept. The re-call is
		// the SAME idempotent status check; create/side-effecting nodes don't match
		// the poll pattern, so they're never re-invoked.
		if node.Kind == sdkr.FlowNodeTool && isPollNode(node) {
			polled, perr := autoPollNode(ctx, node, renderedInput, outBytes,
				func(c context.Context) ([]byte, error) {
					s, e := w.engine.runTool(c, def, msg.SessionID, tc)
					return []byte(s), e
				})
			if perr != nil {
				return nil, perr
			}
			outBytes = polled
		}

		if json.Valid(outBytes) {
			return unwrapToolJSON(outBytes), nil
		}
		wrapped, _ := json.Marshal(outStr)
		return json.RawMessage(wrapped), nil
	}

	// runNode wraps execNode with opt-in runtime adaptation: when an adaptive node
	// fails or soft-fails on a shape surprise, the model salvages usable output so
	// the flow keeps running (bounded to one attempt per node). markAdapted flags
	// salvaged nodes so the trace can show which ones the runtime rescued.
	//
	// adaptBudget makes the one-attempt bound explicit ACROSS cycle visits: a
	// node that burned its salvage on visit 1 does not get a fresh attempt when
	// a back edge re-visits it — without this, a bounded cycle would multiply
	// LLM repair calls against the same defect.
	adaptBudget := map[string]bool{}
	runNode := func(ctx context.Context, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
		out, err := execNode(ctx, node, renderedInput)
		adaptive := node.Adaptive || w.engine.adaptiveNodes
		if adaptive && !adaptBudget[node.ID] && (err != nil || flowSoftError(out) != "") {
			adaptBudget[node.ID] = true
			if err != nil && isRepairableToolArgumentFailure(err.Error()) &&
				(node.Kind == sdkr.FlowNodeTool || node.Kind == sdkr.FlowNodeAgent || node.Tool != "") {
				if repairedInput, ok := w.engine.repairFlowToolInput(ctx, msg, node, renderedInput, err); ok {
					retriedOut, retryErr := execNode(ctx, node, repairedInput)
					if retryErr == nil {
						markAdapted(node.ID)
						return retriedOut, nil
					}
					out, err = retriedOut, retryErr
				}
			}
			if salvaged, ok := w.engine.adaptFlowNode(ctx, msg, node, renderedInput, out, err); ok {
				markAdapted(node.ID)
				return salvaged, nil
			}
		}
		// Producer-side port-type enforcement: when a node SUCCEEDS but a declared
		// output port's type hint doesn't match the value it produced, that's shape
		// drift caught where the context is freshest — at the producer — instead of
		// one node later when a consumer blows up. An adaptive node gets its one
		// salvage attempt to reshape the output; otherwise (or when the reshape
		// still mismatches) the run continues with the original output and only a
		// flow.portdrift event is emitted, preserving today's forgiving behavior.
		if err == nil {
			if drift := flowPortTypeMismatch(node, out); drift != "" {
				w.engine.emitFlowPortDrift(msg, node, drift)
				if adaptive && !adaptBudget[node.ID] {
					adaptBudget[node.ID] = true
					if reshaped, ok := w.engine.adaptFlowNode(ctx, msg, node, renderedInput, out, fmt.Errorf("output shape drift: %s", drift)); ok {
						if flowPortTypeMismatch(node, reshaped) == "" {
							markAdapted(node.ID)
							return reshaped, nil
						}
					}
				}
			}
		}
		return out, err
	}

	out, err := reasoning.RunFlow(ctx, g, vars, runNode, hooks)
	// Durably persist the finished run (success OR failure) to the run history, so
	// it survives a gateway restart and a failed run is still inspectable.
	w.engine.PersistFlowRun(agentID, runID)
	if err != nil {
		w.log.Error("workflow flow failed",
			zap.String("run_id", runID), zap.Error(err))
		return nil, err
	}

	// If the graph designates an explicit output node, its result becomes the
	// flow's final output (what gets delivered to channels) — rather than the
	// last-executed node's. RunFlow mutates `vars` in place, so each node's
	// output is available here by its output-var name. Falls back to `out` when
	// the output node has no var or produced nothing.
	final := out
	if outID := spec.Output; outID != "" {
		for _, n := range spec.Nodes {
			if n.ID == outID && n.Output != "" {
				if v, ok := vars[n.Output]; ok {
					if b, jerr := json.Marshal(v); jerr == nil {
						final = json.RawMessage(b)
					}
				}
			}
		}
	}

	// Business-outcome contract (P0-4). Every node may have executed without an
	// error and the run can still have achieved nothing — zero sources, no
	// audio, an empty message delivered on schedule. Judge the contract against
	// the REAL outputs, record the verdict, and — when the agent opted into
	// enforcement — fail the run so an empty result cannot present as success.
	if def := w.engine.loaderDefinition(msg.AgentID); def != nil && def.Outcome.HasAssertions() {
		report := EvaluateOutcome(def.Outcome, final, outcomeTrace)
		w.engine.emitOutcomeReport(msg, runID, report)
		// Hand the verdict to the caller building the reply, so an unmet
		// contract makes the run not-confident (P0-6) and the existing
		// degraded-delivery marking applies — a scheduled empty brief then
		// arrives labelled instead of looking finished.
		if slot := outcomeCollectorFrom(ctx); slot != nil {
			*slot = report
		}
		if !report.Met {
			w.log.Warn("workflow outcome contract not met",
				zap.String("run_id", runID),
				zap.String("outcome", report.Outcome),
				zap.String("summary", report.Summary))
			if def.Outcome.EnforcementMode() == agent.EnforceFail {
				return nil, fmt.Errorf("outcome contract not met (%s): %s", report.Outcome, report.Summary)
			}
		}
	}

	// A workflow that terminates in channel.send yields a delivery RECEIPT, not
	// content — so an interactive reply would be a cryptic {"ok":true,...}. When
	// that's the case, surface the message the workflow actually sent instead, so
	// the user reads the real answer. Delivery still happened via channel.send.
	if isChannelSendReceipt(final) && strings.TrimSpace(lastSentText) != "" {
		if b, jerr := json.Marshal(lastSentText); jerr == nil {
			return json.RawMessage(b), nil
		}
	}
	return final, nil
}

// loaderDefinition returns the agent definition or nil, tolerating a nil loader
// (tests construct engines without one).
func (e *Engine) loaderDefinition(agentID string) *agent.Definition {
	if e.loader == nil {
		return nil
	}
	return e.loader.Get(agentID)
}

// emitOutcomeReport surfaces the contract verdict as a flow.outcome event, so
// Activity and the run trace can show WHY a run that looks clean is marked
// unsuccessful.
func (e *Engine) emitOutcomeReport(msg message.Message, runID string, report OutcomeReport) {
	if e.sink == nil {
		return
	}
	e.sink.Emit(message.Event{
		Type:      "flow.outcome",
		AgentID:   msg.AgentID,
		SessionID: msg.SessionID,
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"runId":      runID,
			"outcome":    report.Outcome,
			"met":        report.Met,
			"summary":    report.Summary,
			"assertions": report.Assertions,
		},
	})
}

// isChannelSendReceipt reports whether b is a bare channel.send delivery receipt
// ({ok, channel, to}) rather than real content.
func isChannelSendReceipt(b json.RawMessage) bool {
	if len(b) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	if _, hasOK := m["ok"]; !hasOK {
		return false
	}
	if _, hasChannel := m["channel"]; !hasChannel {
		return false
	}
	for key := range m {
		switch key {
		case "ok", "delivered", "channel", "to", "route_source", "text_preview":
		default:
			// Unknown keys imply this is real content, not a delivery receipt.
			return false
		}
	}
	return true
}

func trimFlowNodeRun(rec reasoning.FlowNodeRun) reasoning.FlowNodeRun {
	rec.Input = trimTraceString(rec.Input)
	rec.Error = trimTraceString(rec.Error)
	rec.Output = trimTraceJSON(rec.Output)
	return rec
}

func trimTraceString(s string) string {
	if len(s) <= flowTraceFieldMaxBytes {
		return s
	}
	prefix := s[:flowTraceFieldMaxBytes]
	for !utf8.ValidString(prefix) && len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + fmt.Sprintf("\n\n[truncated: %d bytes omitted from workflow trace]", len(s)-len(prefix))
}

func trimTraceJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) <= flowTraceFieldMaxBytes {
		return raw
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		b, _ := json.Marshal(trimTraceString(s))
		return b
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		b, _ := json.Marshal(map[string]any{
			"truncated": true,
			"summary":   fmt.Sprintf("Workflow trace output omitted because it was %d bytes. The full value remains available to downstream nodes during the run.", len(raw)),
		})
		return b
	}
	b, _ := json.Marshal(trimTraceString(string(raw)))
	return b
}

// parseToolArgs unmarshals a flow node's rendered input into a tool-args map.
// Node inputs are produced by text/template substitution (e.g. {{.search_query}}
// inside a JSON template), so an upstream agent's output containing a raw newline
// lands unescaped inside a JSON string literal and breaks json.Unmarshal. When
// the first parse fails, we escape bare control characters inside string literals
// and retry once before giving up — returning the ORIGINAL error so the message
// reflects the real defect, not the repaired text.
func parseToolArgs(in string) (map[string]any, error) {
	args := map[string]any{}
	if err := json.Unmarshal([]byte(in), &args); err == nil {
		return args, nil
	} else if repaired := repairJSONControlChars(in); repaired != in {
		args = map[string]any{}
		if err2 := json.Unmarshal([]byte(repaired), &args); err2 == nil {
			return args, nil
		}
		return nil, err
	} else {
		return nil, err
	}
}

func (e *Engine) runFlowLLMNode(ctx context.Context, def *agent.Definition, msg message.Message, node sdkr.FlowNode, renderedInput string) (json.RawMessage, error) {
	if e.llmRouter == nil {
		return nil, fmt.Errorf("llm node: no LLM router configured")
	}
	system := strings.TrimSpace(flowParamString(node.Params, "system"))
	if system == "" {
		system = "You are a workflow transform step. Return only the requested output."
	}
	user := strings.TrimSpace(renderedInput)
	if user == "" {
		user = flattenParts(msg.Parts)
	}
	responseFormat := strings.ToLower(strings.TrimSpace(flowParamString(node.Params, "response_format")))
	req := llm.CompletionRequest{
		Model:            def.LLM.Model,
		Temperature:      def.LLM.Temperature,
		TopP:             def.LLM.TopP,
		MaxTokens:        def.LLM.MaxTokens,
		ReasoningEffort:  def.LLM.ReasoningEffort,
		PresencePenalty:  def.LLM.PresencePenalty,
		FrequencyPenalty: def.LLM.FrequencyPenalty,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	if responseFormat == "json" || responseFormat == "json_schema" {
		req.ResponseFormat = responseFormat
	}
	if schema, ok := node.Params["json_schema"].(map[string]any); ok && len(schema) > 0 {
		req.ResponseFormat = "json_schema"
		req.JSONSchema = schema
	}

	provider := def.LLM.Provider
	model := def.LLM.Model
	if provider == "" && e.llmRouter != nil {
		provider = e.llmRouter.DefaultProvider()
	}
	if e.sink != nil {
		e.sink.Emit(message.Event{
			Type: "llm.call", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"provider": provider, "model": model, "node": node.ID},
			Timestamp: time.Now().UTC(),
		})
	}
	start := time.Now()
	resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, req)
	if e.sink != nil {
		payload := map[string]any{
			"provider":    provider,
			"model":       model,
			"node":        node.ID,
			"duration_ms": time.Since(start).Milliseconds(),
		}
		if resp != nil {
			payload["input_tokens"] = resp.InputTokens
			payload["output_tokens"] = resp.OutputTokens
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		e.sink.Emit(message.Event{
			Type: "llm.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: payload, Timestamp: time.Now().UTC(),
		})
	}
	if err != nil {
		return nil, fmt.Errorf("llm node: %w", err)
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return json.RawMessage(`""`), nil
	}
	if json.Valid([]byte(out)) {
		return json.RawMessage(out), nil
	}
	if candidate, ok := extractLLMJSON(out); ok {
		return json.RawMessage(candidate), nil
	}
	wrapped, _ := json.Marshal(out)
	return json.RawMessage(wrapped), nil
}

func extractLLMJSON(out string) (string, bool) {
	text := strings.TrimSpace(out)
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "```") {
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			body := strings.TrimSpace(text[nl+1:])
			if end := strings.LastIndex(body, "```"); end >= 0 {
				body = strings.TrimSpace(body[:end])
			}
			if json.Valid([]byte(body)) {
				return body, true
			}
		}
	}
	for i, r := range text {
		if r != '{' && r != '[' {
			continue
		}
		if candidate, ok := balancedJSONCandidate(text[i:]); ok && json.Valid([]byte(candidate)) {
			return candidate, true
		}
	}
	return "", false
}

func balancedJSONCandidate(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	open := s[0]
	var close byte
	switch open {
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[:i+1]), true
			}
		}
	}
	return "", false
}

func flowParamString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	switch v := params[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

// repairJSONControlChars escapes literal newline, carriage-return, and tab
// characters that appear INSIDE JSON string literals (which are invalid JSON).
// Control characters outside string literals — the structural whitespace between
// tokens — are left untouched. Returns the input unchanged when no repair was
// needed, so callers can detect a no-op.
func repairJSONControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	esc := false
	changed := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			if c == '"' {
				inStr = true
			}
			b.WriteByte(c)
			continue
		}
		if esc {
			b.WriteByte(c)
			esc = false
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			esc = true
		case '"':
			b.WriteByte(c)
			inStr = false
		case '\n':
			b.WriteString(`\n`)
			changed = true
		case '\r':
			b.WriteString(`\r`)
			changed = true
		case '\t':
			b.WriteString(`\t`)
			changed = true
		default:
			b.WriteByte(c)
		}
	}
	if !changed {
		return s
	}
	return b.String()
}

// unwrapToolJSON undoes RunTool's string wrapping when the tool's textual
// output is itself a JSON document: `"{\"ok\":true}"` → `{"ok":true}`.
// Edge predicates and node input templates can then address the fields
// ({{.verdict.ok}}). Non-JSON text stays a JSON string.
func unwrapToolJSON(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return raw // already an object/array/number — pass through
	}
	if json.Valid([]byte(s)) && len(s) > 0 {
		return json.RawMessage(s)
	}
	return raw
}
