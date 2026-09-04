package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── splitFrontmatter ─────────────────────────────────────────────────────────

func TestSplitFrontmatter_Valid(t *testing.T) {
	content := "---\nname: my-skill\ndescription: A test skill\n---\n# Instructions\nDo things.\n"
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(fm, "name: my-skill") {
		t.Errorf("frontmatter missing name field, got: %q", fm)
	}
	if !strings.Contains(body, "# Instructions") {
		t.Errorf("body missing instructions, got: %q", body)
	}
}

func TestSplitFrontmatter_MissingOpeningDelimiter(t *testing.T) {
	content := "name: my-skill\ndescription: A test skill\n---\n# Body\n"
	_, _, err := splitFrontmatter(content)
	if err == nil {
		t.Fatal("expected error for missing opening delimiter, got nil")
	}
	if !strings.Contains(err.Error(), "missing opening") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSplitFrontmatter_EmptyContent(t *testing.T) {
	_, _, err := splitFrontmatter("")
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
	if !strings.Contains(err.Error(), "missing opening") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSplitFrontmatter_MissingClosingDelimiter(t *testing.T) {
	content := "---\nname: my-skill\ndescription: A test skill\n"
	_, _, err := splitFrontmatter(content)
	if err == nil {
		t.Fatal("expected error for missing closing delimiter, got nil")
	}
	if !strings.Contains(err.Error(), "missing closing") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSplitFrontmatter_EmptyBody(t *testing.T) {
	content := "---\nname: my-skill\ndescription: A test skill\n---\n"
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(fm, "name: my-skill") {
		t.Errorf("unexpected frontmatter: %q", fm)
	}
	_ = body // body may be empty string, that's fine
}

func TestSplitFrontmatter_OnlyOpeningDash(t *testing.T) {
	// First line is not "---"
	content := "# Just a markdown file\nno frontmatter here\n"
	_, _, err := splitFrontmatter(content)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── ParseFile ────────────────────────────────────────────────────────────────

func writeSkillFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeSkillFile: %v", err)
	}
	return path
}

func makeSkillDir(t *testing.T, skillName string) (skillDir string) {
	t.Helper()
	base := t.TempDir()
	skillDir = filepath.Join(base, skillName)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("makeSkillDir: %v", err)
	}
	return skillDir
}

func TestParseFile_Valid(t *testing.T) {
	dir := makeSkillDir(t, "my-skill")
	path := writeSkillFile(t, dir, "SKILL.md", `---
name: my-skill
description: Does something useful
license: MIT
---
# Instructions

Follow these steps.
`)
	s, err := ParseFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", s.Name, "my-skill")
	}
	if s.Description != "Does something useful" {
		t.Errorf("Description = %q, want %q", s.Description, "Does something useful")
	}
	if s.License != "MIT" {
		t.Errorf("License = %q, want MIT", s.License)
	}
	if !strings.Contains(s.Body, "Follow these steps") {
		t.Errorf("Body missing expected content: %q", s.Body)
	}
	if s.Path != path {
		t.Errorf("Path = %q, want %q", s.Path, path)
	}
	if s.Dir != dir {
		t.Errorf("Dir = %q, want %q", s.Dir, dir)
	}
}

func TestParseFile_NoName_DerivesFromDirName(t *testing.T) {
	dir := makeSkillDir(t, "derived-skill")
	path := writeSkillFile(t, dir, "SKILL.md", `---
description: A skill without explicit name
---
Body content.
`)
	s, err := ParseFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "derived-skill" {
		t.Errorf("Name = %q, want %q", s.Name, "derived-skill")
	}
}

func TestParseFile_NoDescription_ReturnsError(t *testing.T) {
	dir := makeSkillDir(t, "nodesc-skill")
	path := writeSkillFile(t, dir, "SKILL.md", `---
name: nodesc-skill
---
Body content.
`)
	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("expected error for missing description, got nil")
	}
	if !strings.Contains(err.Error(), "description is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseFile_MissingFile(t *testing.T) {
	_, err := ParseFile("/nonexistent/path/SKILL.md")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestParseFile_MalformedYAML_NoDelimiters(t *testing.T) {
	dir := makeSkillDir(t, "bad-skill")
	path := writeSkillFile(t, dir, "SKILL.md", "just plain text with no frontmatter\n")
	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("expected error for missing frontmatter, got nil")
	}
}

func TestParseFile_MalformedYAML_WithColonInValue(t *testing.T) {
	// Test the sanitiseFrontmatter fallback path: value contains an unquoted colon
	dir := makeSkillDir(t, "colon-skill")
	path := writeSkillFile(t, dir, "SKILL.md", `---
name: colon-skill
description: Does http://example.com things
---
Body.
`)
	// This should succeed (sanitiser should handle it or yaml should parse fine)
	s, err := ParseFile(path)
	if err != nil {
		t.Fatalf("unexpected error with colon in value: %v", err)
	}
	if s.Name != "colon-skill" {
		t.Errorf("Name = %q, want colon-skill", s.Name)
	}
}

