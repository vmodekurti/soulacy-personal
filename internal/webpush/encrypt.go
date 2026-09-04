// Package webpush implements the Web Push protocol end to end using only the Go
// standard library: message encryption per RFC 8291 (aes128gcm content coding
// from RFC 8188) and VAPID authentication per RFC 8292. This powers the mobile
// companion's push notifications (e.g. "a tool needs your approval") without
// pulling in any third-party dependency.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Keys are the client's public key material from a PushSubscription.
type Keys struct {
	P256dh string `json:"p256dh"` // base64url, uncompressed EC point (65 bytes)
	Auth   string `json:"auth"`   // base64url, 16-byte auth secret
}

// Subscription is a browser PushSubscription as delivered to the server.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     Keys   `json:"keys"`
}

// encrypt produces an RFC 8188 aes128gcm body encrypting plaintext for the given
// subscription, and returns the body. recordSize caps the single record; callers
// pass a value >= len(plaintext)+ overhead.
func encrypt(sub Subscription, plaintext []byte, recordSize uint32) ([]byte, error) {
	uaPublicRaw, err := b64Decode(sub.Keys.P256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: bad p256dh: %w", err)
	}
	authSecret, err := b64Decode(sub.Keys.Auth)
	if err != nil {
		return nil, fmt.Errorf("webpush: bad auth: %w", err)
	}
	if len(authSecret) != 16 {
		return nil, fmt.Errorf("webpush: auth secret must be 16 bytes, got %d", len(authSecret))
	}

	curve := ecdh.P256()
	uaPub, err := curve.NewPublicKey(uaPublicRaw)
	if err != nil {
		return nil, fmt.Errorf("webpush: invalid client public key: %w", err)
	}
	asPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPublicRaw := asPriv.PublicKey().Bytes() // uncompressed, 65 bytes

	ecdhSecret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: ecdh failed: %w", err)
	}

	// RFC 8291 §3.4: derive the input keying material.
	// key_info = "WebPush: info" || 0x00 || ua_public || as_public
	keyInfo := append([]byte("WebPush: info\x00"), uaPublicRaw...)
	keyInfo = append(keyInfo, asPublicRaw...)
	ikm, err := hkdf.Key(sha256.New, ecdhSecret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	// RFC 8188 §2.2: content-encryption key and nonce.
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Single record: plaintext followed by the 0x02 last-record delimiter.
	record := append(append([]byte{}, plaintext...), 0x02)
	ciphertext := gcm.Seal(nil, nonce, record, nil)

	// Assemble the aes128gcm header (RFC 8188 §2.1):
	//   salt(16) || rs(4, big-endian) || idlen(1) || keyid(as_public, 65)
	if recordSize < uint32(len(ciphertext))+16 {
		recordSize = uint32(len(ciphertext)) + 16
	}
	header := make([]byte, 0, 16+4+1+len(asPublicRaw)+len(ciphertext))
	header = append(header, salt...)
	rs := make([]byte, 4)
	binary.BigEndian.PutUint32(rs, recordSize)
	header = append(header, rs...)
	header = append(header, byte(len(asPublicRaw)))
	header = append(header, asPublicRaw...)
	header = append(header, ciphertext...)
	return header, nil
}
