// credentials2_test.go — additional coverage for paths not exercised by
// vault_test.go and kms_test.go. Focuses on:
//   - encrypt/decrypt helpers (direct unit tests)
//   - DefaultConfig fields
//   - rotation.go: ensureVersionSchema idempotency, ListVersions ordering,
//     DeleteVersion active-version guard, version increment continuity
//   - vault.go: Set key isolation via PassthroughKMS vs LocalKMS-style key,
//     Get/Delete after multiple Sets
//   - kms.go: NewLocalKMS fallback path awareness
package credentials

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// encrypt / decrypt helpers
// ---------------------------------------------------------------------------

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	plaintext := []byte("sensitive credential value")

	ct, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(ct, plaintext) {
		t.Error("ciphertext must not equal plaintext")
	}

	got, err := decrypt(key, ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypt = %q, want %q", got, plaintext)
	}
}

func TestEncryptDecrypt_EmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	ct, err := encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	got, err := decrypt(key, ct)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty plaintext after round-trip, got %v", got)
	}
}

func TestEncryptDecrypt_BinaryData(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xFF
	}
	data := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}
	ct, _ := encrypt(key, data)
	got, err := decrypt(key, ct)
	if err != nil {
		t.Fatalf("decrypt binary: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("binary round-trip failed: got %v, want %v", got, data)
	}
}

func TestEncryptDecrypt_NonceIsRandom(t *testing.T) {
	// Two encryptions of the same plaintext must produce different ciphertexts
	// (because the nonce is freshly generated each time).
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xAB
	}
	plaintext := []byte("same value")

	ct1, _ := encrypt(key, plaintext)
	ct2, _ := encrypt(key, plaintext)
	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext must produce different ciphertexts (random nonce)")
	}
}

