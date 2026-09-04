package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedDefaultsLoad guards the shipped templates: every embedded
// SOUL.yaml under embedded/ must parse cleanly and expose a non-empty
// display name and description. A typo in a template would otherwise only
// surface when a user opens the picker in the GUI.
func TestEmbeddedDefaultsLoad(t *testing.T) {
	cat := New("") // no user dir — pure embedded view
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one embedded template, got 0")
	}
	for _, e := range entries {
		if e.Name == "" {
			t.Errorf("template missing Name (filename basename)")
		}
		if e.DisplayName == "" {
			t.Errorf("template %s missing display name (Definition.Name)", e.Name)
		}
		if strings.TrimSpace(e.Description) == "" {
			t.Errorf("template %s missing description", e.Name)
		}
		if e.Definition == nil {
			t.Errorf("template %s did not parse to a Definition", e.Name)
			continue
		}
		if strings.TrimSpace(e.MockPrompt) == "" {
			t.Errorf("template %s missing mock prompt", e.Name)
		}
		if len(e.Setup) == 0 {
			t.Errorf("template %s missing setup metadata", e.Name)
		}
		// Every template must declare a system_prompt; an empty one means
		// the agent will silently misbehave when instantiated.
		if strings.TrimSpace(e.Definition.SystemPrompt) == "" {
			t.Errorf("template %s has empty system_prompt", e.Name)
		}
		// Source must be embedded for these.
		if e.Source != "embedded" {
			t.Errorf("template %s: source=%q, want embedded", e.Name, e.Source)
		}
	}
}

func TestCronTemplatesExposeScheduleAndDeliverySetup(t *testing.T) {
	cat := New("")
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	for _, e := range entries {
		if e.Definition == nil || e.Definition.Trigger != "cron" {
			continue
		}
		var sawSchedule, sawDelivery bool
		for _, item := range e.Setup {
			switch item.Key {
			case "schedule":
				sawSchedule = true
				if item.Status != "ready" {
					t.Errorf("%s schedule status = %q, want ready", e.Name, item.Status)
				}
			case "delivery":
				sawDelivery = true
				if item.Status != "needs_setup" {
					t.Errorf("%s delivery status = %q, want needs_setup until output channel is configured", e.Name, item.Status)
				}
			}
		}
		if !sawSchedule {
			t.Errorf("%s missing schedule setup item", e.Name)
		}
		if !sawDelivery {
			t.Errorf("%s missing delivery setup item", e.Name)
		}
		if e.ScheduleHint == "" || e.OutputHint == "" {
			t.Errorf("%s missing schedule/output hints", e.Name)
		}
	}
}

func TestRAGTemplatesExposeKnowledgeSetup(t *testing.T) {
	cat := New("")
	for _, name := range []string{"rag-over-docs", "compliance-auditor"} {
		e, err := cat.Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		var sawKnowledge bool
		for _, item := range e.Setup {
			if item.Key == "knowledge" {
				sawKnowledge = true
				if item.Status != "needs_setup" {
					t.Errorf("%s knowledge status = %q, want needs_setup", name, item.Status)
				}
			}
		}
		if !sawKnowledge {
			t.Errorf("%s missing knowledge setup item", name)
		}
	}
}

func TestTemplateMetadataDetectsNonLocalProviderSecrets(t *testing.T) {
	e, err := parseEntry("paid.yaml", []byte(`
id: paid
name: Paid Provider
description: uses a paid provider
system_prompt: hi
llm:
  provider: anthropic
  model: claude-sonnet-4-6
`), "user")
	if err != nil {
		t.Fatalf("parseEntry: %v", err)
	}
	if len(e.RequiredSecrets) != 1 {
		t.Fatalf("RequiredSecrets len = %d, want 1", len(e.RequiredSecrets))
	}
	if e.RequiredSecrets[0].Key != "anthropic.api_key" {
		t.Fatalf("secret key = %q, want anthropic.api_key", e.RequiredSecrets[0].Key)
	}
	var sawModel bool
	for _, item := range e.Setup {
		if item.Key == "model" {
			sawModel = true
			if item.Status != "needs_setup" {
				t.Fatalf("model setup status = %q, want needs_setup", item.Status)
			}
		}
	}
	if !sawModel {
		t.Fatal("missing model setup item")
	}
}

