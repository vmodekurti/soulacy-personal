// safearchive_test.go — the attacks, each named for what it does rather than
// for which check catches it.
//
// Every one of these is a real archive, built in the test, run through the real
// extractor, against a real temporary directory. A table of "does this string
// look like traversal" would pass for an extractor that checks the name and
// then writes somewhere else entirely, which is the shape of half the CVEs this
// package exists to be immune to.
package safearchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
	mode     int64
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: e.name, Typeflag: flag, Mode: mode, Linkname: e.linkname}
		if flag == tar.TypeReg {
			header.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func extractTar(t *testing.T, archive []byte, limits Limits) (string, Result, error) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "out")
	result, err := ExtractTarGz(bytes.NewReader(archive), dst, limits)
	return dst, result, err
}

func mustRefuse(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was accepted", what)
	}
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("%s failed with an error that is not a refusal (%v), so a caller "+
			"cannot tell a hostile archive from a full disk", what, err)
	}
}

func TestATraversingEntryIsRefusedAndWritesNothing(t *testing.T) {
	for _, name := range []string{"../escaped", "../../escaped", "a/../../escaped", "/etc/escaped"} {
		dst, _, err := extractTar(t, tarGz(t, tarEntry{name: name, body: "x"}), Limits{})
		if name == "/etc/escaped" {
			// An absolute name joins back inside the root, so it is contained
			// rather than refused — asserted explicitly so the containment is
			// a decision rather than an accident.
			if err != nil {
				t.Fatalf("an absolute entry name was refused rather than contained: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(dst, "etc", "escaped")); statErr != nil {
				t.Fatalf("an absolute entry did not land inside the destination: %v", statErr)
			}
			continue
		}
		mustRefuse(t, err, "entry "+name)
		parent := filepath.Dir(dst)
		if _, statErr := os.Stat(filepath.Join(parent, "escaped")); !os.IsNotExist(statErr) {
			t.Fatalf("entry %q wrote outside the destination", name)
		}
	}
}

// THE TWO-ENTRY ATTACK. Entry one is a symlink named `pkg` pointing at a
// directory outside the root; entry two writes `pkg/payload`, a name that
// joins to a path INSIDE the root and passes every string check. Refusing
// symlink entries is the first defence; the parent-resolution check is the
// second, and it is what also covers a link already sitting in the destination.
func TestASymlinkEntryIsRefusedRatherThanSkipped(t *testing.T) {
	outside := t.TempDir()
	archive := tarGz(t,
		tarEntry{name: "pkg", typeflag: tar.TypeSymlink, linkname: outside},
		tarEntry{name: "pkg/payload", body: "pwned"},
	)
	_, _, err := extractTar(t, archive, Limits{})
	mustRefuse(t, err, "a symlink entry")
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("the refusal does not say what it refused: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "payload")); !os.IsNotExist(statErr) {
		t.Fatal("the write landed outside the destination through the link")
	}
}

// The same attack with the link planted BEFORE extraction, which the entry-type
// refusal cannot see.
func TestAPreexistingSymlinkInTheDestinationDoesNotBecomeAnExit(t *testing.T) {
	outside := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "pkg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := ExtractTarGz(bytes.NewReader(tarGz(t, tarEntry{name: "pkg/payload", body: "pwned"})), dst, Limits{})
	mustRefuse(t, err, "a write through a planted link")
	if _, statErr := os.Stat(filepath.Join(outside, "payload")); !os.IsNotExist(statErr) {
		t.Fatal("the write landed outside the destination through a planted link")
	}
}

func TestDeviceAndHardLinkEntriesAreRefused(t *testing.T) {
	for _, e := range []tarEntry{
		{name: "dev", typeflag: tar.TypeChar},
		{name: "blk", typeflag: tar.TypeBlock},
		{name: "pipe", typeflag: tar.TypeFifo},
		{name: "link", typeflag: tar.TypeLink, linkname: "other"},
	} {
		_, _, err := extractTar(t, tarGz(t, e), Limits{})
		mustRefuse(t, err, "entry type of "+e.name)
	}
}

// A total bound is what stops the classic bomb: a few kilobytes of DEFLATE
// that expand to fill the disk.
func TestATotalSizeBombIsRefused(t *testing.T) {
	big := strings.Repeat("A", 4096)
	entries := make([]tarEntry, 0, 64)
	for i := 0; i < 64; i++ {
		entries = append(entries, tarEntry{name: filepath.Join("d", string(rune('a'+i%26))+string(rune('a'+i/26))), body: big})
	}
	_, _, err := extractTar(t, tarGz(t, entries...), Limits{MaxTotalBytes: 8192, MaxEntryBytes: 8192, MaxEntries: 1000})
	mustRefuse(t, err, "an archive past the total-byte bound")
	if !strings.Contains(err.Error(), "total limit") {
		t.Fatalf("the refusal blames the wrong bound: %v", err)
	}
}

// The bound a byte budget cannot see: ten million zero-length files weigh
// nothing and exhaust the filesystem's inodes, which fails the machine rather
// than the request.
func TestAnEntryCountBombIsRefused(t *testing.T) {
	entries := make([]tarEntry, 0, 200)
	for i := 0; i < 200; i++ {
		entries = append(entries, tarEntry{name: filepath.Join("many", strconvItoa(i))})
	}
	_, result, err := extractTar(t, tarGz(t, entries...), Limits{MaxEntries: 10})
	mustRefuse(t, err, "an archive past the entry-count bound")
	if result.Files+result.Dirs > 10 {
		t.Fatalf("the extractor wrote %d entries past a bound of 10", result.Files+result.Dirs)
	}
}

// One oversized entry must not consume the whole budget, so a legitimate
// archive behind a hostile one in the same batch is not starved.
func TestAnOversizedEntryIsRefusedByName(t *testing.T) {
	_, _, err := extractTar(t, tarGz(t, tarEntry{name: "fat", body: strings.Repeat("A", 4096)}),
		Limits{MaxEntryBytes: 100, MaxTotalBytes: 1 << 20, MaxEntries: 10})
	mustRefuse(t, err, "an entry past the per-entry bound")
	if !strings.Contains(err.Error(), `"fat"`) {
		t.Fatalf("the refusal does not name the entry: %v", err)
	}
}

// The boundary, both sides. An entry EXACTLY at the limit must extract whole;
// one byte over must be refused, not truncated.
//
// Both halves are needed. Asserting only the refusal passes for an extractor
// off by one in the strict direction, which silently rejects legitimate
// archives sized at the documented maximum. Asserting only the acceptance
// passes for one with no bound at all. And the refusal must be a REFUSAL: a
// truncated file the caller believes is complete is worse than an error —
// for an executable or a nested archive it is a corruption the next stage
// reports as its own bug.
func TestTheSizeBoundIsExactAndNeverTruncates(t *testing.T) {
	limits := Limits{MaxEntryBytes: 100, MaxTotalBytes: 1 << 20, MaxEntries: 10}

	exact := strings.Repeat("A", 100)
	dst, _, err := extractTar(t, tarGz(t, tarEntry{name: "exact", body: exact}), limits)
	if err != nil {
		t.Fatalf("an entry exactly at the limit was refused: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "exact"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != exact {
		t.Fatalf("an entry at the limit was written as %d bytes, want %d", len(body), len(exact))
	}

	overDst, _, err := extractTar(t, tarGz(t, tarEntry{name: "over", body: strings.Repeat("A", 101)}), limits)
	mustRefuse(t, err, "an entry one byte over the limit")
	// Whatever landed on disk, the CALL reported failure — so nothing
	// downstream treats the partial file as a complete one.
	if partial, readErr := os.ReadFile(filepath.Join(overDst, "over")); readErr == nil && len(partial) == 101 {
		t.Fatal("the oversized entry was written in full despite the refusal")
	}
}

// A duplicate entry name is a real trick: an archive carrying `a.py` twice is
// scanned as the first copy and extracted as the second by any extractor that
// truncates.
func TestADuplicateEntryNameIsRefused(t *testing.T) {
	_, _, err := extractTar(t, tarGz(t,
		tarEntry{name: "a.py", body: "harmless"},
		tarEntry{name: "a.py", body: "payload"},
	), Limits{})
	mustRefuse(t, err, "a duplicate entry name")
}

// A legitimate archive still extracts, with the tree it declared. Without this
// every assertion above is satisfied by an extractor that refuses everything.
func TestAnOrdinaryArchiveExtracts(t *testing.T) {
	dst, result, err := extractTar(t, tarGz(t,
		tarEntry{name: "pkg", typeflag: tar.TypeDir},
		tarEntry{name: "pkg/main.go", body: "package main\n"},
		tarEntry{name: "pkg/sub/util.go", body: "package sub\n"},
	), Limits{})
	if err != nil {
		t.Fatalf("an ordinary archive was refused: %v", err)
	}
	if result.Files != 2 || result.Dirs != 1 {
		t.Fatalf("result = %+v, want 2 files and 1 dir", result)
	}
	body, err := os.ReadFile(filepath.Join(dst, "pkg", "sub", "util.go"))
	if err != nil || string(body) != "package sub\n" {
		t.Fatalf("nested file = %q, %v", body, err)
	}
}

// The zip path takes the same policy, asserted separately because it is a
// separate implementation — a shared policy that only one format honours is
// the bug this package was written to remove.
func TestTheZipPathRefusesTheSameThings(t *testing.T) {
	build := func(t *testing.T, mutate func(*zip.Writer)) []byte {
		t.Helper()
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		mutate(zw)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	write := func(zw *zip.Writer, name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}

	traversing := build(t, func(zw *zip.Writer) { write(zw, "../escaped", "x") })
	dst := filepath.Join(t.TempDir(), "out")
	_, err := ExtractZip(bytes.NewReader(traversing), int64(len(traversing)), dst, Limits{})
	mustRefuse(t, err, "a traversing zip entry")

	// A zip symlink is a regular member whose external attributes carry
	// ModeSymlink and whose body is the link target. Built explicitly here
	// because zip.Writer.Create only ever makes regular files, so a test that
	// used it would assert nothing about the entry-type refusal.
	linked := build(t, func(zw *zip.Writer) {
		header := &zip.FileHeader{Name: "pkg"}
		header.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, "/etc"); err != nil {
			t.Fatal(err)
		}
	})
	dst = filepath.Join(t.TempDir(), "out")
	_, err = ExtractZip(bytes.NewReader(linked), int64(len(linked)), dst, Limits{})
	mustRefuse(t, err, "a symlink zip entry")
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("the refusal does not say what it refused: %v", err)
	}

	duplicate := build(t, func(zw *zip.Writer) {
		write(zw, "a.py", "harmless")
		write(zw, "a.py", "payload")
	})
	dst = filepath.Join(t.TempDir(), "out")
	_, err = ExtractZip(bytes.NewReader(duplicate), int64(len(duplicate)), dst, Limits{})
	mustRefuse(t, err, "a duplicate zip entry name")

	ordinary := build(t, func(zw *zip.Writer) { write(zw, "pkg/main.go", "package main\n") })
	dst = filepath.Join(t.TempDir(), "out")
	result, err := ExtractZip(bytes.NewReader(ordinary), int64(len(ordinary)), dst, Limits{})
	if err != nil {
		t.Fatalf("an ordinary zip was refused: %v", err)
	}
	if result.Files != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// SelectTarGz never writes to disk, so traversal is unreachable — but a name
// that climbs is a signal about the archive regardless, and the bounds still
// have to hold.
func TestSelectingFromATarballIsBoundedAndRefusesClimbingNames(t *testing.T) {
	found, err := SelectTarGz(bytes.NewReader(tarGz(t,
		tarEntry{name: "release/bin/soulacy", body: "BINARY"},
		tarEntry{name: "release/bin/sy", body: "CLI"},
		tarEntry{name: "release/README", body: "docs"},
	)), map[string]bool{"soulacy": true, "sy": true}, Limits{})
	if err != nil {
		t.Fatalf("an ordinary release tarball was refused: %v", err)
	}
	if string(found["soulacy"]) != "BINARY" || string(found["sy"]) != "CLI" {
		t.Fatalf("selection returned %v", found)
	}
	if _, unwanted := found["README"]; unwanted {
		t.Fatal("selection returned an entry nobody asked for")
	}

	_, err = SelectTarGz(bytes.NewReader(tarGz(t, tarEntry{name: "../soulacy", body: "x"})),
		map[string]bool{"soulacy": true}, Limits{})
	mustRefuse(t, err, "a climbing name in a selection")

	// The entry-count bound counts entries the caller does NOT keep, because
	// the cost an archive imposes is what it contains, not what is wanted.
	entries := make([]tarEntry, 0, 50)
	for i := 0; i < 50; i++ {
		entries = append(entries, tarEntry{name: "junk/" + strconvItoa(i), body: "x"})
	}
	_, err = SelectTarGz(bytes.NewReader(tarGz(t, entries...)), map[string]bool{"soulacy": true}, Limits{MaxEntries: 10})
	mustRefuse(t, err, "a selection past the entry-count bound")

	_, err = SelectTarGz(bytes.NewReader(tarGz(t,
		tarEntry{name: "a/soulacy", body: "one"},
		tarEntry{name: "b/soulacy", body: "two"},
	)), map[string]bool{"soulacy": true}, Limits{})
	mustRefuse(t, err, "two entries selecting to the same name")
}

func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
