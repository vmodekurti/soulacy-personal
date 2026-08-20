// engine.go — the agent execution loop.
// The Engine is the heart of Soulacy. It receives a message, assembles the
// full context (system prompt + memory + history + tools), fires the LLM, and
// if the LLM requests tool calls, executes them in a sandboxed Python subprocess
// before re-entering the loop. This continues until the LLM produces a plain
// text response or the max_turns limit is hit.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/injection"
	"github.com/soulacy/soulacy/internal/intent"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/trust"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// EventSink receives structured events as they happen during agent execution.
func (e *Engine) executeToolCalls(ctx context.Context, def *agent.Definition, sessionID string, calls []message.ToolCall, seen map[string]string, seenMu *sync.Mutex) []message.ToolResult {
	if shouldParallelizePeerCalls(def, calls) {
		return e.executeToolCallsWithParallelPeers(ctx, def, sessionID, calls, seen, seenMu)
	}

	results := make([]message.ToolResult, len(calls))

	for i, tc := range calls {
		results[i] = e.executeOneToolCall(ctx, def, sessionID, normalizeToolCall(tc), seen, seenMu)
	}
	return results
}

func (e *Engine) executeToolCallsWithParallelPeers(ctx context.Context, def *agent.Definition, sessionID string, calls []message.ToolCall, seen map[string]string, seenMu *sync.Mutex) []message.ToolResult {
	results := make([]message.ToolResult, len(calls))
	var wg sync.WaitGroup

	for i, raw := range calls {
		tc := normalizeToolCall(raw)
		if isAgentToolCall(tc.Name) {
			wg.Add(1)
			go func(i int, tc message.ToolCall) {
				defer wg.Done()
				results[i] = e.executeOneToolCall(ctx, def, sessionID, tc, seen, seenMu)
			}(i, tc)
			continue
		}
		results[i] = e.executeOneToolCall(ctx, def, sessionID, tc, seen, seenMu)
	}

	wg.Wait()
	return results
}

