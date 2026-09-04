package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"
)

// TestEncryptRoundTrip encrypts a payload for a synthetic client subscription
// and decrypts it back using the client's private key, validating the full
// RFC 8291/8188 aes128gcm path.
func TestEncryptRoundTrip(t *testing.T) {
	curve := ecdh.P256()
	uaPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uaPublic := uaPriv.PublicKey().Bytes()
	authSecret := make([]byte, 16)
	rand.Read(authSecret)

	sub := Subscription{
		Endpoint: "https://push.example.com/x",
		Keys:     Keys{P256dh: b64Encode(uaPublic), Auth: b64Encode(authSecret)},
	}
	plaintext := []byte(`{"title":"Approval needed","body":"shell_exec"}`)

	body, err := encrypt(sub, plaintext, 4096)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Parse the aes128gcm header.
	if len(body) < 21 {
		t.Fatalf("body too short")
	}
	salt := body[0:16]
	idlen := int(body[20])
	off := 21
	asPublic := body[off : off+idlen]
	ciphertext := body[off+idlen:]

	asPub, err := curve.NewPublicKey(asPublic)
	if err != nil {
		t.Fatalf("parse as public: %v", err)
	}
	ecdhSecret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}

	keyInfo := append([]byte("WebPush: info\x00"), uaPublic...)
	keyInfo = append(keyInfo, asPublic...)
	ikm, _ := hkdf.Key(sha256.New, ecdhSecret, authSecret, string(keyInfo), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)

	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	record, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	// Strip the 0x02 delimiter.
	if len(record) == 0 || record[len(record)-1] != 0x02 {
		t.Fatalf("missing last-record delimiter")
	}
	got := record[:len(record)-1]
	if string(got) != string(plaintext) {
		t.Fatalf("round trip mismatch: %q != %q", got, plaintext)
	}
}

// TestVAPIDHeaderSignsAndVerifies checks the VAPID JWT is well-formed and its
// ES256 signature verifies against the generated public key.
func TestVAPIDHeaderSignsAndVerifies(t *testing.T) {
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	ecPriv, err := parsePrivate(priv)
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := vapidAuthHeader(ecPriv, pub, "mailto:test@example.com", "https://push.example.com/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hdr, "vapid t=") || !strings.Contains(hdr, ", k="+pub) {
		t.Fatalf("header malformed: %s", hdr)
	}
	jwt := strings.TrimPrefix(strings.SplitN(hdr, ", k=", 2)[0], "vapid t=")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt should have 3 parts, got %d", len(parts))
	}
	sig, err := b64Decode(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("bad signature: err=%v len=%d", err, len(sig))
	}

	// Verify with the public key point.
	pubRaw, _ := b64Decode(pub)
	if len(pubRaw) != 65 || pubRaw[0] != 0x04 {
		t.Fatalf("public key not uncompressed point")
	}
	x := new(big.Int).SetBytes(pubRaw[1:33])
	y := new(big.Int).SetBytes(pubRaw[33:65])
	pk := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[0:32])
	s := new(big.Int).SetBytes(sig[32:64])
	if !ecdsa.Verify(pk, digest[:], r, s) {
		t.Fatalf("VAPID signature failed to verify")
	}
}

func TestGenerateVAPIDKeys_Sizes(t *testing.T) {
	pub, priv, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := b64Decode(pub)
	if len(pb) != 65 {
		t.Fatalf("public key = %d bytes, want 65", len(pb))
	}
	db, _ := b64Decode(priv)
	if len(db) != 32 {
		t.Fatalf("private key = %d bytes, want 32", len(db))
	}
	_ = binary.BigEndian // keep import parity with encrypt.go expectations
}
