package studio

import "testing"

func TestDiffYAML(t *testing.T) {
	before := "name: a\ntools:\n  - read_file\ninput: \"{{x}}\"\n"
	after := "name: a\ntools:\n  - read_file\n  - write_file\ninput: \"{{y}}\"\n"

	lines, stats, text := DiffYAML(before, after)
	if len(lines) == 0 {
		t.Fatal("expected diff lines")
	}
	// One line changed (input) → 1 removed + 1 added; plus one pure addition (write_file).
	if stats.Added != 2 || stats.Removed != 1 {
		t.Errorf("stats = +%d/-%d, want +2/-1", stats.Added, stats.Removed)
	}
	// Context lines are preserved.
	if !containsLine(lines, " ", "name: a") {
		t.Error("expected unchanged 'name: a' context line")
	}
	if !containsLine(lines, "+", "  - write_file") {
		t.Error("expected added write_file line")
	}
	if !containsLine(lines, "-", "input: \"{{x}}\"") {
		t.Error("expected removed old input line")
	}
	if text == "" {
		t.Error("expected non-empty unified text")
	}
}

func TestDiffYAMLIdentical(t *testing.T) {
	s := "name: a\ntools:\n  - read_file\n"
	_, stats, _ := DiffYAML(s, s)
	if stats.Added != 0 || stats.Removed != 0 {
		t.Errorf("identical inputs should have no changes, got +%d/-%d", stats.Added, stats.Removed)
	}
}

func TestDiffYAMLEmpty(t *testing.T) {
	_, stats, _ := DiffYAML("", "new line\n")
	if stats.Added != 1 || stats.Removed != 0 {
		t.Errorf("empty→one line should be +1/-0, got +%d/-%d", stats.Added, stats.Removed)
	}
}

func containsLine(lines []DiffLine, op, text string) bool {
	for _, l := range lines {
		if l.Op == op && l.Text == text {
			return true
		}
	}
	return false
}
