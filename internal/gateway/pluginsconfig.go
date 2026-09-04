package gateway

// plugins_config redaction (Story E17). Plugin settings are schemaless and
// nest freely, so redaction recurses: any key matching the secret-name
// heuristic (shared with unknown-channel redaction) is masked when a value
// is present; empty secrets stay empty so the GUI can tell "unset" from
// "set". The source config is never mutated.

// safePluginsConfigView deep-copies the plugins_config map with secret
// values redacted.
func safePluginsConfigView(pc map[string]map[string]any) map[string]map[string]any {
	if pc == nil {
		return nil
	}
	out := make(map[string]map[string]any, len(pc))
	for pluginID, settings := range pc {
		out[pluginID] = redactSettingsMap(settings)
	}
	return out
}

func redactSettingsMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	safe := make(map[string]any, len(m))
	for k, v := range m {
		switch vv := v.(type) {
		case map[string]any:
			safe[k] = redactSettingsMap(vv)
		default:
			if isSecretChannelKey(nil, k) && valuePresent(v) {
				safe[k] = "***"
			} else {
				safe[k] = v
			}
		}
	}
	return safe
}