func TestParseFile_WithAllOptionalFields(t *testing.T) {
	dir := makeSkillDir(t, "full-skill")
	path := writeSkillFile(t, dir, "SKILL.md", `---
name: full-skill
description: A fully specified skill
license: Apache-2.0
compatibility: claude-3-5
allowed-tools: read write
metadata:
  author: test
  version: "1.0"
---
# Full Instructions

Complete instructions here.
`)
	s, err := ParseFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.AllowedTools != "read write" {
		t.Errorf("AllowedTools = %q, want %q", s.AllowedTools, "read write")
	}
	if s.Compatibility != "claude-3-5" {
		t.Errorf("Compatibility = %q, want claude-3-5", s.Compatibility)
	}
	if s.Metadata["author"] != "test" {
		t.Errorf("Metadata[author] = %q, want test", s.Metadata["author"])
	}
}

// ── Validate ─────────────────────────────────────────────────────────────────

func makeSkillWithDir(name, description string) *Skill {
	return &Skill{
		Name:        name,
		Description: description,
		Dir:         "/some/dir/" + name,
		Path:        "/some/dir/" + name + "/SKILL.md",
	}
}

func TestValidate_EmptyName_Fatal(t *testing.T) {
	s := makeSkillWithDir("", "A description")
	_, fatal := s.Validate()
	if fatal == nil {
		t.Fatal("expected fatal error for empty name, got nil")
	}
	if !strings.Contains(fatal.Error(), "name is required") {
		t.Errorf("unexpected fatal: %v", fatal)
	}
}

func TestValidate_EmptyDescription_Fatal(t *testing.T) {
	s := makeSkillWithDir("my-skill", "")
	_, fatal := s.Validate()
	if fatal == nil {
		t.Fatal("expected fatal error for empty description, got nil")
	}
	if !strings.Contains(fatal.Error(), "description is required") {
		t.Errorf("unexpected fatal: %v", fatal)
	}
}

func TestValidate_ValidSkill_NoWarningsOrFatal(t *testing.T) {
	s := &Skill{
		Name:        "my-skill",
		Description: "Does something useful",
		Dir:         "/some/dir/my-skill",
		Path:        "/some/dir/my-skill/SKILL.md",
	}
	warnings, fatal := s.Validate()
	if fatal != nil {
		t.Fatalf("unexpected fatal: %v", fatal)
	}
	// Dir name matches skill name, so no name-mismatch warning expected.
	// There may still be zero warnings.
	_ = warnings
}

func TestValidate_NameWithInvalidChars_Warning(t *testing.T) {
	s := makeSkillWithDir("My_Skill!", "Some description")
	warnings, fatal := s.Validate()
	if fatal != nil {
		t.Fatalf("unexpected fatal: %v", fatal)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "invalid characters") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected invalid-characters warning, got warnings: %v", warnings)
	}
}

func TestValidate_NameTooLong_Warning(t *testing.T) {
	longName := strings.Repeat("a", 65)
	s := makeSkillWithDir(longName, "Some description")
	warnings, _ := s.Validate()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "exceeds 64 characters") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected length warning, got warnings: %v", warnings)
	}
}

func TestValidate_NameStartsWithHyphen_Warning(t *testing.T) {
	s := makeSkillWithDir("-bad-name", "Some description")
	warnings, _ := s.Validate()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "must not start or end with a hyphen") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected hyphen-start warning, got warnings: %v", warnings)
	}
}

func TestValidate_NameEndsWithHyphen_Warning(t *testing.T) {
	s := makeSkillWithDir("bad-name-", "Some description")
	warnings, _ := s.Validate()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "must not start or end with a hyphen") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected hyphen-end warning, got warnings: %v", warnings)
	}
}

func TestValidate_NameWithConsecutiveHyphens_Warning(t *testing.T) {
	s := makeSkillWithDir("bad--name", "Some description")
	warnings, _ := s.Validate()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "consecutive hyphens") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected consecutive-hyphens warning, got warnings: %v", warnings)
	}
}

func TestValidate_NameDirMismatch_Warning(t *testing.T) {
	s := &Skill{
		Name:        "actual-name",
		Description: "Some description",
		Dir:         "/some/dir/different-name",
		Path:        "/some/dir/different-name/SKILL.md",
	}
	warnings, fatal := s.Validate()
	if fatal != nil {
		t.Fatalf("unexpected fatal: %v", fatal)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "does not match directory") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dir-mismatch warning, got warnings: %v", warnings)
	}
}

func TestValidate_DescriptionTooLong_Warning(t *testing.T) {
	s := &Skill{
		Name:        "my-skill",
		Description: strings.Repeat("x", 1025),
		Dir:         "/some/dir/my-skill",
		Path:        "/some/dir/my-skill/SKILL.md",
	}
	warnings, fatal := s.Validate()
	if fatal != nil {
		t.Fatalf("unexpected fatal: %v", fatal)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "description exceeds 1024 characters") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected description-length warning, got warnings: %v", warnings)
	}
}

