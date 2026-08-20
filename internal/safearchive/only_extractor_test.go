// only_extractor_test.go — this package is the only one that opens an archive
// somebody else produced.
//
// Three extractors existed before it, each with a different subset of the same
// guards, and the one that mattered most had already been found the hard way:
// internal/knowledge's .docx reader measured the COMPRESSED upload and expanded
// roughly 1000:1 inside a single io.ReadAll, reachable from a route that needs
// only the chat permission. The other two were each missing a different pair of
// bounds.
//
// That is what a rule looks like when it lives in a comment. This guard makes
// it live in the type system's next best thing: a fourth extractor gets the
// policy by not being allowed to exist.
package safearchive

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// archiveReaders are the constructors that begin reading an archive. Writers
// are deliberately absent: producing an archive is not the operation with a
// threat model, and internal/workspaceexport writes one on every export.
var archiveReaders = regexp.MustCompile(`\b(?:tar\.NewReader|zip\.NewReader|zip\.OpenReader|gzip\.NewReader)\b`)

// allowedElsewhere names the non-test files outside this package that may
// still open an archive, each with the reason its threat model differs.
//
// An entry here is a claim a reviewer can check. The absence of one is the
// build failing.
var allowedElsewhere = map[string]string{
	// Reads a single named member of an uploaded .docx entirely in memory and
	// never writes a path from the archive to disk, so traversal and link
	// escape are structurally unreachable rather than merely checked. Its
	// decompression bound is the one this whole guard exists because of, and
	// it is enforced twice: on the declared size and again on the bytes that
	// actually arrive.
	"internal/knowledge/ingest.go": "reads one named member into memory; writes nothing to disk",
}

func TestArchivesAreOnlyOpenedThroughTheSafeExtractor(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	selfPackage := filepath.Join("internal", "safearchive")

	var offenders []string
	seen := map[string]bool{}
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "site", "gui", "website":
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(rel, selfPackage+string(filepath.Separator)) {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !archiveReaders.Match(source) {
			return nil
		}
		seen[rel] = true
		if _, allowed := allowedElsewhere[rel]; !allowed {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offenders)
	for _, offender := range offenders {
		t.Errorf("%s opens an archive directly — use internal/safearchive, or add it to "+
			"allowedElsewhere with the reason its threat model differs", offender)
	}

	// The allowlist must not outlive its entries. One naming a file that no
	// longer opens an archive is a stale exemption, and the next thing written
	// in that file inherits it silently.
	for rel, reason := range allowedElsewhere {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is exempted with no reason", rel)
		}
		if !seen[rel] {
			t.Errorf("%s is exempted from the archive guard but no longer opens an archive — remove the entry", rel)
		}
	}
}
