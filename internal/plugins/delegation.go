// delegation.go implements vault credential delegation to plugin sidecars
// (Story E6, docs/EXTENSIBILITY.md §5.3).
//
// Plugins declare the secrets they need in plugin.yaml (pkg/plugin.
// CredentialRef); the host injects ONLY those into the sidecar's environment
// at spawn, on top of a minimal whitelisted base environment — the sidecar
// never inherits the gateway's process environment. Vault paths are
// namespace-scoped: `from: <namespace>/<key>` is only valid when the
// namespace equals the plugin's own ID, and plugin secrets live under the
// vault namespace "plugin:<id>" so they can never collide with agent
// credentials.
//
// Rotation: WatchCredentials polls a fingerprint (SHA-256, never the values)
// of the declared secrets and invokes a callback on change; the supervisor
// (internal/channels/external) restarts the sidecar, and its per-spawn env
// resolver picks up the new values. Secrets are never written to disk or
// logs by this code; see docs/PLUGIN_CREDENTIALS.md for the env-transport
// limitations and the planned v2 handshake-frame delivery.
package plugins

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/soulacy/soulacy/internal/wsroot"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/caps"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/pkg/plugin"
)

// PluginVaultNamespace is the vault agent-ID namespace for a plugin's
// secrets. Using the principal string ("plugin:<id>") keeps plugin secrets
// disjoint from agent credentials in the same vault.
func PluginVaultNamespace(pluginID string) string {
	return string(caps.PluginPrincipal(pluginID))
}

// envKeyPattern is the canonical uppercase env-var grammar.
var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// ValidateCredentialRefs checks the manifest credential declarations of one
// plugin: env-var key grammar, unique keys, and `<pluginID>/<key>` vault
// paths (own namespace only).
func ValidateCredentialRefs(pluginID string, refs []plugin.CredentialRef) error {
	seen := make(map[string]bool, len(refs))
	for i, r := range refs {
		if !envKeyPattern.MatchString(r.Key) {
			return fmt.Errorf("credential %d: key %q is not a valid env var name (want [A-Z_][A-Z0-9_]*)", i, r.Key)
		}
		if seen[r.Key] {
			return fmt.Errorf("credential %d: duplicate key %q", i, r.Key)
		}
		seen[r.Key] = true

		ns, key, ok := strings.Cut(r.From, "/")
		if !ok || ns == "" || key == "" || strings.Contains(key, "/") {
			return fmt.Errorf("credential %d (%s): from %q must be <namespace>/<key>", i, r.Key, r.From)
		}
		if ns != pluginID {
			return fmt.Errorf("credential %d (%s): namespace %q is not this plugin's (%q) — cross-namespace references are forbidden", i, r.Key, ns, pluginID)
		}
	}
	return nil
}

// baseEnvAllowlist is the minimal parent environment passed to sidecars.
// Everything else — including any host API keys — is withheld.
var baseEnvAllowlist = []string{
	"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL", "TZ", "USER", "SHELL",
}

// Delegator resolves manifest credential declarations into a sidecar
// environment. It holds the vault but never logs or persists secret values.
type Delegator struct {
	vault credentials.Vault
	log   *zap.Logger
	// workspaceID is the tenant whose vault the declared secrets are read
	// from (MU-017 criterion 4: "only referenced secret handles *for its
	// workspace*").
	//
	// The vault has been workspace-aware for a while; this resolver was
	// passing wsroot.PersonalWorkspaceID unconditionally. So a plugin wired
	// for any tenant read the personal workspace's secrets — the same
	// substitution the memory archive was making, and with the same shape:
	// the call succeeds, the sidecar starts, and it is holding the wrong
	// tenant's credential.
	workspaceID string
}

// NewDelegator builds a Delegator for the personal workspace. log must not be
// nil (zap.NewNop in tests).
func NewDelegator(vault credentials.Vault, log *zap.Logger) *Delegator {
	return NewDelegatorInWorkspace(vault, wsroot.PersonalWorkspaceID, log)
}

