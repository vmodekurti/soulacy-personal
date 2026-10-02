package runtime

import (
	"context"
	"time"

	"github.com/soulacy/soulacy/internal/taskcontract"
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
