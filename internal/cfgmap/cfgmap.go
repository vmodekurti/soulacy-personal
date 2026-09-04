// Package cfgmap provides small, strict coercion helpers for reading values
// out of schemaless factory config maps (Story E10). The SDK factory
// registries pass each driver its config entry verbatim as map[string]any;
// these helpers keep the per-driver parsing terse and uniform.
//
// Philosophy: coercions are conservative. A value of the wrong type falls
// back to the caller's default instead of being force-stringified — a typo'd
// config should surface as "missing required key", not as a garbage value.
package cfgmap

import (
	"strconv"
	"strings"
)

// Str returns m[key] when it is a non-empty string, else def.
func Str(m map[string]any, key, def string) string {
	if s, ok := m[key].(string); ok && s != "" {
		return s
	}
	return def
}

// Int returns m[key] coerced from int/int64/float64/string, else def.
func Int(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// Bool returns m[key] coerced from bool or a recognised boolean string
// (true/false/yes/no/1/0/on/off, case-insensitive), else def.
func Bool(m map[string]any, key string, def bool) bool {
	switch v := m[key].(type) {
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

// BoolPtr returns a *bool when m[key] is a bool or *bool, else nil
// (nil means "not configured" — providers treat it as their default).
func BoolPtr(m map[string]any, key string) *bool {
	switch v := m[key].(type) {
	case bool:
		b := v
		return &b
	case *bool:
		return v
	}
	return nil
}

// Map returns m[key] when it is a map[string]any, else nil.
func Map(m map[string]any, key string) map[string]any {
	if mm, ok := m[key].(map[string]any); ok {
		return mm
	}
	return nil
}
