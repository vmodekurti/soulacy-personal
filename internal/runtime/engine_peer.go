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
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// EventSink receives structured events as they happen during agent execution.
type agentCallDepthKey struct{}

func agentCallDepth(ctx context.Context) int {
	if v, ok := ctx.Value(agentCallDepthKey{}).(int); ok {
		return v
	}
	return 0
}

func withAgentCallDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, agentCallDepthKey{}, d)
}

// chainDeadlineKey carries a wall-clock deadline for the entire nested-agent
// chain. It is stamped once at depth 0 (the first agent call from a tool
// handler) and propagated unchanged through every sub-agent invocation.
//
// Without this, a 5-deep chain of agents each with a 5-minute run_timeout
// could hang the gateway for up to 25 minutes before any cancellation fires.
// With it, the whole fan-out is bounded to a single run_timeout budget.
type chainDeadlineKey struct{}

// withChainDeadline stamps deadline into ctx if no chain deadline exists yet
// (depth 0 case). At depth > 0 the existing deadline is preserved unchanged so
// the budget is not reset on every recursive call.
func withChainDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if _, already := ctx.Value(chainDeadlineKey{}).(time.Time); already {
		// Deadline already set by an ancestor — inherit it unchanged.
		return ctx, func() {}
	}
	ctx = context.WithValue(ctx, chainDeadlineKey{}, deadline)
	return context.WithDeadline(ctx, deadline)
}

// resolveAgentRefs expands the SOUL.yaml `agents:` list into actual peer
// Definitions. Supports "*" / "all" wildcards. Always excludes the caller
// itself — agents can't accidentally invoke themselves through a wildcard,
// which would otherwise be a single-step infinite loop. (Explicit self-call
// via a literal ID is also blocked here; require an explicit `agent__self`
// alias later if that pattern proves useful.)
func (e *Engine) resolveAgentRefs(refs []string, callerID string) []*agent.Definition {
	if len(refs) == 0 {
		return nil
	}
	wantAll := false
	for _, r := range refs {
		if r == "*" || r == "all" {
			wantAll = true
			break
		}
	}
	if wantAll {
		all := e.loader.All()
		out := make([]*agent.Definition, 0, len(all))
		for _, d := range all {
			if d.ID == callerID || !d.Enabled || e.loader.IsBuiltin(d.ID) {
				continue // exclude self, disabled agents, and built-ins from wildcards
			}
			out = append(out, d)
		}
		return out
	}
	out := make([]*agent.Definition, 0, len(refs))
	for _, r := range refs {
		if r == callerID {
			continue // can't call self
		}
		if d := e.loader.Get(r); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// buildAgentCallSchemas produces one ToolSchema per declared peer. Names are
// `agent__<peer-id>`, descriptions are pulled from the peer's SOUL.yaml so
// the LLM can pick the right peer based on what each one does.
func (e *Engine) buildAgentCallSchemas(def *agent.Definition) []llm.ToolSchema {
	peers := e.resolveAgentRefs(def.Agents, def.ID)
	if len(peers) == 0 {
		return nil
	}
	out := make([]llm.ToolSchema, 0, len(peers))
	for _, p := range peers {
		desc := strings.TrimSpace(p.Description)
		if desc == "" {
			desc = "(no description provided)"
		}
		if def.StructuredPeerResults {
			desc += " The tool result is returned as a SOULACY_AGENT_RESULT JSON envelope with target_agent, ok, content, and structured when the peer reply is valid JSON."
		}
		out = append(out, llm.ToolSchema{
			Name:        AgentToolPrefix + p.ID,
			Description: fmt.Sprintf("Delegate a sub-task to the %q agent. %s", p.ID, desc),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "The instruction, question, or task to send to this agent. Be specific and self-contained — the agent has no shared context with you.",
					},
				},
				"required": []string{"message"},
			},
		})
	}
	return out
}