// TestUserDirOverridesEmbedded confirms the precedence rule: a user-supplied
// template with the same basename as an embedded one wins. Without this, a
// user couldn't customise a shipped template by dropping a replacement.
func TestUserDirOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	// Pick the first embedded name dynamically so this test doesn't break
	// when we add or rename a default template.
	embCat := New("")
	embAll, err := embCat.List()
	if err != nil || len(embAll) == 0 {
		t.Fatalf("need at least one embedded template; got err=%v len=%d", err, len(embAll))
	}
	target := embAll[0].Name
	userYAML := []byte("id: user-override\nname: User Override\ndescription: replaced\nsystem_prompt: hi\n")
	if err := os.WriteFile(filepath.Join(dir, target+".yaml"), userYAML, 0644); err != nil {
		t.Fatal(err)
	}

	cat := New(dir)
	entry, err := cat.Get(target)
	if err != nil {
		t.Fatalf("Get(%s): %v", target, err)
	}
	if entry.Source != "user" {
		t.Errorf("user file should shadow embedded; source=%q", entry.Source)
	}
	if entry.DisplayName != "User Override" {
		t.Errorf("user definition should win; got DisplayName=%q", entry.DisplayName)
	}
}

// TestInstantiateUniquesID confirms the uniqueness predicate is honoured —
// if "rag-over-docs" already exists, the next instantiation should land at
// "rag-over-docs-2", not collide.
func TestInstantiateUniquesID(t *testing.T) {
	cat := New("")
	taken := map[string]bool{"rag-over-docs": true, "rag-over-docs-2": true}
	mustBeUnique := func(id string) bool { return !taken[id] }

	def, err := cat.Instantiate("rag-over-docs", "", mustBeUnique)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if def.ID != "rag-over-docs-3" {
		t.Errorf("expected suffix-bumped ID rag-over-docs-3, got %q", def.ID)
	}
	if def.SourcePath != "" {
		t.Errorf("SourcePath should be cleared so Loader writes a fresh file; got %q", def.SourcePath)
	}
}

// TestInstantiateDesiredIDWins confirms a caller-supplied ID is respected
// when free.
func TestInstantiateDesiredIDWins(t *testing.T) {
	cat := New("")
	def, err := cat.Instantiate("basic-chat", "my-bot", func(string) bool { return true })
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if def.ID != "my-bot" {
		t.Errorf("desired ID should win; got %q", def.ID)
	}
}

// ---------------------------------------------------------------------------
// Catalog.Get — not found
// ---------------------------------------------------------------------------

// TestGetNotFoundReturnsError verifies that Get returns an error (not a nil
// Entry and nil error) when the template name does not exist.
func TestGetNotFoundReturnsError(t *testing.T) {
	cat := New("")
	_, err := cat.Get("this-template-does-not-exist")
	if err == nil {
		t.Fatal("Get(unknown): expected an error, got nil")
	}
}

// ---------------------------------------------------------------------------
// readUserDir edge cases
// ---------------------------------------------------------------------------

// TestUserDirMissingIsOK verifies that a non-existent userDir is silently
// treated as empty (no error, empty list).
func TestUserDirMissingIsOK(t *testing.T) {
	cat := New("/tmp/soulacy_no_such_dir_" + t.Name())
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("List with missing userDir: %v", err)
	}
	// Should still return embedded templates; just no user entries.
	for _, e := range entries {
		if e.Source == "user" {
			t.Errorf("unexpected user entry %q when userDir missing", e.Name)
		}
	}
}

