package storage

// Plugin database migrations (Story E16). Compiled-in plugins register
// schema migrations from init() — the same self-registration pattern as the
// sdk/registry factories (E10). The host applies pending migrations
// transactionally during the database boot phase, into a dedicated plugin
// database (never the core system stores), after enforcing the namespace
// rules: a plugin may only create and touch its own `plugin_<id>_*` tables.
//
// Validation and execution are host responsibilities (the SDK stays
// stdlib-only and declarative); see docs/PLUGIN_MIGRATIONS.md.

import (
	"fmt"
	"sync"
)

// Migration is one registered schema step. Migrations apply in registration
// order per plugin and exactly once (the host tracks applied names).
type Migration struct {
	// PluginID is the owning plugin. Table names in UpSQL must use the
	// `plugin_<id>_` prefix (id sanitised: non-alphanumerics → '_').
	PluginID string
	// Name identifies the step within the plugin (e.g. "001_create_items").
	// Applied-once bookkeeping keys on (PluginID, Name).
	Name string
	// UpSQL is the forward DDL/DML. No down migrations — plugins must write
	// additive, idempotent-on-retry steps (the host rolls back a failed step).
	UpSQL string
}

var (
	migMu      sync.Mutex
	migrations []Migration
	migSeen    = map[string]bool{}
)

// RegisterMigration registers a schema migration for pluginID. Duplicate
// (pluginID, name) pairs and empty fields error (call from init(); treat a
// non-nil error as a programmer mistake).
func RegisterMigration(pluginID, name, upSQL string) error {
	if pluginID == "" {
		return fmt.Errorf("storage: migration plugin id must not be empty")
	}
	if name == "" {
		return fmt.Errorf("storage: migration name must not be empty (plugin %q)", pluginID)
	}
	if upSQL == "" {
		return fmt.Errorf("storage: migration %s/%s has empty SQL", pluginID, name)
	}
	key := pluginID + "\x00" + name
	migMu.Lock()
	defer migMu.Unlock()
	if migSeen[key] {
		return fmt.Errorf("storage: migration %s/%s already registered", pluginID, name)
	}
	migSeen[key] = true
	migrations = append(migrations, Migration{PluginID: pluginID, Name: name, UpSQL: upSQL})
	return nil
}

// MustRegisterMigration is RegisterMigration that panics on error — the
// idiomatic form inside plugin init() functions.
func MustRegisterMigration(pluginID, name, upSQL string) {
	if err := RegisterMigration(pluginID, name, upSQL); err != nil {
		panic(err)
	}
}

// RegisteredMigrations returns all migrations in registration order (copy).
func RegisteredMigrations() []Migration {
	migMu.Lock()
	defer migMu.Unlock()
	out := make([]Migration, len(migrations))
	copy(out, migrations)
	return out
}

// PluginMigrations returns pluginID's migrations in registration order (copy).
func PluginMigrations(pluginID string) []Migration {
	migMu.Lock()
	defer migMu.Unlock()
	var out []Migration
	for _, m := range migrations {
		if m.PluginID == pluginID {
			out = append(out, m)
		}
	}
	return out
}
