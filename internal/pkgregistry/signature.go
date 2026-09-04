package pkgregistry

// Ed25519 package signatures (E18/E19 follow-up).
//
// Contract: the registry operator signs the RAW 32 BYTES of the package
// archive's sha256 digest (the decoded checksum, not its hex string) with
// an ed25519 private key; Package.Signature carries the signature
// base64-encoded (std encoding). Operators publish the 32-byte public key
// hex-encoded as the `signing_key` of a registries: config entry. A
// registry with a signing_key configured REQUIRES every package to carry a
// valid signature — unsigned packages are refused, never silently waved
// through.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// SignChecksum signs a package's sha256 hex checksum with priv and returns
// the base64 signature. Used by the reference registry server.
func SignChecksum(priv ed25519.PrivateKey, checksumHex string) (string, error) {
	digest, err := hex.DecodeString(checksumHex)
	if err != nil || len(digest) != 32 {
		return "", fmt.Errorf("pkgregistry: checksum is not a sha256 hex digest")
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, digest)), nil
}

// VerifySignature checks sigB64 against checksumHex under pub.
func VerifySignature(pub ed25519.PublicKey, checksumHex, sigB64 string) error {
	digest, err := hex.DecodeString(checksumHex)
	if err != nil || len(digest) != 32 {
		return fmt.Errorf("pkgregistry: checksum is not a sha256 hex digest")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("pkgregistry: signature is not valid base64: %w", err)
	}
	if !ed25519.Verify(pub, digest, sig) {
		return fmt.Errorf("pkgregistry: signature verification FAILED — archive or signature tampered, or wrong signing key")
	}
	return nil
}

// parseSigningKey decodes a hex-encoded 32-byte ed25519 public key.
func parseSigningKey(hexKey string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("pkgregistry: signing_key is not hex: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pkgregistry: signing_key must be %d bytes (got %d)", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
