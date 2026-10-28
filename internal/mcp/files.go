package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FileVaultNamespace is the credential-vault namespace that holds the uploaded
// authentication files for one MCP server (cookie jars, token files, Playwright
// storage state). The gateway upload handler writes blobs here and the launch
// path reads them back, so both sides MUST agree on this derivation.
func FileVaultNamespace(serverID string) string {
	return "mcp_file_" + serverID
}

// provisionFiles materializes serverID's uploaded files (if any) and returns
// the directory they were written to, or "" when none are configured. It is a
// no-op unless the client was built with a MaterializeFiles source and a
// FileDir.
func (c *Client) provisionFiles(serverID string) (string, error) {
	if c.materializeFiles == nil || c.fileDir == "" {
		return "", nil
	}
	files, err := c.materializeFiles(context.Background(), serverID)
	if err != nil {
		return "", fmt.Errorf("load uploaded files: %w", err)
	}
	return materializeServerFiles(c.fileDir, serverID, files)
}

// mcpFileDirName is the subdirectory under the workspace root that holds the
// materialized per-server file directories.
const mcpFileDirName = "mcp-files"

// FileDirEnv is the environment variable pointing a stdio MCP subprocess at the
// directory holding its materialized uploaded files. A server reads e.g.
// "$SOULACY_MCP_FILE_DIR/cookies.txt".
const FileDirEnv = "SOULACY_MCP_FILE_DIR"

// materializeServerFiles writes files into a private per-server directory under
// baseDir and returns that directory, or "" when there is nothing to write.
//
// The directory is 0700 and each file 0600: the contents are decrypted secrets
// that only the MCP subprocess should read. Files present on disk but no longer
// in the vault set are removed, so a deleted upload does not linger across
// restarts. The vault is the source of truth — anything a server writes back to
// these files is overwritten on the next launch.
func materializeServerFiles(baseDir, serverID string, files map[string][]byte) (string, error) {
	if baseDir == "" || len(files) == 0 {
		return "", nil
	}
	dir := filepath.Join(baseDir, mcpFileDirName, sanitizeID(serverID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mcp files: create dir: %w", err)
	}
	// MkdirAll does not chmod an existing dir; enforce 0700 either way.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("mcp files: secure dir: %w", err)
	}

	keep := make(map[string]bool, len(files))
	for name := range files {
		if base := safeFileBase(name); base != "" {
			keep[base] = true
		}
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !keep[e.Name()] {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		base := safeFileBase(name)
		if base == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, base), files[name], 0o600); err != nil {
			return "", fmt.Errorf("mcp files: write %s: %w", base, err)
		}
	}
	return dir, nil
}

// safeFileBase returns the path-free base name of a stored file, or "" if the
// name could escape the directory. Defense in depth behind the upload handler's
// own validation.
func safeFileBase(name string) string {
	base := filepath.Base(name)
	if base == "." || base == ".." || base == string(filepath.Separator) {
		return ""
	}
	if base != name {
		return ""
	}
	return base
}
