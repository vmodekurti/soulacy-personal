// Package safearchive is the one place in this repo that unpacks an archive
// somebody else produced.
//
// MU-016 criterion: "archive extraction rejects traversal, escaping links, and
// decompression bombs." Three implementations of that rule existed —
// internal/plugininstall, internal/updates, internal/knowledge — each with a
// different subset of the guards, which is the shape a rule takes when it is a
// convention rather than a mechanism. The one that mattered most had already
// been found the hard way: internal/knowledge's .docx reader measured the
// COMPRESSED upload and expanded ~1000:1 inside a single io.ReadAll.
//
// So this package is the mechanism, and `TestArchivesAreOnlyOpenedThroughTheSafeExtractor`
// fails the build on a `tar.NewReader`/`zip.NewReader` anywhere else. A fourth
// extractor written next year gets these guards by not being allowed to exist.
//
// WHAT IT REFUSES, AND WHY REFUSING BEATS SKIPPING. The previous extractors
// SKIPPED entries they would not handle — symlinks, devices, hardlinks — with
// a comment saying plugins are plain file trees. Skipping is wrong in both
// directions. A benign archive that happens to carry a symlink installs and
// then does not work, with nothing in any log saying why; a hostile one learns
// exactly which entry types are ignored and pays no price for probing. An
// archive containing a device node is not a package with a stray file in it.
// It is either broken or an attack, and both deserve the same answer: stop,
// and say which entry.
package safearchive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Limits bound what an archive may cost to unpack.
//
// THREE bounds, not one, because they stop three different attacks and any one
// of them alone leaves the other two open:
//
//   - MaxTotalBytes stops the classic decompression bomb: a few kilobytes of
//     DEFLATE that expand to fill the disk.
//   - MaxEntryBytes stops one entry consuming the whole budget, so a legitimate
//     archive behind a hostile one in the same batch is not starved.
//   - MaxEntries stops the bomb that a byte bound cannot see. Ten million
//     zero-length files weigh nothing and exhaust the filesystem's inodes,
//     which fails the machine rather than the request.
type Limits struct {
	MaxTotalBytes int64
	MaxEntryBytes int64
	MaxEntries    int
}

// DefaultLimits are sized for the largest legitimate artifact this system
// handles — a plugin bundle with vendored dependencies — not for the largest
// imaginable one.
func DefaultLimits() Limits {
	return Limits{
		MaxTotalBytes: 256 << 20,
		MaxEntryBytes: 128 << 20,
		MaxEntries:    20_000,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = d.MaxTotalBytes
	}
	if l.MaxEntryBytes <= 0 {
		l.MaxEntryBytes = d.MaxEntryBytes
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = d.MaxEntries
	}
	return l
}

// Result reports what an extraction produced, so a caller can log or assert on
// it rather than inferring it from the filesystem.
type Result struct {
	Files int
	Dirs  int
	Bytes int64
}

// ErrUnsafeArchive is the class every refusal here belongs to. Callers that
// need to distinguish "this archive is hostile or broken" from "the disk is
// full" match on this rather than on message text.
var ErrUnsafeArchive = errors.New("safearchive: refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsafeArchive, fmt.Sprintf(format, args...))
}

// root is a destination directory with its symlinks already resolved.
//
// RESOLVING IT ONCE, UP FRONT, is what makes the containment check meaningful.
// The previous extractors compared a joined path against the destination
// string they were handed. If any component of that string was itself a
// symlink — /var → /private/var on macOS is the everyday case — then every
// legitimate entry looked like an escape, and the check that was supposed to
// stop an attack instead refused normal archives on one platform. Worse, a
// caller passing a relative or uncleaned destination got a prefix comparison
// between two differently-shaped strings, whose outcome is not the one anybody
// reasoned about.
type root struct {
	path string
}

func newRoot(dst string) (root, error) {
	if strings.TrimSpace(dst) == "" {
		return root{}, refuse("no destination directory given")
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return root{}, err
	}
	resolved, err := filepath.EvalSymlinks(dst)
	if err != nil {
		return root{}, err
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return root{}, err
	}
	return root{path: filepath.Clean(abs)}, nil
}

