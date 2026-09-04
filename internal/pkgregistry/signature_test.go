package pkgregistry

// E18/E19 follow-up: ed25519 package signatures. The registry signs the
// archive's sha256 digest; clients with a signing_key configured for that
// registry verify on Fetch and refuse mismatches OR unsigned packages.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestSignVerifyChecksum_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	checksum := strings.Repeat("ab", 32) // valid hex sha256

	sig, err := SignChecksum(priv, checksum)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySignature(pub, checksum, sig); err != nil {
		t.Errorf("valid signature refused: %v", err)
	}
	// Tampered checksum → refused.
	if err := VerifySignature(pub, strings.Repeat("cd", 32), sig); err == nil {
		t.Error("signature over different digest must be refused")
	}
	// Garbage signature → refused.
	if err := VerifySignature(pub, checksum, "not-base64!!"); err == nil {
		t.Error("malformed signature must be refused")
	}
	// Wrong key → refused.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifySignature(otherPub, checksum, sig); err == nil {
		t.Error("signature from another key must be refused")
	}
}

// signedRegistry serves one signed package.
func signedRegistry(t *testing.T, slug, checksum, signature string, archive []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/v1/packages/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"slug": slug, "version": "1.0.0", "checksum": checksum,
			"signature": signature, "source": srv.URL + "/a.tar.gz",
		})
	})
	mux.HandleFunc("/a.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPProvider_SignatureVerification(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	archive, checksum := makeTarGz(t, map[string]string{"SKILL.md": "# signed"})
	sig, err := SignChecksum(priv, checksum)
	if err != nil {
		t.Fatal(err)
	}
	srv := signedRegistry(t, "signed-skill", checksum, sig, archive)

	p, err := newHTTPProvider(map[string]any{
		"id": "signed", "base_url": srv.URL,
		"signing_key": hex.EncodeToString(pub),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.VerifiesSignatures() {
		t.Error("provider with signing_key must report VerifiesSignatures")
	}

	ctx := context.Background()
	pkg, err := p.Resolve(ctx, "signed-skill")
	if err != nil {
		t.Fatal(err)
	}

	// Happy path: signature verifies, archive extracts.
	dst := t.TempDir()
	if err := p.Fetch(ctx, pkg, dst); err != nil {
		t.Fatalf("Fetch signed package: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		t.Errorf("extraction missing: %v", err)
	}

	// Tampered signature → refused before extraction.
	bad := pkg
	otherSig, _ := SignChecksum(priv, strings.Repeat("00", 32))
	bad.Signature = otherSig
	if err := p.Fetch(ctx, bad, t.TempDir()); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("tampered signature: err = %v, want signature refusal", err)
	}

	// Unsigned package against a key-configured registry → refused.
	bad = pkg
	bad.Signature = ""
	if err := p.Fetch(ctx, bad, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Errorf("unsigned package: err = %v, want unsigned refusal", err)
	}
}

func TestHTTPProvider_NoKeyIgnoresSignature(t *testing.T) {
	// Without signing_key the provider works as before (checksum only) and
	// reports that it cannot verify.
	archive, checksum := makeTarGz(t, map[string]string{"SKILL.md": "x"})
	srv := signedRegistry(t, "s", checksum, "whatever-signature", archive)
	p, err := newHTTPProvider(map[string]any{"id": "plain", "base_url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if p.VerifiesSignatures() {
		t.Error("provider without signing_key must not claim verification")
	}
	pkg, err := p.Resolve(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Fetch(context.Background(), pkg, t.TempDir()); err != nil {
		t.Errorf("Fetch without key must still work on checksum alone: %v", err)
	}
}

func TestHTTPProvider_BadSigningKeyRefusedAtConstruction(t *testing.T) {
	if _, err := newHTTPProvider(map[string]any{
		"id": "x", "base_url": "https://r.example.com", "signing_key": "zz-not-hex",
	}); err == nil {
		t.Error("malformed signing_key must error at construction, not at first fetch")
	}
	if _, err := newHTTPProvider(map[string]any{
		"id": "x", "base_url": "https://r.example.com", "signing_key": "abcd", // wrong length
	}); err == nil {
		t.Error("wrong-length signing_key must error at construction")
	}
}

func TestEngine_SignatureVerificationLookup(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	eng, errs := FromConfig([]config.RegistryConfig{
		{ID: "signed", Type: "http", BaseURL: "https://a.example.com",
			SigningKey: hex.EncodeToString(pub), Priority: 1},
		{ID: "plain", Type: "http", BaseURL: "https://b.example.com", Priority: 2},
		{ID: "git", Type: "git", Priority: 3},
	}, nil)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if !eng.VerifiesSignatures("signed") {
		t.Error("signed registry must report verification")
	}
	if eng.VerifiesSignatures("plain") || eng.VerifiesSignatures("git") || eng.VerifiesSignatures("ghost") {
		t.Error("keyless/unknown providers must not report verification")
	}
}
