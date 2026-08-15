package ownership

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestCatalogIsCompleteAndInternallyConsistent(t *testing.T) {
	if err := ValidateCatalog(); err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, table := range Tables {
		if _, err := os.Stat(filepath.Join(repoRoot, table.IsolationTest)); err != nil {
			t.Errorf("table %s:%s isolation test %q: %v", table.Source, table.Name, table.IsolationTest, err)
		}
	}
	for _, repository := range Repositories {
		if _, err := os.Stat(filepath.Join(repoRoot, repository.IsolationTest)); err != nil {
			t.Errorf("repository %s isolation test %q: %v", repository.Source, repository.IsolationTest, err)
		}
	}
	required := []string{"agents", "definitions", "sessions", "messages", "events", "memory", "costs", "credentials", "secrets", "schedules", "approvals", "knowledge", "vectors", "artifacts", "workboard", "studio-drafts", "studio-traces", "studio-learning", "skills", "mcp", "plugins", "registries", "channels", "webhooks", "shares", "api-keys", "audit"}
	known := map[string]bool{}
	for _, resource := range Resources {
		known[resource.Name] = true
	}
	for _, name := range required {
		if !known[name] {
			t.Errorf("required resource %q is not classified", name)
		}
	}
}

func TestEveryStoreArchiveAndVaultRepositoryIsClassified(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	discovered, err := discoverRepositoryDeclarations(filepath.Join(repoRoot, "internal"), repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	classified := make([]string, 0, len(Repositories))
	for _, repository := range Repositories {
		classified = append(classified, repository.Source)
	}
	sort.Strings(classified)
	if strings.Join(discovered, "\n") != strings.Join(classified, "\n") {
		t.Fatalf("durable repository inventory mismatch\n\ndiscovered:\n%s\n\nclassified:\n%s\n\nClassify new Store/Archive/Vault persistence in internal/ownership/catalog.go and add its isolation test.", strings.Join(discovered, "\n"), strings.Join(classified, "\n"))
	}
}

func TestEveryDurableTableDeclarationIsClassified(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	discovered, err := discoverTableDeclarations(filepath.Join(repoRoot, "internal"), repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	classified := make([]string, 0, len(Tables))
	for _, table := range Tables {
		classified = append(classified, table.Source+":"+table.Name)
	}
	sort.Strings(classified)
	if strings.Join(discovered, "\n") != strings.Join(classified, "\n") {
		t.Fatalf("durable table inventory mismatch\n\ndiscovered:\n%s\n\nclassified:\n%s\n\nClassify new persistence in internal/ownership/catalog.go and add its isolation test.", strings.Join(discovered, "\n"), strings.Join(classified, "\n"))
	}
}

// Every declared store is now workspace-scoped and names a real isolation
// test. This assertion used to run the other way — it required blockers to
// exist, as a guard against declaring the work finished early — and it is
// inverted rather than deleted so the property stays machine-checked in the
// direction that now matters.
//
// A new personal-only store is not forbidden: some genuinely are
// single-tenant, and the classification is the point. But it must be a
// deliberate entry in the catalog with a reason, not something that arrives by
// forgetting, so this fails and names it.
func TestNoStoreRemainsUnscopedForMultiUserUse(t *testing.T) {
	blockers := MultiUserBlockers()
	if len(blockers) != 0 {
		t.Fatalf("%d store(s) are still personal-only and would leak or disappear data in a multi-user deployment:\n  %s",
			len(blockers), strings.Join(blockers, "\n  "))
	}
}

// The catalog is only as good as the tests it names. A Scoped entry pointing
// at a file that does not exist is a claim with nothing behind it.
func TestEveryScopedStoreNamesATestThatExists(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	seen := map[string]bool{}
	check := func(kind, source string, isolation Isolation, test string) {
		if isolation != Scoped {
			return
		}
		if strings.TrimSpace(test) == "" {
			t.Errorf("%s %s is scoped but names no isolation test", kind, source)
			return
		}
		if seen[test] {
			return
		}
		seen[test] = true
		if _, err := os.Stat(filepath.Join(repoRoot, test)); err != nil {
			t.Errorf("%s %s names isolation test %q, which does not exist", kind, source, test)
		}
	}
	for _, table := range Tables {
		check("table", table.Source+":"+table.Name, table.Isolation, table.IsolationTest)
	}
	for _, repository := range Repositories {
		check("repository", repository.Source, repository.Isolation, repository.IsolationTest)
	}
}

var createTablePattern = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["\x60]?([a-z_][a-z0-9_]*)`)

func discoverTableDeclarations(root, repoRoot string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			for _, match := range createTablePattern.FindAllStringSubmatch(value, -1) {
				seen[filepath.ToSlash(relative)+":"+strings.ToLower(match[1])] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(seen))
	for key := range seen {
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}

func discoverRepositoryDeclarations(root, repoRoot string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, spec := range general.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				name := strings.ToLower(typeSpec.Name.Name)
				// "loader" is included because a loader that owns a durable
				// registry is a repository by any other name — internal/runtime
				// and internal/plugins both hold tenant-visible state that the
				// original suffix list walked straight past.
				if strings.HasSuffix(name, "store") || strings.HasSuffix(name, "archive") || strings.HasSuffix(name, "history") || strings.HasSuffix(name, "vault") || strings.HasSuffix(name, "checkpoint") || strings.HasSuffix(name, "loader") {
					seen[filepath.ToSlash(relative)] = true
				}
			}
		}
		// Not every store is a type. internal/studio persists drafts and rules
		// through package-level functions that take a root directory, so the
		// type-name scan never saw them even though they hold workspace data.
		// A mutating exported function whose first parameter is a root or dir
		// path is persistence, whatever it is spelled.
		if declaresRootDirPersistence(file) {
			seen[filepath.ToSlash(relative)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(seen))
	for source := range seen {
		result = append(result, source)
	}
	sort.Strings(result)
	return result, nil
}

// declaresRootDirPersistence reports whether a file exposes package-level
// mutating persistence keyed by a caller-supplied root directory.
//
// Read-only accessors are deliberately excluded: reading a manifest out of a
// directory is not a store, and including them would bury the real ones.
func declaresRootDirPersistence(file *ast.File) bool {
	mutating := []string{"Save", "Write", "Delete", "Store", "Put", "Remove"}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || !function.Name.IsExported() {
			continue
		}
		if function.Type.Params == nil || len(function.Type.Params.List) == 0 {
			continue
		}
		first := function.Type.Params.List[0]
		if len(first.Names) == 0 {
			continue
		}
		identifier, ok := first.Type.(*ast.Ident)
		if !ok || identifier.Name != "string" {
			continue
		}
		switch strings.ToLower(first.Names[0].Name) {
		case "root", "dir":
		default:
			continue
		}
		for _, verb := range mutating {
			if strings.HasPrefix(function.Name.Name, verb) {
				return true
			}
		}
	}
	return false
}
