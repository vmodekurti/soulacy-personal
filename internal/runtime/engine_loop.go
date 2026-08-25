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
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/metrics"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// EventSink receives structured events as they happen during agent execution.
func (e *Engine) Handle(ctx context.Context, msg message.Message) (reply message.Message, err error) {
	metadata := llm.CallMetadataFromContext(ctx)
	if metadata.Subject == "" {
		metadata.Subject = msg.UserID
	}
	if metadata.AgentID == "" {
		metadata.AgentID = msg.AgentID
	}
	if metadata.SessionID == "" {
		metadata.SessionID = msg.SessionID
	}
	if metadata.RunID == "" {
		metadata.RunID = msg.ID
	}
	if metadata.Source == "" {
		metadata.Source = msg.Channel
		if metadata.Source == "" {
			metadata.Source = "runtime"
		}
	}
	if metadata.Trigger == "" && msg.Metadata != nil {
		metadata.Trigger = msg.Metadata["trigger"]
	}
	if metadata.DataClassification == "" && msg.Metadata != nil {
		metadata.DataClassification = msg.Metadata["data_classification"]
	}
	ctx = llm.WithCallMetadata(ctx, metadata)
	// Per-run timing + outcome counter. Outcome resolved at deferred-call
	// time so panics still record as "error". (PRODUCTION_AUDIT → MED/Observability)
	//
	// Failure notification (added 2026-05-28): when the run errors and the
	// engine has a FailureNotifier wired (typically from main.go via
	// chanReg.Send), we fire it here from the same defer. Single source of
	// truth so a hosted cron failure produces the same notification as a
	// channel-triggered failure with no per-call-site duplication. The
	// notifier itself decides what to do — by default it (a) honours the
	// agent's notify_on_failure block, and (b) for inbound channel
	// messages with no explicit target, replies on the same channel so
	// the original user sees the error.
	runStart := time.Now()
	runOutcome := "error"
	runID := strings.TrimSpace(metadata.RunID)
	if runID == "" {
		runID = fmt.Sprintf("%s-%d", msg.SessionID, runStart.UnixNano())
		metadata.RunID = runID
		ctx = llm.WithCallMetadata(ctx, metadata)
	}
	var runProvider, runModel, runStrategy string
	defer func() {
		degraded := reply.Metadata != nil && strings.EqualFold(reply.Metadata[message.MetaReasoningDegraded], "true")
		success := err == nil && runOutcome == "success" && !degraded
		metrics.AgentRunDuration.Observe(time.Since(runStart).Seconds())
		metrics.AgentRunsTotal.WithLabelValues(runOutcome).Inc()
		// A single explicit terminal event gives learning/telemetry consumers an
		// authoritative run boundary. Session IDs are conversational and may span
		// hundreds of turns; error events may be recovered. Neither is a run ID.
		if e.sink != nil {
			e.emit(ctx, message.Event{
				Type: "run.completed", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload: map[string]any{
					"run_id": runID, "provider": runProvider, "model": runModel,
					"strategy": runStrategy, "success": success, "degraded": degraded,
					"outcome": runOutcome,
				},
				Timestamp: time.Now().UTC(),
			})
		}
		if err != nil {
			// Push failed run to dead-letter queue, if one is wired.
			if e.dlqStore != nil {
				payload, _ := json.Marshal(msg)
				dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if dlqErr := e.dlqStore.PushFailed(dctx, WorkspaceFromContext(ctx), msg.AgentID, payload, err.Error()); dlqErr != nil {
					e.log.Warn("dlq push failed", zap.Error(dlqErr))
				}
				dcancel()
			}
			if e.failureNotifier != nil {
				d := e.loader.GetInWorkspace(WorkspaceFromContext(ctx), msg.AgentID)
				if d == nil {
					// Synthesize a placeholder so the notifier can still
					// route the message via the inbound-channel fallback
					// when the agent ID itself was unknown.
					d = &agent.Definition{ID: msg.AgentID}
				}
				notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				e.failureNotifier.NotifyFailure(notifyCtx, d, msg, err.Error())
			}
		}
	}()

	// S2.1 — Panic isolation. Channel- and cron-driven runs execute in bare
	// worker goroutines (internal/app/wire_subsystems.go) that are NOT behind
	// Fiber's recover middleware, so an un-recovered panic in a tool handler,
	// provider decode, or any helper would crash the entire process — taking
	// down every channel, the scheduler, and all in-flight HTTP requests with
	// it. This recover converts a panic into an ordinary run error. It is
	// registered AFTER the metrics/DLQ/notifier defer above so it runs FIRST
	// during unwind: it sets the named return `err`, which the outer defer then
	// observes to record the "error" outcome, push to the DLQ, and fire the
	// failure notifier — exactly as for a normal error.
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			e.log.Error("engine: recovered panic in Handle",
				zap.String("agent", msg.AgentID),
				zap.String("session", msg.SessionID),
				zap.Any("panic", r),
				zap.ByteString("stack", stack))
			metrics.AgentPanicsTotal.Inc()
			err = fmt.Errorf("engine: recovered panic: %v", r)
		}
	}()

	// Stamp session ID onto the context so nested helpers (audit, confirm) can
	// retrieve it without threading msg through every call.
	ctx = context.WithValue(ctx, inboundMsgKey{}, msg)

	// Attach a run-scoped collector so the generate_chart builtin can stash
	// chart specs that we append to the final reply (rendered by the GUI),
	// without depending on the model to echo the spec verbatim.
	ctx = withChartSink(ctx)

	// Task #32 — OTEL span for this agent run.
	if e.tracer != nil {
		var span interface{ End() }
		ctx, span = e.tracer.Start(ctx, "engine.Handle",
			"agent.id", msg.AgentID,
			"session.id", msg.SessionID)
		defer span.End()
	}

	// Resolve agent definition
	// Agent identity is (workspace, id). The gateway already resolved the
	// workspace into the run principal, so execution must use that same scope.
	// Looking up the legacy personal registry here made a workspace agent appear
	// in Deployed but fail in Playground as "unknown agent".
	def := e.loader.GetInWorkspace(WorkspaceFromContext(ctx), msg.AgentID)
	if def == nil {
		return message.Message{}, fmt.Errorf("engine: unknown agent %q", msg.AgentID)
	}
	def = def.Clone()
	applyPlaygroundOverrides(def, msg.Metadata)
	runProvider = strings.TrimSpace(def.LLM.Provider)
	runModel = strings.TrimSpace(def.LLM.Model)
	runStrategy = strings.TrimSpace(def.Reasoning.Strategy)
	if def.Workflow != nil {
		runStrategy = "workflow"
	} else if runStrategy == "" {
		runStrategy = "auto"
	}
	if locker, ok := e.historyStore.(session.ConversationLocker); ok && strings.TrimSpace(msg.SessionID) != "" {
		release, lockErr := locker.LockConversation(ctx, WorkspaceFromContext(ctx), msg.SessionID)
		if lockErr != nil {
			return message.Message{}, fmt.Errorf("serialize conversation: %w", lockErr)
		}
		defer release()
	}
	if metadata.DataClassification == "" {
		metadata.DataClassification = strings.TrimSpace(def.LLM.DataClassification)
		ctx = llm.WithCallMetadata(ctx, metadata)
	}
	if !def.Enabled {
		return message.Message{}, fmt.Errorf("engine: agent %q is disabled", msg.AgentID)
	}
	if def.ID == SystemAgentID && msg.Channel != "http" && msg.Channel != "internal" {
		return message.Message{}, fmt.Errorf("engine: system agent is only available on http/internal channel")
	}

	// Router short-circuit. Kind=="router" agents have no LLM loop — they
	// match the inbound text against def.Routes and forward to the first
	// matching peer via the existing agent__<id> peer-call path. See
	// docs/CHANNEL_DESIGN.md Q2. The peer's reply is returned verbatim.
	// Defined as a method so router-specific logic (rule matching, trace
	// events) lives alongside the dispatcher without bloating Handle.
	if def.Kind == "router" {
		return e.dispatchRouter(ctx, def, msg)
	}

	// Provider allowlist guard. Closes the "GUI dropdown fat-finger →
	// paid-API hit" class of failure: an agent that declares
	// `allowed_providers: [ollama]` can never dial out to Anthropic /
	// OpenAI / Gemini even if someone saves the wrong provider into the
	// agent's LLM config. Emit an error event so the trace shows the
	// actionable message ("provider 'anthropic' not in allowed_providers
	// [ollama]") instead of a downstream "401 credit balance" or "404
	// model not found" failure that looks like the model's fault.
	if !providerAllowed(def.LLM.AllowedProviders, def.LLM.Provider) {
		errMsg := fmt.Sprintf(
			"engine: llm provider %q not in allowed_providers %v for agent %q "+
				"(set llm.allowed_providers in SOUL.yaml to widen, or change "+
				"llm.provider to one already on the list)",
			def.LLM.Provider, def.LLM.AllowedProviders, msg.AgentID,
		)
		e.emit(ctx, message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"message":           errMsg,
				"reason":            "provider_not_allowed",
				"provider":          def.LLM.Provider,
				"allowed_providers": def.LLM.AllowedProviders,
			},
			Timestamp: time.Now().UTC(),
		})
		return message.Message{}, fmt.Errorf("%s", errMsg)
	}
	if !providerAllowed(def.LLM.AllowedModels, def.LLM.Model) {
		errMsg := fmt.Sprintf(
			"engine: llm model %q not in allowed_models %v for agent %q",
			def.LLM.Model, def.LLM.AllowedModels, msg.AgentID)
		e.emit(ctx, message.Event{
			Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"message": errMsg, "reason": "model_not_allowed",
				"model": def.LLM.Model, "allowed_models": def.LLM.AllowedModels,
			},
			Timestamp: time.Now().UTC(),
		})
		return message.Message{}, fmt.Errorf("%s", errMsg)
	}

	e.emit(ctx, message.Event{
		Type: "message.in", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: trimMessageForEvent(msg), Timestamp: time.Now().UTC(),
	})

	// E5 — Structured Workflow Scaffolding: if the agent declares a workflow,
	// delegate entirely to WorkflowExecutor instead of the free-form LLM loop.
	// The executor checkpoints each step and can resume after a crash.
	if def.Workflow != nil {
		we := NewWorkflowExecutor(*def.Workflow, e, e.checkpoints, e.log)
		// Collect the business-outcome verdict so an unmet contract can mark the
		// reply degraded below (P0-4/P0-6).
		var outcomeReport OutcomeReport
		wfCtx := WithOutcomeCollector(ctx, &outcomeReport)
		wfResult, wfErr := we.Run(wfCtx, msg, "")
		if wfErr != nil {
			e.emit(ctx, message.Event{
				Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "workflow", "error": wfErr.Error()},
				Timestamp: time.Now().UTC(),
			})
			return message.Message{}, fmt.Errorf("engine: workflow: %w", wfErr)
		}
		var replyText string
		if wfResult != nil {
			if err := json.Unmarshal(wfResult, &replyText); err != nil {
				// Not a plain JSON string — use the raw JSON as the reply text.
				replyText = string(wfResult)
			}
		}
		if replyText == "" {
			replyText = "(workflow completed)"
		}
		reply = message.Message{
			ID:          msg.ID,
			WorkspaceID: msg.WorkspaceID,
			SessionID:   msg.SessionID,
			AgentID:     msg.AgentID,
			Channel:     msg.Channel,
			ThreadID:    msg.ThreadID,
			Role:        message.RoleAssistant,
			Parts:       message.Text(replyText),
			CreatedAt:   time.Now().UTC(),
		}
		// A run whose business-outcome contract went unmet is NOT a clean run,
		// however cleanly its nodes executed. Marking it here means the
		// scheduler's degraded-delivery path labels it rather than presenting an
		// empty brief as a finished one (P0-6: confidence incorporates
		// completion contracts, not merely tool errors).
		if def.Outcome.HasAssertions() && !outcomeReport.Met {
			if reply.Metadata == nil {
				reply.Metadata = map[string]string{}
			}
			reply.Metadata[message.MetaReasoningDegraded] = "true"
			reply.Metadata[message.MetaOutcome] = outcomeReport.Outcome
			if outcomeReport.Summary != "" {
				reply.Metadata[message.MetaOutcomeSummary] = outcomeReport.Summary
			}
		}
		e.emit(ctx, message.Event{
			Type: "message.out", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: trimMessageForEvent(reply), Timestamp: time.Now().UTC(),
		})
		// Workflow agents bypass finalizeReply, so persist the episodic record
		// here too. The workflow IS the active feature, so it qualifies for the
		// auto-default (saves unless a brain_memory block opts out).
		e.writeEpisodic(ctx, def, msg.AgentID, flattenParts(msg.Parts), replyText, true)
		// Record the turn so follow-ups in this session carry conversation
		// context (flowHistoryTranscript reads it on the next run).
		e.recordWorkflowTurn(ctx, msg, replyText)
		runOutcome = "success"
		return reply, nil
	}

	// Retrieve or create session
	sess, err := e.getOrCreateSessionContext(ctx, WorkspaceFromContext(ctx), msg.SessionID, msg.AgentID)
	if err != nil {
		return message.Message{}, err
	}

	// PERF-1: mark the session as actively in use for the duration of this
	// Handle call so the eviction sweep never reclaims a mid-conversation
	// session. lastAccess was already bumped by getOrCreateSession.
	sess.mu.Lock()
	sess.inUse++
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.inUse--
		sess.lastAccess = time.Now().UTC()
		sess.mu.Unlock()
	}()

	// ── Passphrase gate ───────────────────────────────────────────────────────
	// Enforced in Go before the LLM is ever invoked. The model cannot bypass
	// this check regardless of prompt injection or instruction following.
	if sec := def.Security; sec != nil && sec.Passphrase != "" {
		sess.mu.Lock()
		verified := sess.PassphraseVerified
		sess.mu.Unlock()

		userText := flattenParts(msg.Parts)
		if !verified {
			if userText == sec.Passphrase {
				// Correct passphrase — mark session as verified and acknowledge.
				sess.mu.Lock()
				sess.PassphraseVerified = true
				sess.mu.Unlock()
				reply = message.Message{
					ID:          msg.ID + "-auth",
					WorkspaceID: msg.WorkspaceID,
					SessionID:   msg.SessionID,
					AgentID:     msg.AgentID,
					Channel:     msg.Channel,
					ThreadID:    msg.ThreadID,
					Role:        message.RoleAssistant,
					Parts:       message.Text("✅ Access granted. How can I help you?"),
					CreatedAt:   time.Now().UTC(),
				}
				return reply, nil
			}
			// Wrong or missing passphrase — challenge without invoking the LLM.
			prompt := sec.PassphrasePrompt
			if prompt == "" {
				prompt = "🔒 Please provide your access passphrase to continue."
			}
			reply = message.Message{
				ID:          msg.ID + "-auth",
				WorkspaceID: msg.WorkspaceID,
				SessionID:   msg.SessionID,
				AgentID:     msg.AgentID,
				Channel:     msg.Channel,
				ThreadID:    msg.ThreadID,
				Role:        message.RoleAssistant,
				Parts:       message.Text(prompt),
				CreatedAt:   time.Now().UTC(),
			}
			return reply, nil
		}
	}

	// Persist inbound message to memory
	if err := e.memory.Write(memory.Entry{
		WorkspaceID: WorkspaceFromContext(ctx),
		AgentID:     msg.AgentID, SessionID: msg.SessionID,
		Scope:   memory.ScopeSession,
		Content: fmt.Sprintf("[%s] %s", msg.Username, flattenParts(msg.Parts)),
	}); err != nil {
		e.log.Warn("memory write failed (inbound)", zap.String("agent", msg.AgentID), zap.Error(err))
	}

	// Prime the prefix cache for this Handle. Cleared on exit so a
	// hot-reload between user messages picks up the new def's catalogs.
	// (PRODUCTION_AUDIT → MED/Engine.)
	sysPrefix := e.buildSystemPrefix(ctx, def)
	if modePrompt := responseModeSystemPrompt(msg.Metadata); modePrompt != "" {
		sysPrefix += "\n\n" + modePrompt
	}
	rawGoal := flattenParts(msg.Parts)
	sess.mu.Lock()
	// S1 (Cohort F) — annotate inbound text from shared external
	// channels so the model treats the sender identity conservatively.
	// Web/HTTP + internal callers are the operator's own channel and
	// stay unannotated so their user experience is unchanged.
	userContent := annotateInboundForTrust(msg, rawGoal)
	e.appendHistoryLocked(sess, llm.ChatMessage{
		Role: "user", Content: userContent,
	})
	sess.cachedPrefix = sysPrefix
	// S3 (Cohort F) — record the operator's original goal (without the
	// S1 annotation header) so the intent gate can check whether an
	// injection-influenced tool call is justified by what the operator
	// actually asked for.
	sess.userGoal = rawGoal
	// A fresh user turn also resets the "last evidence" flag — the
	// operator has spoken again, so we start the trust window over.
	sess.lastEvidenceUntrusted = false
	sess.mu.Unlock()
	defer func() {
		sess.mu.Lock()
		sess.cachedPrefix = ""
		sess.mu.Unlock()
	}()

	// Story 16 — pluggable reasoning loops: agents that declare a reasoning:
	// block with a strategy run through the E15 reasoning Loop instead of the
	// classic tool loop below. Agents without one are untouched — the ok=false
	// branch falls straight through to the existing path.
	//
	// Guard: only enter the reasoning loop when a real backend exists for the
	// agent's effective provider. react/plan_execute only have backends for
	// Anthropic / OpenAI-compatible / Ollama; for everything else (e.g.
	// google/gemini) DefaultBackendFor would silently fall back to local Ollama
	// and fail. In that case we degrade to the classic tool loop, which works
	// with every provider.
	if loopCfg, ok := reasoning.LoopConfigFromDefinition(def, sysPrefix, e.providerSupportsNativeTools(def)); ok {
		if strings.TrimSpace(def.Reasoning.StepTimeout) == "" {
			loopCfg.StepTimeout = e.effectiveStepTimeout()
		}
		if strings.TrimSpace(def.Reasoning.TotalTimeout) == "" {
			loopCfg.TotalTimeout = e.effectiveRunTimeout()
		}
		if e.reasoningBackendAvailable(def) {
			reply, rerr := e.handleWithReasoning(ctx, def, sess, msg, loopCfg)
			if rerr == nil {
				runOutcome = "success"
			}
			return reply, rerr
		}
		e.log.Warn("reasoning strategy requested but no backend for the agent's provider; using classic tool loop instead",
			zap.String("agent", def.ID),
			zap.String("strategy", def.Reasoning.Strategy),
			zap.String("provider", e.effectiveProvider(def)))
		// fall through to the classic loop below
	}

	// Build context messages
	chatMsgs := e.buildContext(ctx, def, sess, msg)

	// Build tool schemas for this agent (Python tools + opt-in Go built-ins).
	// Pass the inbound channel so system tools are gated to HTTP-only.
	tools := e.allToolSchemasForContext(ctx, def, msg.Channel)
	packageInstallReq, forcePackageInstall := parseURLPackageInstallRequest(flattenParts(msg.Parts))
	forcePackageInstall = forcePackageInstall && def.ID == SystemAgentID &&
		toolSchemaExists(tools, "package_install")

	// Auto-delegate: when SOUL.yaml sets `llm.tool_choice: agent__<id>` and
	// `<id>` is one of the declared peers, do the peer call HERE before the
	// LLM ever runs. Reason: local models (qwen2.5:72b in particular) routinely
	// ignore Ollama's tool_choice constraint and answer from training data
	// instead of delegating. We bypass model cooperation by running the peer
	// ourselves, then inject a synthetic assistant→tool round-trip into
	// chatMsgs so the model's first real turn sees the result in context and
	// just has to synthesise. The model genuinely believes it called the tool.
	autoDelegated := false
	if tc := strings.TrimSpace(def.LLM.ToolChoice); strings.HasPrefix(tc, AgentToolPrefix) {
		peerID := strings.TrimPrefix(tc, AgentToolPrefix)
		isPeer := false
		for _, p := range e.resolveAgentRefs(def.Agents, def.ID) {
			if p.ID == peerID {
				isPeer = true
				break
			}
		}
		if isPeer {
			userText := flattenParts(msg.Parts)
			peerArgs := map[string]any{"message": userText}
			e.emit(ctx, message.Event{
				Type: "tool.call", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload: message.ToolCall{
					ID: "auto-" + uuidShort(), Name: tc, Arguments: peerArgs,
				},
				Timestamp: time.Now().UTC(),
			})
			peerResp, perr := e.runAgentCall(ctx, def, tc, peerArgs)
			peerCallID := "auto-" + uuidShort()
			if perr != nil {
				e.log.Warn("auto-delegate failed; falling through to normal LLM loop",
					zap.String("agent", def.ID), zap.String("peer", peerID), zap.Error(perr))
			} else {
				autoDelegated = true
				// Synthetic assistant message recording the (forced) tool call
				// + the tool-role message carrying the peer's reply. The LLM
				// will see this as if it had decided to delegate on its own.
				chatMsgs = append(chatMsgs,
					llm.ChatMessage{
						Role: "assistant", Content: "",
						ToolCalls: []message.ToolCall{{ID: peerCallID, Name: tc, Arguments: peerArgs}},
					},
					llm.ChatMessage{
						Role: "tool", Content: peerResp, ToolCallID: peerCallID, Name: tc,
					},
				)
				e.emit(ctx, message.Event{
					Type: "tool.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
					Payload:   message.ToolResult{CallID: peerCallID, Name: tc, Content: peerResp},
					Timestamp: time.Now().UTC(),
				})
			}
		}
	}
	// Agentic loop: LLM → tool calls → LLM → … → final reply
	maxTurns := def.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}
	// S3.2 — clamp to the server-side ceiling so a misconfigured agent
	// (max_turns: 10000) can't self-authorise a runaway, cost-heavy loop.
	if ceiling := e.turnsCeiling(); maxTurns > ceiling {
		e.log.Warn("engine: clamping max_turns to server ceiling",
			zap.String("agent", msg.AgentID),
			zap.Int("requested", maxTurns),
			zap.Int("ceiling", ceiling))
		maxTurns = ceiling
	}

	// S3.1 — per-run token/cost budget. We accumulate token usage and call
	// count as the loop runs and check them BEFORE each LLM call, halting with
	// a terminal reply the moment the next call would exceed the cap. This is
	// the only thing standing between a runaway loop / prompt injection / deep
	// peer recursion and a surprise bill.
	budgetTokens, budgetCalls := e.effectiveRunBudget(def)
	var usedTokens, usedCalls int

	model := def.LLM.Model
	if model == "" {
		model = "(provider default)"
	}

	// Anti-loop guard: remember tool calls already executed in this run (keyed by
	// name + arguments). If the model re-issues an identical call, we return the
	// cached result with a nudge to move on instead of re-running it — this stops
	// weaker models from burning every turn re-calling the same tool.
	seen := make(map[string]string)
	var seenMu sync.Mutex

	// Per-tool repeat counter (loop guard for weak models). The exact-args dedup
	// above misses a model that RE-WORDS its arguments every turn (e.g. gemma
	// reissuing web_search with slightly different queries). When a non-stateful
	// tool is called more than repeatToolNudgeAt times, we inject a one-time
	// system steer telling the model it already has enough from that tool.
	toolNameCount := make(map[string]int)
	const repeatToolNudgeAt = 3

	var finalContent string
	continuingOutput := false
	outputLimitStillHit := false
	for turn := 0; turn < maxTurns; turn++ {
		// S3.1 — budget gate. Check BEFORE issuing the call so we never spend
		// past the cap. When exceeded we stop the loop and let the
		// final-synthesis / current finalContent path return what we have,
		// with a clear terminal note appended below.
		if reason := budgetExceeded(budgetTokens, usedTokens, budgetCalls, usedCalls); reason != "" {
			e.log.Warn("engine: run budget exceeded — halting",
				zap.String("agent", msg.AgentID),
				zap.String("reason", reason),
				zap.Int("used_tokens", usedTokens),
				zap.Int("budget_tokens", budgetTokens),
				zap.Int("used_calls", usedCalls),
				zap.Int("budget_calls", budgetCalls))
			metrics.AgentBudgetHaltsTotal.Inc()
			e.emit(ctx, message.Event{
				Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "budget", "reason": reason},
				Timestamp: time.Now().UTC(),
			})
			note := "\n\n⚠ Run halted: " + reason + ". Increase the agent's budget block or simplify the task."
			if strings.TrimSpace(finalContent) == "" {
				if partial := strings.TrimSpace(bestEffortFinal(chatMsgs)); partial != "" {
					finalContent = partial + note
				} else {
					finalContent = "I had to stop before finishing: " + reason + "."
				}
			} else {
				finalContent += note
			}
			break
		}
		// Enable streaming on the final-turn request when the agent opted in
		// AND the caller attached a token callback. We only stream when there
		// are no tools (streaming + tool calls requires careful merging that
		// providers handle differently; the non-streaming path is authoritative
		// for tool-call turns). Providers set resp.Stream = nil when they fall
		// through to the non-streaming code path, so the drain below is a no-op.
		streamCB := streamCallback(ctx)
		// Stream the final-turn reply when the agent opts in and there are no
		// tools on this turn. We no longer require a stream callback: even
		// without one (the plain /chat path), we now relay tokens to the event
		// sink as `assistant.delta` events so the web UI can render the answer
		// live over the existing /ws/events socket.
		requestTools := tools
		if continuingOutput {
			// A continuation is part of the same final answer. Do not let the
			// model start another tool cycle after it already began synthesizing.
			requestTools = nil
		}
		wantStream := def.StreamReply && len(requestTools) == 0

		// Story 4 / S5.1 — proactively keep the prompt within the model's
		// context window. Reserve room for the completion, then trim the oldest
		// non-system turns until the estimated prompt fits. This turns a
		// silent-overflow 400 into graceful, oldest-first truncation.
		ctxLimit := modelContextLimit(def.LLM.Provider, def.LLM.Model)
		reserveOut := def.LLM.MaxTokens
		if reserveOut <= 0 {
			reserveOut = 1024
		}
		// Reserve the estimated prompt plus maximum output before dialing the
		// provider. This prevents a run with one token remaining from starting a
		// large final request and overshooting its declared budget.
		if budgetTokens > 0 {
			promptTokens := estimateTokens(chatMsgs, requestTools)
			remainingOutput := budgetTokens - usedTokens - promptTokens
			if remainingOutput <= 0 {
				finalContent = strings.TrimSpace(bestEffortFinal(chatMsgs))
				if finalContent != "" {
					finalContent += "\n\n"
				}
				needed := usedTokens + promptTokens + reserveOut
				recommended := budgetTokens + budgetTokens/2
				if needed > recommended {
					recommended = needed
				}
				recommended = ((recommended + 9999) / 10000) * 10000
				ceiling := defaultMaxBudgetTokens
				if e.runBudgetConfigured {
					ceiling = e.maxRunBudget.MaxTokens
				}
				if ceiling > 0 && recommended > ceiling {
					recommended = ceiling
				}
				finalContent += fmt.Sprintf("⚠ Run paused before the next model call because its prompt no longer fits the run token budget. This run used about %d of %d tokens. Recommended next-run token budget: **%d** (LLM-call limit: %d; deployment ceiling: %d).", usedTokens, budgetTokens, recommended, budgetCalls, ceiling)
				metrics.AgentBudgetHaltsTotal.Inc()
				break
			}
			if reserveOut > remainingOutput {
				reserveOut = remainingOutput
			}
		}
		inputBudget := ctxLimit - reserveOut
		if trimmed, dropped := trimMessagesToFit(chatMsgs, requestTools, inputBudget); dropped > 0 {
			chatMsgs = trimmed
			e.log.Warn("engine: trimmed history to fit context window",
				zap.String("agent", msg.AgentID),
				zap.String("model", model),
				zap.Int("dropped_messages", dropped),
				zap.Int("input_budget_tokens", inputBudget))
		}

		req := llm.CompletionRequest{
			Model:            def.LLM.Model,
			Messages:         chatMsgs,
			Tools:            requestTools,
			Temperature:      def.LLM.Temperature,
			TopP:             def.LLM.TopP,
			MaxTokens:        reserveOut,
			Stream:           wantStream,
			ResponseFormat:   def.LLM.ResponseFormat,
			ReasoningEffort:  def.LLM.ReasoningEffort,
			PresencePenalty:  def.LLM.PresencePenalty,
			FrequencyPenalty: def.LLM.FrequencyPenalty,
		}
		// Tool-choice constraint applies ONLY on turn 1 AND only if we didn't
		// already auto-delegate above (otherwise we'd force the same tool a
		// second time after its result is already in context). After turn 1 the
		// model must be free to synthesise the final answer once tool results
		// have come back — leaving it forced would trap us in a tool-call loop.
		if turn == 0 && !autoDelegated && def.LLM.ToolChoice != "" && len(tools) > 0 {
			req.ToolChoice = def.LLM.ToolChoice
		}
		// The built-in System agent has a deterministic, typed URL installer.
		// Constrain explicit install-from-URL requests to that tool so weaker
		// OpenAI-compatible models cannot merely narrate "I should call
		// shell_exec" and finish with zero tool calls. The tool still travels
		// through the normal confirmation, intent, sandbox and audit pipeline.
		if turn == 0 && !autoDelegated && forcePackageInstall {
			req.ToolChoice = "package_install"
		}

		e.emit(ctx, message.Event{
			Type: "llm.call", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"provider": def.LLM.Provider, "model": model, "turn": turn + 1},
			Timestamp: time.Now().UTC(),
		})
		usedCalls++
		llmStart := time.Now()

		llmCtx, llmCancel := context.WithTimeout(ctx, e.effectiveLLMTimeout())
		resp, err := e.llmRouter.Complete(llmCtx, def.LLM.Provider, req)
		llmCancel()
		// Story 4 / S5.1 — reactive recovery: if the provider still rejects the
		// prompt as too large (our estimate was optimistic, or the model's real
		// window is smaller than our table), aggressively halve the non-system
		// history and retry ONCE before giving up.
		if err != nil && isContextExceededErr(err) && ctx.Err() == nil && (budgetCalls == 0 || usedCalls < budgetCalls) {
			if shrunk, dropped := trimMessagesToFit(chatMsgs, tools, estimateTokens(chatMsgs, tools)/2); dropped > 0 {
				e.log.Warn("engine: provider reported context exceeded — retrying with trimmed history",
					zap.String("agent", msg.AgentID), zap.Int("dropped_messages", dropped))
				chatMsgs = shrunk
				req.Messages = chatMsgs
				usedCalls++
				retryCtx, retryCancel := context.WithTimeout(ctx, e.effectiveLLMTimeout())
				resp, err = e.llmRouter.Complete(retryCtx, def.LLM.Provider, req)
				retryCancel()
			}
		}
		// Prometheus: per-call duration + outcome counter + token counts.
		// (PRODUCTION_AUDIT → MED/Observability)
		llmProviderLabel := def.LLM.Provider
		if llmProviderLabel == "" {
			llmProviderLabel = "(default)"
		}
		metrics.LLMCallDuration.WithLabelValues(llmProviderLabel, model).Observe(time.Since(llmStart).Seconds())
		if err != nil {
			metrics.LLMCallsTotal.WithLabelValues(llmProviderLabel, model, "error").Inc()
			// Story 3 — name the clock that fired. A bare "context deadline
			// exceeded" leaves the operator guessing whether it was the agent's
			// run_timeout, a tool timeout, or provider slowness. When the run
			// context is already done, the run_timeout (or shutdown) is the
			// cause, not the provider.
			outErr := fmt.Errorf("engine: llm call: %w", err)
			if ctxErr := ctx.Err(); ctxErr != nil {
				if errors.Is(ctxErr, context.Canceled) {
					outErr = fmt.Errorf("engine: run cancelled (shutdown or caller cancel) during llm call: %w", err)
				} else {
					outErr = fmt.Errorf("engine: agent run_timeout exceeded while waiting on the LLM provider %q (model %q); raise the agent's run_timeout or check provider latency: %w", llmProviderLabel, model, err)
				}
			} else if errors.Is(err, context.DeadlineExceeded) {
				outErr = fmt.Errorf("engine: llm timeout for provider %q model %q after %s: %w", llmProviderLabel, model, e.effectiveLLMTimeout(), err)
			}
			e.emit(ctx, message.Event{
				Type: "error", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "llm", "error": outErr.Error()},
				Timestamp: time.Now().UTC(),
			})
			return message.Message{}, outErr
		}
		metrics.LLMCallsTotal.WithLabelValues(llmProviderLabel, model, "success").Inc()
		// Drain the streaming channel, delivering tokens to the caller's
		// callback and accumulating them into resp.Content. If the provider
		// didn't open a stream (non-streaming path or tool-call turn),
		// resp.Stream is nil and this block is a no-op.
		if resp.Stream != nil {
			var sb strings.Builder
			for token := range resp.Stream {
				if streamCB != nil {
					streamCB(token)
				}
				// Relay each token to the event sink so the web UI can render
				// the reply as it arrives. Best-effort: the hub drops events for
				// slow clients, and the authoritative full reply is returned by
				// Handle regardless, so a dropped delta only affects the live
				// preview, never the final message.
				e.emit(ctx, message.Event{
					Type: "assistant.delta", AgentID: msg.AgentID, SessionID: msg.SessionID,
					Payload:   map[string]any{"text": token},
					Timestamp: time.Now().UTC(),
				})
				sb.WriteString(token)
			}
			resp.Content = sb.String()
		}
		// A few open-weight models advertise native tool calling but emit a
		// compact XML invocation in message content instead of the provider's
		// typed tool_calls field. Recover only a pure, whole-response invocation
		// of a tool that was actually offered. The recovered call then follows the
		// ordinary RBAC, intent, confirmation, isolation and audit path below.
		if len(resp.ToolCalls) == 0 {
			if recovered, ok := recoverXMLToolCalls(resp.Content, tools); ok {
				resp.Content = ""
				resp.ToolCalls = recovered
			} else if strings.HasPrefix(strings.TrimSpace(resp.Content), "<") {
				// Keep this visible in production: an XML-looking assistant reply is
				// usually a provider/model tool-call compatibility issue. Never log
				// the response body (it may contain user data); the offered names are
				// configuration metadata and make an allowlist mismatch diagnosable.
				offered := make([]string, 0, len(tools))
				for _, tool := range tools {
					offered = append(offered, tool.Name)
				}
				e.log.Warn("model emitted XML-like content that was not recoverable as an offered tool call",
					zap.String("agent", msg.AgentID),
					zap.Int("offered_tool_count", len(tools)),
					zap.Strings("offered_tools", offered))
			}
		}
		// Do not let provider quirks change the operator's install target or
		// package type. Some OpenAI-compatible models ignore tool_choice, call
		// fetch_url first, or send kind=auto even after the user explicitly says
		// "MCP server". Replace the first-turn decision with the request parsed
		// from the operator's own text. The ordinary dispatch path below still
		// performs RBAC, intent checks, confirmation, safety inspection and audit.
		if turn == 0 && !autoDelegated && forcePackageInstall {
			resp.Content = ""
			resp.ToolCalls = []message.ToolCall{{
				ID:   "package-install-" + uuidShort(),
				Name: "package_install",
				Arguments: map[string]any{
					"source_url": packageInstallReq.SourceURL,
					"kind":       packageInstallReq.Kind,
				},
			}}
		}
		// Usage for streams is final only after the provider channel closes.
		// Recording and run-budget accumulation therefore happen after draining.
		if resp.InputTokens > 0 {
			metrics.LLMInputTokens.WithLabelValues(llmProviderLabel, model).Add(float64(resp.InputTokens))
		}
		if resp.OutputTokens > 0 {
			metrics.LLMOutputTokens.WithLabelValues(llmProviderLabel, model).Add(float64(resp.OutputTokens))
		}
		e.recordUsage(ctx, msg.AgentID, msg.SessionID, llmProviderLabel, model,
			resp.InputTokens, resp.OutputTokens)
		usedTokens += resp.InputTokens + resp.OutputTokens + resp.ReasoningTokens + resp.ToolUsePromptTokens

		e.emit(ctx, message.Event{
			Type: "llm.result", AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload: map[string]any{
				"model":         model,
				"input_tokens":  resp.InputTokens,
				"output_tokens": resp.OutputTokens,
				"finish_reason": resp.FinishReason,
				"duration_ms":   time.Since(llmStart).Milliseconds(),
				"tool_calls":    len(resp.ToolCalls),
			},
			Timestamp: time.Now().UTC(),
		})

		// A provider can return HTTP 200 with a partial answer and a stop reason
		// such as "length"/"MAX_TOKENS". Treating that as a completed reply is
		// silent data loss. Continue in the same run, governed by the ordinary
		// max-turn, model-call, token, timeout, and cost ceilings above.
		if len(resp.ToolCalls) == 0 && outputLimitFinishReason(resp.FinishReason) && strings.TrimSpace(resp.Content) != "" {
			finalContent = joinOutputContinuation(finalContent, resp.Content)
			outputLimitStillHit = true
			continuingOutput = true
			chatMsgs = append(chatMsgs,
				llm.ChatMessage{Role: "assistant", Content: resp.Content},
				llm.ChatMessage{Role: "system", Content: "The previous answer hit the provider's per-call output limit. Continue exactly where it stopped. Do not repeat prior text, restart the answer, or call tools; finish the response."},
			)
			e.emit(ctx, message.Event{
				Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "llm", "reason": "output_limit", "action": "continuing", "finish_reason": resp.FinishReason},
				Timestamp: time.Now().UTC(),
			})
			continue
		}

		// No tool calls → we have a final answer
		if len(resp.ToolCalls) == 0 {
			finalContent = joinOutputContinuation(finalContent, resp.Content)
			outputLimitStillHit = false
			break
		}

		// Loop-breaker: if EVERY tool call this turn was already executed with the
		// same arguments, the model is stuck repeating itself (some models emit a
		// spurious tool call alongside their real answer every turn). Stop here and
		// use the model's text as the final answer instead of burning all turns.
		allDup := len(resp.ToolCalls) > 0
		for _, tc := range resp.ToolCalls {
			aj, _ := json.Marshal(tc.Arguments)
			name := normalizeToolCallName(tc.Name)

			// Stateful tools should not trigger the loop breaker since their
			// execution environment or underlying files may have changed.
			if name == "shell_exec" || name == "run_script" || name == "http_request" {
				allDup = false
				break
			}

			seenMu.Lock()
			_, ok := seen[name+"|"+string(aj)]
			seenMu.Unlock()
			if !ok {
				allDup = false
				break
			}
		}
		if allDup {
			// Model is repeating tools instead of answering. Stop the tool loop;
			// the post-loop synthesis step will force a plain-text final answer.
			finalContent = resp.Content // usually empty for these models
			break
		}

		// Execute each tool call
		toolResults := e.executeToolCalls(ctx, def, msg.SessionID, resp.ToolCalls, seen, &seenMu)

		// Loop guard: count repeats of each (non-stateful) tool across the run and,
		// the first time one crosses the threshold, steer the model off it. This
		// catches the "reworded same search 10 times" failure that the exact-args
		// dedup can't, without ever blocking a legitimately varied tool sequence.
		var nudges []string
		for _, tc := range resp.ToolCalls {
			name := normalizeToolCallName(tc.Name)
			if name == "shell_exec" || name == "run_script" || name == "http_request" {
				continue
			}
			toolNameCount[name]++
			if toolNameCount[name] == repeatToolNudgeAt+1 {
				nudges = append(nudges, fmt.Sprintf(
					"You have called %q %d times already and have enough from it. Do NOT call %q again — "+
						"use a DIFFERENT skill/tool to get the data you still need (e.g. read_skill for a specialized skill), "+
						"or write your final answer now from what you already have. Do not fabricate data you did not retrieve.",
					name, toolNameCount[name], name))
			}
		}

		// Append assistant + tool result turns for next loop iteration.
		// NOTE: release sess.mu BEFORE calling buildContext — buildContext locks
		// sess.mu itself, and Go mutexes are not reentrant, so holding it here
		// would deadlock the agent on its second turn (any tool-using agent).
		sess.mu.Lock()
		turns := []llm.ChatMessage{
			{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls},
		}
		for _, tr := range toolResults {
			turns = append(turns, llm.ChatMessage{
				Role: "tool", Content: tr.Content, ToolCallID: tr.CallID, Name: tr.Name,
			})
		}
		// A loop-guard steer (if any) rides along as a system turn so the model
		// sees it on the very next iteration.
		for _, n := range nudges {
			turns = append(turns, llm.ChatMessage{Role: "system", Content: n})
		}
		e.appendHistoryLocked(sess, turns...)
		sess.mu.Unlock()

		chatMsgs = e.buildContext(ctx, def, sess, msg) // rebuild with tool results
	}
	if outputLimitStillHit {
		finalContent += "\n\n⚠ The provider stopped at its per-call output limit, and this run reached its continuation limit before the answer finished. Increase the agent's max turns/output allowance or retry the response."
	}

	if strings.TrimSpace(finalContent) == "" {
		// The model kept calling tools and never produced a plain-text reply.
		// Force a tool-free synthesis from everything already gathered.
		finalContent = e.finalSynthesis(ctx, def, msg.AgentID, msg.SessionID, chatMsgs)
	} else if def.LLM.OutputSchema == nil && reasoning.IsProgressPreamble(finalContent) {
		// The model ended on a progress note ("I'll start by loading the cookies…")
		// instead of the actual deliverable — common when it runs out of turns
		// mid-plan. Force one tool-free synthesis so the user gets the finished
		// result built from everything already gathered, not an intent statement.
		if synth := strings.TrimSpace(e.finalSynthesis(ctx, def, msg.AgentID, msg.SessionID, chatMsgs)); synth != "" && !reasoning.IsProgressPreamble(synth) {
			finalContent = synth
		}
	}

	// Safety net: never surface leaked reasoning control JSON (thought/action/
	// is_done) as the reply, even on the classic (non-loop) path where a model
	// primed with a ReAct-style prompt emits its step object as text. Skipped
	// when a structured OutputSchema is declared — that JSON is intentional.
	if def.LLM.OutputSchema == nil && strings.TrimSpace(finalContent) != "" {
		finalContent = reasoning.SanitizeFinalOutput(finalContent, nil)
	}

	// Structured output enforcement: if the agent has an output_schema, validate
	// the final reply parses as JSON. On failure, do ONE corrective retry that
	// asks the model to fix its output (with response_format=json_schema). If
	// that still fails, we surface whatever we have — the caller can inspect.
	if def.LLM.OutputSchema != nil && strings.TrimSpace(finalContent) != "" {
		if _, perr := parseJSONLoose(finalContent); perr != nil {
			e.emit(ctx, message.Event{
				Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
				Payload:   map[string]any{"stage": "output-schema", "error": perr.Error(), "retry": true},
				Timestamp: time.Now().UTC(),
			})
			corrected := e.finalSynthesisStructured(ctx, def, msg.AgentID, msg.SessionID, chatMsgs, finalContent, perr)
			if corrected != "" {
				finalContent = corrected
			}
		}
	}

	if strings.TrimSpace(finalContent) == "" {
		// Synthesis still produced nothing usable. Rather than throw away a
		// completed run's work, fall back to the best content we already have:
		// the last substantive assistant message, else a concise digest of the
		// gathered tool results. Keep the warning event for observability but
		// make the reply useful to the user.
		finalContent = bestEffortFinal(chatMsgs)
		stage := "loop"
		level := "warn"
		errText := "synthesis empty; recovered best-effort final from context"
		if strings.TrimSpace(finalContent) == "" {
			finalContent = "(no final response produced)"
			level = "error"
			errText = "no final response produced after synthesis"
		}
		e.emit(ctx, message.Event{
			Type: level, AgentID: msg.AgentID, SessionID: msg.SessionID,
			Payload:   map[string]any{"stage": stage, "error": errText},
			Timestamp: time.Now().UTC(),
		})
	}

	reply = e.finalizeReply(ctx, def, sess, msg, finalContent)

	runOutcome = "success" // flips the deferred AgentRunsTotal counter from "error"
	return reply, nil
}

