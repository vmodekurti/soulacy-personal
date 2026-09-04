// kms_test.go — tests for the KMS provider implementations.
// Focuses on PassthroughKMS (fully deterministic, no OS calls) and the
// pure-logic parts of LocalKMS.DeriveKey. platformSecret() is OS-dependent
// and not directly tested here.
package credentials

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
)

// ---------------------------------------------------------------------------
// NewPassthroughKMS
// ---------------------------------------------------------------------------

func TestNewPassthroughKMSRejectsNon32ByteKey(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		key := make([]byte, n)
		_, err := NewPassthroughKMS(key)
		if err == nil {
			t.Errorf("NewPassthroughKMS(%d bytes): expected error, got nil", n)
		}
	}
}

func TestNewPassthroughKMSAccepts32ByteKey(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	kms, err := NewPassthroughKMS(key)
	if err != nil {
		t.Fatalf("NewPassthroughKMS: %v", err)
	}
	if kms == nil {
		t.Fatal("kms is nil")
	}
}

func TestNewPassthroughKMSCopiesKey(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	original := make([]byte, 32)
	copy(original, key)

	kms, _ := NewPassthroughKMS(key)
	// Mutate original key — stored key must be independent.
	key[0] ^= 0xFF

	derived, _ := kms.DeriveKey(context.Background(), "any-agent")
	if !bytes.Equal(derived, original) {
		t.Error("PassthroughKMS did not copy the key — mutation affected stored key")
	}
}

// ---------------------------------------------------------------------------
// PassthroughKMS.DeriveKey
// ---------------------------------------------------------------------------

func TestPassthroughKMSDeriveKeyReturnsCopyOfKey(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	kms, _ := NewPassthroughKMS(key)

	d1, err := kms.DeriveKey(context.Background(), "agent-a")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if !bytes.Equal(d1, key) {
		t.Error("derived key does not match original")
	}

	// Returned slice must be a copy — mutating it must not affect future calls.
	d1[0] ^= 0xFF
	d2, _ := kms.DeriveKey(context.Background(), "agent-a")
	if bytes.Equal(d1, d2) {
		t.Error("DeriveKey returned aliased slice — mutation affected next call")
	}
}

func TestPassthroughKMSDeriveKeySameForAnyAgentID(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	kms, _ := NewPassthroughKMS(key)

	d1, _ := kms.DeriveKey(context.Background(), "agent-alpha")
	d2, _ := kms.DeriveKey(context.Background(), "agent-beta")
	if !bytes.Equal(d1, d2) {
		t.Error("PassthroughKMS should return the same key for any agentID")
	}
}

// ---------------------------------------------------------------------------
// LocalKMS.DeriveKey (deterministic from a fixed master secret)
// ---------------------------------------------------------------------------

func TestLocalKMSDeriveKeyDifferentAgentsProduceDifferentKeys(t *testing.T) {
	// Build a LocalKMS with a known master secret via the internal struct.
	// We can't call NewLocalKMS() safely (platformSecret is OS-specific),
	// so we construct directly via the exported type.
	master := make([]byte, 32)
	rand.Read(master)
	kms := &LocalKMS{masterSecret: master}

	d1, err := kms.DeriveKey(context.Background(), "agent-one")
	if err != nil {
		t.Fatalf("DeriveKey agent-one: %v", err)
	}
	d2, err := kms.DeriveKey(context.Background(), "agent-two")
	if err != nil {
		t.Fatalf("DeriveKey agent-two: %v", err)
	}
	if bytes.Equal(d1, d2) {
		t.Error("different agentIDs should produce different derived keys")
	}
}

func TestLocalKMSDeriveKeySameAgentIsDeterministic(t *testing.T) {
	master := make([]byte, 32)
	rand.Read(master)
	kms := &LocalKMS{masterSecret: master}

	d1, _ := kms.DeriveKey(context.Background(), "stable-agent")
	d2, _ := kms.DeriveKey(context.Background(), "stable-agent")
	if !bytes.Equal(d1, d2) {
		t.Error("same agentID must always produce the same derived key")
	}
}

func TestLocalKMSDeriveKeyIs32Bytes(t *testing.T) {
	master := make([]byte, 32)
	rand.Read(master)
	kms := &LocalKMS{masterSecret: master}

	d, err := kms.DeriveKey(context.Background(), "any")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if len(d) != 32 {
		t.Errorf("derived key length = %d, want 32", len(d))
	}
}
