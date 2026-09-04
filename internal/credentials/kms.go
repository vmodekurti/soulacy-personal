package credentials

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// machineSecretFile is the name of the persisted master-secret file written
// next to the credential vault when no hardware machine id is available.
const machineSecretFile = ".machine-secret"

// KMSProvider derives or retrieves the AES-256 encryption key for a given agentID.
type KMSProvider interface {
	// DeriveKey returns a 32-byte AES key for the given agentID.
	DeriveKey(ctx context.Context, agentID string) ([]byte, error)
}

// ---------------------------------------------------------------------------
// LocalKMS
// ---------------------------------------------------------------------------

// LocalKMS derives per-agent keys using HKDF-SHA256 keyed from a hardware-
// derived master secret. The master secret is resolved once at construction.
type LocalKMS struct {
	masterSecret []byte
}

// NewLocalKMS creates a LocalKMS. It attempts to derive the master secret from
// the platform hardware ID (IOPlatformUUID on macOS, /etc/machine-id on Linux).
// If neither is available a random ephemeral secret is used and a warning is
// written to stderr.
func NewLocalKMS() (*LocalKMS, error) {
	secret, err := platformSecret()
	if err != nil {
		// Fallback: random ephemeral secret. Log the warning via stderr
		// (zap not available here without an import cycle).
		fmt.Fprintln(os.Stderr, "soulacy/credentials: WARNING — could not derive hardware machine secret; using ephemeral random key. Credentials will not survive process restarts.")
		secret = make([]byte, 32)
		if _, rerr := io.ReadFull(rand.Reader, secret); rerr != nil {
			return nil, fmt.Errorf("credentials: generate fallback secret: %w", rerr)
		}
	}
	return &LocalKMS{masterSecret: secret}, nil
}

// NewLocalKMSWithStore creates a LocalKMS that survives restarts even without a
// hardware machine id (the common case inside containers, where /etc/machine-id
// is absent or empty). Resolution order:
//
//  1. Platform hardware id (IOPlatformUUID on macOS, /etc/machine-id on Linux).
//  2. A persisted random secret stored at storeDir/.machine-secret. Created on
//     first run and reused thereafter, so credentials encrypted with it can be
//     decrypted across restarts as long as storeDir persists (it lives next to
//     the credential vault, which is already on the data volume).
//  3. As a last resort, an ephemeral random secret (credentials won't survive
//     a restart) — only when storeDir can't be read or written.
//
// storeDir is typically the directory containing the credential vault DB. An
// empty storeDir skips step 2 and matches the legacy NewLocalKMS behaviour.
func NewLocalKMSWithStore(storeDir string) (*LocalKMS, error) {
	if secret, err := platformSecret(); err == nil {
		return &LocalKMS{masterSecret: secret}, nil
	}
	if storeDir != "" {
		if secret, err := loadOrCreatePersistedSecret(storeDir); err == nil {
			fmt.Fprintln(os.Stderr, "soulacy/credentials: no hardware machine id; using a persisted key file under the workspace. Credentials will survive restarts as long as the data volume persists.")
			return &LocalKMS{masterSecret: secret}, nil
		}
	}
	fmt.Fprintln(os.Stderr, "soulacy/credentials: WARNING — could not derive or persist a machine secret; using ephemeral random key. Credentials will not survive process restarts.")
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return nil, fmt.Errorf("credentials: generate fallback secret: %w", err)
	}
	return &LocalKMS{masterSecret: secret}, nil
}

// loadOrCreatePersistedSecret returns the hex-encoded master secret stored in
// dir, creating a fresh random one (0600) on first use. The returned bytes are
// the file contents verbatim, so reads and writes are byte-identical and HKDF
// derivation stays stable across restarts.
func loadOrCreatePersistedSecret(dir string) ([]byte, error) {
	path := filepath.Join(dir, machineSecretFile)
	if data, err := os.ReadFile(path); err == nil {
		if s := bytes.TrimSpace(data); len(s) >= 64 {
			return s, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return nil, fmt.Errorf("credentials: generate persisted secret: %w", err)
	}
	enc := make([]byte, hex.EncodedLen(len(raw)))
	hex.Encode(enc, raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("credentials: create secret dir: %w", err)
	}
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		return nil, fmt.Errorf("credentials: write persisted secret: %w", err)
	}
	return enc, nil
}

// DeriveKey returns a 32-byte AES-256 key for the given agentID via HKDF-SHA256.
func (k *LocalKMS) DeriveKey(_ context.Context, agentID string) ([]byte, error) {
	info := []byte("soulacy-credential-" + agentID)
	r := hkdf.New(sha256.New, k.masterSecret, nil, info)
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("credentials: hkdf derive: %w", err)
	}
	return key, nil
}

// platformSecret returns the hardware-bound machine identifier as raw bytes.
func platformSecret() ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return machinePlatformUUID()
	case "linux":
		return linuxMachineID()
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// machinePlatformUUID reads IOPlatformUUID from ioreg output on macOS.
var ioregUUIDRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

func machinePlatformUUID() ([]byte, error) {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	matches := ioregUUIDRe.FindSubmatch(out)
	if len(matches) < 2 {
		return nil, fmt.Errorf("IOPlatformUUID not found in ioreg output")
	}
	return []byte(strings.TrimSpace(string(matches[1]))), nil
}

// linuxMachineID reads /etc/machine-id.
func linuxMachineID() ([]byte, error) {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return nil, fmt.Errorf("read /etc/machine-id: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return nil, fmt.Errorf("/etc/machine-id is empty")
	}
	return []byte(id), nil
}

// ---------------------------------------------------------------------------
// PassthroughKMS
// ---------------------------------------------------------------------------

// PassthroughKMS returns the same fixed 32-byte key for every agentID.
// Intended for testing or explicit key management scenarios.
type PassthroughKMS struct {
	key []byte
}

// NewPassthroughKMS creates a PassthroughKMS with the given 32-byte key.
// Returns an error if the key is not exactly 32 bytes.
func NewPassthroughKMS(key []byte) (*PassthroughKMS, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("credentials: passthrough KMS key must be 32 bytes, got %d", len(key))
	}
	k := make([]byte, 32)
	copy(k, key)
	return &PassthroughKMS{key: k}, nil
}

// DeriveKey returns the fixed key regardless of agentID.
func (p *PassthroughKMS) DeriveKey(_ context.Context, _ string) ([]byte, error) {
	out := make([]byte, 32)
	copy(out, p.key)
	return out, nil
}
