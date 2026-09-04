package plugininstall

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxExtractBytes bounds total extracted size (zip-bomb guard).
const maxExtractBytes = 256 << 20 // 256 MiB

// secureTarget resolves name inside dst, refusing path traversal loudly —
// an archive trying to climb out is hostile, not something to silently fix.
func secureTarget(dst, name string) (string, error) {
	target := filepath.Join(dst, name) // Join cleans; ".." entries can climb
	if target != dst && !strings.HasPrefix(target, dst+string(os.PathSeparator)) {
		return "", fmt.Errorf("plugininstall: archive entry %q escapes the target directory", name)
	}
	return target, nil
}

func extractTarGz(path, dst string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("plugininstall: gzip: %w", err)
	}
	defer gz.Close()

	var total int64
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("plugininstall: tar: %w", err)
		}
		target, terr := secureTarget(dst, hdr.Name)
		if terr != nil {
			return terr
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return err
			}
			n, cerr := io.Copy(out, io.LimitReader(tr, maxExtractBytes-total))
			out.Close()
			if cerr != nil {
				return cerr
			}
			if total += n; total >= maxExtractBytes {
				return fmt.Errorf("plugininstall: archive exceeds %d bytes extracted — refusing", maxExtractBytes)
			}
		default:
			// symlinks/devices are skipped — plugins are plain file trees
		}
	}
}

func extractZip(path, dst string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("plugininstall: zip: %w", err)
	}
	defer zr.Close()

	var total int64
	for _, f := range zr.File {
		target, terr := secureTarget(dst, f.Name)
		if terr != nil {
			return terr
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			rc.Close()
			return err
		}
		n, cerr := io.Copy(out, io.LimitReader(rc, maxExtractBytes-total))
		out.Close()
		rc.Close()
		if cerr != nil {
			return cerr
		}
		if total += n; total >= maxExtractBytes {
			return fmt.Errorf("plugininstall: archive exceeds %d bytes extracted — refusing", maxExtractBytes)
		}
	}
	return nil
}
