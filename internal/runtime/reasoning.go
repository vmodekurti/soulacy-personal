// reasoning.go — Story 16: wiring the pluggable reasoning loops (E15) into
// the engine's live execution path.
//
// When an agent's SOUL.yaml declares a reasoning: block with a strategy, the
// engine runs the task through reasoning.Loop instead of the classic
// single-call tool loop. Tool calls issued by the loop are bridged back into
// Engine.runTool, so they respect the exact same dispatch order and policy
// surface as classic agents: MCP allowlists, plugin tools, peer-agent calls,
// built-in confirmation gates, the Python sandbox, audit logging, and SSRF
// protection. Agents without a reasoning block keep today's behaviour
// untouched.
//
// Step traces surface as engine events so the GUI thinking section and the
// activity feed can render them:
//
//	reasoning.start  {strategy, max_steps, tools}
//	reasoning.step   {index, thought, tool, observation, duration_ms} (per step)
//	reasoning.result {steps, confident, duration_ms}
//
// tool.call / tool.result events fire in real time from the executor bridge,
// exactly like the classic loop.
package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// routerCompleter adapts the engine's llm.Router to reasoning.Completer, so the
// reasoning loop can run on ANY provider the router serves — not just the few
// with a hand-written native backend. This is what makes ReAct/Plan-Execute
// agents work with google/gemini, ollama_cloud, grok, etc.
type routerCompleter struct {
	router   *llm.Router
	provider string
}

