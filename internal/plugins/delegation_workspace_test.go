// delegation_workspace_test.go — MU-017 criterion 4: an extension receives
// only the referenced secret handles *for its workspace*.
package plugins

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/plugin"
)

func newWorkspaceVault(t *testing.T) credentials.Vault { return testVault(t) }

func envValue(env []string, key string) (string, bool) {
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// The vault has been workspace-aware for a while; the delegator was passing
// the personal workspace unconditionally. So a plugin wired for any tenant
// read personal's secrets — the call succeeds, the sidecar starts, and it is
// holding the wrong tenant's credential.
func TestASidecarReceivesItsOwnWorkspacesSecret(t *testing.T) {
	vault := newWorkspaceVault(t)
	ctx := context.Background()
	ns := PluginVaultNamespace("reporter")
	for workspace, secret := range map[string]string{
		wsroot.PersonalWorkspaceID: "personal-token",
		"ws_a":                     "alpha-token",
		"ws_b":                     "beta-token",
	} {
		if err := vault.Set(ctx, workspace, ns, "api_token", []byte(secret)); err != nil {
			t.Fatal(err)
		}
	}
	refs := []plugin.CredentialRef{{Key: "REPORTER_TOKEN", From: "reporter/api_token"}}

	for workspace, want := range map[string]string{
		wsroot.PersonalWorkspaceID: "personal-token",
		"ws_a":                     "alpha-token",
		"ws_b":                     "beta-token",
	} {
		env, err := NewDelegatorInWorkspace(vault, workspace, zap.NewNop()).Env(ctx, "reporter", refs)
		if err != nil {
			t.Fatalf("resolve for %s: %v", workspace, err)
		}
		got, ok := envValue(env, "REPORTER_TOKEN")
		if !ok {
			t.Fatalf("the declared credential did not reach the sidecar for %s", workspace)
		}
		if got != want {
			t.Fatalf("workspace %s received %q, want %q", workspace, got, want)
		}
	}
}

// A workspace that never stored the secret gets an error, not a neighbour's
// value and not a silent empty one. Starting a sidecar without a credential it
// declared only produces confusing downstream failures; starting it with the
// wrong tenant's is worse.
func TestAMissingSecretIsAnErrorRatherThanASubstitution(t *testing.T) {
	vault := newWorkspaceVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", PluginVaultNamespace("reporter"), "api_token", []byte("alpha-token")); err != nil {
		t.Fatal(err)
	}
	refs := []plugin.CredentialRef{{Key: "REPORTER_TOKEN", From: "reporter/api_token"}}

	env, err := NewDelegatorInWorkspace(vault, "ws_b", zap.NewNop()).Env(ctx, "reporter", refs)
	if err == nil {
		t.Fatalf("a workspace with no secret of its own was given an environment: %v", env)
	}
	if strings.Contains(err.Error(), "alpha-token") {
		t.Fatal("the error message echoed another workspace's secret value")
	}
}

// Rotation is watched in the same workspace the spawn env reads. Watching the
// wrong one means a tenant's rotation is never noticed, while an unrelated
// workspace's rotation restarts their sidecar for no reason.
func TestRotationIsDetectedInTheSameWorkspaceItIsReadFrom(t *testing.T) {
	vault := newWorkspaceVault(t)
	ctx := context.Background()
	ns := PluginVaultNamespace("reporter")
	if err := vault.Set(ctx, "ws_a", ns, "api_token", []byte("alpha-token")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(ctx, wsroot.PersonalWorkspaceID, ns, "api_token", []byte("personal-token")); err != nil {
		t.Fatal(err)
	}
	refs := []plugin.CredentialRef{{Key: "REPORTER_TOKEN", From: "reporter/api_token"}}

	d := NewDelegatorInWorkspace(vault, "ws_a", zap.NewNop())
	before := d.fingerprint(ctx, "reporter", refs)

	// Another workspace rotating must not look like a change here.
	if err := vault.Set(ctx, wsroot.PersonalWorkspaceID, ns, "api_token", []byte("personal-rotated")); err != nil {
		t.Fatal(err)
	}
	if d.fingerprint(ctx, "reporter", refs) != before {
		t.Fatal("another workspace's rotation registered as this workspace's change")
	}

	// This workspace rotating must.
	if err := vault.Set(ctx, "ws_a", ns, "api_token", []byte("alpha-rotated")); err != nil {
		t.Fatal(err)
	}
	if d.fingerprint(ctx, "reporter", refs) == before {
		t.Fatal("this workspace's rotation went unnoticed")
	}
}

// Criterion 4's other half, already true and worth pinning: the sidecar gets
// the secrets it declared and nothing else, from its own namespace only.
func TestOnlyDeclaredSecretsFromItsOwnNamespaceAreDelegated(t *testing.T) {
	vault := newWorkspaceVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", PluginVaultNamespace("reporter"), "api_token", []byte("alpha-token")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(ctx, "ws_a", PluginVaultNamespace("other"), "api_token", []byte("other-plugin-token")); err != nil {
		t.Fatal(err)
	}
	d := NewDelegatorInWorkspace(vault, "ws_a", zap.NewNop())

	// A reference into another plugin's namespace is refused at validation,
	// before any vault read happens.
	if _, err := d.Env(ctx, "reporter", []plugin.CredentialRef{
		{Key: "OTHER_TOKEN", From: "other/api_token"},
	}); err == nil {
		t.Fatal("a cross-namespace credential reference was resolved")
	}

	env, err := d.Env(ctx, "reporter", []plugin.CredentialRef{
		{Key: "REPORTER_TOKEN", From: "reporter/api_token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := envValue(env, "OTHER_TOKEN"); leaked {
		t.Fatal("an undeclared credential reached the sidecar")
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "other-plugin-token") {
		t.Fatal("another plugin's secret value reached the sidecar")
	}
}