// NewDelegatorInWorkspace builds a Delegator that reads one workspace's vault.
func NewDelegatorInWorkspace(vault credentials.Vault, workspaceID string, log *zap.Logger) *Delegator {
	return &Delegator{vault: vault, log: log, workspaceID: wsroot.Normalize(workspaceID)}
}

// workspace is the tenant this delegator reads for, normalising a zero value
// so a Delegator built by struct literal in a test still behaves.
func (d *Delegator) workspace() string { return wsroot.Normalize(d.workspaceID) }

// Env builds the complete sidecar environment for a plugin: the whitelisted
// base env plus exactly the declared secrets. A missing secret is an error —
// spawning a sidecar without a credential it declared would only produce
// confusing downstream failures.
func (d *Delegator) Env(ctx context.Context, pluginID string, refs []plugin.CredentialRef) ([]string, error) {
	if err := ValidateCredentialRefs(pluginID, refs); err != nil {
		return nil, fmt.Errorf("plugins: %s: %w", pluginID, err)
	}
	env := make([]string, 0, len(baseEnvAllowlist)+len(refs))
	for _, k := range baseEnvAllowlist {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	ns := PluginVaultNamespace(pluginID)
	for _, r := range refs {
		_, key, _ := strings.Cut(r.From, "/")
		val, err := d.vault.Get(ctx, d.workspace(), ns, key)
		if err != nil {
			// Deliberately omits the value (there is none) and never echoes
			// stored secrets; only the path is named.
			return nil, fmt.Errorf("plugins: %s: credential %s (from %s): %w", pluginID, r.Key, r.From, err)
		}
		env = append(env, r.Key+"="+string(val))
	}
	d.log.Debug("plugins: sidecar env resolved",
		zap.String("plugin", pluginID),
		zap.String("workspace", d.workspace()),
		zap.Int("credentials", len(refs)),
	)
	return env, nil
}

// fingerprint hashes the declared secrets' current values (never retaining
// or exposing them). Missing secrets hash their error marker so appearing/
// disappearing also counts as a change.
func (d *Delegator) fingerprint(ctx context.Context, pluginID string, refs []plugin.CredentialRef) [32]byte {
	ns := PluginVaultNamespace(pluginID)
	keys := make([]string, 0, len(refs))
	for _, r := range refs {
		_, key, _ := strings.Cut(r.From, "/")
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		val, err := d.vault.Get(ctx, d.workspace(), ns, key)
		h.Write([]byte(key))
		h.Write([]byte{0})
		if err != nil {
			h.Write([]byte("<absent>"))
		} else {
			sum := sha256.Sum256(val)
			h.Write(sum[:])
		}
		h.Write([]byte{0})
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// WatchCredentials polls the declared secrets every interval and calls
// onChange when any of them is rotated, replaced, added, or removed. The
// goroutine exits when ctx is cancelled. Only hashes are kept in memory;
// values are never logged.
func WatchCredentials(ctx context.Context, vault credentials.Vault, pluginID string,
	refs []plugin.CredentialRef, interval time.Duration, log *zap.Logger, onChange func()) {
	WatchCredentialsInWorkspace(ctx, vault, wsroot.PersonalWorkspaceID, pluginID, refs, interval, log, onChange)
}

// WatchCredentialsInWorkspace is WatchCredentials for one tenant's vault. The
// watch has to read the same workspace the spawn env does, or a rotation in
// the tenant's vault would never be noticed while a rotation in personal's
// would restart a sidecar for no reason.
func WatchCredentialsInWorkspace(ctx context.Context, vault credentials.Vault, workspaceID, pluginID string,
	refs []plugin.CredentialRef, interval time.Duration, log *zap.Logger, onChange func()) {
	if len(refs) == 0 || onChange == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	d := NewDelegatorInWorkspace(vault, workspaceID, log)
	baseline := d.fingerprint(ctx, pluginID, refs)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			fp := d.fingerprint(ctx, pluginID, refs)
			if fp != baseline {
				baseline = fp
				log.Info("plugins: credential change detected; requesting sidecar restart",
					zap.String("plugin", pluginID))
				onChange()
			}
		}
	}()
}