// resolve maps an archive entry name to a path inside the root, or refuses.
//
// Two checks, and they are NOT equal partners — stated plainly because a
// mutation test showed it:
//
//   - The parent check is the containment guarantee. It resolves the deepest
//     EXISTING ancestor of the target and refuses if that lands outside the
//     root. It catches the two-entry attack — entry one creates a symlink
//     `pkg` pointing at /etc, entry two writes `pkg/passwd`, a name that joins
//     to a path inside the root and passes every string test while the write
//     lands in /etc — and it equally covers a link that was already sitting in
//     the destination before extraction began.
//   - The join check is a FAST PATH, not a second containment guard. Deleting
//     it leaves every traversal test in this package still passing, because
//     the parent check catches those too. It earns its place by refusing an
//     obviously hostile name before any syscall, so an archive with a million
//     traversing entries costs a million string compares rather than a million
//     EvalSymlinks calls.
//
// Recording that asymmetry matters more than it looks: the next person to read
// this would otherwise assume two independent defences and feel safe removing
// the expensive one.
func (r root) resolve(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", refuse("archive contains an entry with no name")
	}
	if strings.ContainsRune(name, 0) {
		return "", refuse("archive entry %q contains a NUL byte", name)
	}
	target := filepath.Clean(filepath.Join(r.path, name))
	if target != r.path && !strings.HasPrefix(target, r.path+string(os.PathSeparator)) {
		return "", refuse("archive entry %q escapes the destination directory", name)
	}
	// Walk the existing parents. filepath.EvalSymlinks on the full target
	// fails while the file does not exist yet, so the deepest EXISTING
	// ancestor is what can be resolved and is what an attacker could have
	// planted.
	parent := filepath.Dir(target)
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			if resolved != r.path && !strings.HasPrefix(resolved, r.path+string(os.PathSeparator)) {
				return "", refuse("archive entry %q resolves through a link out of the destination directory", name)
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return target, nil
}

// budget tracks the three bounds across one extraction.
type budget struct {
	limits Limits
	result Result
}

func (b *budget) entry(name string) error {
	if b.result.Files+b.result.Dirs >= b.limits.MaxEntries {
		return refuse("archive has more than %d entries (at %q)", b.limits.MaxEntries, name)
	}
	return nil
}

// copyBounded writes one entry, refusing rather than truncating.
//
// It reads ONE BYTE past the allowance so the difference between "exactly at
// the limit" and "over it" is observable. io.Copy with a LimitReader set to
// the remaining budget silently produces a truncated file when the source is
// larger, and a truncated file that the caller believes is complete is worse
// than a refusal — for an executable or an archive-within-an-archive it is a
// corruption the next stage reports as its own bug.
func (b *budget) copyBounded(dst io.Writer, src io.Reader, name string) (int64, error) {
	allowance := b.limits.MaxEntryBytes
	if remaining := b.limits.MaxTotalBytes - b.result.Bytes; remaining < allowance {
		allowance = remaining
	}
	if allowance < 0 {
		allowance = 0
	}
	n, err := io.Copy(dst, io.LimitReader(src, allowance+1))
	b.result.Bytes += n
	if err != nil {
		return n, err
	}
	if n > allowance {
		if b.limits.MaxTotalBytes-(b.result.Bytes-n) <= b.limits.MaxEntryBytes {
			return n, refuse("archive expands past the %d byte total limit (at %q)", b.limits.MaxTotalBytes, name)
		}
		return n, refuse("archive entry %q is larger than the %d byte per-entry limit", name, b.limits.MaxEntryBytes)
	}
	return n, nil
}

