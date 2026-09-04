package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/skill"
)

// newTestLoader creates a Loader with only the given scan dirs, bypassing the
// home-directory discovery in New(). This keeps tests hermetic: they won't pick
// up skills the developer has installed in ~/.agents/skills or ~/.soulacy/skills.
func newTestLoader(scanDirs ...string) *Loader {
	return &Loader{
		scanDirs: scanDirs,
		skills:   make(map[string]*skill.Skill),
		log:      zap.NewNop(),
	}
}

// testLogger returns a no-op zap logger for tests.
func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	log, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	return log
}

// noopLogger returns a zap.NewNop() logger (silent).
func noopLogger() *zap.Logger {
	return zap.NewNop()
}

// writeSkillMD creates a SKILL.md file at dir/SKILL.md with valid frontmatter.
func writeSkillMD(t *testing.T, dir, name, description string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n# Instructions\nDo the thing.\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeSkillMD: %v", err)
	}
}

// makeSkillDir creates a skill directory with a valid SKILL.md inside a root dir.
func makeSkillDir(t *testing.T, root, skillDirName, skillName, description string) string {
	t.Helper()
	skillDir := filepath.Join(root, skillDirName)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("makeSkillDir MkdirAll: %v", err)
	}
	writeSkillMD(t, skillDir, skillName, description)
	return skillDir
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_ReturnsLoader(t *testing.T) {
	l := New("", nil, noopLogger())
	if l == nil {
		t.Fatal("New returned nil")
	}
}

func TestNew_WorkDirIncludedInScanDirs(t *testing.T) {
	workDir := t.TempDir()
	l := New(workDir, nil, noopLogger())
	found := false
	for _, d := range l.scanDirs {
		if strings.HasPrefix(d, workDir) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("workDir %q not reflected in scanDirs: %v", workDir, l.scanDirs)
	}
}

func TestNew_ExtraDirsAppended(t *testing.T) {
	extra := []string{"/tmp/extra1", "/tmp/extra2"}
	l := New("", extra, noopLogger())
	for _, e := range extra {
		found := false
		for _, d := range l.scanDirs {
			if d == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("extra dir %q not found in scanDirs: %v", e, l.scanDirs)
		}
	}
}

func TestNew_EmptyWorkDir_NoProjectDirs(t *testing.T) {
	l := New("", nil, noopLogger())
	for _, d := range l.scanDirs {
		if strings.HasPrefix(d, "/.agents") || strings.HasPrefix(d, "/.soulacy") {
			t.Errorf("unexpected project-relative dir in scanDirs when workDir is empty: %q", d)
		}
	}
}

// ── Scan ─────────────────────────────────────────────────────────────────────

func TestScan_FindsSkillInExtraDir(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "my-skill", "my-skill", "Does something useful")

	l := newTestLoader(root)
	errs := l.Scan()
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if l.Count() != 1 {
		t.Errorf("expected 1 skill, got %d", l.Count())
	}
}

func TestScan_FindsMultipleSkills(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "skill-a", "skill-a", "First skill")
	makeSkillDir(t, root, "skill-b", "skill-b", "Second skill")
	makeSkillDir(t, root, "skill-c", "skill-c", "Third skill")

	l := newTestLoader(root)
	errs := l.Scan()
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if l.Count() != 3 {
		t.Errorf("expected 3 skills, got %d", l.Count())
	}
}

func TestScan_NonExistentDir_NoError(t *testing.T) {
	l := newTestLoader("/absolutely/nonexistent/path/xyz")
	errs := l.Scan()
	// Non-existent dirs are silently skipped (logged at Debug level)
	if len(errs) != 0 {
		t.Errorf("expected no errors for nonexistent dir, got: %v", errs)
	}
	if l.Count() != 0 {
		t.Errorf("expected 0 skills, got %d", l.Count())
	}
}