// flowHistoryMaxMsgs caps how many recent chat messages a workflow run pulls
// in for conversation continuity (~6 turns = 12 user/assistant messages).
const flowHistoryMaxMsgs = 12

// flowHistoryTranscript returns a compact "User:/Assistant:" transcript of the
// last maxMsgs messages for the session, used to give a workflow's entry agent
// the prior turns so follow-ups resolve without the user restating context.
// Empty when there's no history yet.
func (e *Engine) flowHistoryTranscript(ctx context.Context, sessionID, agentID string, maxMsgs int) string {
	if sessionID == "" {
		return ""
	}
	sess, err := e.getOrCreateSessionContext(ctx, WorkspaceFromContext(ctx), sessionID, agentID)
	if err != nil {
		e.log.Warn("workflow history restore failed", zap.String("session", sessionID), zap.Error(err))
		return ""
	}
	sess.mu.Lock()
	hist := sess.History
	if maxMsgs > 0 && len(hist) > maxMsgs {
		hist = hist[len(hist)-maxMsgs:]
	}
	cp := make([]llm.ChatMessage, len(hist))
	copy(cp, hist)
	sess.mu.Unlock()

	var b strings.Builder
	for _, m := range cp {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		} else if m.Role != "user" {
			continue // skip system/tool turns in the user-facing transcript
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// recordWorkflowTurn persists a workflow agent's user+assistant turn to the
// in-memory session history (so the next turn's flowHistoryTranscript sees it)
// and the durable conversation store. Workflow agents bypass finalizeReply, so
// without this they'd never accumulate conversational context.
func (e *Engine) recordWorkflowTurn(ctx context.Context, msg message.Message, replyText string) {
	userText := flattenParts(msg.Parts)
	sess := e.getOrCreateSessionInWorkspace(WorkspaceFromContext(ctx), msg.SessionID, msg.AgentID)
	sess.mu.Lock()
	e.appendHistoryLocked(sess,
		llm.ChatMessage{Role: "user", Content: userText},
		llm.ChatMessage{Role: "assistant", Content: replyText},
	)
	sess.mu.Unlock()

	if e.historyStore != nil {
		// The run's own tenant and requester. Conversation history is
		// user-private, so both travel with every turn: a turn stored without
		// them is one no scoped read will ever return.
		workspaceID, subject := WorkspaceFromContext(ctx), SubjectFromContext(ctx)
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			WorkspaceID: workspaceID, Subject: subject,
			SessionID: msg.SessionID, AgentID: msg.AgentID, Role: "user", Content: userText,
		}); err != nil {
			e.log.Warn("history store: append workflow user turn failed", zap.Error(err))
		}
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			WorkspaceID: workspaceID, Subject: subject,
			SessionID: msg.SessionID, AgentID: msg.AgentID, Role: "assistant", Content: replyText,
		}); err != nil {
			e.log.Warn("history store: append workflow assistant turn failed", zap.Error(err))
		}
	}
}