func (rc routerCompleter) Complete(ctx context.Context, model, system, user string, params reasoning.PhaseParams) (string, error) {
	if params.MaxTokens <= 0 {
		params.MaxTokens = 1024
	}
	responseFormat := strings.TrimSpace(params.ResponseFormat)
	if responseFormat == "" {
		responseFormat = "json"
	}
	resp, err := rc.router.Complete(ctx, rc.provider, llm.CompletionRequest{
		Model: model,
		Messages: []llm.ChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens:   params.MaxTokens,
		Temperature: phaseTemperature(params, 0.1),
		TopP:        params.TopP,
		// Best-effort structured output: providers that support it return JSON;
		// the rest ignore the hint and the strict prompt + lenient parse cover it.
		ResponseFormat: responseFormat,
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("reasoning: router returned a nil response for provider %q", rc.provider)
	}
	return resp.Content, nil
}

func phaseTemperature(params reasoning.PhaseParams, fallback float64) float64 {
	if params.Temperature > 0 {
		return params.Temperature
	}
	return fallback
}

// captureBackend wraps an LLMBackend to remember the most recent error from any
// phase. The reasoning strategies intentionally swallow Think/Reflect errors
// (a failed step still reflects on partial work), which means a model that
// can't be reached surfaces only as the opaque "loop produced empty output".
// Capturing the error lets handleWithReasoning explain WHY — e.g. provider not
// connected, unknown model, or bad key.
type captureBackend struct {
	inner   reasoning.LLMBackend
	lastErr error
}

func (c *captureBackend) Think(ctx context.Context, req reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	r, err := c.inner.Think(ctx, req)
	if err != nil {
		c.lastErr = err
	}
	return r, err
}

func (c *captureBackend) Plan(ctx context.Context, systemPrompt, taskInput string, maxSteps int) (reasoning.Plan, error) {
	p, err := c.inner.Plan(ctx, systemPrompt, taskInput, maxSteps)
	if err != nil {
		c.lastErr = err
	}
	return p, err
}

func (c *captureBackend) Reflect(ctx context.Context, req reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	r, err := c.inner.Reflect(ctx, req)
	if err != nil {
		c.lastErr = err
	}
	return r, err
}

// emptyReasoningMessage turns a blank reasoning result into a clear, actionable
// message for the user instead of a silent "(no final response produced)".
func (e *Engine) emptyReasoningMessage(def *agent.Definition, lastErr error) string {
	prov := strings.TrimSpace(def.LLM.Provider)
	if prov == "" && e.llmRouter != nil {
		prov = e.llmRouter.DefaultProvider()
	}
	model := strings.TrimSpace(def.LLM.Model)

	switch {
	case model == "":
		return fmt.Sprintf("This agent has no model configured (llm.model is empty), so it can't run. "+
			"Set a provider and model — for example provider %q with a model you have available — then try again.", prov)
	case e.llmRouter != nil && e.llmRouter.Provider(strings.ToLower(prov)) == nil:
		return fmt.Sprintf("The provider %q isn't connected, so the agent's model %q couldn't be reached. "+
			"Connect it on the Providers page (or pick a provider that's set up), then try again.", prov, model)
	case lastErr != nil:
		return fmt.Sprintf("The agent's model didn't return a response. Provider %q (model %q) reported: %s. "+
			"Check that the provider is connected and the model name is correct.", prov, model, lastErr.Error())
	default:
		return "(no final response produced — the model returned an empty answer)"
	}
}

// reasoningBackendFor selects the LLM backend for an agent's reasoning loop:
// a test override if set; otherwise the governed router-backed backend whenever
// the provider is registered. Native backends are retained only as a fallback
// for embedders that construct an Engine without a router.
func (e *Engine) reasoningBackendFor(def *agent.Definition) reasoning.LLMBackend {
	if e.reasoningBackendFactory != nil {
		return e.reasoningBackendFactory(def)
	}
	rdef := e.reasoningDef(def)
	prov := strings.ToLower(strings.TrimSpace(rdef.LLM.Provider))
	if prov == "" && e.llmRouter != nil {
		prov = strings.ToLower(strings.TrimSpace(e.llmRouter.DefaultProvider()))
	}
	if e.llmRouter != nil && e.llmRouter.Provider(prov) != nil {
		return reasoning.ApplyTuning(reasoning.NewRouterBackend(routerCompleter{router: e.llmRouter, provider: prov}, rdef.LLM.Model), rdef)
	}
	// No registered router provider — keep the historic native fallback for
	// SDK embedders and narrowly constructed tests.
	return reasoning.ApplyTuning(reasoning.DefaultBackendFor(rdef, e.reasoningKeys), rdef)
}

// SetReasoningKeys wires cloud-provider API keys for reasoning LLM backends.
// Called from internal/app at boot; safe to call before traffic starts.
func (e *Engine) SetReasoningKeys(keys reasoning.ProviderKeys) {
	e.reasoningKeys = keys
}

// SetReasonerOverride sets the optional global llm.reasoner provider/model
// fallback used by reasoning agents that have no provider/model pin. Empty
// strings clear the fallback. Called at boot.
func (e *Engine) SetReasonerOverride(provider, model string) {
	e.reasonerProvider = strings.TrimSpace(provider)
	e.reasonerModel = strings.TrimSpace(model)
}

// SetReasoningBackendFactory overrides how the engine builds the reasoning
// LLM backend for an agent. nil (the default) means
// reasoning.DefaultBackendFor(def, keys). Used by tests to inject fakes and
// available as an extension point for embedders.
func (e *Engine) SetReasoningBackendFactory(f func(*agent.Definition) reasoning.LLMBackend) {
	e.reasoningBackendFactory = f
}

// reasoningToolExecutor bridges sdk/reasoning.ToolExecutor onto the engine's
// tool dispatch. Every call goes through Engine.runTool — the same path the
// classic loop uses — so sandboxing, audit, confirmation gates, and MCP/plugin
// allowlists all apply unchanged.
type reasoningToolExecutor struct {
	e         *Engine
	def       *agent.Definition
	sessionID string
	schemas   map[string]llm.ToolSchema
}

func (x reasoningToolExecutor) Execute(ctx context.Context, call reasoning.ToolCall) reasoning.Observation {
	args := make(map[string]any, len(call.Arguments)+len(call.Input))
	if len(call.Arguments) > 0 {
		for k, v := range call.Arguments {
			args[k] = v
		}
	} else {
		for k, v := range call.Input {
			args[k] = v
		}
	}
	tc := normalizeToolCall(message.ToolCall{
		ID:        "rsn-" + uuidShort(),
		Name:      call.Tool,
		Arguments: args,
	})

	x.e.sink.Emit(message.Event{
		Type: "tool.call", AgentID: x.def.ID, SessionID: x.sessionID,
		Payload: tc, Timestamp: time.Now().UTC(),
	})

	toolStart := time.Now()
	result, err := x.e.runTool(ctx, x.def, x.sessionID, tc)
	metrics.ToolCallDuration.WithLabelValues(tc.Name).Observe(time.Since(toolStart).Seconds())
	isErr := err != nil
	if isErr {
		result = "error: " + err.Error()
		metrics.ToolCallsTotal.WithLabelValues(tc.Name, "error").Inc()
	} else {
		metrics.ToolCallsTotal.WithLabelValues(tc.Name, "success").Inc()
	}

	x.e.sink.Emit(message.Event{
		Type: "tool.result", AgentID: x.def.ID, SessionID: x.sessionID,
		Payload:   message.ToolResult{CallID: tc.ID, Name: tc.Name, Content: result, IsError: isErr},
		Timestamp: time.Now().UTC(),
	})

	if err != nil {
		return reasoning.Observation{
			Error:   err,
			Source:  tc.Name,
			Content: x.toolErrorObservation(tc, err),
		}
	}
	return reasoning.Observation{Content: result, Source: tc.Name}
}

func (x reasoningToolExecutor) toolErrorObservation(call message.ToolCall, err error) string {
	msg := fmt.Sprintf("tool error: %s", err)
	if x.schemas == nil {
		return msg
	}
	schema, ok := x.schemas[call.Name]
	if !ok {
		return msg
	}
	hint := toolSchemaHint(schema)
	if strings.TrimSpace(hint) == "" {
		return msg
	}
	return msg + "\n\nExpected arguments for " + call.Name + ": " + hint + ". Retry once with corrected arguments, choose another available tool, or finish with the useful result already gathered."
}

func toolSchemaHint(schema llm.ToolSchema) string {
	params := schema.Parameters
	if len(params) == 0 {
		return ""
	}
	props, _ := params["properties"].(map[string]any)
	required := requiredSchemaFields(params["required"])
	parts := make([]string, 0, len(props))
	for name, raw := range props {
		label := name
		if required[name] {
			label += "*"
		}
		if m, ok := raw.(map[string]any); ok {
			if typ := strings.TrimSpace(fmt.Sprint(m["type"])); typ != "" && typ != "<nil>" {
				label += ":" + typ
			}
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

func requiredSchemaFields(raw any) map[string]bool {
	out := map[string]bool{}
	switch v := raw.(type) {
	case []string:
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out[s] = true
			}
		}
	case []any:
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out[s] = true
			}
		}
	}
	return out
}

