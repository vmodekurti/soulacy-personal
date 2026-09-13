// Package publishedfiles provides bounded, read-only access to an explicitly
// published directory. The root is server configuration, never a request value.
package publishedfiles

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxEntries = 512
const MaxPreviewBytes = 128 * 1024

var ErrPath = errors.New("invalid published path")
var ErrNotFound = errors.New("published file not found")
var ErrPreview = errors.New("preview requires a UTF-8 text file of at most 128 KiB")
var ErrUnavailable = errors.New("published folder is unavailable")
var ErrChanged = errors.New("file changed while reading; refresh and try again")

type Entry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Kind        string    `json:"kind"`
	SizeBytes   int64     `json:"size_bytes"`
	ModifiedAt  time.Time `json:"modified_at"`
	Previewable bool      `json:"previewable"`
}

type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
	ReadOnly  bool    `json:"read_only"`
}

type Preview struct {
	Path       string    `json:"path"`
	Content    string    `json:"content"`
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
	SHA256     string    `json:"sha256"`
	ReadOnly   bool      `json:"read_only"`
}

// ValidPath accepts canonical relative URLs only; callers decode URL query
// encoding exactly once. Percent signs are literal filenames, never re-decoded.
func ValidPath(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) || len(value) > 2048 || strings.Contains(value, "\\") {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 24 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") || len(part) > 255 {
			return false
		}
		for _, r := range part {
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				return false
			}
		}
	}
	return true
}

func ValidRoot(root string) bool {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && root == filepath.Clean(home) {
		return false
	}
	return true
}

func textFile(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md", ".markdown", ".json", ".yaml", ".yml", ".toml", ".csv", ".tsv", ".log", ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".svelte", ".swift", ".rs", ".sh", ".sql", ".css", ".html", ".xml", ".mmd", ".mermaid":
		return true
	}
	return false
}

func List(root, relative string) (Listing, error) {
	out := Listing{Path: relative, Entries: []Entry{}, ReadOnly: true}
	if !ValidPath(relative) {
		return out, ErrPath
	}
	dir, err := openPath(root, relative, true)
	if err != nil {
		return out, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(MaxEntries + 1)
	if err != nil && err != io.EOF {
		return out, ErrUnavailable
	}
	out.Truncated = len(entries) > MaxEntries
	if out.Truncated {
		entries = entries[:MaxEntries]
	}
	for _, entry := range entries {
		if !ValidPath(entry.Name()) {
			continue
		}
		// Metadata is read relative to the same pinned directory handle. A path
		// replacement or symlink cannot redirect this stat outside the share.
		info, err := entryInfo(dir, entry.Name())
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) || !singleLink(info) {
			continue
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		out.Entries = append(out.Entries, Entry{
			Name: entry.Name(), Path: path.Join(relative, entry.Name()), Kind: kind,
			SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC(),
			Previewable: info.Mode().IsRegular() && info.Size() <= MaxPreviewBytes && textFile(entry.Name()),
		})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Kind != out.Entries[j].Kind {
			return out.Entries[i].Kind == "directory"
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}

func Read(root, relative string) (Preview, error) {
	out := Preview{Path: relative, ReadOnly: true}
	if relative == "" || !ValidPath(relative) {
		return out, ErrPath
	}
	if !textFile(relative) {
		return out, ErrPreview
	}
	file, err := openPath(root, relative, false)
	if err != nil {
		return out, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !singleLink(before) {
		return out, ErrNotFound
	}
	if before.Size() > MaxPreviewBytes {
		return out, ErrPreview
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxPreviewBytes+1))
	if err != nil {
		return out, ErrUnavailable
	}
	if len(data) > MaxPreviewBytes || !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
		return out, ErrPreview
	}
	after, err := file.Stat()
	if err != nil || !singleLink(after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || after.Size() != int64(len(data)) {
		return out, ErrChanged
	}
	out.Content, out.SizeBytes, out.ModifiedAt = string(data), int64(len(data)), after.ModTime().UTC()
	out.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	return out, nil
}
