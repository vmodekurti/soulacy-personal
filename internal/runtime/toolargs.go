// toolargs.go — type-safe argument coercion helpers for built-in tool handlers.
//
// Every built-in tool handler receives its arguments as map[string]any —
// the raw JSON-decoded values from the LLM's tool call. JSON numbers decode
// as float64, booleans as bool, and strings as string, but the LLM may also
// send a number-typed field as a string (e.g. "1024" instead of 1024).
// These helpers normalise that variability into the concrete Go types the
// handler needs, returning a sane zero-value when the field is absent or
// of an unexpected type.
//
// Usage:
//
//	path  := argString(args, "path")
//	limit := argInt(args, "max_bytes", 1<<20) // default 1 MiB
//	flag  := argBool(args, "show_hidden")
package runtime

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// argString extracts a string argument by name. Returns "" if the key is
// absent or the value cannot be coerced to a string.
func argString(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// argStringDefault extracts a string argument, returning def when absent or empty.
func argStringDefault(args map[string]any, key, def string) string {
	s := argString(args, key)
	if s == "" {
		return def
	}
	return s
}

// argContentText extracts text-like content for storage tools. It preserves
// strings exactly, but also accepts parsed JSON objects/arrays from Studio's
// {{ toJson .value }} handoffs and serializes them as readable JSON text.
func argContentText(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(stripMarkdownJSONFence(s))
	}
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(b)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

func argContentTextFirst(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := strings.TrimSpace(argContentText(args, key)); s != "" {
			return s
		}
	}
	return ""
}

func stripMarkdownJSONFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	lines := strings.Split(t, "\n")
	if len(lines) < 2 {
		return s
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(first, "```") || last != "```" {
		return s
	}
	inner := strings.Join(lines[1:len(lines)-1], "\n")
	return strings.TrimSpace(inner)
}

// argInt extracts an integer argument by name, returning def when the key is
// absent, nil, or non-numeric. Handles both float64 (JSON default) and
// string representations (e.g. "1024").
func argInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return int(f)
		}
	}
	return def
}

// argInt64 is like argInt but returns int64.
func argInt64(args map[string]any, key string, def int64) int64 {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return int64(f)
		}
	}
	return def
}

// argFloat extracts a float64 argument, returning def when absent or
// non-numeric.
func argFloat(args map[string]any, key string, def float64) float64 {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f
		}
	}
	return def
}

// argBool extracts a boolean argument, returning false when absent. Handles
// bool values as well as the string representations "true"/"1"/"yes" and
// "false"/"0"/"no" (case-insensitive) that some LLMs emit.
func argBool(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		parsed, _ := strconv.ParseBool(b)
		return parsed
	case float64:
		return b != 0
	case int:
		return b != 0
	}
	return false
}

// argStringSlice extracts a []string argument. The LLM may send either a
// proper JSON array or a comma-separated string; both forms are handled.
// Returns nil when the key is absent.
func argStringSlice(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			} else {
				out = append(out, fmt.Sprintf("%v", item))
			}
		}
		return out
	case string:
		if s == "" {
			return nil
		}
		// Tolerate a raw comma-separated string fallback.
		var out []string
		for _, part := range splitCSV(s) {
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	}
	return nil
}

// splitCSV splits on commas, trimming surrounding whitespace from each part.
func splitCSV(s string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := trimSpace(s[start:i])
			parts = append(parts, part)
			start = i + 1
		}
	}
	return parts
}

func trimSpace(s string) string {
	lo, hi := 0, len(s)
	for lo < hi && (s[lo] == ' ' || s[lo] == '\t' || s[lo] == '\n' || s[lo] == '\r') {
		lo++
	}
	for hi > lo && (s[hi-1] == ' ' || s[hi-1] == '\t' || s[hi-1] == '\n' || s[hi-1] == '\r') {
		hi--
	}
	return s[lo:hi]
}