func TestScan_SkipsGitDir(t *testing.T) {
	root := t.TempDir()
	// Create a .git directory with a SKILL.md inside — should be skipped
	gitSkillDir := filepath.Join(root, ".git", "my-skill")
	if err := os.MkdirAll(gitSkillDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeSkillMD(t, gitSkillDir, "my-skill", "Hidden skill")

	// Also create a real skill
	makeSkillDir(t, root, "real-skill", "real-skill", "Real skill")

	l := newTestLoader(root)
	l.Scan()
	if l.Count() != 1 {
		t.Errorf("expected 1 skill (git dir skipped), got %d", l.Count())
	}
}

func TestScan_MalformedSkillMD_ReturnsError(t *testing.T) {
	root := t.TempDir()
	badSkillDir := filepath.Join(root, "bad-skill")
	if err := os.MkdirAll(badSkillDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Write a SKILL.md with no frontmatter at all
	badContent := "just plain text with no frontmatter delimiters\n"
	if err := os.WriteFile(filepath.Join(badSkillDir, "SKILL.md"), []byte(badContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l := New("", []string{root}, noopLogger())
	errs := l.Scan()
	if len(errs) == 0 {
		t.Error("expected parse errors for malformed SKILL.md, got none")
	}
}

func TestScan_MissingDescription_ReturnsError(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "nodesc-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\nname: nodesc-skill\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l := New("", []string{root}, noopLogger())
	errs := l.Scan()
	if len(errs) == 0 {
		t.Error("expected error for missing description, got none")
	}
}

func TestScan_LaterDirOverridesEarlier(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	makeSkillDir(t, root1, "shared-skill", "shared-skill", "From root1")
	makeSkillDir(t, root2, "shared-skill", "shared-skill", "From root2")

	l := New("", []string{root1, root2}, noopLogger())
	l.Scan()

	s := l.Get("shared-skill")
	if s == nil {
		t.Fatal("expected to find shared-skill")
	}
	// root2 wins (later dir)
	if s.Description != "From root2" {
		t.Errorf("expected description from root2, got %q", s.Description)
	}
}

func TestScan_IsIdempotent(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "my-skill", "my-skill", "Idempotent test")

	l := newTestLoader(root)
	l.Scan()
	l.Scan() // second scan should give same result
	if l.Count() != 1 {
		t.Errorf("expected 1 skill after double scan, got %d", l.Count())
	}
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGet_ReturnsNilForUnknownSkill(t *testing.T) {
	l := New("", nil, noopLogger())
	l.Scan()
	if s := l.Get("nonexistent"); s != nil {
		t.Errorf("expected nil for unknown skill, got %+v", s)
	}
}

func TestGet_ReturnsCorrectSkill(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "target-skill", "target-skill", "The target skill")
	makeSkillDir(t, root, "other-skill", "other-skill", "Another skill")

	l := New("", []string{root}, noopLogger())
	l.Scan()

	s := l.Get("target-skill")
	if s == nil {
		t.Fatal("expected to find target-skill, got nil")
	}
	if s.Name != "target-skill" {
		t.Errorf("Name = %q, want target-skill", s.Name)
	}
	if s.Description != "The target skill" {
		t.Errorf("Description = %q, want 'The target skill'", s.Description)
	}
}

func TestGet_BeforeScan_ReturnsNil(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "my-skill", "my-skill", "Some skill")

	l := New("", []string{root}, noopLogger())
	// No Scan call — should return nil
	if s := l.Get("my-skill"); s != nil {
		t.Errorf("expected nil before scan, got %+v", s)
	}
}

// ── All ───────────────────────────────────────────────────────────────────────

func TestAll_ReturnsAllLoadedSkills(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "skill-one", "skill-one", "First")
	makeSkillDir(t, root, "skill-two", "skill-two", "Second")

	l := newTestLoader(root)
	l.Scan()

	all := l.All()
	if len(all) != 2 {
		t.Errorf("expected 2 skills from All(), got %d", len(all))
	}
}

func TestAll_EmptyWhenNoSkills(t *testing.T) {
	l := newTestLoader() // no scan dirs — guaranteed empty
	l.Scan()

	all := l.All()
	if len(all) != 0 {
		t.Errorf("expected 0 skills, got %d", len(all))
	}
}

func TestAll_BeforeScan_ReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "my-skill", "my-skill", "A skill")

	l := New("", []string{root}, noopLogger())
	all := l.All() // no Scan
	if len(all) != 0 {
		t.Errorf("expected 0 before scan, got %d", len(all))
	}
}

// ── Count ─────────────────────────────────────────────────────────────────────

func TestCount_ReturnsCorrectCount(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "s1", "s1", "Skill one")
	makeSkillDir(t, root, "s2", "s2", "Skill two")
	makeSkillDir(t, root, "s3", "s3", "Skill three")

	l := newTestLoader(root)
	l.Scan()

	if l.Count() != 3 {
		t.Errorf("expected Count()=3, got %d", l.Count())
	}
}

// ── BuildCatalog ─────────────────────────────────────────────────────────────

func TestBuildCatalog_NonEmptyWhenSkillsExist(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "catalog-skill", "catalog-skill", "A catalogued skill")

	l := New("", []string{root}, noopLogger())
	l.Scan()

	catalog := l.BuildCatalog()
	if catalog == "" {
		t.Fatal("expected non-empty catalog, got empty string")
	}
	if !strings.Contains(catalog, "<available_skills>") {
		t.Errorf("catalog missing <available_skills> tag: %q", catalog)
	}
	if !strings.Contains(catalog, "catalog-skill") {
		t.Errorf("catalog missing skill name: %q", catalog)
	}
	if !strings.Contains(catalog, "A catalogued skill") {
		t.Errorf("catalog missing skill description: %q", catalog)
	}
}

func TestBuildCatalog_EmptyWhenNoSkills(t *testing.T) {
	l := newTestLoader() // no scan dirs — guaranteed empty
	l.Scan()

	catalog := l.BuildCatalog()
	if catalog != "" {
		t.Errorf("expected empty catalog when no skills, got: %q", catalog)
	}
}

