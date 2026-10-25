package runtime

import (
	"strings"

	"github.com/soulacy/soulacy/pkg/message"
)

const agensiTransientReceipt = "[Agensi skill loaded for this run. Licensed skill instructions were intentionally not retained.]"

// Agensi skill bodies are licensed, run-scoped instructions. Soulacy may pass
// the body to the active model call, but it must not copy it into telemetry,
// learning memory, durable run evidence, or conversation archives.
func isTransientAgensiSkillResult(toolName string) bool {
	return strings.EqualFold(strings.TrimSpace(toolName), "mcp__agensi__get_skill")
}

func agensiObservableResult(call message.ToolCall, result string, failed bool) string {
	if failed || !isTransientAgensiSkillResult(call.Name) {
		return result
	}
	return agensiTransientReceipt
}

// redactAgensiSkillHistory is called when a Handle invocation finishes. The
// active run keeps the full body in memory long enough to follow the skill;
// subsequent turns and session eviction see only this receipt.
func redactAgensiSkillHistory(sess *Session) {
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for i := range sess.History {
		entry := &sess.History[i]
		if entry.Role == "tool" && isTransientAgensiSkillResult(entry.Name) {
			entry.Content = agensiTransientReceipt
		}
	}
}

func redactAgensiToolResultForEvent(result message.ToolResult) message.ToolResult {
	if !result.IsError && isTransientAgensiSkillResult(result.Name) {
		result.Content = agensiTransientReceipt
	}
	return result
}