// dispatchRouter handles Kind=="router" agents — see docs/CHANNEL_DESIGN.md
// Q2. The router has no LLM loop: it inspects the inbound text, picks the
// first matching route, and dispatches to the named peer agent via the
// existing peer-call path. The peer's reply becomes the router's reply.
//
// Matching:
//  1. Routes are tried in declaration order; the first match wins.
//  2. Each route has at most one effective match clause — Regex,
//     Prefix, or any-of Contains. A route with NONE of those clauses
//     is the "else" fallback (must be last if present).
//  3. Match is case-insensitive for Prefix and Contains. Regex follows
//     Go's regexp semantics (use `(?i)…` for case-insensitive patterns).
//  4. If no route matches and no fallback exists, the router emits an
//     error event and returns an empty reply.
//
// Authorization: the Target must be in the router's declared peer list
// (def.Agents). runAgentCall enforces this independently, so a router
// can't manufacture a forbidden Target via a bad config — but we also
// pre-validate here to produce a clearer error event.
//
// Trace: emits a `router.match` event recording which route matched
// (and what text triggered it) so misclassifications are debuggable.
func (e *Engine) dispatchRouter(ctx context.Context, def *agent.Definition, msg message.Message) (message.Message, error) {
	text := flattenParts(msg.Parts)
	matchIdx, matched := pickRouterRoute(def.Routes, text)
	if !matched {
		errMsg := fmt.Sprintf("engine: router %q has no matching route for inbound text and no fallback configured", def.ID)
		e.sink.Emit(message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"message":     errMsg,
				"reason":      "router_no_match",
				"text_prefix": truncate(text, 200),
			},
			Timestamp: time.Now().UTC(),
		})
		return message.Message{}, fmt.Errorf("%s", errMsg)
	}
	route := def.Routes[matchIdx]

	e.sink.Emit(message.Event{
		Type: "router.match", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"router":      def.ID,
			"target":      route.Target,
			"match_index": matchIdx,
			"match_kind":  routeMatchKind(route),
		},
		Timestamp: time.Now().UTC(),
	})

	// Reuse the existing peer-call path for the actual dispatch. This
	// gives us — for free — the peer-list authorization check
	// (runAgentCall.allowed) and the depth-limit guard.
	args := map[string]any{"message": text}
	peerReply, err := e.runAgentCall(ctx, def, AgentToolPrefix+route.Target, args)
	if err != nil {
		return message.Message{}, err
	}

	return message.Message{
		ID:        msg.ID,
		SessionID: msg.SessionID,
		AgentID:   def.ID,
		Channel:   msg.Channel,
		ThreadID:  msg.ThreadID,
		UserID:    msg.UserID,
		Username:  msg.Username,
		Role:      message.RoleAssistant,
		Parts:     message.Text(peerReply),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// pickRouterRoute returns the index of the first matching route, or
// (-1, false) if no route matches and no fallback exists. A "fallback"
// route is one with no Regex, no Prefix, and no Contains — pure else.
// Pre-compiles regexes per call; cheap enough at typical route counts.
// For agents with hundreds of routes this could be cached at load time,
// but that's premature optimisation given typical router shapes (3-10
// routes).
func pickRouterRoute(routes []agent.RouterRoute, text string) (int, bool) {
	lowText := strings.ToLower(text)
	for i, r := range routes {
		switch {
		case r.Regex != "":
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				continue // skip malformed; runtime warning is too noisy here
			}
			if re.MatchString(text) {
				return i, true
			}
		case r.Prefix != "":
			if strings.HasPrefix(lowText, strings.ToLower(r.Prefix)) {
				return i, true
			}
		case len(r.Contains) > 0:
			for _, sub := range r.Contains {
				if sub == "" {
					continue
				}
				if strings.Contains(lowText, strings.ToLower(sub)) {
					return i, true
				}
			}
		default:
			// No match clauses → this is the else fallback. Always wins.
			return i, true
		}
	}
	return -1, false
}

func routeMatchKind(r agent.RouterRoute) string {
	switch {
	case r.Regex != "":
		return "regex"
	case r.Prefix != "":
		return "prefix"
	case len(r.Contains) > 0:
		return "contains"
	default:
		return "fallback"
	}
}