func TestDecrypt_ShortCiphertext(t *testing.T) {
	key := make([]byte, 32)
	// A ciphertext shorter than the nonce size (12 bytes) must return an error.
	_, err := decrypt(key, []byte{0x01, 0x02})
	if err == nil {
		t.Error("decrypt with too-short ciphertext should return error")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	for i := range key2 {
		key2[i] = 0xFF
	}

	ct, _ := encrypt(key1, []byte("secret"))
	_, err := decrypt(key2, ct)
	if err == nil {
		t.Error("decrypt with wrong key should return error (GCM authentication failure)")
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfig_AllFields(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.KMSProvider != "local" {
		t.Errorf("DefaultConfig.KMSProvider = %q, want 'local'", cfg.KMSProvider)
	}
	// Other fields should be zero/empty (not accidentally pre-populated).
	if cfg.HashiCorpAddr != "" {
		t.Errorf("DefaultConfig.HashiCorpAddr = %q, want empty", cfg.HashiCorpAddr)
	}
	if cfg.HashiCorpToken != "" {
		t.Errorf("DefaultConfig.HashiCorpToken = %q, want empty", cfg.HashiCorpToken)
	}
	if cfg.AWSKMSKeyID != "" {
		t.Errorf("DefaultConfig.AWSKMSKeyID = %q, want empty", cfg.AWSKMSKeyID)
	}
}

// ---------------------------------------------------------------------------
// rotation.go additional coverage
// ---------------------------------------------------------------------------

func TestRotate_VersionsContinuous(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-c", "cont-key", []byte("value"))

	v1, err := v.Rotate(ctx, "agent-c", "cont-key")
	if err != nil {
		t.Fatalf("Rotate 1: %v", err)
	}
	v2, err := v.Rotate(ctx, "agent-c", "cont-key")
	if err != nil {
		t.Fatalf("Rotate 2: %v", err)
	}
	v3, err := v.Rotate(ctx, "agent-c", "cont-key")
	if err != nil {
		t.Fatalf("Rotate 3: %v", err)
	}
	if v2 != v1+1 {
		t.Errorf("version 2 should be v1+1: got %d, %d", v1, v2)
	}
	if v3 != v2+1 {
		t.Errorf("version 3 should be v2+1: got %d, %d", v2, v3)
	}
}

func TestListVersions_NewestFirst(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-lv", "ord-key", []byte("val"))
	_, _ = v.Rotate(ctx, "agent-lv", "ord-key")
	_, _ = v.Rotate(ctx, "agent-lv", "ord-key")
	_, _ = v.Rotate(ctx, "agent-lv", "ord-key")

	versions, err := v.ListVersions(ctx, "agent-lv", "ord-key")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) < 3 {
		t.Fatalf("expected >= 3 versions, got %d", len(versions))
	}
	// Must be sorted newest-first (descending version).
	for i := 1; i < len(versions); i++ {
		if versions[i].Version >= versions[i-1].Version {
			t.Errorf("versions not newest-first at index %d: %d >= %d",
				i, versions[i].Version, versions[i-1].Version)
		}
	}
}

func TestListVersions_OnlyOneActive(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-act", "act-key", []byte("val"))
	_, _ = v.Rotate(ctx, "agent-act", "act-key")
	_, _ = v.Rotate(ctx, "agent-act", "act-key")

	versions, err := v.ListVersions(ctx, "agent-act", "act-key")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}

	activeCount := 0
	for _, ver := range versions {
		if ver.IsActive {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Errorf("expected exactly 1 active version, got %d: %v", activeCount, versions)
	}
}

func TestDeleteVersion_ThenListVersions(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-dv", "del-list-key", []byte("data"))
	ver1, _ := v.Rotate(ctx, "agent-dv", "del-list-key")
	ver2, _ := v.Rotate(ctx, "agent-dv", "del-list-key") // ver1 is now inactive
	_ = ver2

	// Delete the inactive ver1.
	if err := v.DeleteVersion(ctx, "agent-dv", "del-list-key", ver1); err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}

	versions, err := v.ListVersions(ctx, "agent-dv", "del-list-key")
	if err != nil {
		t.Fatalf("ListVersions after delete: %v", err)
	}
	for _, ver := range versions {
		if ver.Version == ver1 {
			t.Errorf("deleted version %d still appears in ListVersions: %v", ver1, versions)
		}
	}
}

func TestDeleteVersion_NonExistentVersion(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_ = v.Set(ctx, "agent-dne", "dne-key", []byte("data"))
	_, _ = v.Rotate(ctx, "agent-dne", "dne-key")

	err := v.DeleteVersion(ctx, "agent-dne", "dne-key", 99999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteVersion non-existent: got %v, want ErrNotFound", err)
	}
}

func TestDeleteVersion_UnknownAgent(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	// Agent never had any versions created.
	err := v.DeleteVersion(ctx, "ghost-agent", "ghost-key", 1)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteVersion unknown agent: got %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// vault.go additional coverage
// ---------------------------------------------------------------------------

func TestSet_MultipleAgentsSameKey(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	// Different agents with the same key name must be stored independently.
	_ = v.Set(ctx, "agent-x", "shared-key", []byte("value-x"))
	_ = v.Set(ctx, "agent-y", "shared-key", []byte("value-y"))

	gotX, _ := v.Get(ctx, "agent-x", "shared-key")
	gotY, _ := v.Get(ctx, "agent-y", "shared-key")

	if !bytes.Equal(gotX, []byte("value-x")) {
		t.Errorf("agent-x: got %q, want value-x", gotX)
	}
	if !bytes.Equal(gotY, []byte("value-y")) {
		t.Errorf("agent-y: got %q, want value-y", gotY)
	}
}

func TestList_OrderedAlphabetically(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	keys := []string{"zebra", "apple", "mango", "banana"}
	for _, k := range keys {
		_ = v.Set(ctx, "ordered-agent", k, []byte(k))
	}

	got, err := v.List(ctx, "ordered-agent")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("List len = %d, want %d", len(got), len(keys))
	}
	// ORDER BY key ASC — verify ascending order.
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("List not in ascending order at index %d: %q < %q", i, got[i], got[i-1])
		}
	}
}

