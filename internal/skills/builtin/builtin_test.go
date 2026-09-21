package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/pkg/skill"
)

// Every shipped skill must parse, validate without a fatal, be named after
// its directory, and carry an eval so it can be checked.
func TestCatalogIsValid(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("catalog is empty")
	}
	dir := t.TempDir()
	if _, err := Seed(dir, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		s, err := skill.ParseFile(filepath.Join(dir, name, "SKILL.md"))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		warnings, fatal := s.Validate()
		if fatal != nil {
			t.Errorf("%s: %v", name, fatal)
		}
		for _, w := range warnings {
			t.Errorf("%s: %s", name, w)
		}
		if s.Name != name {
			t.Errorf("%s: frontmatter name %q", name, s.Name)
		}
		if len(s.Description) > 1024 {
			t.Errorf("%s: description too long (%d)", name, len(s.Description))
		}
		if _, err := os.Stat(filepath.Join(dir, name, "references", "eval.md")); err != nil {
			t.Errorf("%s: missing references/eval.md", name)
		}
	}
}

func TestSeed_RefreshesOursKeepsTheirs(t *testing.T) {
	dir := t.TempDir()
	res, err := Seed(dir, nil)
	if err != nil || len(res.Written) != len(Names()) || len(res.Kept) != 0 {
		t.Fatalf("first seed: %+v err=%v", res, err)
	}
	// Second run: nothing to do.
	res, _ = Seed(dir, nil)
	if len(res.Written) != 0 || len(res.Kept) != 0 {
		t.Fatalf("second seed should be a no-op: %+v", res)
	}
	name := Names()[0]
	skillMD := filepath.Join(dir, name, "SKILL.md")

	// The person edits a shipped skill: keep their version forever.
	orig, _ := os.ReadFile(skillMD)
	_ = os.WriteFile(skillMD, append(orig, []byte("\n<!-- my tweak -->\n")...), 0o644)
	res, _ = Seed(dir, nil)
	if len(res.Kept) != 1 || res.Kept[0] != name || len(res.Written) != 0 {
		t.Fatalf("edited skill must be kept: %+v", res)
	}
	if b, _ := os.ReadFile(skillMD); string(b) == string(orig) {
		t.Fatal("edit was overwritten")
	}

	// A directory the person created with a catalog name: never touched.
	other := Names()[len(Names())-1]
	_ = os.RemoveAll(filepath.Join(dir, other))
	_ = os.MkdirAll(filepath.Join(dir, other), 0o755)
	_ = os.WriteFile(filepath.Join(dir, other, "SKILL.md"), []byte("---\nname: "+other+"\ndescription: mine\n---\nmine\n"), 0o644)
	res, _ = Seed(dir, nil)
	found := false
	for _, k := range res.Kept {
		found = found || k == other
	}
	if !found {
		t.Fatalf("user-owned dir must be kept: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, other, "SKILL.md")); string(b) != "---\nname: "+other+"\ndescription: mine\n---\nmine\n" {
		t.Fatal("user-owned skill was overwritten")
	}
	if IsBuiltin(filepath.Join(dir, other)) {
		t.Fatal("user-owned dir must not be labelled built-in")
	}
}

// A refreshed catalog (new file content) replaces our unedited copy.
func TestSeed_UpgradeRefreshesUnedited(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir, nil); err != nil {
		t.Fatal(err)
	}
	name := Names()[0]
	// Simulate an older seeded copy by rewriting the manifest hash and file.
	p := filepath.Join(dir, name, "SKILL.md")
	m, _ := readManifest(dir)
	_ = os.WriteFile(p, []byte("old"), 0o644)
	m[name+"/SKILL.md"] = hashOf([]byte("old"))
	_ = writeManifest(dir, m)
	res, _ := Seed(dir, nil)
	if len(res.Written) != 1 || res.Written[0] != name {
		t.Fatalf("unedited old copy must be refreshed: %+v", res)
	}
	if b, _ := os.ReadFile(p); string(b) == "old" {
		t.Fatal("old copy not refreshed")
	}
	if !IsBuiltin(filepath.Join(dir, name)) {
		t.Fatal("refreshed skill must keep the marker")
	}
}