// writeEpisodic persists a task→reply pair as an episodic brain memory record.
// It fires when the brain store is wired AND episodic memory is "on" for the
// agent: either episodic is explicitly enabled, or NO brain_memory block was
// configured and the calling subsystem is active (featureActive) — the
// auto-default shared by reasoning loops (strategy set) and workflows (always
// active in the workflow branch). A no-op otherwise.
func (e *Engine) writeEpisodic(ctx context.Context, def *agent.Definition, agentID, taskInput, finalContent string, featureActive bool) {
	brain := e.brainStore(ctx)
	if brain == nil || def == nil {
		return
	}
	bm := def.BrainMemory
	noBrainCfg := !bm.Episodic.Enabled && !bm.Semantic.Enabled && !bm.Procedural.Enabled
	episodicOn := bm.Episodic.Enabled || (featureActive && noBrainCfg)
	if !episodicOn {
		return
	}
	rec := agentmemory.ResultToEpisodicRecord(agentID, taskInput, finalContent, nil)
	if err := brain.Write(rec); err != nil {
		e.log.Warn("brain memory write failed", zap.String("agent", agentID), zap.Error(err))
	}
}

// finalizeReply is the shared tail of a successful run — classic loop and
// reasoning loop (Story 16) both end here so the persistence contract stays
// identical: append the assistant turn to in-memory session history, persist
// to session memory, write the episodic brain record, build the reply,
// emit message.out, and append both turns to the durable history store.
func (e *Engine) finalizeReply(ctx context.Context, def *agent.Definition, sess *Session, msg message.Message, finalContent string) message.Message {
	// Last line of defense before persistence and channel delivery: no runtime
	// path should surface ReAct/control JSON or provider answer envelopes as the
	// assistant's visible reply. SanitizeFinalOutput preserves legitimate JSON
	// payloads and only unwraps known answer/control shapes.
	if def != nil && def.LLM.OutputSchema != nil {
		finalContent = reasoning.SanitizeControlOutput(finalContent, nil)
	} else {
		finalContent = reasoning.SanitizeFinalOutput(finalContent, nil)
	}
	spokenContent := ""
	if strings.EqualFold(strings.TrimSpace(msg.Metadata["response.mode"]), "voice") {
		finalContent, spokenContent = splitVoiceResponse(finalContent)
	}

	// Append any charts before writing ANY history surface. Previously the
	// live reply and durable conversation row contained the chart, but the
	// in-memory session history captured the pre-chart prose. Revisiting a
	// conversation through that cache therefore made a chart appear to vanish.
	finalContent = appendCollectedCharts(finalContent, chartSinkFrom(ctx))

	// Append final assistant response to the in-memory session history
	sess.mu.Lock()
	e.appendHistoryLocked(sess, llm.ChatMessage{
		Role: "assistant", Content: finalContent,
	})
	sess.mu.Unlock()

	// Persist reply to session memory
	if err := e.memory.Write(memory.Entry{
		WorkspaceID: WorkspaceFromContext(ctx),
		AgentID:     msg.AgentID, SessionID: msg.SessionID,
		Scope:   memory.ScopeSession,
		Content: fmt.Sprintf("[%s] %s", def.Name, finalContent),
	}); err != nil {
		e.log.Warn("memory write failed (reply)", zap.String("agent", msg.AgentID), zap.Error(err))
	}

	// RL-09: persist task + reply as an episodic brain memory record. The
	// reasoning loop's "feature active" signal is a configured strategy.
	e.writeEpisodic(ctx, def, msg.AgentID, flattenParts(msg.Parts), finalContent, def.Reasoning.Strategy != "")
	e.proposeLearning(ctx, def, msg, finalContent)

	reply := message.Message{
		ID:          msg.ID, // correlate reply to request
		WorkspaceID: msg.WorkspaceID,
		SessionID:   msg.SessionID,
		AgentID:     msg.AgentID,
		Channel:     msg.Channel,
		ThreadID:    msg.ThreadID,
		Role:        message.RoleAssistant,
		Parts:       message.Text(finalContent),
		CreatedAt:   time.Now().UTC(),
	}
	if spokenContent != "" {
		reply.Metadata = map[string]string{"response.spoken": spokenContent}
	}

	e.emit(ctx, message.Event{
		Type: "message.out", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: trimMessageForEvent(reply), Timestamp: time.Now().UTC(),
	})

	// Persist user + assistant turns to the conversation history store.
	if e.historyStore != nil {
		workspaceID, subject := WorkspaceFromContext(ctx), SubjectFromContext(ctx)
		userContent := flattenParts(msg.Parts)
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			WorkspaceID: workspaceID, Subject: subject,
			SessionID: msg.SessionID, AgentID: msg.AgentID,
			Role: "user", Content: userContent,
		}); err != nil {
			e.log.Warn("history store: append user turn failed", zap.Error(err))
		}
		if err := e.historyStore.Append(ctx, session.ConversationEntry{
			WorkspaceID: workspaceID, Subject: subject,
			SessionID: msg.SessionID, AgentID: msg.AgentID,
			Role: "assistant", Content: finalContent,
		}); err != nil {
			e.log.Warn("history store: append assistant turn failed", zap.Error(err))
		}
	}

	return reply
}

