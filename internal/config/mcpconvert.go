package config

import (
	"path/filepath"

	"github.com/soulacy/soulacy/internal/mcp"
)

// ToMCP turns a configured MCP server into the client's ServerConfig.
//
// This exists because the same conversion was written by hand in three places
// — startup wiring, the config-reload path, and the HTTP handlers — and they
// disagreed. The reload path was missing two fields, which produced a bug with
// no error message anywhere in it:
//
// Saving an MCP server writes config.yaml. Writing config.yaml triggers a
// reload. The reload re-added every server through its own copy of this
// conversion, so whatever the two missing fields controlled was silently
// reverted a moment after being set — while a restart, which goes through the
// startup copy, applied it correctly. A setting that works after a restart and
// not before sends you looking at your own change rather than at the reload.
//
// The fields were keeps_processes (a browser server lost its browser to the
// process janitor on every call) and env_secret_refs (a server authenticating
// from the vault lost its credentials on any config reload).
//
// So there is one conversion now, and a test that fails when a field is added
// to MCPServerConfig and not mapped here.
//
// managedRoot is the directory a managed server's executable must resolve
// beneath; it is empty for servers that are not managed.
func (c MCPServerConfig) ToMCP(managedRoot string) mcp.ServerConfig {
	out := mcp.ServerConfig{
		Transport:     c.Transport,
		Command:       c.Command,
		Args:          c.Args,
		Env:           c.Env,
		EnvSecretRefs: c.EnvSecretRefs,
		URL:           c.URL,
		Headers:       c.Headers,
		Query:         c.Query,
		Auth: mcp.AuthConfig{
			Type:            c.Auth.Type,
			Header:          c.Auth.Header,
			Scheme:          c.Auth.Scheme,
			SecretRef:       c.Auth.SecretRef,
			TokenURL:        c.Auth.TokenURL,
			ClientID:        c.Auth.ClientID,
			ClientSecretRef: c.Auth.ClientSecretRef,
			Scopes:          c.Auth.Scopes,
			Audience:        c.Auth.Audience,
		},
		Timeout:        c.Timeout,
		PublicOnly:     c.PublicOnly,
		KeepsProcesses: c.KeepsProcesses,
	}
	if c.ManagedOnly && managedRoot != "" {
		out.ManagedRoot = filepath.Join(managedRoot, "mcp-servers")
	}
	return out
}
