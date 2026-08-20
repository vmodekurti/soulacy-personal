package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/redact"
)

// tenantcreds.go — a tenant's MCP server must run as the TENANT.
//
// THE LEAK. The pool next door gives each workspace its own subprocesses, and
// that closed the filesystem half of the problem: two tenants' relative paths
// resolve to two different trees. It did nothing about the other half. Every
// one of those processes was started from the operator's template, `Env` and
// `Headers` included — which is where a server's GITHUB_TOKEN and its
// `Authorization: Bearer sk-…` live.
//
// So tenant A's agent called `github__list_repos` as the OPERATOR. It saw
// whatever that token sees, which includes everything tenant B pushed.
// Upstream saw one identity for the whole deployment: no per-tenant
// attribution, no per-tenant rate limit, and no way to cut off one tenant
// without cutting off all of them. Isolating the process while sharing the
// identity isolates nothing that matters.
//
// THE RULE. A credential must be DERIVED from the workspace, never inherited
// from something shared. That is the same rule the filesystem roots, the
// scratch directories and the vault's own encryption keys already follow.
//
// WHY WITHHOLD RATHER THAN FALL BACK. The obvious design is "the tenant's
// value if they set one, otherwise the operator's". Its failure mode is
// silence: a tenant who has not supplied a token gets the operator's and
// everything works, which is the bug wearing the costume of a working feature.
// A server whose credentials the tenant has not supplied is simply not started
// for them, and the reason is reported so it reads as a setup step rather than
// a fault.

// CredentialNamespace is where one server's per-workspace secrets are filed.
//
// The credential vault is keyed (workspace, agent, key), and MCP secrets are
// not an agent's — so they get a reserved namespace with a prefix no agent ID
// can produce, because ValidateAgentID rejects ':'. Without it, a workspace
// with an agent literally named "github" would share a namespace with the
// github MCP server.
//
// It lives HERE, in the package that defines the resolver, so the writer (the
// API that stores a tenant's token) and the reader (the pool that starts their
// server with it) cannot disagree about where it went. Two spellings of this
// string is a token that saves successfully and is never found.
func CredentialNamespace(serverID string) string {
	return "mcp:" + strings.TrimSpace(serverID)
}

// CredentialResolver supplies one workspace's own value for a server secret.
//
// Supplied by the caller for the same reason Confinement is: the authority on
// a workspace's credentials is the vault, and this package must not become a
// second one.
type CredentialResolver interface {
	// ServerSecret returns the workspace's value for one server's env var or
	// header. ok=false means the workspace has not supplied it.
	ServerSecret(workspaceID, serverID, key string) (value string, ok bool)
}

// CredentialResolverFunc adapts a plain function to CredentialResolver.
type CredentialResolverFunc func(workspaceID, serverID, key string) (string, bool)

func (f CredentialResolverFunc) ServerSecret(workspaceID, serverID, key string) (string, bool) {
	return f(workspaceID, serverID, key)
}

// WithheldServer is a template server a workspace cannot start yet.
type WithheldServer struct {
	ServerID string `json:"server_id"`
	// Missing names the credentials the workspace still has to supply, sorted
	// so the message is stable between calls.
	Missing []string `json:"missing"`
}

func (w WithheldServer) Error() string {
	return fmt.Sprintf("mcp server %q needs this workspace's own %s", w.ServerID,
		strings.Join(w.Missing, ", "))
}

// tenantize replaces the operator's secrets in a template server with the
// workspace's own, or reports what is missing.
//
// Only SECRET-NAMED keys are replaced, decided by redact.SecretKeyName — the
// single predicate this repo uses for "does this look like a credential", so
// that MCP does not become a fifth opinion about it. A template `LOG_LEVEL` or
// `GITHUB_ORG` is configuration the operator meant to share and passes
// through; a template `GITHUB_TOKEN` is an identity and does not.
//
// An empty template value is not a secret being shared — it is a placeholder
// telling the tenant what to fill in — so it is still required from the
// workspace rather than passed through as "".
func tenantize(serverID string, sc ServerConfig, workspaceID string, resolver CredentialResolver) (ServerConfig, *WithheldServer) {
	if sc.SharedCredentials {
		// Explicit operator opt-in: see config.MCPServerConfig.SharedCredentials.
		return sc, nil
	}
	var missing []string

	if len(sc.Env) > 0 {
		next := make(map[string]string, len(sc.Env))
		for key, value := range sc.Env {
			if !redact.SecretKeyName(key) {
				next[key] = value
				continue
			}
			if resolver != nil {
				if own, ok := resolver.ServerSecret(workspaceID, serverID, key); ok && own != "" {
					next[key] = own
					continue
				}
			}
			missing = append(missing, key)
		}
		sc.Env = next
	}

	if len(sc.Headers) > 0 {
		next := make(map[string]string, len(sc.Headers))
		for key, value := range sc.Headers {
			if !redact.SecretKeyName(key) {
				next[key] = value
				continue
			}
			if resolver != nil {
				if own, ok := resolver.ServerSecret(workspaceID, serverID, key); ok && own != "" {
					next[key] = own
					continue
				}
			}
			missing = append(missing, key)
		}
		sc.Headers = next
	}

	if len(missing) == 0 {
		return sc, nil
	}
	sort.Strings(missing)
	return sc, &WithheldServer{ServerID: serverID, Missing: missing}
}
