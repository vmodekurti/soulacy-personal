package app

import (
	"context"
	"time"

	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// mcpcreds.go — the vault-backed answer to "whose credential is this".
//
// Built here rather than in the gateway because the pool is constructed during
// subsystem wiring, before the gateway exists, and the requirement has to be
// installed BEFORE any workspace asks for its first client. A resolver
// attached later would leave whichever workspace called first running on the
// operator's token.

// vaultMCPCredentials resolves a workspace's own MCP secrets from the
// per-workspace credential vault.
//
// An error or a missing value resolves to "not supplied", never to the
// operator's value. A vault that is unreachable must WITHHOLD the server:
// falling back is the leak, and it would happen exactly when the vault is
// broken and nobody is watching MCP.
func vaultMCPCredentials(vault credentials.Vault) mcp.CredentialResolver {
	if vault == nil {
		// Not nil-the-interface: a nil resolver with the requirement on
		// withholds every credentialed server, which is the honest reading of
		// "we cannot tell whose credential this would be". Returning nil here
		// would be the same outcome, but saying it explicitly keeps the
		// fail-closed direction from looking like an oversight.
		return mcp.CredentialResolverFunc(func(string, string, string) (string, bool) {
			return "", false
		})
	}
	return mcp.CredentialResolverFunc(func(workspaceID, serverID, key string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		value, err := vault.Get(ctx, wsroot.Normalize(workspaceID), mcp.CredentialNamespace(serverID), key)
		if err != nil || len(value) == 0 {
			return "", false
		}
		return string(value), true
	})
}

// vaultPluginSettings resolves a workspace's own value for a plugin setting
// that looks like a credential.
//
// It reads the SAME vault namespace a plugin's declared credentials use, so a
// tenant sets a plugin secret in exactly one place. Building a second
// per-workspace settings store would have blessed the mistake of putting a
// credential in `settings:` and given tenants two places to look.
//
// Fails closed for the same reason the MCP resolver does: an unreachable vault
// means "not supplied", never "use the operator's".
func vaultPluginSettings(vault credentials.Vault) plugins.SettingsResolver {
	if vault == nil {
		return plugins.SettingsResolverFunc(func(string, string, string) (string, bool) {
			return "", false
		})
	}
	return plugins.SettingsResolverFunc(func(workspaceID, pluginID, key string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		value, err := vault.Get(ctx, wsroot.Normalize(workspaceID),
			plugins.PluginVaultNamespace(pluginID), key)
		if err != nil || len(value) == 0 {
			return "", false
		}
		return string(value), true
	})
}