// TestUserDirSkipsSubdirectories verifies that sub-directories inside the user
// templates directory are silently ignored.
func TestUserDirSkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	// Create a sub-directory — it should be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	// Write one real template so List has something to return.
	yaml := []byte("id: sub-test\nname: Sub Test\ndescription: desc\nsystem_prompt: hi\n")
	if err := os.WriteFile(filepath.Join(dir, "sub-test.yaml"), yaml, 0644); err != nil {
		t.Fatal(err)
	}

	cat := New(dir)
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.Name == "subdir" {
			t.Errorf("sub-directory should not appear as a template entry")
		}
	}
}

// TestUserDirSkipsNonYAMLFiles verifies that non-.yaml / non-.yml files in the
// user templates directory are silently ignored.
func TestUserDirSkipsNonYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	// Non-YAML files that must be skipped.
	for _, name := range []string{"readme.md", "notes.txt", "config.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ignored"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Write one valid template so the catalog isn't empty.
	valid := []byte("id: only-one\nname: Only One\ndescription: ok\nsystem_prompt: hi\n")
	if err := os.WriteFile(filepath.Join(dir, "only-one.yaml"), valid, 0644); err != nil {
		t.Fatal(err)
	}

	cat := New(dir)
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	userEntries := 0
	for _, e := range entries {
		if e.Source == "user" {
			userEntries++
		}
	}
	if userEntries != 1 {
		t.Errorf("expected exactly 1 user entry, got %d", userEntries)
	}
}

// TestUserDirAcceptsYMLExtension verifies that .yml files (not just .yaml) are
// picked up from the user templates directory.
func TestUserDirAcceptsYMLExtension(t *testing.T) {
	dir := t.TempDir()
	yaml := []byte("id: yml-test\nname: YML Test\ndescription: uses .yml\nsystem_prompt: hi\n")
	if err := os.WriteFile(filepath.Join(dir, "yml-test.yml"), yaml, 0644); err != nil {
		t.Fatal(err)
	}

	cat := New(dir)
	entry, err := cat.Get("yml-test")
	if err != nil {
		t.Fatalf("Get yml-test: %v", err)
	}
	if entry.Source != "user" {
		t.Errorf("source = %q, want user", entry.Source)
	}
	if entry.DisplayName != "YML Test" {
		t.Errorf("DisplayName = %q, want YML Test", entry.DisplayName)
	}
}

// TestUserDirSkipsBrokenYAML verifies that a syntactically broken YAML file
// in the user directory is silently skipped rather than causing List to error.
func TestUserDirSkipsBrokenYAML(t *testing.T) {
	dir := t.TempDir()
	broken := []byte("id: [\nbroken: yaml: here\n!!!\n")
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), broken, 0644); err != nil {
		t.Fatal(err)
	}
	// Add a valid template alongside the broken one.
	valid := []byte("id: good\nname: Good\ndescription: valid\nsystem_prompt: hi\n")
	if err := os.WriteFile(filepath.Join(dir, "good.yaml"), valid, 0644); err != nil {
		t.Fatal(err)
	}

	cat := New(dir)
	entries, err := cat.List()
	if err != nil {
		t.Fatalf("List with a broken user template: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "good" {
			found = true
		}
		if e.Name == "broken" {
			t.Errorf("broken template should have been skipped, but appeared in list")
		}
	}
	if !found {
		t.Error("valid template 'good' not found in list")
	}
}

// ---------------------------------------------------------------------------
// uniqueID
// ---------------------------------------------------------------------------

// TestUniqueIDBaseAlreadyFree verifies that uniqueID returns the base ID
// unchanged when the mustBeUnique predicate immediately returns true.
func TestUniqueIDBaseAlreadyFree(t *testing.T) {
	got := uniqueID("my-agent", func(string) bool { return true })
	if got != "my-agent" {
		t.Errorf("uniqueID free base = %q, want my-agent", got)
	}
}

