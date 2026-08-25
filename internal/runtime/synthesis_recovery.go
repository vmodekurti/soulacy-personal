package runtime

import (
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
)

// bestEffortFinal recovers a usable reply from the conversation context when
// every synthesis attempt yielded empty content. It prefers the last
// substantive assistant message, then falls back to a digest of tool results.
func bestEffortFinal(chatMsgs []llm.ChatMessage) string {
	for i := len(chatMsgs) - 1; i >= 0; i-- {
		m := chatMsgs[i]
		if m.Role == "assistant" {
			if c := strings.TrimSpace(m.Content); c != "" {
				return c
			}
		}
	}

	const maxToolResults = 6
	const maxPerResult = 1200
	var collected []string
	for i := len(chatMsgs) - 1; i >= 0 && len(collected) < maxToolResults; i-- {
		m := chatMsgs[i]
		if m.Role != "tool" {
			continue
		}
		c := strings.TrimSpace(m.Content)
		if c == "" {
			continue
		}
		if len(c) > maxPerResult {
			c = c[:maxPerResult] + "…"
		}
		label := m.Name
		if label == "" {
			label = "result"
		}
		collected = append(collected, "- "+label+": "+c)
	}
	if len(collected) == 0 {
		return ""
	}
	for l, r := 0, len(collected)-1; l < r; l, r = l+1, r-1 {
		collected[l], collected[r] = collected[r], collected[l]
	}
	return "Based on the information gathered:\n\n" + strings.Join(collected, "\n")
}

// shouldRetryEmptySynthesis distinguishes a cheap compatibility glitch from a
// model that spent a full generation reasoning without a user-facing answer.
func shouldRetryEmptySynthesis(resp *llm.CompletionResponse) bool {
	if resp == nil || strings.TrimSpace(resp.Content) != "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(resp.FinishReason), "length") {
		return false
	}
	const largeReasoningOnlyGeneration = 2048
	return resp.OutputTokens <= largeReasoningOnlyGeneration && resp.ReasoningTokens <= largeReasoningOnlyGeneration
}