// createFile opens an extraction target without following a symlink and
// without overwriting an existing one.
//
// O_EXCL is the guard against a DUPLICATE entry name, which is a real trick
// rather than a theoretical one: an archive carrying `a.py` twice is scanned
// as the first and extracted as the second by any extractor that truncates.
// Refusing means the two can never differ.
//
// O_NOFOLLOW (where the platform has it) is belt-and-braces beside the
// parent-resolution check above: even if a link were planted between the check
// and the open, the kernel refuses. O_EXCL already refuses an existing symlink
// at the target itself on every platform, so the fallback is not a hole — it
// only gives up the narrow race.
func createFile(target string, mode os.FileMode) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY|noFollow, mode.Perm()&0o755)
	if err != nil {
		if os.IsExist(err) {
			return nil, refuse("archive contains %q more than once", filepath.Base(target))
		}
		return nil, err
	}
	return file, nil
}

// writeEntry creates one file and fills it, removing it again if anything goes
// wrong.
//
// THE REMOVAL MATTERS. copyBounded reads one byte past the allowance to tell
// "exactly at the limit" from "over it", which means an oversized entry is
// fully written before it is detected. Leaving that file behind gives a failed
// extraction a complete-LOOKING artifact on disk — and the caller that
// abandons the destination directory is the careful one. The careless one, or
// a later scan of the staging area, finds a file that nothing reports as
// partial. A refusal should leave nothing to find.
func writeEntry(b *budget, target string, mode os.FileMode, src io.Reader, name string) error {
	file, err := createFile(target, mode)
	if err != nil {
		return err
	}
	_, copyErr := b.copyBounded(file, src, name)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return nil
}

// ExtractTarGz unpacks a gzip-compressed tar into dst.
func ExtractTarGz(reader io.Reader, dst string, limits Limits) (Result, error) {
	r, err := newRoot(dst)
	if err != nil {
		return Result{}, err
	}
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return Result{}, refuse("not a readable gzip stream: %v", err)
	}
	defer gz.Close()

	b := &budget{limits: limits.withDefaults()}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return b.result, nil
		}
		if err != nil {
			return b.result, refuse("unreadable tar stream: %v", err)
		}
		if err := b.entry(header.Name); err != nil {
			return b.result, err
		}
		target, err := r.resolve(header.Name)
		if err != nil {
			return b.result, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return b.result, err
			}
			b.result.Dirs++
		case tar.TypeReg:
			if err := writeEntry(b, target, os.FileMode(header.Mode), tr, header.Name); err != nil {
				return b.result, err
			}
			b.result.Files++
		default:
			// Symlinks, hardlinks, devices, FIFOs, sockets. Refused by name
			// and type rather than skipped — see the package comment.
			return b.result, refuse("archive entry %q is a %s, which cannot appear in a file tree from an untrusted source",
				header.Name, entryKind(header.Typeflag))
		}
	}
}

// ExtractZip unpacks a zip into dst.
func ExtractZip(reader io.ReaderAt, size int64, dst string, limits Limits) (Result, error) {
	r, err := newRoot(dst)
	if err != nil {
		return Result{}, err
	}
	zr, err := zip.NewReader(reader, size)
	if err != nil {
		return Result{}, refuse("not a readable zip: %v", err)
	}

	b := &budget{limits: limits.withDefaults()}
	for _, entry := range zr.File {
		if err := b.entry(entry.Name); err != nil {
			return b.result, err
		}
		target, err := r.resolve(entry.Name)
		if err != nil {
			return b.result, err
		}
		mode := entry.Mode()
		switch {
		case entry.FileInfo().IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return b.result, err
			}
			b.result.Dirs++
		case !mode.IsRegular():
			return b.result, refuse("archive entry %q is a %s, which cannot appear in a file tree from an untrusted source",
				entry.Name, zipEntryKind(mode))
		default:
			// The declared size is checked first so an obvious bomb costs
			// nothing to reject. It is NOT the guard — the header is
			// attacker-controlled and can lie, which is why copyBounded
			// measures the bytes that actually arrive.
			if entry.UncompressedSize64 > uint64(b.limits.MaxEntryBytes) {
				return b.result, refuse("archive entry %q declares %d bytes, over the %d byte per-entry limit",
					entry.Name, entry.UncompressedSize64, b.limits.MaxEntryBytes)
			}
			rc, err := entry.Open()
			if err != nil {
				return b.result, err
			}
			writeErr := writeEntry(b, target, mode, rc, entry.Name)
			rc.Close()
			if writeErr != nil {
				return b.result, writeErr
			}
			b.result.Files++
		}
	}
	return b.result, nil
}