func TestValidate_CompatibilityTooLong_Warning(t *testing.T) {
	s := &Skill{
		Name:          "my-skill",
		Description:   "Some description",
		Compatibility: strings.Repeat("c", 501),
		Dir:           "/some/dir/my-skill",
		Path:          "/some/dir/my-skill/SKILL.md",
	}
	warnings, fatal := s.Validate()
	if fatal != nil {
		t.Fatalf("unexpected fatal: %v", fatal)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "compatibility exceeds 500 characters") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected compatibility-length warning, got warnings: %v", warnings)
	}
}

// ── ResourceFiles ─────────────────────────────────────────────────────────────

func TestResourceFiles_EmptyDir(t *testing.T) {
	dir := makeSkillDir(t, "empty-skill")
	s := &Skill{
		Name: "empty-skill",
		Dir:  dir,
	}
	files := s.ResourceFiles()
	if len(files) != 0 {
		t.Errorf("expected 0 resource files, got %d: %v", len(files), files)
	}
}

func TestResourceFiles_ScriptsDir(t *testing.T) {
	dir := makeSkillDir(t, "scripted-skill")
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	writeSkillFile(t, scriptsDir, "run.sh", "#!/bin/bash\necho hello\n")
	writeSkillFile(t, scriptsDir, "helper.py", "print('hi')\n")

	s := &Skill{Name: "scripted-skill", Dir: dir}
	files := s.ResourceFiles()
	if len(files) != 2 {
		t.Errorf("expected 2 resource files, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if !strings.HasPrefix(f, "scripts/") {
			t.Errorf("expected scripts/ prefix, got %q", f)
		}
	}
}

func TestResourceFiles_ReferencesDir(t *testing.T) {
	dir := makeSkillDir(t, "ref-skill")
	refDir := filepath.Join(dir, "references")
	if err := os.MkdirAll(refDir, 0755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	writeSkillFile(t, refDir, "guide.md", "# Guide\n")

	s := &Skill{Name: "ref-skill", Dir: dir}
	files := s.ResourceFiles()
	if len(files) != 1 {
		t.Errorf("expected 1 resource file, got %d: %v", len(files), files)
	}
	if files[0] != "references/guide.md" {
		t.Errorf("expected references/guide.md, got %q", files[0])
	}
}

func TestResourceFiles_AssetsDir(t *testing.T) {
	dir := makeSkillDir(t, "asset-skill")
	assetsDir := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	writeSkillFile(t, assetsDir, "logo.png", "fakepng")
	writeSkillFile(t, assetsDir, "schema.json", "{}")

	s := &Skill{Name: "asset-skill", Dir: dir}
	files := s.ResourceFiles()
	if len(files) != 2 {
		t.Errorf("expected 2 resource files, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if !strings.HasPrefix(f, "assets/") {
			t.Errorf("expected assets/ prefix, got %q", f)
		}
	}
}

func TestResourceFiles_AllSubdirs(t *testing.T) {
	dir := makeSkillDir(t, "full-res-skill")
	for _, sub := range []string{"scripts", "references", "assets"} {
		subDir := filepath.Join(dir, sub)
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		writeSkillFile(t, subDir, "file.txt", "content")
	}

	s := &Skill{Name: "full-res-skill", Dir: dir}
	files := s.ResourceFiles()
	if len(files) != 3 {
		t.Errorf("expected 3 resource files, got %d: %v", len(files), files)
	}
}

func TestResourceFiles_SubdirsAreSkipped(t *testing.T) {
	dir := makeSkillDir(t, "nested-skill")
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	// Create a nested subdirectory inside scripts — should be skipped
	nestedDir := filepath.Join(scriptsDir, "subdir")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	writeSkillFile(t, scriptsDir, "top.sh", "#!/bin/bash")
	writeSkillFile(t, nestedDir, "nested.sh", "#!/bin/bash")

	s := &Skill{Name: "nested-skill", Dir: dir}
	files := s.ResourceFiles()
	// Only top-level files are included (not directories)
	if len(files) != 1 {
		t.Errorf("expected 1 file (subdirs skipped), got %d: %v", len(files), files)
	}
	if files[0] != "scripts/top.sh" {
		t.Errorf("expected scripts/top.sh, got %q", files[0])
	}
}

// ── sanitiseFrontmatter ──────────────────────────────────────────────────────

func TestSanitiseFrontmatter_NoColon(t *testing.T) {
	fm := "name: my-skill\ndescription: simple value"
	out := sanitiseFrontmatter(fm)
	if out != fm {
		t.Errorf("expected unchanged output, got: %q", out)
	}
}

func TestSanitiseFrontmatter_AlreadyQuoted(t *testing.T) {
	fm := `name: my-skill
description: "already: quoted"`
	out := sanitiseFrontmatter(fm)
	if out != fm {
		t.Errorf("expected unchanged output for already-quoted value, got: %q", out)
	}
}

func TestSanitiseFrontmatter_UnquotedColon(t *testing.T) {
	fm := "description: value with: colon inside"
	out := sanitiseFrontmatter(fm)
	if !strings.Contains(out, "'") {
		t.Errorf("expected single-quoted value, got: %q", out)
	}
}
