// workspace_test.go — the cross-tenant isolation contract for the credential
// vault (MU-015).
package credentials

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/wsroot"
)

func newWorkspaceVault(t *testing.T) (*SQLiteVault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.db")
	kms, err := NewLocalKMS()
	if err != nil {
		t.Fatal(err)
	}
	vault, err := NewSQLiteVault(path, kms)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return vault, path
}

// "openai_api_key" is a name every tenant uses. One tenant setting it must not
// overwrite or expose another's.
func TestSameSecretNameInTwoWorkspacesAreDistinctSecrets(t *testing.T) {
	vault, _ := newWorkspaceVault(t)
	ctx := context.Background()

	if err := vault.Set(ctx, "ws_a", "agent", "openai_api_key", []byte("alpha-key")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(ctx, "ws_b", "agent", "openai_api_key", []byte("beta-key")); err != nil {
		t.Fatal(err)
	}

	a, err := vault.Get(ctx, "ws_a", "agent", "openai_api_key")
	if err != nil {
		t.Fatal(err)
	}
	b, err := vault.Get(ctx, "ws_b", "agent", "openai_api_key")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, []byte("alpha-key")) || !bytes.Equal(b, []byte("beta-key")) {
		t.Fatalf("workspaces overwrote each other: %q / %q", a, b)
	}
}

// A secret that exists only in another workspace must be indistinguishable
// from one that does not exist.
func TestSecretFromAnotherWorkspaceReadsAsAbsent(t *testing.T) {
	vault, _ := newWorkspaceVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "agent", "token", []byte("confidential")); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Get(ctx, "ws_b", "agent", "token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace read = %v, want ErrNotFound", err)
	}
	keys, err := vault.List(ctx, "ws_b", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("another workspace's secret names were listed: %v", keys)
	}
	// A delete naming another workspace must not remove the owner's secret.
	if err := vault.Delete(ctx, "ws_b", "agent", "token"); err != nil {
		t.Fatal(err)
	}
	if got, err := vault.Get(ctx, "ws_a", "agent", "token"); err != nil || !bytes.Equal(got, []byte("confidential")) {
		t.Fatalf("another workspace's delete removed the owner's secret: %q %v", got, err)
	}
}

func TestEraseWorkspaceDestroysKeysCiphertextAndHistoryOnlyForTarget(t *testing.T) {
	vault, _ := newWorkspaceVault(t)
	ctx := context.Background()
	for workspace, value := range map[string]string{"ws_delete": "gone", "ws_keep": "present"} {
		if err := vault.Set(ctx, workspace, "agent", "token", []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := vault.Rotate(ctx, "ws_delete", "agent", "token"); err != nil {
		t.Fatal(err)
	}

	removed, err := vault.EraseWorkspace(ctx, "ws_delete")
	if err != nil {
		t.Fatal(err)
	}
	if removed < 3 { // wrapped DEK, current credential, version history
		t.Fatalf("erasure removed %d rows, want key, ciphertext, and history", removed)
	}
	if _, err := vault.Get(ctx, "ws_delete", "agent", "token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("erased secret read = %v, want ErrNotFound", err)
	}
	if got, err := vault.Get(ctx, "ws_keep", "agent", "token"); err != nil || string(got) != "present" {
		t.Fatalf("neighbouring workspace changed: %q %v", got, err)
	}
	for _, table := range []string{"workspace_data_keys", "credentials", "credential_versions"} {
		var count int
		if err := vault.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE workspace_id=?`, "ws_delete").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d erased-workspace rows", table, count)
		}
	}
}

// The workspace is part of the key derivation, not just the lookup. A database
// read that somehow escaped the workspace predicate still yields ciphertext
// the reader cannot open — the boundary survives a query bug.
func TestCiphertextIsNotDecryptableByAnotherWorkspacesKey(t *testing.T) {
	kms, err := NewLocalKMS()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keyA, err := kms.DeriveKey(ctx, "ws_a", "agent")
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := kms.DeriveKey(ctx, "ws_b", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(keyA, keyB) {
		t.Fatal("two workspaces derived the same encryption key")
	}

	ciphertext, err := encrypt(keyA, []byte("alpha secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(keyB, ciphertext); err == nil {
		t.Fatal("another workspace's key decrypted the ciphertext")
	}
	if plaintext, err := decrypt(keyA, ciphertext); err != nil || !bytes.Equal(plaintext, []byte("alpha secret")) {
		t.Fatalf("the owning workspace could not decrypt its own secret: %q %v", plaintext, err)
	}
}

// Product invariant 7, cryptographically: the personal workspace keeps the
// original derivation, so credentials an existing installation already stored
// still decrypt. Changing it would break them silently, at the moment an agent
// needed them.
func TestPersonalDerivationIsUnchanged(t *testing.T) {
	kms, err := NewLocalKMS()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	legacyInfo := []byte("soulacy-credential-agent")
	for _, workspaceID := range []string{"", wsroot.PersonalWorkspaceID} {
		if got := deriveInfo(workspaceID, "agent"); !bytes.Equal(got, legacyInfo) {
			t.Fatalf("personal derivation changed to %q", got)
		}
	}
	personal, err := kms.DeriveKey(ctx, wsroot.PersonalWorkspaceID, "agent")
	if err != nil {
		t.Fatal(err)
	}
	unscoped, err := kms.DeriveKey(ctx, "", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(personal, unscoped) {
		t.Fatal("an absent workspace derived a different key from the personal one")
	}
}

// A vault written before tenants existed keeps working: the primary key is
// rebuilt and rows are assigned to the personal workspace, whose derivation is
// unchanged, so the values still decrypt.
func TestExistingVaultMigratesAndStaysDecryptable(t *testing.T) {
	vault, path := newWorkspaceVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, wsroot.PersonalWorkspaceID, "agent", "token", []byte("written before tenants")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}

	kms, err := NewLocalKMS()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteVault(path, kms)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := reopened.Get(ctx, wsroot.PersonalWorkspaceID, "agent", "token")
	if err != nil {
		t.Fatalf("a pre-existing credential became unreadable: %v", err)
	}
	if !bytes.Equal(got, []byte("written before tenants")) {
		t.Fatalf("value changed across migration: %q", got)
	}
}

// Rotation keeps version history inside the owning workspace.
func TestRotationHistoryIsWorkspaceScoped(t *testing.T) {
	vault, _ := newWorkspaceVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "agent", "token", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(ctx, "ws_b", "agent", "token", []byte("other tenant")); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Rotate(ctx, "ws_a", "agent", "token"); err != nil {
		t.Fatal(err)
	}

	mine, err := vault.ListVersions(ctx, "ws_a", "agent", "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) == 0 {
		t.Fatal("the owning workspace lost its own version history")
	}
	theirs, err := vault.ListVersions(ctx, "ws_b", "agent", "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 0 {
		t.Fatalf("another workspace saw the rotation history: %+v", theirs)
	}
	// The other tenant's live value is untouched by the rotation.
	if got, err := vault.Get(ctx, "ws_b", "agent", "token"); err != nil || !bytes.Equal(got, []byte("other tenant")) {
		t.Fatalf("rotation in one workspace changed another: %q %v", got, err)
	}
}
