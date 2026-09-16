package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// StarterAgents are installed on disk the first time a gateway runs, so a new
// installation has something useful under Deployed rather than an empty list
// and a Templates page to go shopping in.
//
// "system" and "genie" are not here: they are built in (see
// runtime.Loader.seedBuiltins) and need no file. This list is for agents that
// should exist as ordinary, editable YAML.
//
// Editable, but not all of them deletable. Steward is core to what Soulacy
// does, so runtime.IsUndeletableAgent refuses to remove it; it can still be
// rewritten, renamed and switched off like any other agent. The record below
// still governs seeding — an installation that deleted Steward before this
// rule existed keeps it deleted, because silently recreating an agent someone
// removed would be its own surprise.
var StarterAgents = []string{"getting-to-know-you", "steward"}

// starterRecord remembers which starters an installation has already been
// offered. It is not a list of what exists now: deleting a starter must keep
// it deleted, and adding a new starter in a later release must still install
// that one.
type starterRecord struct {
	Seeded []string `json:"seeded"`
}

const starterRecordName = ".starter-agents.json"

// SeedStarterAgents installs any starter the installation has not been given
// before. Safe to call on every boot.
//
// Failure is never fatal. A gateway that cannot write a starter agent is
// still a working gateway, and refusing to start over a convenience would be
// the wrong trade.
func (s *Server) SeedStarterAgents() {
	if len(s.cfg.AgentDirs) == 0 || s.loader == nil {
		return
	}
	dir := s.cfg.AgentDirs[0]
	if strings.TrimSpace(dir) == "" {
		return
	}
	recordPath := filepath.Join(dir, starterRecordName)
	record := readStarterRecord(recordPath)
	already := map[string]bool{}
	for _, name := range record.Seeded {
		already[name] = true
	}

	installed := 0
	for _, name := range StarterAgents {
		if already[name] {
			continue
		}
		// Mark it seeded even when the install fails, so a template that is
		// broken on this gateway cannot make every boot retry and re-log.
		record.Seeded = append(record.Seeded, name)
		if s.loader.Get(name) != nil {
			continue // someone already installed it by hand
		}
		if err := s.installStarter(dir, name); err != nil {
			s.log.Warn("could not install starter agent",
				zap.String("template", name), zap.Error(err))
			continue
		}
		installed++
		s.log.Info("installed starter agent", zap.String("agent", name))
	}
	if installed == 0 && len(record.Seeded) == len(already) {
		return
	}
	writeStarterRecord(recordPath, record, s.log)
}

func (s *Server) installStarter(dir, template string) error {
	def, err := s.templatesCatalog().Instantiate(template+"-template", template, func(candidate string) bool {
		return s.loader.Get(candidate) == nil
	})
	if err != nil {
		// Some templates are named without the suffix; try the bare name.
		def, err = s.templatesCatalog().Instantiate(template, template, func(candidate string) bool {
			return s.loader.Get(candidate) == nil
		})
		if err != nil {
			return err
		}
	}
	// A starter must run on whatever model this gateway is configured for,
	// not the conservative example provider the template carries.
	s.applyTemplateDefinitionDefaults(def)
	def.Enabled = true
	return s.loader.Upsert(dir, def)
}

func readStarterRecord(path string) starterRecord {
	var record starterRecord
	data, err := os.ReadFile(path) // #nosec G304 — path is the configured agent dir
	if err != nil {
		return record
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return starterRecord{}
	}
	sort.Strings(record.Seeded)
	return record
}

func writeStarterRecord(path string, record starterRecord, log *zap.Logger) {
	sort.Strings(record.Seeded)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return
	}
	// On a genuinely fresh install nothing has created the agent directory
	// yet, and a starter that failed to install leaves it that way.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		if log != nil {
			log.Warn("could not create the agent directory", zap.String("path", path), zap.Error(err))
		}
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil && log != nil {
		log.Warn("could not record starter agents", zap.String("path", path), zap.Error(err))
	}
}
