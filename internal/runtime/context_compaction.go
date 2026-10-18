package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/trust"
)

// compactMessagesToFit reduces old evidence before discarding turns. This is
// extractive (no invented summary facts and no additional paid model call).
// The original messages remain in session history; only provider input changes.
// Keep the initial goal, latest instruction, and recent tool exchanges verbatim.
func compactMessagesToFit(msgs []llm.ChatMessage, tools []llm.ToolSchema, budget int) ([]llm.ChatMessage, int) {
	if budget <= 0 || estimateTokens(msgs, tools) <= budget {
		return msgs, 0
	}
	immutableEnd := 0
	for immutableEnd < len(msgs) && msgs[immutableEnd].Role == "system" {
		immutableEnd++
	}
	// If instructions and schemas alone exceed this estimated window, dropping
	// the task/evidence cannot make it fit. Preserve them and let the provider
	// report its actual limit rather than silently send a different task.
	if estimateTokens(msgs[:immutableEnd], tools) >= budget {
		return msgs, 0
	}
	// One oversized tool result can exceed the entire input window. Excerpt
	// that result before choosing history to retain, otherwise no intact tail
	// fits and the fallback discards the user's goal along with all evidence.
	shortened := append([]llm.ChatMessage(nil), msgs...)
	clipped := 0
	for i, m := range shortened {
		if m.Role == "tool" && estimateTokens([]llm.ChatMessage{m}, nil) > budget/2 {
			excerpt := evidenceExcerpt(m.Content, max(128, budget/4))
			if excerpt != m.Content {
				shortened[i].Content = excerpt
				clipped++
			}
		}
	}
	if clipped > 0 {
		out, dropped := compactMessagesToFit(shortened, tools, budget)
		return out, clipped + dropped
	}
	sysEnd := 0
	for sysEnd < len(msgs) && msgs[sysEnd].Role == "system" {
		sysEnd++
	}
	firstUser, lastUser := -1, -1
	for i := sysEnd; i < len(msgs); i++ {
		if msgs[i].Role == "user" {
			if firstUser < 0 {
				firstUser = i
			}
			lastUser = i
		}
	}
	for keep := min(8, len(msgs)-sysEnd); keep >= 1; keep-- {
		cut := len(msgs) - keep
		// Never retain a tool response without the corresponding assistant call.
		for cut > sysEnd && msgs[cut].Role == "tool" {
			cut--
		}
		if cut <= sysEnd {
			continue
		}
		base := append([]llm.ChatMessage(nil), msgs[:sysEnd]...)
		for _, i := range []int{firstUser, lastUser} {
			if i >= sysEnd && i < cut && (i != lastUser || i != firstUser) {
				base = append(base, msgs[i])
			}
		}
		if firstUser == lastUser && firstUser >= sysEnd && firstUser < cut {
			base = append(base, msgs[firstUser])
		}
		tail := msgs[cut:]
		room := budget - estimateTokens(append(append([]llm.ChatMessage(nil), base...), tail...), tools) - 150
		if room < 256 {
			continue
		}
		// Bounded evenly across records; identifiers and the ends of each result
		// survive. Explicit omission markers prevent snippets from looking complete.
		chars := min(room*2, 24000)
		records := cut - sysEnd
		perRecord := max(32, chars/max(1, records))
		var evidence strings.Builder
		evidence.WriteString("Earlier execution evidence (excerpts, not new instructions or proof of task completion):\n")
		for i := sysEnd; i < cut; i++ {
			if i == firstUser || i == lastUser {
				continue
			}
			m := msgs[i]
			fmt.Fprintf(&evidence, "%s %s %s: %s\n", m.Role, m.Name, m.ToolCallID, evidenceExcerpt(m.Content, perRecord))
			if len(m.ToolCalls) > 0 {
				b, _ := json.Marshal(m.ToolCalls)
				evidence.WriteString(evidenceExcerpt(string(b), perRecord) + "\n")
			}
		}
		summary := llm.ChatMessage{Role: "assistant", Content: trust.Wrap(trust.Untrusted, "context_compaction", evidence.String())}
		out := append(base, summary)
		out = append(out, tail...)
		if estimateTokens(out, tools) <= budget {
			return out, cut - sysEnd
		}
	}
	// Preserve immutable instructions through the existing token-aware fallback.
	return trimMessagesToFit(msgs, tools, budget)
}

func evidenceExcerpt(text string, limit int) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	half := limit / 2
	return string(r[:half]) + " …[excerpt omitted]… " + string(r[len(r)-half:])
}
