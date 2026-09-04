package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/pkgregistry"
)

func TestSignAgentPackageBytes(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "agent-package-key.hex")
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(priv.Seed())), 0600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":"soulacy.agent.package/v1","integrity":{"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}`)
	signed, err := signAgentPackageBytes(raw, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Integrity struct {
			SHA256    string `json:"sha256"`
			Signature string `json:"signature"`
			PublicKey string `json:"public_key"`
		} `json:"integrity"`
	}
	if err := json.Unmarshal(signed, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Integrity.PublicKey != hex.EncodeToString(pub) {
		t.Fatalf("public key = %q, want %q", pkg.Integrity.PublicKey, hex.EncodeToString(pub))
	}
	if pkg.Integrity.Signature == "" {
		t.Fatal("signature was not added")
	}
	if err := pkgregistry.VerifySignature(pub, pkg.Integrity.SHA256, pkg.Integrity.Signature); err != nil {
		t.Fatalf("signature did not verify: %v", err)
	}
}
