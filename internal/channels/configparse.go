package channels

import (
	"fmt"
	"strconv"
	"strings"
)

// Config-map parsing helpers shared by the host wiring and the channel
// factory registrations (Story E10). Channel config blocks arrive from YAML
// as map[string]any; these helpers normalise the common shapes.

// ParseInt64List extracts an []int64 from m[key] tolerating the numeric
// types YAML loaders produce (int, int64, float64). Non-numeric entries are
// skipped. Returns nil when the key is absent or yields no values.
func ParseInt64List(m map[string]any, key string) []int64 {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	var out []int64
	if list, ok := raw.([]any); ok {
		for _, item := range list {
			switch v := item.(type) {
			case int64:
				out = append(out, v)
			case float64:
				out = append(out, int64(v))
			case int:
				out = append(out, int64(v))
			}
		}
	}
	return out
}

// ParseStringList extracts a string list from values that may have been
// loaded from YAML as []string, []any, or a whitespace-separated string.
func ParseStringList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(v)
	default:
		return nil
	}
}

// ParseDelimitedList is ParseStringList that additionally splits a scalar
// string on commas, newlines, and tabs (the shapes users type into config).
func ParseDelimitedList(raw any) []string {
	switch v := raw.(type) {
	case []string, []any:
		return ParseStringList(raw)
	case string:
		parts := strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\t'
		})
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

// ParseBoolValue coerces bool-ish config values (bool or true/false/yes/no/
// 1/0/on/off strings), falling back to def.
func ParseBoolValue(raw any, def bool) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		}
	}
	return def
}

// ActivationFromConfig builds an ActivationPolicy from a channel config
// block. defaultIgnoreGroups supplies the channel's house default when the
// key is absent (true for chat platforms, false for webhook channels).
func ActivationFromConfig(m map[string]any, defaultIgnoreGroups bool) ActivationPolicy {
	trigger := ""
	if raw, ok := m["trigger_phrase"]; ok && raw != nil {
		trigger = strings.TrimSpace(fmt.Sprint(raw))
	}
	if trigger == "" {
		trigger = "!soulacy"
	}
	userIDs := ParseDelimitedList(m["allowed_user_ids"])
	for _, uid := range ParseInt64List(m, "allowed_user_ids") {
		userIDs = append(userIDs, strconv.FormatInt(uid, 10))
	}
	// The two parsers overlap on numeric entries — dedupe, preserving order.
	seen := make(map[string]bool, len(userIDs))
	deduped := userIDs[:0]
	for _, id := range userIDs {
		if !seen[id] {
			seen[id] = true
			deduped = append(deduped, id)
		}
	}
	userIDs = deduped
	return ActivationPolicy{
		TriggerPhrase:    trigger,
		IgnoreGroups:     ParseBoolValue(m["ignore_groups"], defaultIgnoreGroups),
		AllowedThreadIDs: ParseDelimitedList(m["allowed_chat_ids"]),
		AllowedUserIDs:   userIDs,
		// Config-driven channels are real chat platforms: a direct message
		// activates the agent without needing the trigger phrase (the phrase
		// only gates noisy group chats).
		DirectActivatesWithoutPhrase: true,
	}
}
