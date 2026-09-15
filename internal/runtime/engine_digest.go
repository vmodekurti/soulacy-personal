package runtime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
)

// bestEffortFinal recovers a usable reply from the conversation context when
// every synthesis attempt yielded empty content (e.g. a reasoning model that
// kept spending its turns inside <think> blocks, or an agent that ran out of
// turns). It prefers the last substantive assistant message; failing that,
// it builds a readable digest of the gathered tool results so the user gets
// the information that was collected, in words rather than raw payloads.
// Returns "" only when there is genuinely nothing to surface.
func bestEffortFinal(chatMsgs []llm.ChatMessage) string {
	// 1) Last substantive assistant message.
	for i := len(chatMsgs) - 1; i >= 0; i-- {
		m := chatMsgs[i]
		if m.Role == "assistant" {
			if c := strings.TrimSpace(m.Content); c != "" {
				return c
			}
		}
	}
	// 2) Digest of tool results (most recent first, capped for sanity).
	const maxToolResults = 6
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
		label := strings.TrimSpace(m.Name)
		if label == "" {
			label = "result"
		}
		collected = append(collected, "**"+humanizeToolName(label)+"**\n"+humanizeToolResult(c))
	}
	if len(collected) == 0 {
		return ""
	}
	// Reverse back to chronological order for readability.
	for l, r := 0, len(collected)-1; l < r; l, r = l+1, r-1 {
		collected[l], collected[r] = collected[r], collected[l]
	}
	var b strings.Builder
	b.WriteString("I ran out of steps before I could write this up properly. Here is what I gathered:\n\n")
	b.WriteString(strings.Join(collected, "\n\n"))
	return b.String()
}

// humanizeToolName turns "mobile.invoke" into "mobile invoke".
func humanizeToolName(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "_", " "), ".", " ")
}

// humanizeToolResult renders a tool payload for a person. A JSON object
// becomes "key: value" lines (nested values summarised, identifiers and
// timestamps dropped); anything else is trimmed text.
func humanizeToolResult(content string) string {
	const maxChars = 1200
	const maxKeys = 10
	trimmed := strings.TrimSpace(content)
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil || len(obj) == 0 {
		var arr []any
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			return fmt.Sprintf("%d items", len(arr))
		}
		if len(trimmed) > maxChars {
			return trimmed[:maxChars] + "…"
		}
		return trimmed
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		if isNoiseKey(k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
	}
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, "- "+strings.ReplaceAll(k, "_", " ")+": "+summarizeValue(obj[k]))
	}
	if len(lines) == 0 {
		return "(nothing readable)"
	}
	return strings.Join(lines, "\n")
}

func isNoiseKey(k string) bool {
	k = strings.ToLower(k)
	return k == "id" || strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "_at") || k == "device_id" || k == "params"
}

func summarizeValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "none"
	case string:
		if len(t) > 160 {
			return t[:160] + "…"
		}
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%.2f", t)
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case []any:
		if len(t) == 0 {
			return "none"
		}
		parts := make([]string, 0, min(len(t), 5))
		for _, item := range t[:min(len(t), 5)] {
			if m, ok := item.(map[string]any); ok {
				named := false
				for _, key := range []string{"label", "name", "title", "type", "summary"} {
					if v, ok := m[key].(string); ok && v != "" {
						parts = append(parts, v)
						named = true
						break
					}
				}
				if !named {
					parts = append(parts, "item")
				}
				continue
			}
			parts = append(parts, summarizeValue(item))
		}
		out := strings.Join(parts, ", ")
		if len(t) > 5 {
			out += fmt.Sprintf(" and %d more", len(t)-5)
		}
		return out
	case map[string]any:
		inner := humanizeToolResult(mustJSON(t))
		return strings.ReplaceAll("\n"+inner, "\n- ", "\n  - ")
	default:
		return fmt.Sprint(t)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
