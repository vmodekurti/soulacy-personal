// Package skills discovers and manages Agent Skills for Soulacy.
//
// Agent Skills are loaded from multiple filesystem locations following the
// convention established by agentskills.io. Skills are scanned once at startup
// and can be rescanned on demand (e.g. when the user installs a new skill).
//
// Scan priority (project-level overrides user-level when names collide):
//  1. Project-level: <cwd>/.agents/skills/  (cross-client interoperability)
//  2. Project-level: <cwd>/.soulacy/skills/  (Soulacy-native)
//  3. User-level:    ~/.soulacy/skills/  (Soulacy-native)
//  4. User-level:    ~/.agents/skills/  (cross-client interoperability)
//  5. Extra dirs:    any additional dirs from config
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/skill"

	"github.com/soulacy/soulacy/internal/config"
)

// Loader discovers and caches Agent Skills from the filesystem.
type Loader struct {
	// scanDirs is the ordered list of directories to scan.
	// Earlier entries have lower priority (later entries override on name collision).
	scanDirs []string

	mu     sync.RWMutex
	skills map[string]*skill.Skill // keyed by skill name
	log    *zap.Logger
}

// New creates a skill Loader that scans the standard locations plus any
// extra directories supplied via config.
//
// workDir should be the working directory of the gateway (project-level skills).
// extraDirs is an optional list of additional directories from config.
func New(workDir string, extraDirs []string, log *zap.Logger) *Loader {
	home, _ := os.UserHomeDir()

	// Build ordered scan list. We scan user-level first, then project-level,
	// so project-level skills override user-level on collision (project wins).
	dirs := []string{}

	// User-level (lowest priority)
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".agents", "skills"), // cross-client
			workspaceSkillsDir(home),                 // Soulacy-native ("soulspace" aware)
		)
	}

	// Project-level (overrides user-level)
	if workDir != "" {
		dirs = append(dirs,
			filepath.Join(workDir, ".agents", "skills"),  // cross-client
			filepath.Join(workDir, ".soulacy", "skills"), // Soulacy-native
		)
	}

	// Extra dirs from config (highest priority — explicitly configured)
	dirs = append(dirs, extraDirs...)

	return &Loader{
		scanDirs: dirs,
		skills:   make(map[string]*skill.Skill),
		log:      log,
	}
}

// Scan discovers all skills across all configured directories.
// Later directories override earlier ones when names collide (project > user).
// Returns non-fatal errors (e.g. unreadable dirs, malformed SKILL.md files).
func (l *Loader) Scan() []error {
	found := make(map[string]*skill.Skill)
	var errs []error

	for _, dir := range l.scanDirs {
		if err := l.scanDir(dir, found, &errs); err != nil {
			// Directory-level error (e.g. unreadable) — log and continue
			l.log.Debug("skills: cannot scan directory",
				zap.String("dir", dir), zap.Error(err))
		}
	}

	l.mu.Lock()
	l.skills = found
	l.mu.Unlock()

	if len(found) > 0 {
		names := make([]string, 0, len(found))
		for n := range found {
			names = append(names, n)
		}
		l.log.Info("skills loaded",
			zap.Int("count", len(found)),
			zap.String("skills", strings.Join(names, ", ")),
		)
	}

	return errs
}

// All returns all loaded skills (unordered).
func (l *Loader) All() []*skill.Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*skill.Skill, 0, len(l.skills))
	for _, s := range l.skills {
		out = append(out, s)
	}
	return out
}

// Get returns the skill with the given name, or nil if not found.
func (l *Loader) Get(name string) *skill.Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.skills[name]
}

// Count returns the number of loaded skills.
func (l *Loader) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.skills)
}

// BuildCatalog returns an XML skill catalog suitable for injection into an agent
// system prompt. Only name and description are included (tier 1 disclosure).
// Returns an empty string if no skills are loaded.
func (l *Loader) BuildCatalog() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	for _, s := range l.skills {
		sb.WriteString("  <skill>\n")
		sb.WriteString(fmt.Sprintf("    <name>%s</name>\n", xmlEscape(s.Name)))
		sb.WriteString(fmt.Sprintf("    <description>%s</description>\n", xmlEscape(s.Description)))
		sb.WriteString(fmt.Sprintf("    <location>%s</location>\n", xmlEscape(s.Path)))
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// ── internal ─────────────────────────────────────────────────────────────────

const (
	maxScanDepth = 5   // prevent runaway scanning in deeply nested trees
	maxDirs      = 500 // prevent scanning enormous directory trees
)

func (l *Loader) scanDir(root string, found map[string]*skill.Skill, errs *[]error) error {
	info, err := os.Stat(root)
	if err != nil {
		return err // directory doesn't exist — silently skip
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}

	scanned := 0
	return l.walk(root, 0, found, errs, &scanned)
}

func (l *Loader) walk(dir string, depth int, found map[string]*skill.Skill, errs *[]error, scanned *int) error {
	if depth > maxScanDepth || *scanned > maxDirs {
		return nil
	}
	*scanned++

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, e := range entries {
		// Skip hidden dirs that are never skills
		if e.Name() == ".git" || e.Name() == "node_modules" || e.Name() == ".svn" {
			continue
		}

		if !e.IsDir() {
			// Check if this is a SKILL.md directly in the scan root (unlikely but valid)
			continue
		}

		skillPath := filepath.Join(dir, e.Name(), "SKILL.md")
		if _, statErr := os.Stat(skillPath); statErr == nil {
			// Found a SKILL.md — parse it
			s, parseErr := skill.ParseFile(skillPath)
			if parseErr != nil {
				*errs = append(*errs, parseErr)
				l.log.Warn("skills: parse error", zap.String("file", skillPath), zap.Error(parseErr))
				continue
			}

			// Validate (warnings only — don't skip on cosmetic issues)
			if warnings, fatal := s.Validate(); fatal != nil {
				*errs = append(*errs, fmt.Errorf("skill %s: %w", skillPath, fatal))
				continue
			} else {
				for _, w := range warnings {
					l.log.Debug("skills: validation warning",
						zap.String("skill", s.Name), zap.String("warning", w))
				}
			}

			// Log collision
			if existing, ok := found[s.Name]; ok {
				l.log.Debug("skills: name collision — later directory wins",
					zap.String("name", s.Name),
					zap.String("keeping", skillPath),
					zap.String("shadowing", existing.Path),
				)
			}
			found[s.Name] = s
		} else {
			// No SKILL.md here — recurse to find nested skills
			_ = l.walk(filepath.Join(dir, e.Name()), depth+1, found, errs, scanned)
		}
	}
	return nil
}

// xmlEscape escapes the five predefined XML entities.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// workspaceSkillsDir resolves the workspace skills directory (soulspace
// layout for new installs, flat ~/.soulacy/skills for legacy ones).
func workspaceSkillsDir(home string) string {
	if ws, err := config.ResolveWorkspace(); err == nil {
		return ws.Skills
	}
	return filepath.Join(home, ".soulacy", "skills")
}
