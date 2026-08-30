package sessioncapturecompanion

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
)

// files contains the source-only Chrome companion. It never contains tenant
// data or deployment secrets; the gateway packages it on demand so hosted
// users do not need a CLI or terminal for website-session capture.
//
//go:embed extension/*
var files embed.FS

// Archive returns an installable source archive for Chrome's "Load unpacked"
// flow. A source archive keeps the companion auditable and lets operators pin
// exactly the version shipped by their Soulacy gateway.
func Archive() ([]byte, error) {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	err := fs.WalkDir(files, "extension", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		body, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		writer, err := zw.Create("soulacy-session-capture/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := writer.Write(body); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