// ExtractZipFile is the path-taking convenience over ExtractZip.
func ExtractZipFile(path, dst string, limits Limits) (Result, error) {
	file, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Result{}, err
	}
	return ExtractZip(file, info.Size(), dst, limits)
}

// ExtractTarGzFile is the path-taking convenience over ExtractTarGz.
func ExtractTarGzFile(path, dst string, limits Limits) (Result, error) {
	file, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()
	return ExtractTarGz(file, dst, limits)
}

func entryKind(typeflag byte) string {
	switch typeflag {
	case tar.TypeSymlink:
		return "symbolic link"
	case tar.TypeLink:
		return "hard link"
	case tar.TypeChar:
		return "character device"
	case tar.TypeBlock:
		return "block device"
	case tar.TypeFifo:
		return "FIFO"
	default:
		return fmt.Sprintf("entry of type %q", string(typeflag))
	}
}

func zipEntryKind(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode&os.ModeDevice != 0:
		return "device node"
	case mode&os.ModeNamedPipe != 0:
		return "FIFO"
	case mode&os.ModeSocket != 0:
		return "socket"
	default:
		return "non-regular file"
	}
}

// SelectTarGz reads selected entries out of a gzip-compressed tar INTO MEMORY,
// without touching the filesystem.
//
// A separate entrypoint rather than "extract then read two files", because the
// two operations have genuinely different threat models and collapsing them
// would weaken this one. Nothing here writes an attacker-controlled path, so
// traversal is structurally unreachable rather than merely checked — and the
// caller (the self-updater) wants exactly two known binaries out of a release
// tarball whose directory layout is not its business.
//
// What it does still need, and did not have when it was hand-rolled inside
// internal/updates: a CUMULATIVE bound and an ENTRY-COUNT bound. That code
// bounded each entry at 512 MiB and nothing else, so an archive of ten
// thousand entries all named `soulacy` allocated 512 MiB ten thousand times in
// sequence — each one immediately garbage, and the process resident for as
// long as any one of them lived.
//
// `want` is matched against the entry's BASE NAME, which is what the caller
// means by "the soulacy binary, wherever the release layout puts it". Entry
// names are still refused for traversal, because a name that climbs is a
// signal about the archive regardless of whether this particular reader would
// have been hurt by it.
func SelectTarGz(reader io.Reader, want map[string]bool, limits Limits) (map[string][]byte, error) {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, refuse("not a readable gzip stream: %v", err)
	}
	defer gz.Close()

	b := &budget{limits: limits.withDefaults()}
	found := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return found, nil
		}
		if err != nil {
			return nil, refuse("unreadable tar stream: %v", err)
		}
		// Counted whether or not it is wanted: the cost an archive imposes is
		// the entries it contains, not the ones the caller keeps.
		if err := b.entry(header.Name); err != nil {
			return nil, err
		}
		b.result.Files++
		if err := safeEntryName(header.Name); err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(filepath.Clean(header.Name))
		if !want[base] {
			continue
		}
		if _, duplicate := found[base]; duplicate {
			return nil, refuse("archive contains %q more than once", base)
		}
		var buf bytesBuffer
		if _, err := b.copyBounded(&buf, tr, header.Name); err != nil {
			return nil, err
		}
		found[base] = buf.data
	}
}

// safeEntryName refuses a name that climbs or is absolute, without needing a
// destination directory to compare against.
func safeEntryName(name string) error {
	if strings.TrimSpace(name) == "" {
		return refuse("archive contains an entry with no name")
	}
	if strings.ContainsRune(name, 0) {
		return refuse("archive entry %q contains a NUL byte", name)
	}
	cleaned := filepath.Clean(name)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return refuse("archive entry %q escapes its own directory", name)
	}
	return nil
}

// bytesBuffer is a minimal growable sink. bytes.Buffer would do, but importing
// it here for one use pulls a second notion of "how big is this" into a file
// whose entire subject is bounding exactly that.
type bytesBuffer struct{ data []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}