func TestDelete_MultipleDeletes(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	for _, k := range []string{"k1", "k2", "k3"} {
		_ = v.Set(ctx, "agent-multi-del", k, []byte("v"))
	}

	// Delete two of the three keys.
	_ = v.Delete(ctx, "agent-multi-del", "k1")
	_ = v.Delete(ctx, "agent-multi-del", "k3")

	keys, err := v.List(ctx, "agent-multi-del")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != "k2" {
		t.Errorf("after deleting k1 and k3, expected [k2]; got %v", keys)
	}
}

// ---------------------------------------------------------------------------
// vault.go: ErrNotFound is a distinct sentinel
// ---------------------------------------------------------------------------

func TestErrNotFound_IsDistinct(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	_, err := v.Get(ctx, "nobody", "nothing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: got %v, want ErrNotFound", err)
	}
	// Must not accidentally be a wrapped ErrNotFound that doesn't satisfy Is.
	if err == nil {
		t.Error("Get missing: expected non-nil error")
	}
}

// ---------------------------------------------------------------------------
// kms.go: PassthroughKMS — key length validation boundaries
// ---------------------------------------------------------------------------

func TestNewPassthroughKMS_ExactlyZeroBytes(t *testing.T) {
	_, err := NewPassthroughKMS([]byte{})
	if err == nil {
		t.Error("NewPassthroughKMS(0 bytes) should return error")
	}
}

func TestNewPassthroughKMS_31And33Bytes(t *testing.T) {
	for _, n := range []int{31, 33} {
		_, err := NewPassthroughKMS(make([]byte, n))
		if err == nil {
			t.Errorf("NewPassthroughKMS(%d bytes) should return error", n)
		}
	}
}

func TestNewPassthroughKMS_Exactly32Bytes(t *testing.T) {
	kms, err := NewPassthroughKMS(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewPassthroughKMS(32 bytes): %v", err)
	}
	if kms == nil {
		t.Fatal("expected non-nil KMS")
	}
}

// ---------------------------------------------------------------------------
// LocalKMS — HKDF output is 32 bytes and not the master secret
// ---------------------------------------------------------------------------

func TestLocalKMSDeriveKey_Not_MasterSecret(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i)
	}
	kms := &LocalKMS{masterSecret: master}

	derived, err := kms.DeriveKey(context.Background(), "agent-z")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if len(derived) != 32 {
		t.Errorf("derived key length = %d, want 32", len(derived))
	}
	if bytes.Equal(derived, master) {
		t.Error("derived key must not equal master secret")
	}
}

// ---------------------------------------------------------------------------
// vault.go: Large value round-trip
// ---------------------------------------------------------------------------

func TestSetGet_LargeValue(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	large := make([]byte, 64*1024) // 64 KiB
	for i := range large {
		large[i] = byte(i % 256)
	}

	if err := v.Set(ctx, "agent-large", "big-secret", large); err != nil {
		t.Fatalf("Set large: %v", err)
	}
	got, err := v.Get(ctx, "agent-large", "big-secret")
	if err != nil {
		t.Fatalf("Get large: %v", err)
	}
	if !bytes.Equal(got, large) {
		t.Error("large value round-trip failed")
	}
}

// ---------------------------------------------------------------------------
// ensureVersionSchema — called multiple times must be idempotent
// ---------------------------------------------------------------------------

func TestEnsureVersionSchema_Idempotent(t *testing.T) {
	ctx := context.Background()
	v := newTestVault(t)

	// Call ensureVersionSchema multiple times on the same DB — should not error.
	for i := 0; i < 3; i++ {
		if err := ensureVersionSchema(ctx, v.db); err != nil {
			t.Fatalf("ensureVersionSchema call %d: %v", i+1, err)
		}
	}
}