func (e *Engine) executeOneToolCall(ctx context.Context, def *agent.Definition, sessionID string, tc message.ToolCall, seen map[string]string, seenMu *sync.Mutex) message.ToolResult {
	e.emit(ctx, message.Event{
		Type: "tool.call", AgentID: def.ID, SessionID: sessionID,
		Payload: tc, Timestamp: time.Now().UTC(),
	})

	// Anti-loop guard: if this exact tool+args was already run this turn,
	// don't run it again — hand back the prior result and tell the model
	// to move on.
	argsJSON, _ := json.Marshal(tc.Arguments)
	key := tc.Name + "|" + string(argsJSON)
	seenMu.Lock()
	prev, dup := seen[key]
	if !dup {
		// Reserve while holding the same lock as the read. The old read-unlock-
		// execute-write sequence allowed two concurrent callers to both observe
		// absence and perform the same side effect. A duplicate that encounters
		// this marker waits for the owner to publish its result below.
		seen[key] = toolCallInFlight
	}
	seenMu.Unlock()
	if dup && prev == toolCallInFlight {
		var waitErr error
		prev, waitErr = awaitToolCallResult(ctx, key, seen, seenMu)
		if waitErr != nil {
			prev = "error: duplicate tool call was cancelled while waiting for the original result: " + waitErr.Error()
		}
	}

	var result string
	isErr := false
	if dup {
		result = fmt.Sprintf(
			"NOTE: you already called %s with these arguments this run; do not call it again. "+
				"Use the result below and proceed to the next step.\n\n%s", tc.Name, prev)
	} else {
		// Per-tool timing + outcome counter. (PRODUCTION_AUDIT →
		// MED/Observability)
		toolStart := time.Now()
		var err error
		result, err = e.runTool(ctx, def, sessionID, tc)
		toolLabel := e.toolMetricLabel(tc.Name)
		metrics.ToolCallDuration.WithLabelValues(toolLabel).Observe(time.Since(toolStart).Seconds())
		if err != nil {
			result = fmt.Sprintf("error: %v", err)
			isErr = true
			metrics.ToolCallsTotal.WithLabelValues(toolLabel, "error").Inc()
		} else {
			metrics.ToolCallsTotal.WithLabelValues(toolLabel, "success").Inc()
		}
		seenMu.Lock()
		seen[key] = result
		seenMu.Unlock()
	}

	// S1 (Cohort F) — classify the tool result's trust boundary and, for
	// external-content tools, wrap the body in an <external_content>
	// envelope so the model treats it as evidence rather than
	// instruction. Errors are always considered trusted framework
	// status ("tool X failed: …") because the error string is minted by
	// us, not by the remote source. Duplicate-run guards return prior
	// content that was already wrapped, so we skip re-wrapping when the
	// content already carries an envelope.
	trustLevel := trust.ToolTrust(tc.Name)
	sourceCategory := trust.SourceCategory(tc.Name)
	if !isErr && trustLevel == trust.Untrusted && !trust.IsWrapped(result) {
		result = trust.Wrap(trustLevel, tc.Name, result)
	}
	trustStr := trustLevel.String()
	if isErr {
		// Error strings are framework metadata; mark the record accordingly
		// even though we didn't wrap the (unwrapped) message.
		trustStr = trust.Trusted.String()
	}

	// S2 (Cohort F) — scan the wrapped body for prompt-injection
	// patterns. Findings never block the tool result itself (the
	// operator still gets to see what the source returned); they land
	// on the emitted event so Activity + Studio can render the warning
	// and S3's intent gate consumes MaxSeverity when deciding whether
	// to allow / prompt / deny an unrelated followup privileged tool
	// call. Trusted framework-minted content is not scanned — it
	// wasn't attacker-authored.
	var scanReport injection.Report
	if !isErr && trustLevel == trust.Untrusted {
		scanBody := result
		if env, ok := trust.Extract(result); ok {
			scanBody = env.Body
		}
		scanReport = injection.ScanTrusted(scanBody, tc.Name)
		if scanReport.MaxSeverity != injection.SeverityNone {
			agentID := ""
			if def != nil {
				agentID = def.ID
			}
			e.recordInjectionFinding(ctx, agentID, sessionID, tc.Name, scanReport)
		}
	}

	// S3 (Cohort F) — track whether the most recent evidence source
	// was untrusted. The intent gate reads this on the NEXT tool call
	// so it can distinguish "user asked for a shell command" from
	// "we just fetched a page that told us to shell out." Only real
	// evidence flips the flag; errors don't reset it (an errored
	// fetch still might have leaked untrusted headers in the error).
	if !isErr {
		agentID := ""
		if def != nil {
			agentID = def.ID
		}
		if sess := e.lookupSession(agentID, sessionID); sess != nil {
			sess.mu.Lock()
			sess.lastEvidenceUntrusted = trustLevel == trust.Untrusted
			sess.mu.Unlock()
		}
	}

	out := message.ToolResult{
		CallID:  tc.ID,
		Name:    tc.Name,
		Content: result,
		IsError: isErr,
		Trust:   trustStr,
		Source:  sourceCategory,
	}

	// (The ToolObserver fires inside runTool so it captures both reasoning-loop
	// and fixed-workflow tool calls uniformly.)

	eventPayload := any(out)
	if scanReport.MaxSeverity != injection.SeverityNone {
		// Enrich the event payload with the scan report so Activity /
		// Studio have first-class access without re-scanning. We keep
		// the plain ToolResult under the `tool_result` key so old
		// consumers can still pull it, and expose the report under
		// `injection` for the S2 UI surface.
		eventPayload = map[string]any{
			"tool_result": out,
			"injection": map[string]any{
				"max_severity": scanReport.MaxSeverity.String(),
				"findings":     scanReport.Findings,
				"counts":       scanReport.Counts,
			},
		}
	}
	e.emit(ctx, message.Event{
		Type: "tool.result", AgentID: def.ID, SessionID: sessionID,
		Payload: eventPayload, Timestamp: time.Now().UTC(),
	})
	return out
}

const toolCallInFlight = "\x00soulacy:tool-call-in-flight\x00"