// runAgentCall is the dispatcher invoked when an LLM emits a tool call whose
// name starts with AgentToolPrefix. It validates the peer reference, enforces
// the depth limit, and recurses into engine.Handle with a fresh session.
func (e *Engine) runAgentCall(ctx context.Context, callerDef *agent.Definition, fullToolName string, args map[string]any) (string, error) {
	targetID := strings.TrimPrefix(fullToolName, AgentToolPrefix)
	if targetID == "" || targetID == fullToolName {
		return "", fmt.Errorf("agent call: malformed tool name %q", fullToolName)
	}

	// Authorisation: the LLM is only allowed to invoke peers the parent
	// explicitly declared in its SOUL.yaml `agents:` list. We don't trust the
	// model not to manufacture an `agent__some-other-id` tool name.
	allowed := false
	for _, p := range e.resolveAgentRefs(callerDef.Agents, callerDef.ID) {
		if p.ID == targetID {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("agent call: %q is not in this agent's declared peer list", targetID)
	}

	// Depth limit — bounds A → B → A → … recursion chains.
	depth := agentCallDepth(ctx)
	depthLimit := e.agentCallDepthLimit()
	if depth >= depthLimit {
		return "", fmt.Errorf("agent call depth limit (%d) exceeded calling %q — possible infinite loop", depthLimit, targetID)
	}

	target := e.loader.Get(targetID)
	if target == nil {
		return "", fmt.Errorf("agent call: %q not loaded", targetID)
	}
	if !target.Enabled {
		return "", fmt.Errorf("agent call: %q is disabled", targetID)
	}

	msg := strings.TrimSpace(argString(args, "message"))
	if msg == "" {
		return "", fmt.Errorf("agent call: message is required")
	}

	e.log.Info("agent call",
		zap.String("caller", callerDef.ID),
		zap.String("target", targetID),
		zap.Int("depth", depth+1),
	)

	// Wall-clock chain budget: at depth 0 (first sub-agent call) stamp a
	// deadline equal to the CALLER's run_timeout so the entire nested chain
	// is bounded to one timeout budget, not one per depth level.
	// withChainDeadline is a no-op at depth > 0 — the ancestor deadline is
	// preserved unchanged through all recursive calls.
	chainTimeout := e.toolTimeout // fallback if agent has no run_timeout
	if callerDef.RunTimeout != "" {
		if d, err := time.ParseDuration(callerDef.RunTimeout); err == nil && d > 0 {
			chainTimeout = d
		}
	}
	subCtx, chainCancel := withChainDeadline(ctx, time.Now().Add(chainTimeout))
	defer chainCancel()

	subCtx = withAgentCallDepth(subCtx, depth+1)
	subSessionID := "agent-call-" + uuidShort()
	e.sink.Emit(message.Event{Type: "agent.call.started", AgentID: callerDef.ID, SessionID: inboundSessionID(ctx), Payload: map[string]any{"target_agent": targetID, "subagent_session_id": subSessionID, "depth": depth + 1}, Timestamp: time.Now().UTC()})
	reply, err := e.Handle(subCtx, message.Message{
		AgentID:   targetID,
		SessionID: subSessionID,
		Channel:   "internal",
		Username:  "agent:" + callerDef.ID,
		Parts:     message.Text(msg),
	})
	if err != nil {
		e.sink.Emit(message.Event{Type: "agent.call.failed", AgentID: callerDef.ID, SessionID: inboundSessionID(ctx), Payload: map[string]any{"target_agent": targetID, "subagent_session_id": subSessionID, "error": err.Error()}, Timestamp: time.Now().UTC()})
		return "", fmt.Errorf("agent call %q: %w", targetID, err)
	}
	content := flattenParts(reply.Parts)
	e.sink.Emit(message.Event{Type: "agent.call.completed", AgentID: callerDef.ID, SessionID: inboundSessionID(ctx), Payload: map[string]any{"target_agent": targetID, "subagent_session_id": subSessionID, "content": content}, Timestamp: time.Now().UTC()})
	if callerDef.StructuredPeerResults {
		return formatAgentCallResult(targetID, content), nil
	}
	return content, nil
}

func inboundSessionID(ctx context.Context) string {
	if msg, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
		return msg.SessionID
	}
	return ""
}

func formatAgentCallResult(targetID, content string) string {
	payload := map[string]any{
		"kind":         "agent_result",
		"target_agent": targetID,
		"ok":           true,
		"content":      content,
	}
	if parsed, ok := parseJSONValue(content); ok {
		payload["structured"] = parsed
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return content
	}
	return "SOULACY_AGENT_RESULT\n```json\n" + string(b) + "\n```"
}

func parseJSONValue(s string) (any, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return nil, false
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, false
	}
	return out, true
}

// uuidShort returns a fresh UUIDv4 string. Used for sub-agent session IDs
// and synthetic tool-call IDs in the auto-delegate path. Was time-based
// (`time.Now().UnixNano()`) — two parallel agent calls on the same tick
// could collide on the same session id and end up sharing a Session struct
// via `e.sessions`, bleeding history across them.
// (PRODUCTION_AUDIT → MEDIUM/Engine)
func uuidShort() string {
	return uuid.New().String()
}

// effectiveProvider resolves the provider an agent will actually use: its
// configured llm.provider, or the gateway default when unset.
func (e *Engine) effectiveProvider(def *agent.Definition) string {
	p := def.LLM.Provider
	if p == "" && e.llmRouter != nil {
		p = e.llmRouter.DefaultProvider()
	}
	return p
}

// providerSupportsNativeTools reports whether the agent's effective provider can
// call tools via the native completion API. Drives the default ("auto"/unset)
// execution-strategy choice in LoopConfigFromDefinition.
func (e *Engine) providerSupportsNativeTools(def *agent.Definition) bool {
	rdef := e.reasoningDef(def)
	prov := e.effectiveProvider(rdef)
	return reasoning.ProviderSupportsNativeTools(prov)
}

