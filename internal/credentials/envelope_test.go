// envelope_test.go — MU-015 part 2.
//
// The property the derived-key design could not have is ROTATION WITHOUT DATA
// LOSS, so that is what most of these test: a rotated workspace keeps every
// existing credential readable, and new writes land under the new key.
//
// The second property is that a legacy vault keeps working forever. Not "until
// a migration runs" — forever — because a vault restored from a backup taken
// before this change contains version-0 rows, and a build that could not read
// them would turn a restore into data loss.
package credentials

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func envelopeVault(t *testing.T) (*SQLiteVault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	kms, err := NewPassthroughKMS(key)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := NewSQLiteVault(path, kms)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	if vault.keys == nil {
		t.Fatal("the vault did not enable envelope encryption for a wrapping KMS")
	}
	return vault, path
}

// derivedOnlyKMS implements KMSProvider and NOT KeyWrapper, which is what a
// deployment with a bespoke KMS supplied before this change looks like.
type derivedOnlyKMS struct{ key []byte }

func (k derivedOnlyKMS) DeriveKey(context.Context, string, string) ([]byte, error) {
	return k.key, nil
}

func legacyVault(t *testing.T, path string) *SQLiteVault {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	vault, err := NewSQLiteVault(path, derivedOnlyKMS{key: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	if vault.keys != nil {
		t.Fatal("a KMS that cannot wrap enabled envelope encryption")
	}
	return vault
}

// THE PROPERTY. Rotate, and everything written before is still readable while
// everything written after uses the new key.
func TestRotatingAWorkspaceKeyKeepsExistingCredentialsReadable(t *testing.T) {
	vault, _ := envelopeVault(t)
	ctx := context.Background()

	if err := vault.Set(ctx, "ws_a", "bot", "api_key", []byte("before-rotation")); err != nil {
		t.Fatal(err)
	}
	before, err := vault.RotateWorkspaceKey(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if before < 2 {
		t.Fatalf("rotation produced version %d, want at least 2", before)
	}

	got, err := vault.Get(ctx, "ws_a", "bot", "api_key")
	if err != nil {
		t.Fatalf("a credential written before rotation is no longer readable: %v", err)
	}
	if string(got) != "before-rotation" {
		t.Fatalf("value = %q", got)
	}

	// A new write uses the new version, which is what makes the rotation take
	// effect without a migration that could fail halfway.
	if err := vault.Set(ctx, "ws_a", "bot", "second", []byte("after-rotation")); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := vault.db.QueryRow(
		`SELECT key_version FROM credentials WHERE workspace_id='ws_a' AND agent_id='bot' AND key='second'`).
		Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != before {
		t.Fatalf("a post-rotation write used version %d, want %d", version, before)
	}
	// And the pre-rotation row still names the old version, rather than having
	// been quietly rewritten.
	var old int
	if err := vault.db.QueryRow(
		`SELECT key_version FROM credentials WHERE workspace_id='ws_a' AND agent_id='bot' AND key='api_key'`).
		Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old >= before {
		t.Fatalf("rotation rewrote existing rows (version %d); it must be a key operation, not a data migration", old)
	}
}

// Re-encryption is the separate, resumable operation that closes out an old
// key. Running it twice must be harmless, because it is the thing an operator
// re-runs after it failed partway.
func TestReencryptionMovesEveryRowAndIsSafeToRepeat(t *testing.T) {
	vault, _ := envelopeVault(t)
	ctx := context.Background()
	for _, key := range []string{"one", "two", "three"} {
		if err := vault.Set(ctx, "ws_a", "bot", key, []byte("value-"+key)); err != nil {
			t.Fatal(err)
		}
	}
	newVersion, err := vault.RotateWorkspaceKey(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}

	moved, err := vault.ReencryptWorkspace(ctx, "ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if moved != 3 {
		t.Fatalf("re-encrypted %d rows, want 3", moved)
	}
	rows, err := vault.db.Query(`SELECT key, key_version FROM credentials WHERE workspace_id='ws_a'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var version int
		if err := rows.Scan(&key, &version); err != nil {
			t.Fatal(err)
		}
		if version != newVersion {
			t.Errorf("%s is still at version %d after re-encryption", key, version)
		}
	}
	// Values survive, and a second pass is harmless.
	for _, key := range []string{"one", "two", "three"} {
		got, err := vault.Get(ctx, "ws_a", "bot", key)
		if err != nil || string(got) != "value-"+key {
			t.Fatalf("%s = %q, %v", key, got, err)
		}
	}
	if _, err := vault.ReencryptWorkspace(ctx, "ws_a"); err != nil {
		t.Fatalf("a repeated re-encryption failed: %v", err)
	}
}

// A vault written before envelope encryption stays readable, forever. Not
// "until a migration runs": a restore from an old backup contains version-0
// rows, and a build that could not read them would turn a restore into data
// loss.
func TestALegacyVaultStaysReadableAndUpgradesOnWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	ctx := context.Background()

	// Written by a build with no wrapping KMS.
	old := legacyVault(t, path)
	if err := old.Set(ctx, "ws_a", "bot", "api_key", []byte("written-before")); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopened by a build that CAN wrap. The same key material derives the
	// same legacy key, so the old row opens.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	kms, err := NewPassthroughKMS(key)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := NewSQLiteVault(path, kms)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()

	got, err := upgraded.Get(ctx, "ws_a", "bot", "api_key")
	if err != nil {
		t.Fatalf("a legacy credential is unreadable after upgrade: %v", err)
	}
	if string(got) != "written-before" {
		t.Fatalf("value = %q", got)
	}
	// Still labelled legacy — nothing rewrote it behind the operator's back.
	var version int
	if err := upgraded.db.QueryRow(
		`SELECT key_version FROM credentials WHERE workspace_id='ws_a' AND agent_id='bot' AND key='api_key'`).
		Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != legacyKeyVersion {
		t.Fatalf("the legacy row was rewritten to version %d on open", version)
	}
	// A write moves it to an envelope.
	if err := upgraded.Set(ctx, "ws_a", "bot", "api_key", []byte("written-after")); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(
		`SELECT key_version FROM credentials WHERE workspace_id='ws_a' AND agent_id='bot' AND key='api_key'`).
		Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version == legacyKeyVersion {
		t.Fatal("a write to a legacy row left it legacy-encrypted")
	}
}

// The AAD binds a ciphertext to its row. Without it a database write that
// swaps two rows' ciphertext produces two credentials that decrypt fine and
// hold each other's values — which for a credential means an agent quietly
// authenticating as something else.
func TestACiphertextMovedToAnotherRowDoesNotOpen(t *testing.T) {
	vault, _ := envelopeVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "bot", "innocent", []byte("innocent-value")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(ctx, "ws_a", "bot", "privileged", []byte("privileged-value")); err != nil {
		t.Fatal(err)
	}

	// Swap the two ciphertexts, as a compromised writer or a bad restore would.
	var privileged []byte
	if err := vault.db.QueryRow(
		`SELECT ciphertext FROM credentials WHERE workspace_id='ws_a' AND agent_id='bot' AND key='privileged'`).
		Scan(&privileged); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.db.Exec(
		`UPDATE credentials SET ciphertext=? WHERE workspace_id='ws_a' AND agent_id='bot' AND key='innocent'`,
		privileged); err != nil {
		t.Fatal(err)
	}

	if _, err := vault.Get(ctx, "ws_a", "bot", "innocent"); err == nil {
		t.Fatal("a ciphertext moved to another row decrypted successfully")
	}
}

// Two workspaces get different data keys, so a row moved between them is
// unreadable even though both are in the same table.
func TestOneWorkspacesCiphertextIsUnreadableInAnother(t *testing.T) {
	vault, _ := envelopeVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "bot", "api_key", []byte("a-secret")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(ctx, "ws_b", "bot", "api_key", []byte("b-secret")); err != nil {
		t.Fatal(err)
	}

	var aCipher []byte
	var aVersion int
	if err := vault.db.QueryRow(
		`SELECT ciphertext, key_version FROM credentials WHERE workspace_id='ws_a' AND agent_id='bot' AND key='api_key'`).
		Scan(&aCipher, &aVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.db.Exec(
		`UPDATE credentials SET ciphertext=?, key_version=? WHERE workspace_id='ws_b' AND agent_id='bot' AND key='api_key'`,
		aCipher, aVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Get(ctx, "ws_b", "bot", "api_key"); err == nil {
		t.Fatal("one workspace's ciphertext opened under another workspace's key")
	}
}

// A wrapped data key moved between workspaces must not unwrap, or the
// per-workspace key boundary is decoration.
func TestAWrappedDataKeyDoesNotUnwrapInAnotherWorkspace(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	kms, err := NewPassthroughKMS(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dek := []byte("0123456789abcdef0123456789abcdef")

	wrapped, err := kms.WrapKey(ctx, "ws_a", dek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kms.UnwrapKey(ctx, "ws_b", wrapped); err == nil {
		t.Fatal("a data key wrapped for one workspace unwrapped in another")
	}
	round, err := kms.UnwrapKey(ctx, "ws_a", wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if string(round) != string(dek) {
		t.Fatal("the data key did not round-trip")
	}
}

// A version the catalog does not have is a distinct failure from a key that
// exists and does not open the data: one is a catalog problem an operator can
// act on, the other is corruption or tampering.
func TestAMissingKeyVersionIsReportedAsMissingNotAsCorruption(t *testing.T) {
	vault, _ := envelopeVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "bot", "api_key", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.db.Exec(
		`UPDATE credentials SET key_version=99 WHERE workspace_id='ws_a' AND agent_id='bot' AND key='api_key'`); err != nil {
		t.Fatal(err)
	}
	_, err := vault.Get(ctx, "ws_a", "bot", "api_key")
	if !errors.Is(err, ErrNoDataKey) {
		t.Fatalf("a row naming an unknown key version reported %v, want ErrNoDataKey", err)
	}
}

// An envelope-encrypted row read by a build whose KMS cannot unwrap must say
// so, rather than surfacing as a decryption error somebody reads as
// corruption: the data is fine and the KMS is wrong.
func TestAnEnvelopeRowReadWithoutAWrapperSaysWhatIsWrong(t *testing.T) {
	vault, path := envelopeVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "bot", "api_key", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}

	downgraded := legacyVault(t, path)
	_, err := downgraded.Get(ctx, "ws_a", "bot", "api_key")
	if err == nil {
		t.Fatal("an enveloped credential was read by a vault that cannot unwrap")
	}
	if !strings.Contains(err.Error(), "cannot unwrap") {
		t.Fatalf("the error blames decryption rather than the KMS: %v", err)
	}
}

// Rotating a workspace that has never stored a credential mints its first key
// rather than failing — otherwise "rotate everything" fails on any workspace
// that has not been used yet.
func TestRotatingAnUnusedWorkspaceMintsItsFirstKey(t *testing.T) {
	vault, _ := envelopeVault(t)
	version, err := vault.RotateWorkspaceKey(context.Background(), "ws_untouched")
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
}

// Exactly one key is un-retired at a time. Two would make "the newest key"
// arbitrary between them, so a credential written under one could be opened
// with the other on a different process.
func TestOnlyOneDataKeyIsActiveAtATime(t *testing.T) {
	vault, _ := envelopeVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "bot", "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := vault.RotateWorkspaceKey(ctx, "ws_a"); err != nil {
			t.Fatal(err)
		}
	}
	var active int
	if err := vault.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_data_keys WHERE workspace_id='ws_a' AND retired_at IS NULL`).
		Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("%d data keys are active, want exactly 1", active)
	}
	// And the retired ones are still present, because deleting them would make
	// rotation a data-loss event.
	var total int
	if err := vault.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_data_keys WHERE workspace_id='ws_a'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("%d data keys retained, want 4 — a retired key was deleted", total)
	}
}

// THE CORRUPTION THIS AVOIDS. A vault whose SCHEMA has key_version but whose
// KMS cannot wrap still writes the column — explicitly to 0 — when it stores
// legacy ciphertext over an enveloped row.
//
// The alternative, branching the write on the KMS instead of the schema, uses
// an INSERT that omits key_version. On conflict that leaves the column at its
// OLD value while replacing the ciphertext with legacy bytes, so the row claims
// to be enveloped and is not. Nothing can ever read it again — and nothing
// reports an error at the moment it happens.
func TestANonWrappingBuildOverwritingAnEnvelopeRowLeavesItReadable(t *testing.T) {
	vault, path := envelopeVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "bot", "api_key", []byte("enveloped")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}

	// A build that cannot wrap, opening a vault that already has the column.
	downgraded := legacyVault(t, path)
	if err := downgraded.Set(ctx, "ws_a", "bot", "api_key", []byte("rewritten")); err != nil {
		t.Fatal(err)
	}
	got, err := downgraded.Get(ctx, "ws_a", "bot", "api_key")
	if err != nil {
		t.Fatalf("the row it just wrote is unreadable: %v", err)
	}
	if string(got) != "rewritten" {
		t.Fatalf("value = %q", got)
	}
	var version int
	if err := downgraded.db.QueryRow(
		`SELECT key_version FROM credentials WHERE workspace_id='ws_a' AND agent_id='bot' AND key='api_key'`).
		Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != legacyKeyVersion {
		t.Fatalf("the row still claims key version %d while holding legacy ciphertext — "+
			"nothing will ever read it again", version)
	}
}