func TestBuildCatalog_XMLEscapesSpecialChars(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "xml-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Description with special XML chars — use YAML single-quote to avoid ambiguity
	content := "---\nname: xml-skill\ndescription: 'Skill with <tags> & quotes'\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l := New("", []string{root}, noopLogger())
	l.Scan()

	catalog := l.BuildCatalog()
	if strings.Contains(catalog, "<tags>") {
		t.Error("catalog should XML-escape < in description, found raw <tags>")
	}
	if !strings.Contains(catalog, "&lt;tags&gt;") {
		t.Errorf("catalog missing XML-escaped &lt;tags&gt;, got: %q", catalog)
	}
	if !strings.Contains(catalog, "&amp;") {
		t.Errorf("catalog missing XML-escaped &amp;, got: %q", catalog)
	}
}

func TestBuildCatalog_ContainsLocationField(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "loc-skill", "loc-skill", "Has location")

	l := New("", []string{root}, noopLogger())
	l.Scan()

	catalog := l.BuildCatalog()
	if !strings.Contains(catalog, "<location>") {
		t.Errorf("catalog missing <location> element: %q", catalog)
	}
}

// ── xmlEscape ─────────────────────────────────────────────────────────────────

func TestXMLEscape_Ampersand(t *testing.T) {
	got := xmlEscape("a & b")
	if got != "a &amp; b" {
		t.Errorf("xmlEscape ampersand: got %q, want %q", got, "a &amp; b")
	}
}

func TestXMLEscape_LessThan(t *testing.T) {
	got := xmlEscape("a < b")
	if got != "a &lt; b" {
		t.Errorf("xmlEscape less-than: got %q, want %q", got, "a &lt; b")
	}
}

func TestXMLEscape_GreaterThan(t *testing.T) {
	got := xmlEscape("a > b")
	if got != "a &gt; b" {
		t.Errorf("xmlEscape greater-than: got %q, want %q", got, "a &gt; b")
	}
}

func TestXMLEscape_DoubleQuote(t *testing.T) {
	got := xmlEscape(`say "hello"`)
	if got != "say &quot;hello&quot;" {
		t.Errorf("xmlEscape double-quote: got %q, want %q", got, "say &quot;hello&quot;")
	}
}

func TestXMLEscape_SingleQuote(t *testing.T) {
	got := xmlEscape("it's")
	if got != "it&#39;s" {
		t.Errorf("xmlEscape single-quote: got %q, want %q", got, "it&#39;s")
	}
}

func TestXMLEscape_NoSpecialChars(t *testing.T) {
	input := "plain text"
	got := xmlEscape(input)
	if got != input {
		t.Errorf("xmlEscape plain: got %q, want %q", got, input)
	}
}

func TestXMLEscape_AllSpecialChars(t *testing.T) {
	input := `<a & "b" & 'c'>`
	got := xmlEscape(input)
	if strings.ContainsAny(got, `<>"'&`) {
		// The only & allowed is as part of &amp; etc.
		// Actually it will contain & from entity refs, let's check differently.
		t.Logf("xmlEscape all special: %q", got)
	}
	// Check that no raw < or > remain
	if strings.Contains(got, "<a") || strings.Contains(got, "c'>") {
		t.Errorf("xmlEscape did not escape all special chars: %q", got)
	}
}

// ── scanDir / walk edge cases ─────────────────────────────────────────────────

func TestScan_NotADirectory_ReturnsError(t *testing.T) {
	// Create a file (not a dir) and pass it as a scan dir via extraDirs
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l := newTestLoader(filePath)
	// Scan should not panic; the file-not-a-directory case is handled internally
	// The scanDir function returns an error which is logged at Debug and skipped
	l.Scan()
	// No assertion on errs here since file-not-a-dir is a dir-level error
	// (logged but not appended to returned errs)
	if l.Count() != 0 {
		t.Errorf("expected 0 skills for non-dir path, got %d", l.Count())
	}
}

func TestScan_NestedSkills_Found(t *testing.T) {
	// Skills can be nested one level deep
	root := t.TempDir()
	nestedParent := filepath.Join(root, "category")
	if err := os.MkdirAll(nestedParent, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	makeSkillDir(t, nestedParent, "nested-skill", "nested-skill", "A nested skill")

	l := newTestLoader(root)
	l.Scan()

	if l.Count() != 1 {
		t.Errorf("expected 1 nested skill, got %d", l.Count())
	}
}

func TestScan_SkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	nmDir := filepath.Join(root, "node_modules", "some-skill")
	if err := os.MkdirAll(nmDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeSkillMD(t, nmDir, "some-skill", "A skill in node_modules")

	l := newTestLoader(root)
	l.Scan()

	if l.Count() != 0 {
		t.Errorf("expected node_modules to be skipped, got %d skills", l.Count())
	}
}

func TestScan_WithLogger(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "logged-skill", "logged-skill", "Testing with real logger")

	log := testLogger(t)
	defer log.Sync() //nolint:errcheck
	l := &Loader{scanDirs: []string{root}, skills: make(map[string]*skill.Skill), log: log}
	errs := l.Scan()
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if l.Count() != 1 {
		t.Errorf("expected 1 skill, got %d", l.Count())
	}
}
