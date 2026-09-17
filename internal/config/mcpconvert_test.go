package config

import (
	"reflect"
	"testing"
	"time"
)

// fullMCPServerConfig is every field set to something non-zero, so a field the
// conversion forgets shows up as a zero value on the other side.
func fullMCPServerConfig() MCPServerConfig {
	return MCPServerConfig{
		Transport:     "stdio",
		Command:       "/usr/local/bin/server",
		Args:          []string{"--flag"},
		Env:           map[string]string{"A": "1"},
		EnvSecretRefs: map[string]string{"TOKEN": "vault-key"},
		URL:           "https://example.invalid/mcp",
		Headers:       map[string]string{"X-Test": "1"},
		Query:         map[string]string{"q": "1"},
		Auth: MCPAuthConfig{
			Type: "bearer", Header: "Authorization", Scheme: "Bearer",
			SecretRef: "ref", TokenURL: "https://example.invalid/token",
			ClientID: "id", ClientSecretRef: "secret-ref",
			Scopes: []string{"read"}, Audience: "aud",
		},
		Timeout:        30 * time.Second,
		PublicOnly:     true,
		ManagedOnly:    true,
		KeepsProcesses: true,
	}
}

// Every configured field has to reach the client. The bug this guards against
// had no error message: a hand-written copy of this conversion in the reload
// path was missing two fields, so saving a server silently reverted them when
// the write triggered a reload — and a restart, which used a different copy,
// applied them correctly.
func TestToMCPCarriesEveryField(t *testing.T) {
	got := fullMCPServerConfig().ToMCP("/workspace")

	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if v.Field(i).IsZero() {
			t.Errorf("%s is zero after conversion — a configured field did not reach the client", name)
		}
	}

	// Spot-check the two that were actually being dropped, by value rather
	// than by "not zero".
	if !got.KeepsProcesses {
		t.Error("keeps_processes must survive: without it a browser server loses its browser on every call")
	}
	if got.EnvSecretRefs["TOKEN"] != "vault-key" {
		t.Error("env_secret_refs must survive: without it a server loses its credentials on a config reload")
	}
	if got.ManagedRoot != "/workspace/mcp-servers" {
		t.Errorf("ManagedRoot = %q, want the managed directory under the workspace", got.ManagedRoot)
	}
}

// A field added to MCPServerConfig and not mapped is exactly how the last two
// went missing. This fails the moment someone adds one, pointing at the
// conversion rather than leaving it to be found in production months later.
func TestEveryConfigFieldIsAccountedFor(t *testing.T) {
	mapped := map[string]bool{
		"Transport": true, "Command": true, "Args": true, "Env": true,
		"EnvSecretRefs": true, "URL": true, "Headers": true, "Query": true,
		"Auth": true, "Timeout": true, "PublicOnly": true,
		"ManagedOnly": true, "KeepsProcesses": true,
	}
	typ := reflect.TypeOf(MCPServerConfig{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !mapped[name] {
			t.Errorf("MCPServerConfig.%s is new: map it in ToMCP and add it here", name)
		}
	}
	if typ.NumField() != len(mapped) {
		t.Errorf("field count %d does not match the %d accounted for", typ.NumField(), len(mapped))
	}
}

// ManagedRoot is the one field that is not a straight copy: it is only set for
// a managed server, because it is what confines the executable.
func TestManagedRootOnlyForManagedServers(t *testing.T) {
	c := fullMCPServerConfig()
	c.ManagedOnly = false
	if got := c.ToMCP("/workspace").ManagedRoot; got != "" {
		t.Errorf("ManagedRoot = %q for an unmanaged server, want empty", got)
	}
	if got := fullMCPServerConfig().ToMCP("").ManagedRoot; got != "" {
		t.Errorf("ManagedRoot = %q with no workspace, want empty", got)
	}
}