// splitVoiceResponse separates the model's one-call dual presentation. The
// display answer is persisted as normal conversation history; the spoken
// answer rides on reply metadata and is never added to the visible transcript.
// If a model ignores the envelope, callers safely fall back to speaking the
// ordinary answer through the existing Markdown sanitizer.
func splitVoiceResponse(content string) (display, spoken string) {
	const (
		spokenOpen   = "<spoken_response>"
		spokenClose  = "</spoken_response>"
		displayOpen  = "<display_response>"
		displayClose = "</display_response>"
	)
	raw := strings.TrimSpace(content)
	lower := strings.ToLower(raw)
	spokenStart := strings.Index(lower, spokenOpen)
	if spokenStart < 0 {
		return raw, ""
	}
	spokenStart += len(spokenOpen)
	spokenEndRel := strings.Index(lower[spokenStart:], spokenClose)
	if spokenEndRel < 0 {
		return raw, ""
	}
	spokenEnd := spokenStart + spokenEndRel
	spoken = strings.TrimSpace(raw[spokenStart:spokenEnd])

	displayStart := strings.Index(lower, displayOpen)
	if displayStart < 0 {
		return spoken, spoken
	}
	displayStart += len(displayOpen)
	displayEndRel := strings.Index(lower[displayStart:], displayClose)
	if displayEndRel < 0 {
		display = strings.TrimSpace(raw[displayStart:])
	} else {
		display = strings.TrimSpace(raw[displayStart : displayStart+displayEndRel])
	}
	if display == "" {
		display = spoken
	}
	return display, spoken
}

