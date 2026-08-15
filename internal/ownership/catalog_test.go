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

func TestLegacyTenantStoresAreExplicitlyBlockedFromMultiUserUse(t *testing.T) {
	if len(MultiUserBlockers()) == 0 {
		t.Fatal("expected legacy personal-only stores until resource isolation stories are complete")
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
				if strings.HasSuffix(name, "store") || strings.HasSuffix(name, "archive") || strings.HasSuffix(name, "history") || strings.HasSuffix(name, "vault") || strings.HasSuffix(name, "checkpoint") {
					seen[filepath.ToSlash(relative)] = true
				}
			}
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