// A retired key still opens what was written under it. Deleting it instead
// would make rotation a data-loss event, which is the failure the derived-key
// design had.
func TestARetiredKeyStillOpensItsCiphertext(t *testing.T) {
	vault, _ := envelopeVault(t)
	ctx := context.Background()
	if err := vault.Set(ctx, "ws_a", "bot", "old", []byte("under-v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.RotateWorkspaceKey(ctx, "ws_a"); err != nil {
		t.Fatal(err)
	}
	var retired int
	if err := vault.db.QueryRow(
		`SELECT COUNT(*) FROM workspace_data_keys WHERE workspace_id='ws_a' AND retired_at IS NOT NULL`).
		Scan(&retired); err != nil {
		t.Fatal(err)
	}
	if retired != 1 {
		t.Fatalf("%d keys retired, want 1 — a rotation deleted rather than retired", retired)
	}
	got, err := vault.Get(ctx, "ws_a", "bot", "old")
	if err != nil || string(got) != "under-v1" {
		t.Fatalf("a retired key no longer opens its ciphertext: %q %v", got, err)
	}
}

// The wrapping key is workspace-bound on the local KMS too, not only on the
// passthrough one used elsewhere in these tests.
func TestTheLocalKMSBindsAWrappedKeyToItsWorkspace(t *testing.T) {
	kms, err := NewLocalKMSWithStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dek := []byte("0123456789abcdef0123456789abcdef")
	wrapped, err := kms.WrapKey(ctx, "ws_a", dek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kms.UnwrapKey(ctx, "ws_b", wrapped); err == nil {
		t.Fatal("a data key wrapped for one workspace unwrapped in another")
	}
	round, err := kms.UnwrapKey(ctx, "ws_a", wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if string(round) != string(dek) {
		t.Fatal("the data key did not round-trip")
	}

	// And the wrapping key is not any agent's credential key, so compromising
	// one agent does not unwrap the workspace.
	agentKey, err := kms.DeriveKey(ctx, "ws_a", "wrap")
	if err != nil {
		t.Fatal(err)
	}
	wrappingKey, err := kms.DeriveKey(ctx, "ws_a", wrapAgentSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(agentKey) == string(wrappingKey) {
		t.Fatal("an agent literally named \"wrap\" shares the workspace's wrapping key")
	}
}