// finalSynthesis makes one LLM call with NO tools, forcing the model to produce
// a plain-text answer from the context it already gathered. Used when a model
// won't stop emitting tool calls on its own (common with local/Ollama models).
func (e *Engine) finalSynthesis(ctx context.Context, def *agent.Definition, agentID, sessionID string, chatMsgs []llm.ChatMessage) string {
	model := def.LLM.Model
	if model == "" {
		model = "(provider default)"
	}
	// Pre-size to exact capacity so the trailing append doesn't trigger a
	// second re-allocation/copy on long conversations. (PRODUCTION_AUDIT
	// → MED/Engine — minor but free.)
	msgs := make([]llm.ChatMessage, 0, len(chatMsgs)+1)
	msgs = append(msgs, chatMsgs...)
	msgs = append(msgs, llm.ChatMessage{
		Role:    "system",
		Content: "Now write your final response for the user using the information already gathered above. Do NOT call any tools — reply with plain text only.",
	})

	e.emit(ctx, message.Event{
		Type: "llm.call", AgentID: agentID, SessionID: sessionID,
		Payload:   map[string]any{"provider": def.LLM.Provider, "model": model, "turn": "final-synthesis"},
		Timestamp: time.Now().UTC(),
	})
	start := time.Now()
	resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, llm.CompletionRequest{
		Model:            def.LLM.Model,
		Messages:         msgs,
		Temperature:      def.LLM.Temperature,
		TopP:             def.LLM.TopP,
		MaxTokens:        def.LLM.MaxTokens,
		ReasoningEffort:  def.LLM.ReasoningEffort,
		PresencePenalty:  def.LLM.PresencePenalty,
		FrequencyPenalty: def.LLM.FrequencyPenalty,
		// Tools intentionally omitted so the model must answer in text.
	})
	if err != nil {
		e.emit(ctx, message.Event{
			Type: "error", AgentID: agentID, SessionID: sessionID,
			Payload:   map[string]any{"stage": "final-synthesis", "error": err.Error()},
			Timestamp: time.Now().UTC(),
		})
		return ""
	}
	e.emit(ctx, message.Event{
		Type: "llm.result", AgentID: agentID, SessionID: sessionID,
		Payload: map[string]any{
			"model": model, "input_tokens": resp.InputTokens, "output_tokens": resp.OutputTokens,
			"duration_ms": time.Since(start).Milliseconds(), "turn": "final-synthesis",
		},
		Timestamp: time.Now().UTC(),
	})

	// Recovery: a reasoning model (qwen3, deepseek-r1) can burn its whole
	// turn inside a <think> block and hand back EMPTY content even though it
	// produced hundreds of tokens. The provider parser now surfaces post-think
	// text, but if content is STILL empty do ONE retry with an explicit,
	// think-discouraging instruction so a completed run is never discarded.
	if strings.TrimSpace(resp.Content) == "" && !shouldRetryEmptySynthesis(resp) {
		e.emit(ctx, message.Event{
			Type: "warn", AgentID: agentID, SessionID: sessionID,
			Payload: map[string]any{
				"stage": "final-synthesis", "error": "empty content after a large reasoning-only response",
				"retry": false, "recovery": "best-effort context fallback",
			},
			Timestamp: time.Now().UTC(),
		})
		return ""
	}
	if strings.TrimSpace(resp.Content) == "" {
		e.emit(ctx, message.Event{
			Type: "warn", AgentID: agentID, SessionID: sessionID,
			Payload:   map[string]any{"stage": "final-synthesis", "error": "empty content", "retry": true},
			Timestamp: time.Now().UTC(),
		})
		retryMsgs := make([]llm.ChatMessage, 0, len(chatMsgs)+1)
		retryMsgs = append(retryMsgs, chatMsgs...)
		retryMsgs = append(retryMsgs, llm.ChatMessage{
			Role: "system",
			Content: "Output ONLY the final report as plain text. Do not call tools. " +
				"Do not include any <think> blocks or analysis — write the user-facing answer now.",
		})
		retryResp, retryErr := e.llmRouter.Complete(ctx, def.LLM.Provider, llm.CompletionRequest{
			Model:            def.LLM.Model,
			Messages:         retryMsgs,
			Temperature:      def.LLM.Temperature,
			TopP:             def.LLM.TopP,
			MaxTokens:        def.LLM.MaxTokens,
			ReasoningEffort:  def.LLM.ReasoningEffort,
			PresencePenalty:  def.LLM.PresencePenalty,
			FrequencyPenalty: def.LLM.FrequencyPenalty,
			// Tools still omitted so the model must answer in text.
		})
		if retryErr == nil && strings.TrimSpace(retryResp.Content) != "" {
			return retryResp.Content
		}
	}
	return resp.Content
}

