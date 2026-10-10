package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/autopilot"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/taskcontract"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
)

const maxMissionCompletionRetries = 2

type missionProgress struct {
	mu    sync.Mutex
	tools []autopilot.ToolUse
}

func attachMissionProgress(ctx context.Context, contract *agent.MissionContract) (context.Context, *missionProgress) {
	progress := &missionProgress{}
	if contract == nil || !contract.HasChecks() {
		return ctx, progress
	}
	previous := toolObserverFrom(ctx)
	ctx = WithToolObserver(ctx, func(call message.ToolCall, result string, failed bool) {
		status := "succeeded"
		if failed {
			status = "failed"
		}
		progress.mu.Lock()
		progress.tools = append(progress.tools, autopilot.ToolUse{
			Name:   normalizeToolCallName(call.Name),
			CallID: call.ID,
			Status: status,
		})
		progress.mu.Unlock()
		if previous != nil {
			previous(call, result, failed)
		}
	})
	return ctx, progress
}

func (p *missionProgress) evaluate(contract *agent.MissionContract, output string) autopilot.MissionEvaluation {
	if p == nil {
		return autopilot.EvaluateMission(contract, autopilot.Observation{Output: output})
	}
	p.mu.Lock()
	tools := append([]autopilot.ToolUse(nil), p.tools...)
	p.mu.Unlock()
	return autopilot.EvaluateMission(contract, autopilot.Observation{Output: output, Tools: tools})
}

func failedMissionSummary(evaluation autopilot.MissionEvaluation) string {
	missing := make([]string, 0, len(evaluation.Checks))
	for _, check := range evaluation.Checks {
		if check.Status != autopilot.CheckFail {
			continue
		}
		label := strings.TrimSpace(check.Description)
		if label == "" {
			label = strings.TrimSpace(check.ID)
		}
		if label == "" {
			label = string(check.Type)
		}
		if detail := strings.TrimSpace(check.Detail); detail != "" {
			label += ": " + detail
		}
		missing = append(missing, label)
	}
	return strings.Join(missing, "; ")
}

func missingRequiredMissionTool(evaluation autopilot.MissionEvaluation, availableTools []string) string {
	available := make(map[string]string, len(availableTools))
	for _, name := range availableTools {
		available[normalizeToolCallName(name)] = name
	}
	for _, check := range evaluation.Checks {
		if check.Type != agent.MissionCheckRequiredTool || check.Status != autopilot.CheckFail {
			continue
		}
		if name, ok := available[normalizeToolCallName(check.Expected)]; ok {
			return name
		}
	}
	return ""
}

func applyNextMissionToolChoice(req *llm.CompletionRequest, choice *string, toolCount int) {
	if req == nil || choice == nil || *choice == "" || toolCount == 0 {
		return
	}
	req.ToolChoice, *choice = *choice, ""
}

func missionRepairDirective(summary string, attempt int) string {
	return fmt.Sprintf(`The proposed final answer does not yet satisfy the mission contract. Missing: %s.

Continue the run now. Re-plan from the original goal, use a different approved route when an earlier route failed, and use the available tools to produce and verify the missing result. Do not claim success or invent evidence. This is completion repair attempt %d of %d.`, summary, attempt, maxMissionCompletionRetries)
}

func isBuilderMission(contract *agent.MissionContract) bool {
	return contract != nil && strings.HasPrefix(strings.TrimSpace(contract.ID), "builder-")
}

func (e *Engine) repairIncompleteBuilderMission(
	def *agent.Definition,
	mission *agent.MissionContract,
	sess *Session,
	msg message.Message,
	content string,
	progress *missionProgress,
	retries *int,
	availableTools []string,
	turn, maxTurns int,
) ([]llm.ChatMessage, string, bool) {
	if !isBuilderMission(mission) || *retries >= maxMissionCompletionRetries || turn+1 >= maxTurns {
		return nil, "", false
	}
	evaluation := progress.evaluate(mission, content)
	summary := failedMissionSummary(evaluation)
	if summary == "" {
		return nil, "", false
	}
	requiredTool := missingRequiredMissionTool(evaluation, availableTools)
	*retries++
	e.sink.Emit(message.Event{
		Type: "warn", AgentID: msg.AgentID, SessionID: msg.SessionID,
		Payload: map[string]any{"stage": "mission-completion", "missing": summary, "retry": *retries},
	})
	turns := make([]llm.ChatMessage, 0, 2)
	if strings.TrimSpace(content) != "" {
		turns = append(turns, llm.ChatMessage{Role: "assistant", Content: content})
	}
	turns = append(turns, llm.ChatMessage{Role: "system", Content: missionRepairDirective(summary, *retries)})
	sess.mu.Lock()
	e.appendHistoryLocked(sess, turns...)
	sess.mu.Unlock()
	return e.buildContext(def, sess, msg), requiredTool, true
}

func builderMissionFailureSummary(mission *agent.MissionContract, progress *missionProgress, output string) string {
	if !isBuilderMission(mission) {
		return ""
	}
	return failedMissionSummary(progress.evaluate(mission, output))
}

func (e *Engine) finalizeBuilderMissionReply(
	ctx context.Context,
	def *agent.Definition,
	mission *agent.MissionContract,
	sess *Session,
	msg message.Message,
	contract *taskcontract.Contract,
	progress *missionProgress,
	content string,
) (message.Message, bool) {
	summary := builderMissionFailureSummary(mission, progress, content)
	if summary == "" {
		return e.finalizeReply(ctx, def, sess, msg, content), false
	}
	contract.MarkBlocked("mission acceptance checks failed: " + summary)
	e.writeMissionCompletionLesson(def, msg.AgentID, flattenParts(msg.Parts), summary)
	if strings.TrimSpace(content) != "" {
		content = strings.TrimSpace(content) + "\n\nThis run is incomplete. I could not verify: " + summary + "."
	}
	reply := e.finalizeReply(ctx, def, sess, msg, content)
	if reply.Metadata == nil {
		reply.Metadata = map[string]string{}
	}
	reply.Metadata[message.MetaReasoningDegraded] = "true"
	reply.Metadata[message.MetaOutcome] = "partial"
	reply.Metadata[message.MetaOutcomeSummary] = summary
	return reply, true
}

func (e *Engine) writeMissionCompletionLesson(def *agent.Definition, agentID, taskInput, summary string) {
	if e.brainStore == nil || def == nil || strings.TrimSpace(summary) == "" {
		return
	}
	bm := def.BrainMemory
	noBrainCfg := !bm.Episodic.Enabled && !bm.Semantic.Enabled && !bm.Procedural.Enabled
	if !bm.Episodic.Enabled && !noBrainCfg {
		return
	}
	content := "The previous run did not complete its goal. Missing verification: " + summary +
		". On the next run, check these conditions before finalizing and use a different approved route if the first route fails."
	record := agentmemory.ResultToEpisodicRecord(agentID, taskInput, content,
		[]string{"completion_failure", "repair"})
	if err := e.brainStore.Write(record); err != nil {
		e.log.Warn("mission completion lesson write failed", zap.String("agent", agentID), zap.Error(err))
	}
}
