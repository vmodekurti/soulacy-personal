package runtime

import (
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/internal/taskcontract"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func completeTaskContract(contract *taskcontract.Contract, reply *message.Message, runErr error, runOutcome string) (taskcontract.Snapshot, string, bool, bool) {
	degraded := reply.Metadata != nil && strings.EqualFold(reply.Metadata[message.MetaReasoningDegraded], "true")
	snapshot := contract.Complete(runErr, runOutcome == "success", degraded, time.Now().UTC())
	if reply.Metadata == nil {
		reply.Metadata = map[string]string{}
	}
	reply.Metadata[message.MetaTaskState] = snapshot.State
	reply.Metadata[message.MetaTaskOutcome] = snapshot.Outcome
	if snapshot.Blocker != "" {
		reply.Metadata[message.MetaTaskBlocker] = snapshot.Blocker
	}
	recordedOutcome := runOutcome
	if snapshot.State == "incomplete" || snapshot.State == "blocked" || snapshot.State == "waiting_for_input" {
		recordedOutcome = "incomplete"
	}
	success := runErr == nil && runOutcome == "success" && !degraded &&
		snapshot.Outcome != taskcontract.OutcomeBlocked &&
		snapshot.Outcome != taskcontract.OutcomeIncomplete &&
		snapshot.Outcome != taskcontract.OutcomeNeedsInput &&
		snapshot.Outcome != taskcontract.OutcomeFailed
	return snapshot, recordedOutcome, success, degraded
}

func (e *Engine) continuePrematureFinal(def *agent.Definition, sess *Session, msg message.Message, contract *taskcontract.Contract, content string, allow bool, turn, maxTurns int) ([]llm.ChatMessage, bool) {
	if !allow {
		return nil, false
	}
	empty := strings.TrimSpace(content) == ""
	if !empty && (def.LLM.OutputSchema != nil || (!reasoning.IsProgressPreamble(content) && !reasoning.IsInternalScratchNarration(content))) {
		return nil, false
	}
	if turn+1 >= maxTurns {
		if !empty {
			contract.MarkIncomplete("the agent ended by describing work it had not performed")
		}
		return nil, false
	}
	directive := "That response describes unfinished work. Continue the current run now. Use the available tools for the next concrete action. Return a final answer only after the work is complete, or report a specific blocker if it cannot be completed."
	turns := make([]llm.ChatMessage, 0, 2)
	if empty {
		directive = "You returned no answer and the current run is not complete. Continue the same run now. Use the available tools for the next concrete action. Return a final answer only after the goal is complete, or report a specific blocker if no approved route can finish it."
	} else {
		turns = append(turns, llm.ChatMessage{Role: "assistant", Content: content})
	}
	turns = append(turns, llm.ChatMessage{Role: "system", Content: directive})
	sess.mu.Lock()
	e.appendHistoryLocked(sess, turns...)
	sess.mu.Unlock()
	return e.buildContext(def, sess, msg), true
}

func finishPrematureContent(def *agent.Definition, contract *taskcontract.Contract, content string) string {
	if def.LLM.OutputSchema != nil || (!reasoning.IsProgressPreamble(content) && !reasoning.IsInternalScratchNarration(content)) {
		return content
	}
	contract.MarkIncomplete("the agent ended by describing work it had not performed")
	return strings.TrimSpace(content) + "\n\nThis run is incomplete. The agent described additional work but did not perform it before the run ended."
}

func markIncompleteReplyMetadata(reply *message.Message, contract *taskcontract.Contract) {
	snapshot := contract.Snapshot()
	if snapshot.Outcome != taskcontract.OutcomeIncomplete {
		return
	}
	if reply.Metadata == nil {
		reply.Metadata = map[string]string{}
	}
	reply.Metadata[message.MetaReasoningDegraded] = "true"
	reply.Metadata[message.MetaOutcome] = "incomplete"
	reply.Metadata[message.MetaOutcomeSummary] = snapshot.Blocker
}
