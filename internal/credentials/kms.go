package credentials

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
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

// NewLocalKMSWithStore creates a LocalKMS that survives restarts and container
// replacement. Resolution order:
//
//  1. The persisted secret at storeDir/.machine-secret. It lives next to the
//     credential vault on the data volume, so a replacement container keeps
//     the same encryption key even when /etc/machine-id changes.
//  2. On the first run, the platform hardware id (IOPlatformUUID on macOS,
//     /etc/machine-id on Linux) is persisted and used. Persisting the exact
//     bytes preserves compatibility with vaults encrypted by older releases,
//     which used the platform id directly.
//  3. If no platform id is available, a random secret is persisted.
//  4. As a last resort, an ephemeral random secret. Credentials will not
//     survive a restart when no store directory is supplied.
//
// storeDir is typically the directory containing the credential vault DB. An
// empty storeDir skips persistence and matches the legacy NewLocalKMS behavior.
func NewLocalKMSWithStore(storeDir string) (*LocalKMS, error) {
	return newLocalKMSWithStore(storeDir, platformSecret)
}

func newLocalKMSWithStore(storeDir string, platform func() ([]byte, error)) (*LocalKMS, error) {
	if storeDir != "" {
		if secret, err := readPersistedSecret(storeDir); err == nil {
			return &LocalKMS{masterSecret: secret}, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}

		// Older releases encrypted existing vaults directly with the platform
		// id. Persist those exact bytes before opening the vault so the current
		// process remains compatible and every later container reuses them.
		if secret, err := platform(); err == nil && len(bytes.TrimSpace(secret)) >= 16 {
			persisted, perr := persistSecret(storeDir, bytes.TrimSpace(secret))
			if perr != nil {
				return nil, perr
			}
			return &LocalKMS{masterSecret: persisted}, nil
		}

		secret := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, secret); err != nil {
			return nil, fmt.Errorf("credentials: generate persisted secret: %w", err)
		}
		persisted, err := persistSecret(storeDir, secret)
		if err != nil {
			return nil, err
		}
		return &LocalKMS{masterSecret: persisted}, nil
	}

	if secret, err := platform(); err == nil {
		return &LocalKMS{masterSecret: secret}, nil
	}
	fmt.Fprintln(os.Stderr, "soulacy/credentials: WARNING — could not derive or persist a machine secret; using ephemeral random key. Credentials will not survive process restarts.")
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return nil, fmt.Errorf("credentials: generate fallback secret: %w", err)
	}
	return &LocalKMS{masterSecret: secret}, nil
}

// readPersistedSecret returns the master secret exactly as stored. Older
// releases wrote a 64-byte hex string and used those encoded bytes as the HKDF
// input, so the value must never be decoded or normalized during an upgrade.
func readPersistedSecret(dir string) ([]byte, error) {
	path := filepath.Join(dir, machineSecretFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	secret := bytes.TrimSpace(data)
	if len(secret) < 16 {
		return nil, fmt.Errorf("credentials: persisted machine secret at %s is invalid", path)
	}
	return append([]byte(nil), secret...), nil
}

// persistSecret creates the stable workspace key without replacing an
// existing one. O_EXCL closes the startup race when two gateway processes are
// briefly launched together; the loser reads and uses the winner's value.
func persistSecret(dir string, secret []byte) ([]byte, error) {
	if len(bytes.TrimSpace(secret)) < 16 {
		return nil, fmt.Errorf("credentials: machine secret is too short")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("credentials: create secret dir: %w", err)
	}
	path := filepath.Join(dir, machineSecretFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readPersistedSecret(dir)
	}
	if err != nil {
		return nil, fmt.Errorf("credentials: write persisted secret: %w", err)
	}
	if _, err := f.Write(bytes.TrimSpace(secret)); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("credentials: write persisted secret: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("credentials: close persisted secret: %w", err)
	}
	return append([]byte(nil), bytes.TrimSpace(secret)...), nil
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
