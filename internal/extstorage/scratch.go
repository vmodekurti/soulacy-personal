package extstorage

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewScratchDir creates the per-run shared scratch directory for one
// sidecar spawn (Story E24 shared mounts): <root>/<sanitised id>-<random>,
// mode 0700. The returned cleanup removes the directory and everything in
// it. root defaults to <os.TempDir()>/soulacy-scratch when empty — but the
// host always passes the workspace data dir (config.ResolveWorkspace), so
// scratch files inherit the workspace's permissions story, mirroring the
// E13 staging-dir pattern.
func NewScratchDir(root, id string) (dir string, cleanup func(), err error) {
	if root == "" {
		root = filepath.Join(os.TempDir(), "soulacy-scratch")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, fmt.Errorf("extstorage: create scratch root: %w", err)
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", nil, fmt.Errorf("extstorage: scratch entropy: %w", err)
	}
	dir = filepath.Join(root, sanitizeID(id)+"-"+hex.EncodeToString(rnd[:]))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("extstorage: create scratch dir: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("extstorage: scratch abs path: %w", err)
	}
	return abs, func() { _ = os.RemoveAll(abs) }, nil
}

// sanitizeID keeps scratch dir names filesystem-safe.
func sanitizeID(id string) string {
	if id == "" {
		return "sidecar"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