// handleWithReasoning runs one task through the reasoning Loop and finishes
// the run with the same persistence/reply contract as the classic path.
// loopCfg comes from reasoning.LoopConfigFromDefinition — the caller already
// verified the agent opted in.
func (e *Engine) handleWithReasoning(ctx context.Context, def *agent.Definition, sess *Session, msg message.Message, loopCfg reasoning.LoopConfig) (message.Message, error) {
	// Advertise the engine's FULL tool surface to the loop, not just the
	// agent's declared Python tools: built-ins (gated per agent/channel),
	// MCP tools, plugin tools, and peer agents — whatever allToolSchemas
	// would offer the classic loop.
	have := make(map[string]struct{}, len(loopCfg.ToolNames))
	for _, n := range loopCfg.ToolNames {
		have[n] = struct{}{}
	}
	toolSchemas := e.allToolSchemasForContext(ctx, def, msg.Channel)
	schemaByName := make(map[string]llm.ToolSchema, len(toolSchemas))
	for _, s := range toolSchemas {
		schemaByName[s.Name] = s
		if _, dup := have[s.Name]; !dup {
			loopCfg.ToolNames = append(loopCfg.ToolNames, s.Name)
			have[s.Name] = struct{}{}
		}
	}

	backend := &captureBackend{inner: e.reasoningBackendFor(def)}
	executor := reasoningToolExecutor{e: e, def: def, sessionID: msg.SessionID, schemas: schemaByName}
	loop := reasoning.New(loopCfg, backend, executor)

	e.sink.Emit(message.Event{
		Type: "reasoning.start", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"strategy":  string(loopCfg.Strategy),
			"max_steps": loopCfg.MaxSteps,
			"tools":     len(loopCfg.ToolNames),
		},
		Timestamp: time.Now().UTC(),
	})

	result := loop.Run(ctx, def.ID, flattenParts(msg.Parts))

	// Surface the think-act-observe trace. Loop.Run returns the full trace at
	// the end; tool.call/tool.result already streamed live from the bridge.
	for i, step := range result.Steps {
		obs := step.Obs.Content
		if len(obs) > 400 {
			obs = obs[:400] + "…"
		}
		e.sink.Emit(message.Event{
			Type: "reasoning.step", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"index":       i + 1,
				"thought":     step.Thought,
				"tool":        step.Action.Tool,
				"observation": obs,
				"obs_source":  step.Obs.Source,
				"recovery":    step.Obs.Source == "controller",
				"duration_ms": step.Duration.Milliseconds(),
			},
			Timestamp: time.Now().UTC(),
		})
	}

	e.sink.Emit(message.Event{
		Type: "reasoning.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{
			"steps":       len(result.Steps),
			"confident":   result.Confident,
			"duration_ms": result.Duration.Milliseconds(),
		},
		Timestamp: time.Now().UTC(),
	})

	// Story E23 — self-updating rulebooks, versioned. Reflect's
	// updated_rules persists ONLY when the agent opted in via
	// brain_memory.procedural.auto_update, always as a new immutable
	// version. A locked rulebook refuses the write (warn event, run
	// continues) — drift control beats self-tuning.
	if result.UpdatedRules != "" && def.BrainMemory.Procedural.AutoUpdate && e.brainStore != nil {
		if v, uerr := e.brainStore.UpdateProceduralVersioned(def.ID, result.UpdatedRules, "auto_update"); uerr != nil {
			e.sink.Emit(message.Event{
				Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "rulebook", "error": uerr.Error()},
				Timestamp: time.Now().UTC(),
			})
		} else {
			e.sink.Emit(message.Event{
				Type: "rulebook.updated", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"version": v, "source": "auto_update", "bytes": len(result.UpdatedRules)},
				Timestamp: time.Now().UTC(),
			})
		}
	}

	finalContent := result.Output
	if finalContent == "" {
		// Explain WHY instead of a silent blank: no model, provider offline, or
		// the captured backend error (bad key, unknown model, latency, …).
		diag := e.emptyReasoningMessage(def, backend.lastErr)
		finalContent = diag
		errText := "loop produced empty output"
		if backend.lastErr != nil {
			errText = backend.lastErr.Error()
		}
		e.sink.Emit(message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"stage": "reasoning", "error": errText, "detail": diag},
			Timestamp: time.Now().UTC(),
		})
	}

	reply := e.finalizeReply(ctx, def, sess, msg, finalContent)

	// Carry the run's confidence forward on the reply. Until now Confident was
	// pure telemetry — emitted on reasoning.result and then dropped — so a run
	// that ended "degraded / not confident" was handed to channel delivery
	// indistinguishable from a clean one, and a half-finished narration landed
	// in the user's inbox looking like a finished deliverable. Downstream
	// delivery (see scheduler.sendScheduledOutput) reads this to decide how to
	// present the result; interactive callers may ignore it.
	if !result.Confident {
		if reply.Metadata == nil {
			reply.Metadata = map[string]string{}
		}
		reply.Metadata[message.MetaReasoningDegraded] = "true"
		reply.Metadata[message.MetaReasoningSteps] = strconv.Itoa(len(result.Steps))
	}
	return reply, nil
}
