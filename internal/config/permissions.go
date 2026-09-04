package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// secureWorkspacePermissions repairs modes from older installations at every
// startup. Creation modes alone are insufficient: umask may be permissive and
// upgrades must close already-existing group/world access.
func secureWorkspacePermissions(cfg *Config, ws Paths) error {
	if ws.Root != "" {
		if err := chmodIfExists(ws.Root, 0o700); err != nil {
			return err
		}
	}
	for _, dir := range ws.Dirs() {
		if err := chmodIfExists(dir, 0o700); err != nil {
			return err
		}
	}

	// Operational state and logs can contain full prompts, tool results and
	// credentials. Recursively repair them. Code-bearing trees (skills/tools/
	// plugins) are intentionally excluded so executable owner bits survive.
	for _, dir := range []string{ws.Data, ws.Memory, ws.Logs, ws.Audit, ws.Secrets} {
		if err := secureTree(dir); err != nil {
			return err
		}
	}

	files := []string{ws.ConfigFile, ws.CredentialsDB()}
	if cfg != nil {
		files = append(files, cfg.Memory.SQLitePath, cfg.Knowledge.DBPath, cfg.Log.File)
	}
	for _, file := range files {
		if strings.TrimSpace(file) == "" {
			continue
		}
		if err := chmodIfExists(file, 0o600); err != nil {
			return err
		}
	}

	// Agent manifests may contain inline provider credentials. Do not touch
	// sibling executable tools.
	for _, dir := range append([]string{ws.Agents}, cfgAgentDirs(cfg)...) {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "SOUL.y*ml"))
		for _, path := range matches {
			if err := chmodIfExists(path, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func cfgAgentDirs(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	return cfg.AgentDirs
}

func secureTree(root string) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil // never chmod a symlink target outside the workspace
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		}
		return os.Chmod(path, mode)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secure workspace permissions for %s: %w", root, err)
	}
	return nil
}

func chmodIfExists(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}
