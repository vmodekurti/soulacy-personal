package channels

import "strings"

// SplitForLimit splits text into chunks each at most `limit` runes long, so a
// reply that exceeds a platform's hard message-length cap (Telegram 4096,
// Discord 2000) is delivered as several messages instead of failing to send
// (S2.8). It prefers to break on a paragraph, then a line, then a space
// boundary near the limit, falling back to a hard rune cut only when a single
// "word" is itself longer than the limit. Runes (not bytes) are counted so
// multi-byte characters are never split.
func SplitForLimit(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(runes) > limit {
		// Look for the best break point at or before `limit`.
		window := string(runes[:limit])
		cut := limit
		if i := strings.LastIndex(window, "\n\n"); i > 0 {
			cut = len([]rune(window[:i])) + 2
		} else if i := strings.LastIndexByte(window, '\n'); i > 0 {
			cut = len([]rune(window[:i])) + 1
		} else if i := strings.LastIndexByte(window, ' '); i > limit/2 {
			cut = len([]rune(window[:i])) + 1
		}
		if cut <= 0 || cut > limit {
			cut = limit
		}
		chunk := strings.TrimRight(string(runes[:cut]), "\n")
		chunks = append(chunks, chunk)
		runes = runes[cut:]
	}
	if rem := strings.TrimLeft(string(runes), "\n"); rem != "" {
		chunks = append(chunks, rem)
	}
	return chunks
}