// reasoningBackendAvailable reports whether a real reasoning backend exists for
// the agent's effective provider. When false, the engine uses the classic tool
// loop instead of routing to a (likely absent) local Ollama. A custom backend
// factory (tests, embedders) always counts as available — it supplies the
// backend directly, bypassing provider-based resolution.
func (e *Engine) reasoningBackendAvailable(def *agent.Definition) bool {
	if e.reasoningBackendFactory != nil {
		return true
	}
	// Resolve the provider exactly as reasoningBackendFor will (including the
	// global llm.reasoner fallback), so the gate
	// and the backend selection never disagree.
	rdef := e.reasoningDef(def)
	prov := strings.ToLower(strings.TrimSpace(rdef.LLM.Provider))
	if prov == "" && e.llmRouter != nil {
		prov = strings.ToLower(strings.TrimSpace(e.llmRouter.DefaultProvider()))
	}
	if reasoning.BackendAvailable(prov, e.reasoningKeys) {
		return true
	}
	// Any provider the gateway's router serves can now drive the reasoning loop
	// via the router-backed backend (google/gemini, ollama_cloud, grok, …), so
	// ReAct/Plan-Execute agents are no longer limited to a hand-written few.
	return e.llmRouter != nil && e.llmRouter.Provider(prov) != nil
}

// reasoningDef returns the agent definition to use when resolving the reasoning
// backend. An agent-level provider or model is authoritative. The global
// llm.reasoner pair is only a fallback for agents with no provider/model pin.
// Treating it as an override made the model picker in Studio appear to save while
// ReAct and Plan-Execute silently ran a different model configured in Config.
func (e *Engine) reasoningDef(def *agent.Definition) *agent.Definition {
	if def == nil || strings.TrimSpace(def.LLM.Provider) != "" || strings.TrimSpace(def.LLM.Model) != "" ||
		(e.reasonerProvider == "" && e.reasonerModel == "") {
		return def
	}
	cp := *def
	if e.reasonerProvider != "" {
		cp.LLM.Provider = e.reasonerProvider
		cp.LLM.BaseURL = ""
	}
	if e.reasonerModel != "" {
		cp.LLM.Model = e.reasonerModel
	}
	return &cp
}

// providerIsOllama reports whether the agent resolves to the Ollama provider
// (its configured provider, or the gateway default when unset).
func (e *Engine) providerIsOllama(def *agent.Definition) bool {
	p := def.LLM.Provider
	if p == "" {
		p = e.llmRouter.DefaultProvider()
	}
	return p == "ollama"
}

// providerAllowed enforces the SOUL.yaml `llm.allowed_providers` guard.
// Returns true when the configured provider is permitted (the list is
// empty/nil → no restriction, or the provider name is on the list).
//
// Closes the "I clicked the wrong provider in the GUI dropdown and burned
// paid-API credit" failure mode: an agent that declares
// `llm.allowed_providers: [ollama]` is bricked the moment someone tries
// to point it at Anthropic / OpenAI / Gemini — surfaced as a clear error
// in the trace instead of a downstream HTTP 400 / 401 / 402 that looks
// like the model's fault.
//
// Package-level (not a method on Engine) because it has no engine state
// dependencies and so the unit test can exercise it without spinning up
// an LLM router or loader.
func providerAllowed(allowlist []string, provider string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, name := range allowlist {
		if name == provider {
			return true
		}
	}
	return false
}

func applyPlaygroundOverrides(def *agent.Definition, meta map[string]string) {
	if def == nil || len(meta) == 0 {
		return
	}
	if v := strings.TrimSpace(meta["playground.llm.provider"]); v != "" {
		def.LLM.Provider = v
	}
	if v := strings.TrimSpace(meta["playground.llm.model"]); v != "" {
		def.LLM.Model = v
	}
	if v := strings.TrimSpace(meta["playground.llm.temperature"]); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			def.LLM.Temperature = f
		}
	}
	if v := strings.TrimSpace(meta["playground.llm.max_tokens"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			def.LLM.MaxTokens = n
		}
	}
	if v := strings.TrimSpace(meta["playground.llm.top_p"]); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			def.LLM.TopP = f
		}
	}
	if v := strings.TrimSpace(meta["playground.llm.response_format"]); v != "" {
		def.LLM.ResponseFormat = v
	}
	if v := strings.TrimSpace(meta["playground.llm.reasoning_effort"]); v != "" {
		def.LLM.ReasoningEffort = v
	}
	if v := strings.TrimSpace(meta["playground.llm.presence_penalty"]); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			def.LLM.PresencePenalty = f
		}
	}
	if v := strings.TrimSpace(meta["playground.llm.frequency_penalty"]); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			def.LLM.FrequencyPenalty = f
		}
	}
	if v := strings.TrimSpace(meta["playground.max_turns"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			def.MaxTurns = n
		}
	}
	if v := strings.TrimSpace(meta["playground.llm.tool_choice"]); v != "" {
		def.LLM.ToolChoice = v
	}
}

// skillCatalogFor builds the <available_skills> catalog for the named skills.
// names may contain "*" or "all" to include every installed skill.