func awaitToolCallResult(ctx context.Context, key string, seen map[string]string, seenMu *sync.Mutex) (string, error) {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		seenMu.Lock()
		result := seen[key]
		seenMu.Unlock()
		if result != toolCallInFlight {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// recordInjectionFinding stashes the highest injection severity seen
// on the session so S3's tool-call intent gate can consult it later
// on the same turn. Also emits a structured `injection.finding` event
// so Activity + Studio render the warning next to the run trace. Safe
// to call from concurrent goroutines (session mutex).
func (e *Engine) recordInjectionFinding(ctx context.Context, agentID, sessionID, source string, r injection.Report) {
	if sess := e.lookupSession(agentID, sessionID); sess != nil {
		sess.mu.Lock()
		if r.MaxSeverity > sess.injectionMax {
			sess.injectionMax = r.MaxSeverity
		}
		if r.MaxSeverity >= injection.SeverityHigh {
			sess.injectionLastSource = source
		}
		sess.mu.Unlock()
	}
	if e.sink != nil {
		e.emit(ctx, message.Event{
			Type: "injection.finding", AgentID: agentID, SessionID: sessionID,
			Payload: map[string]any{
				"source":       source,
				"max_severity": r.MaxSeverity.String(),
				"findings":     r.Findings,
				"counts":       r.Counts,
			},
			Timestamp: time.Now().UTC(),
		})
	}
	if e.log != nil {
		e.log.Info("injection: pattern scan flagged content",
			zap.String("agent", agentID),
			zap.String("session", sessionID),
			zap.String("source", source),
			zap.String("max_severity", r.MaxSeverity.String()),
			zap.Int("finding_count", len(r.Findings)),
		)
	}
}

// SessionInjectionState reports the highest injection severity
// observed on session (agentID, sessionID) and the tool name that
// produced the most recent High finding. Consumed by S3's intent gate.
// Returns (SeverityNone, "") if the session is unknown or clean.
// Exported so gateway handlers (S7 Doctor dry-run) can consult the
// same state.
func (e *Engine) SessionInjectionState(agentID, sessionID string) (injection.Severity, string) {
	sess := e.lookupSession(agentID, sessionID)
	if sess == nil {
		return injection.SeverityNone, ""
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.injectionMax, sess.injectionLastSource
}

// evaluateIntent runs the S3 tool-call intent gate for `call`. The
// first return is false when the gate is skipped entirely (nil engine,
// non-high-risk tool, empty session id) — the caller must not act on
// the Evaluation in that case. Otherwise returns the Evaluation for
// the caller to switch on.
//
// The gate reads the session's captured user goal + last-evidence
// trust flag + running injection severity so it can distinguish "the
// operator asked to shell out" from "we just fetched a page telling
// us to shell out." See internal/intent for the decision matrix.
func (e *Engine) evaluateIntent(def *agent.Definition, sessionID string, call message.ToolCall) (bool, intent.Evaluation) {
	if e == nil || !intent.IsHighRisk(call.Name) {
		return false, intent.Evaluation{}
	}
	agentID := agentIDOf(def)
	sess := e.lookupSession(agentID, sessionID)
	in := intent.Input{
		ToolName:  call.Name,
		Arguments: call.Arguments,
	}
	// F-Bridge — resolver preferences per-agent, falling back to the
	// workspace default set via SetIntentGateDefault. Empty return means
	// intent.Evaluate treats it as ModePrompt (see internal/intent/intent.go).
	in.Mode = intent.Mode(e.ResolveIntentGate(def))
	if sess != nil {
		sess.mu.Lock()
		in.UserGoal = sess.userGoal
		in.LastEvidenceUntrusted = sess.lastEvidenceUntrusted
		in.InjectionSeverity = sess.injectionMax
		in.InjectionSource = sess.injectionLastSource
		sess.mu.Unlock()
	}
	return true, intent.Evaluate(in)
}

// emitIntentDecision records an intent-gate decision to the event
// stream (Activity + Studio surface it) and to the structured log.
// Fires on Deny and Prompt unconditionally, and on Allow only when
// the decision was influenced by an injection signal so the trace
// documents the near-miss.
func (e *Engine) emitIntentDecision(ctx context.Context, agentID, sessionID string, call message.ToolCall, ev intent.Evaluation) {
	if e == nil || e.sink == nil {
		return
	}
	e.emit(ctx, message.Event{
		Type: "intent.decision", AgentID: agentID, SessionID: sessionID,
		Payload: map[string]any{
			"tool":                 call.Name,
			"decision":             ev.Decision.String(),
			"reason":               ev.Reason,
			"goal_matched":         ev.GoalMatched,
			"injection_influenced": ev.InjectionInfluenced,
		},
		Timestamp: time.Now().UTC(),
	})
}

// agentIDOf is a nil-safe accessor for def.ID used across the Cohort F
// helpers so the engine paths that receive a nil def (test harnesses,
// synthesised specs) don't panic when annotating an event.
func agentIDOf(def *agent.Definition) string {
	if def == nil {
		return ""
	}
	return def.ID
}

// lookupSession returns the in-memory Session for (agentID, sessionID)
// or nil if none is registered. Uses the same key scheme as
// getOrCreateSession so callers see the same struct instance.
func (e *Engine) lookupSession(agentID, sessionID string) *Session {
	if e == nil {
		return nil
	}
	val, ok := e.sessions.Load(agentID + "|" + sessionID)
	if !ok {
		return nil
	}
	sess, _ := val.(*Session)
	return sess
}

func shouldParallelizePeerCalls(def *agent.Definition, calls []message.ToolCall) bool {
	if def == nil || !def.ParallelPeerCalls || len(calls) < 2 {
		return false
	}
	peerCalls := 0
	seenBatch := map[string]bool{}
	for _, raw := range calls {
		tc := normalizeToolCall(raw)
		argsJSON, _ := json.Marshal(tc.Arguments)
		key := tc.Name + "|" + string(argsJSON)
		if seenBatch[key] {
			return false
		}
		seenBatch[key] = true
		if isAgentToolCall(tc.Name) {
			peerCalls++
		}
	}
	return peerCalls >= 2
}

func isAgentToolCall(name string) bool {
	return strings.HasPrefix(normalizeToolCallName(name), AgentToolPrefix)
}

func normalizeToolCall(call message.ToolCall) message.ToolCall {
	call.Name = normalizeToolCallName(call.Name)
	call.Arguments = unwrapToolArguments(call.Name, call.Arguments)
	switch call.Name {
	case "channel.send", "channel.status":
		call.Arguments = normalizeChannelSendArgs(call.Arguments)
	case "fetch_url":
		call.Arguments = normalizeAliasArgs(call.Arguments, map[string][]string{
			"url": {"source_url", "link", "href", "uri"},
		})
	case "web_search":
		call.Arguments = normalizeWebSearchArgs(call.Arguments)
	case "kb_search":
		call.Arguments = normalizeAliasArgs(call.Arguments, map[string][]string{
			"kb":    {"knowledge_base", "kb_name", "collection", "store"},
			"query": {"q", "text", "input", "prompt"},
			"top_k": {"limit", "count", "k"},
		})
	case "kb_write":
		call.Arguments = normalizeAliasArgs(call.Arguments, map[string][]string{
			"kb":      {"knowledge_base", "kb_name", "collection", "store"},
			"content": {"text", "document", "artifact", "data", "body", "markdown"},
			"source":  {"source_url", "url", "file", "file_path"},
		})
	case "queue_create", "queue_put", "queue_take", "queue_list", "queue_clear":
		call.Arguments = normalizeQueueArgs(call.Name, call.Arguments)
	}
	return call
}

func normalizeToolCallName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"agent:", "tool:", "function:", "functions."} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimSpace(strings.TrimPrefix(name, prefix))
			break
		}
	}
	switch strings.ToLower(name) {
	case "google:search", "google_search", "browser.search", "browser_search", "search", "web.search", "web-search", "search_web", "websearch", "internet_search", "internet.search":
		return "web_search"
	case "send_message", "send.notification", "send_notification", "notify", "notification.send", "channel_send", "channel-send", "send_channel", "send.channel", "telegram_send", "telegram.send", "slack_send", "slack.send", "discord_send", "discord.send", "email_send", "email.send", "teams_send", "teams.send", "google_chat_send", "google_chat.send", "googlechat_send", "googlechat.send", "webhook_send", "webhook.send", "whatsapp_send", "whatsapp.send":
		return "channel.send"
	case "channel_status", "channel.status", "channel_diagnose", "channel.diagnose", "channel_doctor", "channel.doctor", "diagnose_channel", "delivery_doctor", "delivery.doctor":
		return "channel.status"
	case "read_url", "open_url", "get_url", "url_fetch", "fetch-url", "http_get", "http.get":
		return "fetch_url"
	case "search_kb", "kb.search", "knowledge_search", "knowledge.search", "rag_search", "rag.search":
		return "kb_search"
	case "write_kb", "kb.write", "kb_add", "kb.add", "kb_store", "kb.store", "store_kb", "knowledge_write", "knowledge.write", "knowledge_store", "knowledge.store":
		return "kb_write"
	case "queue.add", "queue_add", "queue.push", "queue_push", "enqueue", "queue.enqueue":
		return "queue_put"
	case "queue.read", "queue_read", "queue.items", "queue_items", "queue.peek", "queue_peek", "peek_queue":
		return "queue_list"
	case "dequeue", "queue.pop", "queue_pop":
		return "queue_take"
	case "queue.create", "queue-create":
		return "queue_create"
	case "queue.clear", "queue-clear":
		return "queue_clear"
	}
	return name
}

