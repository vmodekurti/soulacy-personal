package studio

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/soulacy/soulacy/internal/redact"
)

// sanitizeLearnedText is the persistence boundary for model-facing memory.
// Learned content is data, never authority: remove secrets/control characters,
// reject common instruction-override payloads, and bound its prompt footprint.
func sanitizeLearnedText(value string, maxRunes int) (string, bool) {
	value = redact.Text(strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t'
	}), " "))
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	lower := strings.ToLower(value)
	for _, unsafe := range []string{
		"ignore previous", "ignore all previous", "ignore the system", "system prompt",
		"developer message", "jailbreak", "do not follow the above", "override instructions",
	} {
		if strings.Contains(lower, unsafe) {
			return "", false
		}
	}
	runes := []rune(value)
	if maxRunes > 0 && len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value, true
}

// learnedData renders memory as a quoted data value so delimiters/newlines in
// the learned text cannot escape into the surrounding instruction structure.
func learnedData(value string) string { return strconv.Quote(value) }
