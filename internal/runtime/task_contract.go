package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/taskcontract"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type taskContractKey struct{}
type taskContractCollectorKey struct{}

func withTaskContract(ctx context.Context, contract *taskcontract.Contract) context.Context {
	if contract == nil {
		return ctx
	}
	return context.WithValue(ctx, taskContractKey{}, contract)
}

func taskContractFrom(ctx context.Context) *taskcontract.Contract {
	contract, _ := ctx.Value(taskContractKey{}).(*taskcontract.Contract)
	return contract
}

func withTaskContractCollector(ctx context.Context, snapshot *taskcontract.Snapshot) context.Context {
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, taskContractCollectorKey{}, snapshot)
}

func taskContractCollectorFrom(ctx context.Context) *taskcontract.Snapshot {
	snapshot, _ := ctx.Value(taskContractCollectorKey{}).(*taskcontract.Snapshot)
	return snapshot
}

func defineScheduledTaskContract(contract *taskcontract.Contract, def *agent.Definition, mission *agent.MissionContract, msg message.Message) {
	if contract == nil || def == nil || msg.Metadata == nil || strings.TrimSpace(msg.Metadata["trigger"]) == "" {
		return
	}
	goal := ""
	criteria := make([]string, 0, 8)
	if mission != nil {
		goal = strings.TrimSpace(mission.Goal)
		for _, check := range mission.Acceptance {
			criterion := strings.TrimSpace(check.Description)
			if criterion == "" {
				criterion = strings.TrimSpace(string(check.Type) + " " + check.Value + " " + check.Tool)
			}
			criteria = append(criteria, criterion)
		}
	}
	if def.Reasoning.Contract != nil {
		if goal == "" {
			goal = strings.TrimSpace(def.Reasoning.Contract.Goal)
		}
		criteria = append(criteria, def.Reasoning.Contract.CompletionCriteria)
	}
	if def.Outcome != nil {
		if goal == "" {
			goal = strings.TrimSpace(def.Outcome.Goal)
		}
		for _, assertion := range def.Outcome.Assertions {
			criteria = append(criteria, assertion.Describe)
		}
	}
	if goal == "" {
		goal = strings.TrimSpace(def.Description)
	}
	if goal == "" {
		goal = strings.TrimSpace(def.SystemPrompt)
	}
	contract.Define(goal, criteria)
}

func (e *Engine) observeTaskTool(ctx context.Context, agentID, sessionID string, call message.ToolCall, result string, failed bool) {
	contract := taskContractFrom(ctx)
	if contract == nil {
		return
	}
	before := contract.Snapshot().Replans
	contract.ObserveTool(call, result, failed)
	after := contract.Snapshot()
	if failed && after.Replans > before && e.sink != nil {
		e.sink.Emit(message.Event{
			Type: "task.contract.replanning", AgentID: agentID, SessionID: sessionID,
			Payload: after, Timestamp: time.Now().UTC(),
		})
	}
}
