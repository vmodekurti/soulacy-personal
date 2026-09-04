// Package skill defines the Agent Skills format for Soulacy.
//
// Spec: https://agentskills.io/specification
//
// A skill is a directory containing a SKILL.md file with YAML frontmatter.
// Skills are discovered at startup, their name+description injected into every
// agent's system prompt as a catalog, and their full body loaded on demand via
// the built-in read_skill tool.
//
// Progressive disclosure (three tiers):
//  1. Catalog  — name + description loaded at startup (~50 tokens/skill)
//  2. Instructions — full SKILL.md body loaded when the agent activates a skill
//  3. Resources — scripts/, references/, assets/ loaded on demand by the LLM
package skill

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill represents a parsed Agent Skill.
type Skill struct {
	// Required frontmatter fields
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Optional frontmatter fields
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"` // space-separated tool names

	// Body — the markdown instructions after the YAML frontmatter.
	// This is what gets loaded when the skill is activated (tier 2).
	Body string `yaml:"-"`

	// Filesystem locations
	Dir  string `yaml:"-"` // absolute path to the skill directory
	Path string `yaml:"-"` // absolute path to SKILL.md
}

// ResourceFiles returns the relative paths of all bundled resource files
// (scripts/, references/, assets/) so the agent can enumerate them.
func (s *Skill) ResourceFiles() []string {
	var files []string
	for _, subdir := range []string{"scripts", "references", "assets"} {
		dir := filepath.Join(s.Dir, subdir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // subdir doesn't exist
		}
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(subdir, e.Name()))
			}
		}
	}
	return files
}

// ParseFile reads and parses a SKILL.md file, returning a Skill or an error.
// Lenient: warns on cosmetic issues (name mismatch, length exceeded) but still
// returns the skill so the agent remains functional.
func ParseFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skill: read %s: %w", path, err)
	}

	frontmatter, body, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("skill: %s: %w", path, err)
	}

	s := &Skill{}
	// First pass: standard YAML parse
	if yamlErr := yaml.Unmarshal([]byte(frontmatter), s); yamlErr != nil {
		// Fallback: try to sanitise common issues (unquoted colons in values)
		sanitised := sanitiseFrontmatter(frontmatter)
		if err2 := yaml.Unmarshal([]byte(sanitised), s); err2 != nil {
			return nil, fmt.Errorf("skill: %s: invalid YAML: %w", path, yamlErr)
		}
	}

	if s.Description == "" {
		return nil, fmt.Errorf("skill: %s: description is required", path)
	}

	s.Body = strings.TrimSpace(body)
	s.Path = path
	s.Dir = filepath.Dir(path)

	// Lenient name fallback: derive from directory name if missing or mismatched
	dirName := filepath.Base(s.Dir)
	if s.Name == "" {
		s.Name = dirName
	}

	return s, nil
}

// Validate checks strict spec constraints. Returns a list of warnings and a
// fatal error (if the skill is unusable). Callers can choose to surface warnings
// without blocking load.
func (s *Skill) Validate() (warnings []string, fatal error) {
	if s.Name == "" {
		fatal = fmt.Errorf("name is required")
		return
	}
	if !validName.MatchString(s.Name) {
		warnings = append(warnings, fmt.Sprintf("name %q contains invalid characters (expected lowercase alphanumeric + hyphens)", s.Name))
	}
	if len(s.Name) > 64 {
		warnings = append(warnings, fmt.Sprintf("name %q exceeds 64 characters", s.Name))
	}
	if strings.HasPrefix(s.Name, "-") || strings.HasSuffix(s.Name, "-") {
		warnings = append(warnings, fmt.Sprintf("name %q must not start or end with a hyphen", s.Name))
	}
	if strings.Contains(s.Name, "--") {
		warnings = append(warnings, fmt.Sprintf("name %q must not contain consecutive hyphens", s.Name))
	}
	if filepath.Base(s.Dir) != s.Name {
		warnings = append(warnings, fmt.Sprintf("name %q does not match directory %q", s.Name, filepath.Base(s.Dir)))
	}
	if s.Description == "" {
		fatal = fmt.Errorf("description is required")
		return
	}
	if len(s.Description) > 1024 {
		warnings = append(warnings, "description exceeds 1024 characters")
	}
	if s.Compatibility != "" && len(s.Compatibility) > 500 {
		warnings = append(warnings, "compatibility exceeds 500 characters")
	}
	return
}

// ── helpers ──────────────────────────────────────────────────────────────────

// validName matches the spec: lowercase letters, digits, and single hyphens.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// splitFrontmatter separates the YAML block (between --- delimiters) from the
// markdown body. Returns an error if the frontmatter delimiters are absent.
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(content))

	// Line 1 must be "---"
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return "", "", fmt.Errorf("missing opening '---' frontmatter delimiter")
	}

	var fmLines []string
	var bodyLines []string
	inBody := false

	for scanner.Scan() {
		line := scanner.Text()
		if !inBody && strings.TrimSpace(line) == "---" {
			inBody = true
			continue
		}
		if inBody {
			bodyLines = append(bodyLines, line)
		} else {
			fmLines = append(fmLines, line)
		}
	}

	if !inBody {
		return "", "", fmt.Errorf("missing closing '---' frontmatter delimiter")
	}

	return strings.Join(fmLines, "\n"), strings.Join(bodyLines, "\n"), nil
}

// sanitiseFrontmatter attempts to fix unquoted values containing colons by
// quoting entire value portions that look like bare strings.
func sanitiseFrontmatter(fm string) string {
	// Replace "key: value with: colon" → "key: 'value with: colon'"
	// Only applies to lines where the value isn't already quoted.
	var out []string
	for _, line := range strings.Split(fm, "\n") {
		colonIdx := strings.Index(line, ": ")
		if colonIdx < 0 {
			out = append(out, line)
			continue
		}
		key := line[:colonIdx]
		val := line[colonIdx+2:]
		// If the value already starts with a quote or is a YAML keyword, leave it
		if strings.HasPrefix(val, `"`) || strings.HasPrefix(val, `'`) ||
			val == "true" || val == "false" || val == "null" ||
			!strings.Contains(val, ":") {
			out = append(out, line)
			continue
		}
		// Wrap in single quotes, escaping any existing single quotes
		val = strings.ReplaceAll(val, `'`, `''`)
		out = append(out, fmt.Sprintf("%s: '%s'", key, val))
	}
	return strings.Join(out, "\n")
}
