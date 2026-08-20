package plugins

import (
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/redact"
)

// tenantsettings.go — plugins_config is one map shared by every workspace.
//
// THE SAME LEAK AS MCP, ONE LAYER DOWN. A plugin's DECLARED credentials
// (manifest `credentials:`) already resolve per workspace through the vault —
// see delegation.go, which has been right about this since it was written.
// Its `settings:` section did not: `plugins_config` in config.yaml is one map,
// held once in Stores and attached to every workspace's loader.
//
// Settings are meant to be configuration, so most of the time this is
// harmless. Nothing stops a plugin author putting an API key there instead of
// declaring it, and when they do, every tenant's plugin runs on the operator's
// key — the same shape of bug, with the same consequence: one identity
// upstream, no per-tenant attribution, no per-tenant revocation.
//
// WHY NOT A PER-WORKSPACE SETTINGS STORE. Because the correct home already
// exists. A secret in `settings` is a plugin author using the wrong field, and
// building a second per-tenant settings mechanism would bless the mistake and
// leave two places a tenant sets plugin secrets. Instead the value is taken
// from the vault the plugin's declared credentials already use, so a tenant
// sets it in exactly one place — and when they have not, the key is withheld
// and the operator is told to declare it properly.

// SettingsResolver supplies one workspace's own value for a plugin setting
// that looks like a credential.
type SettingsResolver interface {
	PluginSecret(workspaceID, pluginID, key string) (value string, ok bool)
}

// SettingsResolverFunc adapts a plain function to SettingsResolver.
type SettingsResolverFunc func(workspaceID, pluginID, key string) (string, bool)

func (f SettingsResolverFunc) PluginSecret(workspaceID, pluginID, key string) (string, bool) {
	return f(workspaceID, pluginID, key)
}

// WithheldSetting is a shared setting a workspace did not receive.
type WithheldSetting struct {
	PluginID string `json:"plugin_id"`
	// Key is the dotted path to the withheld setting, so a nested
	// `auth.api_key` is reported as something an operator can find in the file
	// rather than as a bare "api_key" that appears three times.
	Key string `json:"key"`
}

// tenantizeSettings removes credential-looking values the operator configured
// and substitutes the workspace's own, reporting what is missing.
//
// Secret-ness is decided by redact.SecretKeyName, the single predicate this
// repo uses for the question — the same one MCP uses, so a key that is a
// secret in one place is a secret in the other.
//
// It RECURSES. A shallow check would pass `{"auth": {"api_key": "…"}}`
// untouched, and nesting a credential one level down is the ordinary way these
// files are written, not an edge case.
func tenantizeSettings(pluginID, workspaceID string, section map[string]any,
	resolver SettingsResolver) (map[string]any, []WithheldSetting) {
	if len(section) == 0 {
		return section, nil
	}
	var withheld []WithheldSetting
	out := tenantizeSection(pluginID, workspaceID, "", section, resolver, &withheld)
	sort.Slice(withheld, func(i, j int) bool { return withheld[i].Key < withheld[j].Key })
	return out, withheld
}

func tenantizeSection(pluginID, workspaceID, prefix string, section map[string]any,
	resolver SettingsResolver, withheld *[]WithheldSetting) map[string]any {
	out := make(map[string]any, len(section))
	for key, value := range section {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(map[string]any); ok {
			out[key] = tenantizeSection(pluginID, workspaceID, path, nested, resolver, withheld)
			continue
		}
		if !redact.SecretKeyName(key) {
			out[key] = value
			continue
		}
		// The vault is keyed by the leaf name, not the dotted path: a tenant
		// setting a credential names the credential, and asking them to know
		// where in the operator's file it happened to be nested would make the
		// value unfindable the first time the operator reorganised it.
		if resolver != nil {
			if own, ok := resolver.PluginSecret(workspaceID, pluginID, key); ok && own != "" {
				out[key] = own
				continue
			}
		}
		*withheld = append(*withheld, WithheldSetting{PluginID: pluginID, Key: path})
	}
	return out
}

// WithheldSettingsMessage renders the operator-facing remedy.
//
// Separate from the struct so the boot log and the API say the same sentence.
// The remedy names the manifest field rather than the vault, because the real
// fix is the plugin declaring the credential it needs — the per-workspace
// value is then supplied through the mechanism that already exists.
func WithheldSettingsMessage(items []WithheldSetting) string {
	if len(items) == 0 {
		return ""
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.PluginID+"."+item.Key)
	}
	return "credential-looking plugins_config values are not shared between workspaces: " +
		strings.Join(keys, ", ") + ". Declare them in the plugin manifest's `credentials:` " +
		"section so each workspace supplies its own, or rename them if they are not secrets"
}