func unwrapToolArguments(tool string, args map[string]any) map[string]any {
	if args == nil || len(args) != 1 {
		return args
	}
	for _, key := range []string{"arguments", "args", "parameters", "params", "input"} {
		v, ok := args[key]
		if !ok {
			continue
		}
		if unwrapped, ok := coerceToolArgumentMap(v); ok {
			return unwrapped
		}
	}
	if tool != "queue_put" {
		if v, ok := args["payload"]; ok {
			if unwrapped, ok := coerceToolArgumentMap(v); ok {
				return unwrapped
			}
		}
	}
	return args
}

func coerceToolArgumentMap(v any) (map[string]any, bool) {
	switch typed := v.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[k] = val
		}
		return out, true
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(typed)), &out); err == nil && out != nil {
			return out, true
		}
	}
	return nil, false
}

func normalizeWebSearchArgs(args map[string]any) map[string]any {
	if args == nil {
		return args
	}
	if _, ok := args["query"]; ok {
		return args
	}
	if q, ok := args["q"]; ok {
		args["query"] = q
		return args
	}
	if query, ok := args["queries"]; ok {
		switch v := query.(type) {
		case string:
			args["query"] = v
		case []string:
			args["query"] = strings.Join(v, " ")
		case []any:
			parts := make([]string, 0, len(v))
			for _, item := range v {
				if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
					parts = append(parts, s)
				}
			}
			args["query"] = strings.Join(parts, " ")
		default:
			args["query"] = fmt.Sprint(v)
		}
	}
	return args
}

