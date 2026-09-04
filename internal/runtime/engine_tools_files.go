// engine_tools_files.go — filesystem built-ins.
//
// ARCH-2: mechanically extracted from engine.go (no behaviour change).
// SAFE (read-only): read_file, list_dir, find_files.
// SYSTEM (privileged): write_file. The SEC-3 partition is preserved by
// privilegedSystemTools in engine.go; this file is just the definitions.
package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// buildFileTools returns the files-domain OS-level built-in tools. Extracted
// from buildSystemTools (ARCH-2) — identical definitions, no behaviour change.
// maxScanBytes caps what find_files will read to test a content_pattern.
// find_files is in the always-available SAFE tool partition, so any chat user —
// or any untrusted page an agent reads — can drive it.
const maxScanBytes = 4 << 20

func (e *Engine) buildFileTools() []BuiltinTool {
	return []BuiltinTool{
		{
			Name:        "read_file",
			Gate:        "",
			Description: "Read the contents of a file on the host filesystem. Returns the file content as text.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute or home-relative (~/) path to the file",
					},
					"max_bytes": map[string]any{
						"type":        "integer",
						"description": "Maximum bytes to read (default 100000, max 1000000)",
					},
				},
				"required": []string{"path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				path, err := e.resolveFilesystemPath(argString(args, "path"), false)
				if err != nil {
					return "", fmt.Errorf("read_file: %w", err)
				}
				maxBytes := argInt(args, "max_bytes", 100000)
				if maxBytes <= 0 {
					maxBytes = 100000
				}
				if maxBytes > 1000000 {
					maxBytes = 1000000
				}
				f, err := os.Open(path)
				if err != nil {
					return "", fmt.Errorf("read_file: %w", err)
				}
				defer f.Close()
				buf := make([]byte, maxBytes)
				n, rerr := f.Read(buf)
				if rerr != nil && rerr.Error() != "EOF" {
					return "", fmt.Errorf("read_file: read: %w", rerr)
				}
				return string(buf[:n]), nil
			},
		},
		{
			Name:        "write_file",
			Gate:        "",
			Description: "Write content to a file on the host filesystem. Creates the file and any parent directories if they don't exist. By default overwrites; set append: true to add to the end.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute or home-relative (~/) path to write",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Text content to write",
					},
					"append": map[string]any{
						"type":        "boolean",
						"description": "Append to the file instead of overwriting (default false)",
					},
				},
				"required": []string{"path", "content"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				path, err := e.resolveFilesystemPath(argString(args, "path"), true)
				if err != nil {
					return "", fmt.Errorf("write_file: %w", err)
				}
				content := argString(args, "content")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					return "", fmt.Errorf("write_file: mkdir: %w", err)
				}
				flags := os.O_CREATE | os.O_WRONLY
				if argBool(args, "append") {
					flags |= os.O_APPEND
				} else {
					flags |= os.O_TRUNC
				}
				f, err := os.OpenFile(path, flags, 0644)
				if err != nil {
					return "", fmt.Errorf("write_file: open: %w", err)
				}
				defer f.Close()
				if _, err := f.WriteString(content); err != nil {
					return "", fmt.Errorf("write_file: write: %w", err)
				}
				return fmt.Sprintf("Written %d bytes to %s", len(content), path), nil
			},
		},
		{
			Name:        "list_dir",
			Gate:        "",
			Description: "List the contents of a directory. Returns entry names, types (file/dir), and sizes.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory path (absolute or ~/)",
					},
					"show_hidden": map[string]any{
						"type":        "boolean",
						"description": "Include hidden files (starting with .)",
					},
				},
				"required": []string{"path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				path, err := e.resolveFilesystemPath(argString(args, "path"), false)
				if err != nil {
					return "", fmt.Errorf("list_dir: %w", err)
				}
				showHidden := argBool(args, "show_hidden")
				entries, err := os.ReadDir(path)
				if err != nil {
					return "", fmt.Errorf("list_dir: %w", err)
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Contents of %s:\n", path))
				for _, ent := range entries {
					if !showHidden && strings.HasPrefix(ent.Name(), ".") {
						continue
					}
					info, _ := ent.Info()
					kind := "file"
					size := int64(0)
					if ent.IsDir() {
						kind = "dir "
					} else if info != nil {
						size = info.Size()
					}
					sb.WriteString(fmt.Sprintf("  [%s] %-40s %d bytes\n", kind, ent.Name(), size))
				}
				return sb.String(), nil
			},
		},
		{
			Name:        "find_files",
			Gate:        "",
			Description: "Recursively search for files under a directory by filename glob pattern and/or content regex. Returns matching file paths. Useful for locating config files, finding where a package is installed, or grepping across source trees.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Root directory to search (supports ~ and $VAR expansion)",
					},
					"name_pattern": map[string]any{
						"type":        "string",
						"description": "Glob pattern matched against the filename only (e.g. '*.yaml', 'config.*', 'SOUL.yaml')",
					},
					"content_pattern": map[string]any{
						"type":        "string",
						"description": "Regular expression matched against file contents",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return (default 50)",
					},
				},
				"required": []string{"path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				searchPath, err := e.resolveFilesystemPath(argString(args, "path"), false)
				if err != nil {
					return "", fmt.Errorf("find_files: %w", err)
				}

				namePattern := argString(args, "name_pattern")
				contentPattern := argString(args, "content_pattern")
				maxResults := argInt(args, "max_results", 50)
				if maxResults <= 0 {
					maxResults = 50
				}

				var contentRe *regexp.Regexp
				if contentPattern != "" {
					var err error
					contentRe, err = regexp.Compile(contentPattern)
					if err != nil {
						return "", fmt.Errorf("find_files: invalid content_pattern: %w", err)
					}
				}

				var results []string
				_ = filepath.Walk(searchPath, func(p string, info os.FileInfo, err error) error {
					if err != nil || info.IsDir() {
						return nil
					}
					select {
					case <-ctx.Done():
						return filepath.SkipAll
					default:
					}
					if len(results) >= maxResults {
						return filepath.SkipAll
					}
					if namePattern != "" {
						matched, _ := filepath.Match(namePattern, filepath.Base(p))
						if !matched {
							return nil
						}
					}
					if contentRe != nil {
						// Only the number of MATCHES is bounded by max_results, so a
						// content_pattern that matches nothing used to read every file
						// under the root fully into memory — a single large file was an
						// immediate OOM. read_file, in this same file, has always capped
						// at 1 MB; this path just never did. A content match beyond the
						// first megabyte is not worth an unbounded allocation.
						safePath, policyErr := e.resolveFilesystemPath(p, false)
						if policyErr != nil {
							return nil
						}
						info, statErr := os.Stat(safePath)
						if statErr != nil || info.Size() > maxScanBytes {
							return nil
						}
						data, readErr := os.ReadFile(safePath)
						if readErr != nil {
							return nil
						}
						if !contentRe.Match(data) {
							return nil
						}
					}
					results = append(results, p)
					return nil
				})

				if len(results) == 0 {
					return "No files found matching the criteria.", nil
				}
				return strings.Join(results, "\n"), nil
			},
		},
	}
}
