package plugininstall

import (
	"github.com/soulacy/soulacy/internal/safearchive"
)

// archive.go — plugin bundles are unpacked by internal/safearchive.
//
// This file used to carry its own tar and zip extractors. They had the
// traversal check and a total-byte bound, and were missing three things the
// shared policy has: an entry-count bound (ten million empty files weigh
// nothing and exhaust inodes), a refusal on duplicate entry names, and a
// destination whose own symlinks are resolved before anything is compared
// against it. They also SKIPPED symlink and device entries rather than
// refusing them, so a bundle carrying one installed and then did not work,
// with nothing anywhere saying why.
//
// Keeping thin wrappers rather than calling safearchive from the installer
// directly, because the installer's two branches read better named than
// spelled out, and because a limits choice belongs beside the thing whose size
// it describes.

// maxExtractBytes is the total decompressed size a plugin bundle may reach.
// Sized for a bundle with vendored dependencies, not for the largest
// imaginable archive.
const maxExtractBytes = 256 << 20

func pluginLimits() safearchive.Limits {
	return safearchive.Limits{
		MaxTotalBytes: maxExtractBytes,
		MaxEntryBytes: 128 << 20,
		MaxEntries:    20_000,
	}
}

func extractTarGz(path, dst string) error {
	_, err := safearchive.ExtractTarGzFile(path, dst, pluginLimits())
	return err
}

func extractZip(path, dst string) error {
	_, err := safearchive.ExtractZipFile(path, dst, pluginLimits())
	return err
}