func normalizeChannelSendArgs(args map[string]any) map[string]any {
	return normalizeAliasArgs(args, map[string][]string{
		"channel": {"adapter", "adapter_id", "platform"},
		"to":      {"destination", "target", "recipient", "chat_id", "channel_id", "thread_id", "user_id", "conversation", "room"},
		"text":    {"message", "msg", "body", "content"},
	})
}

func normalizeQueueArgs(tool string, args map[string]any) map[string]any {
	args = normalizeAliasArgs(args, map[string][]string{
		"queue":       {"name", "queue_name", "topic"},
		"ttl_seconds": {"ttl", "ttl_sec", "expires_in"},
		"limit":       {"count", "max", "max_items"},
	})
	if tool == "queue_put" {
		args = normalizeAliasArgs(args, map[string][]string{
			"item": {"value", "payload", "data", "content", "document", "resource", "url"},
		})
	}
	return args
}

func normalizeAliasArgs(args map[string]any, aliases map[string][]string) map[string]any {
	if args == nil {
		return args
	}
	for canonical, names := range aliases {
		if _, ok := args[canonical]; ok {
			continue
		}
		for _, alias := range names {
			if v, ok := args[alias]; ok {
				args[canonical] = v
				break
			}
		}
	}
	return args
}

// toolTimeoutOverrideKey carries a per-node tool-timeout override down the
// context to runTool, so a single flow node can widen (or tighten) its own
// execution budget without changing the global runtime.tool_timeout.
type toolTimeoutOverrideKey struct{}

// WithToolTimeout returns a context that makes d the tool-timeout for any tool or
// inline-python call executed under it. d<=0 is a no-op (keeps the global).
func WithToolTimeout(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, toolTimeoutOverrideKey{}, d)
}