// TestUniqueIDSuffixBumps verifies that uniqueID appends -2, -3, … until it
// finds a free slot.
func TestUniqueIDSuffixBumps(t *testing.T) {
	taken := map[string]bool{"bot": true, "bot-2": true, "bot-3": true}
	got := uniqueID("bot", func(id string) bool { return !taken[id] })
	if got != "bot-4" {
		t.Errorf("uniqueID suffix bump = %q, want bot-4", got)
	}
}

// TestUniqueIDNilPredicateReturnsBase verifies that a nil mustBeUnique predicate
// causes uniqueID to return the base immediately (no panic).
func TestUniqueIDNilPredicateReturnsBase(t *testing.T) {
	got := uniqueID("direct", nil)
	if got != "direct" {
		t.Errorf("uniqueID nil predicate = %q, want direct", got)
	}
}

// ---------------------------------------------------------------------------
// Instantiate — unknown template
// ---------------------------------------------------------------------------

// TestInstantiateUnknownTemplateErrors verifies that Instantiate returns an
// error for a template name that does not exist.
func TestInstantiateUnknownTemplateErrors(t *testing.T) {
	cat := New("")
	_, err := cat.Instantiate("no-such-template", "", func(string) bool { return true })
	if err == nil {
		t.Fatal("Instantiate(unknown): expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// DefaultUserDir
// ---------------------------------------------------------------------------

// TestDefaultUserDirContainsSoulacy verifies DefaultUserDir returns a non-empty
// path containing ".soulacy/templates" (assuming a valid $HOME).
func TestDefaultUserDirContainsSoulacy(t *testing.T) {
	d := DefaultUserDir()
	if d == "" {
		t.Skip("no home directory available in this environment")
	}
	if !strings.Contains(d, ".soulacy") {
		t.Errorf("DefaultUserDir = %q, expected it to contain .soulacy", d)
	}
	if !strings.HasSuffix(d, "templates") {
		t.Errorf("DefaultUserDir = %q, expected it to end with templates", d)
	}
}

// ---------------------------------------------------------------------------
// parseEntry — source field propagation
// ---------------------------------------------------------------------------

// TestParseEntrySourceField verifies that parseEntry correctly propagates the
// source tag ("embedded" or "user") into the returned Entry.
func TestParseEntrySourceField(t *testing.T) {
	yaml := []byte("id: src-test\nname: Src Test\ndescription: check source\nsystem_prompt: ok\n")
	for _, src := range []string{"embedded", "user"} {
		e, err := parseEntry("src-test.yaml", yaml, src)
		if err != nil {
			t.Fatalf("parseEntry source=%s: %v", src, err)
		}
		if e.Source != src {
			t.Errorf("parseEntry: source = %q, want %q", e.Source, src)
		}
	}
}

// TestParseEntryNameStripsExtension verifies that parseEntry derives the Name
// from the filename with both .yaml and .yml stripped correctly.
func TestParseEntryNameStripsExtension(t *testing.T) {
	yaml := []byte("id: ext-test\nname: Ext Test\ndescription: strip ext\nsystem_prompt: hi\n")
	for _, filename := range []string{"my-template.yaml", "my-template.yml"} {
		e, err := parseEntry(filename, yaml, "embedded")
		if err != nil {
			t.Fatalf("parseEntry filename=%s: %v", filename, err)
		}
		if e.Name != "my-template" {
			t.Errorf("parseEntry Name = %q, want my-template (filename %s)", e.Name, filename)
		}
	}
}

// Story E21: the four default agentic workflows ship in the embedded
// catalog. Removing one is a product regression, not a refactor.
func TestDefaultWorkflowsShipped(t *testing.T) {
	c := New("") // no user dir — embedded only
	entries, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"meeting-minutes":    false,
		"inbox-triage":       false,
		"market-monitor":     false,
		"compliance-auditor": false,
	}
	for _, e := range entries {
		if _, ok := want[e.Name]; ok {
			want[e.Name] = true
			if e.Description == "" || e.DisplayName == "" {
				t.Errorf("workflow template %q missing display name/description", e.Name)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("default workflow template %q missing from embedded catalog", name)
		}
	}
}
