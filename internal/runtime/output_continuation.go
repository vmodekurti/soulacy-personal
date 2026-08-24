package runtime

import "strings"

// outputLimitFinishReason recognizes the provider-specific spellings for a
// completion that stopped because its per-call output allowance was exhausted.
func outputLimitFinishReason(reason string) bool {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	switch normalized {
	case "length", "max_tokens", "max_output_tokens", "token_limit", "model_length":
		return true
	default:
		return false
	}
}

// joinOutputContinuation combines provider-sized answer fragments without
// forcing a paragraph boundary into a sentence that was split at a token cap.
func joinOutputContinuation(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	last := current[len(current)-1]
	first := next[0]
	if !isOutputWhitespace(last) && !isOutputWhitespace(first) {
		return current + " " + next
	}
	return current + next
}

func isOutputWhitespace(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}