type toolObserverKey struct{}

// ToolObserver is invoked after each tool call during a run with the call, its
// result content, and whether it errored. It is a lightweight, in-process,
// synchronous tap (distinct from the async event hub) so a caller like Studio's
// "try it" can collect the exact sequence of skills/tools an agent invoked —
// with arguments and a result preview — to answer "did it route right, and what
// did it actually do?".
type ToolObserver func(call message.ToolCall, result string, isError bool)

// WithToolObserver attaches a ToolObserver for the duration of a run.
func WithToolObserver(ctx context.Context, fn ToolObserver) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, toolObserverKey{}, fn)
}

func toolObserverFrom(ctx context.Context) ToolObserver {
	fn, _ := ctx.Value(toolObserverKey{}).(ToolObserver)
	return fn
}

type flowNodeObserverKey struct{}

// FlowNodeObserver is invoked after every executed workflow node (python, tool,
// agent, llm) with its per-node record — id, kind, input, output, error. Like
// ToolObserver it is a synchronous in-process tap; Studio's "Run live" uses it
// to show EVERY node's result (not just tool calls), including a Python node
// that was refused by the consent gate.
type FlowNodeObserver func(rec reasoning.FlowNodeRun)

// WithFlowNodeObserver attaches a FlowNodeObserver for the duration of a run.
func WithFlowNodeObserver(ctx context.Context, fn FlowNodeObserver) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, flowNodeObserverKey{}, fn)
}

func flowNodeObserverFrom(ctx context.Context) FlowNodeObserver {
	fn, _ := ctx.Value(flowNodeObserverKey{}).(FlowNodeObserver)
	return fn
}

// toolTimeoutOverride reads a per-node override set by WithToolTimeout.
func toolTimeoutOverride(ctx context.Context) (time.Duration, bool) {
	d, ok := ctx.Value(toolTimeoutOverrideKey{}).(time.Duration)
	return d, ok && d > 0
}

// effectiveToolTimeout is the timeout a tool call should use: a per-node override
// when present, otherwise the engine's global tool_timeout.
func (e *Engine) effectiveToolTimeout(ctx context.Context) time.Duration {
	if d, ok := toolTimeoutOverride(ctx); ok {
		return d
	}
	return e.toolTimeout
}

// toolTimeoutError turns a bare "context deadline exceeded" from a tool call
// into an actionable message that names the clock that fired (the per-tool
// tool_timeout) and how to extend it — mirroring the LLM-call diagnostics so a
// slow-by-design tool (e.g. NotebookLM research/audio polling) doesn't leave the
// user guessing. It only enriches a deadline error caused by OUR tool timeout:
// when the OUTER run context is already done (outerCtxErr != nil) the cause is
// the run_timeout or a cancel, so the error passes through unchanged, as does any
// non-deadline error.
func toolTimeoutError(name string, d time.Duration, outerCtxErr, callErr error) error {
	if callErr == nil {
		return nil
	}
	if outerCtxErr == nil && errors.Is(callErr, context.DeadlineExceeded) {
		return fmt.Errorf("tool %q exceeded the %s tool_timeout — for slow-by-design tools (e.g. NotebookLM research/audio polling) raise runtime.tool_timeout, set a longer per-tool timeout, or poll in a bounded loop: %w", name, d, callErr)
	}
	return callErr
}

func pythonToolRetries(tool *agent.ToolDef) int {
	if tool == nil || tool.Retries <= 0 {
		return 0
	}
	if tool.Retries > 5 {
		return 5
	}
	return tool.Retries
}

func pythonToolRetryBackoff(tool *agent.ToolDef) time.Duration {
	if tool == nil || strings.TrimSpace(tool.RetryBackoff) == "" {
		return time.Second
	}
	d, err := time.ParseDuration(tool.RetryBackoff)
	if err != nil || d < 0 {
		return time.Second
	}
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

// runTool dispatches a single tool call, notifying any in-context ToolObserver
// with the call + result. This wraps runToolDispatch so EVERY tool execution —
// reasoning-loop OR fixed-workflow (both go through here) — is observed from one
// place, which powers Studio's "try it" trace for agents and workflows alike.