// finalSynthesisStructured re-runs the synthesis with the agent's output schema
// enforced via the provider's native JSON-mode. Called once when the first
// pass produced text that didn't parse as JSON.
func (e *Engine) finalSynthesisStructured(ctx context.Context, def *agent.Definition, agentID, sessionID string, chatMsgs []llm.ChatMessage, previous string, perr error) string {
	model := def.LLM.Model
	if model == "" {
		model = "(provider default)"
	}
	corrective := fmt.Sprintf(
		"Your previous response was not valid JSON matching the required schema "+
			"(error: %s). Re-emit your final answer as a single JSON value that "+
			"validates against the schema. Do NOT include any prose, code fences, "+
			"or explanation — return ONLY the JSON value.\n\nPrevious output:\n%s",
		perr.Error(), previous,
	)
	msgs := make([]llm.ChatMessage, 0, len(chatMsgs)+1)
	msgs = append(msgs, chatMsgs...)
	msgs = append(msgs, llm.ChatMessage{Role: "system", Content: corrective})

	e.emit(ctx, message.Event{
		Type: "llm.call", AgentID: agentID, SessionID: sessionID,
		Payload:   map[string]any{"provider": def.LLM.Provider, "model": model, "turn": "structured-retry"},
		Timestamp: time.Now().UTC(),
	})
	resp, err := e.llmRouter.Complete(ctx, def.LLM.Provider, llm.CompletionRequest{
		Model:            def.LLM.Model,
		Messages:         msgs,
		Temperature:      def.LLM.Temperature,
		TopP:             def.LLM.TopP,
		MaxTokens:        def.LLM.MaxTokens,
		ResponseFormat:   "json_schema",
		JSONSchema:       def.LLM.OutputSchema,
		ReasoningEffort:  def.LLM.ReasoningEffort,
		PresencePenalty:  def.LLM.PresencePenalty,
		FrequencyPenalty: def.LLM.FrequencyPenalty,
	})
	if err != nil {
		e.emit(ctx, message.Event{
			Type: "error", AgentID: agentID, SessionID: sessionID,
			Payload:   map[string]any{"stage": "structured-retry", "error": err.Error()},
			Timestamp: time.Now().UTC(),
		})
		return ""
	}
	return strings.TrimSpace(resp.Content)
}

// parseJSONLoose accepts either a bare JSON value or one wrapped in ```json ... ```
// code fences (a common LLM habit). Returns the parsed value (or an error if
// neither shape parses).
func parseJSONLoose(s string) (any, error) {
	s = strings.TrimSpace(s)
	// Strip surrounding code fences if present.
	if strings.HasPrefix(s, "```") {
		// Remove the leading ``` (and optional "json" tag) and the trailing ```.
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	// Try to find the first { or [ — handles prefatory chatter the model leaks.
	first := -1
	for i, r := range s {
		if r == '{' || r == '[' {
			first = i
			break
		}
	}
	if first > 0 {
		s = s[first:]
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return v, nil
}
