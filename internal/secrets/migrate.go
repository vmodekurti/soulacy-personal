package secrets

import (
	"context"
	"os"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

// Migrate moves any plaintext secrets found in cfg into the vault, then blanks
// those values both in the on-disk config file (cfgPath, if set) and in the
// in-memory cfg. It is safe to call on every startup: it is a no-op when no
// plaintext secrets are present. Returns the number of secrets migrated.
//
// After Migrate, call Overlay to repopulate the in-memory cfg from the vault so
// the running process still has working values.
func (m *Manager) Migrate(ctx context.Context, cfg *config.Config, cfgPath string) (int, error) {
	if !m.Enabled() {
		return 0, ErrNoVault
	}
	vals := secretValuesInConfig(cfg)
	if len(vals) == 0 {
		return 0, nil
	}

	migrated := 0
	movedValues := map[string]bool{}
	for name, val := range vals {
		if existing, exists := m.Get(ctx, name); exists && existing == val {
			movedValues[val] = true
			continue
		}
		if err := m.Set(ctx, name, val); err != nil {
			return migrated, err
		}
		movedValues[val] = true
		migrated++
	}

	// Blank ONLY the values we actually moved to the vault (preserve keys,
	// indentation, comments — and never touch values we didn't migrate, such
	// as the gateway's own server.api_key).
	if cfgPath != "" && len(movedValues) > 0 {
		if body, err := os.ReadFile(cfgPath); err == nil {
			if red, n := RedactSecretValues(string(body), movedValues); n > 0 {
				_ = os.WriteFile(cfgPath, []byte(red), 0o600)
			}
		}
	}

	// Blank in memory; Overlay restores live values from the vault.
	blankConfigSecrets(cfg, movedValues)
	return migrated, nil
}

// blankConfigSecrets clears the in-memory copies of the secret string fields
// whose values were migrated into the vault. Only values in moved are cleared,
// so unmigrated secrets (e.g. server.api_key) are left intact. Overlay then
// restores live values from the vault.
func blankConfigSecrets(cfg *config.Config, moved map[string]bool) {
	if cfg == nil {
		return
	}
	for id, pc := range cfg.LLM.Providers {
		if pc.APIKey != "" && moved[pc.APIKey] {
			pc.APIKey = ""
			cfg.LLM.Providers[id] = pc
		}
	}
	for _, settings := range cfg.Channels {
		for _, k := range channelTokenKeys {
			if raw, ok := settings[k]; ok {
				if s, ok := raw.(string); ok && moved[s] {
					settings[k] = ""
				}
			}
		}
	}
}

// RedactSecretValues blanks the value of any YAML line whose key is a known
// secret key AND whose current value is one we migrated into the vault
// (present in moved). Indentation, the key, and any trailing comment are
// preserved; values we did not migrate (e.g. the gateway's own api_key) are
// untouched. Returns the redacted body and the number of lines changed.
func RedactSecretValues(body string, moved map[string]bool) (string, int) {
	secretKeys := map[string]bool{}
	for _, k := range channelTokenKeys {
		secretKeys[k] = true
	}

	lines := strings.Split(body, "\n")
	changed := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		if !secretKeys[key] {
			continue
		}
		rest := line[colon+1:]

		comment := ""
		if hash := strings.Index(rest, " #"); hash >= 0 {
			comment = rest[hash:]
			rest = rest[:hash]
		}
		valueTrimmed := strings.TrimSpace(rest)
		// Strip surrounding quotes to compare against the raw migrated value.
		unquoted := strings.Trim(valueTrimmed, `"'`)
		if unquoted == "" || !moved[unquoted] {
			continue
		}

		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + key + `: ""` + comment
		changed++
	}
	if changed == 0 {
		return body, 0
	}
	return strings.Join(lines, "\n"), changed
}
