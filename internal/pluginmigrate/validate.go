// Package pluginmigrate validates and applies plugin database migrations
// (Story E16). Plugins register schema steps via the SDK
// (storage.RegisterMigration); the host applies pending steps
// transactionally at boot into the dedicated plugin database
// (~/.soulacy/plugins.db) — never the core system stores.
//
// Namespace model (ties into the E5 plugin-principal model): a plugin may
// only create and touch tables named `plugin_<id>_*` (id sanitised:
// non-alphanumerics → '_'). Everything else — core tables, other plugins'
// tables, ATTACH/PRAGMA/VACUUM escape hatches — is refused before any SQL
// reaches the database.
package pluginmigrate

import (
	"fmt"
	"regexp"
	"strings"
)

// coreTables are the system schemas plugins must never touch, even though
// they live in other database files — defence in depth against a future
// shared-file deployment and against ATTACH tricks that slip past parsing.
var coreTables = []string{
	"agents", "runs",
	"token_usage", "agent_events", "events",
	"conversation_history", "history",
	"workboard_tasks", "workboard_runs", "workboard_comments", "workboard_artifacts",
	"credentials", "credential_versions",
	"rbac", "rbac_grants", "api_keys", "dead_letters",
	"memory_entries", "knowledge_docs", "knowledge_chunks",
	"plugin_schema_migrations", "sqlite_master", "sqlite_sequence",
}

// TablePrefix returns the table namespace prefix for pluginID:
// "plugin_<sanitised id>_".
func TablePrefix(pluginID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(pluginID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return "plugin_" + b.String() + "_"
}

// statement-kind patterns. Each extracts the target TABLE the statement
// touches (index/trigger/view targets resolve to their ON/backing table
// where applicable).
var (
	reCreateTable   = regexp.MustCompile(`(?is)^CREATE\s+(?:TEMP(?:ORARY)?\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w".]+)`)
	reCreateIndex   = regexp.MustCompile(`(?is)^CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?[\w".]+\s+ON\s+([\w".]+)`)
	reCreateTrigger = regexp.MustCompile(`(?is)^CREATE\s+(?:TEMP(?:ORARY)?\s+)?TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?[\w".]+.*?\bON\s+([\w".]+)`)
	reCreateView    = regexp.MustCompile(`(?is)^CREATE\s+(?:TEMP(?:ORARY)?\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w".]+)`)
	reAlterTable    = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+([\w".]+)`)
	reDropTable     = regexp.MustCompile(`(?is)^DROP\s+(?:TABLE|INDEX|TRIGGER|VIEW)\s+(?:IF\s+EXISTS\s+)?([\w".]+)`)
	reInsert        = regexp.MustCompile(`(?is)^INSERT\s+(?:OR\s+\w+\s+)?INTO\s+([\w".]+)`)
	reUpdate        = regexp.MustCompile(`(?is)^UPDATE\s+(?:OR\s+\w+\s+)?([\w".]+)`)
	reDelete        = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+([\w".]+)`)

	// hard-refused anywhere in a statement
	reForbidden = regexp.MustCompile(`(?is)\b(ATTACH|DETACH|PRAGMA|VACUUM|REINDEX|load_extension)\b`)
)

var targetPatterns = []*regexp.Regexp{
	reCreateTable, reCreateIndex, reCreateTrigger, reCreateView,
	reAlterTable, reDropTable, reInsert, reUpdate, reDelete,
}

// Validate checks every statement in upSQL against pluginID's namespace.
// It returns a descriptive error on the first violation; nil means the
// migration is safe to execute against the plugin database.
func Validate(pluginID, upSQL string) error {
	prefix := TablePrefix(pluginID)
	stmts := splitStatements(upSQL)
	if len(stmts) == 0 {
		return fmt.Errorf("pluginmigrate: %s: migration contains no statements", pluginID)
	}
	for _, stmt := range stmts {
		if reForbidden.MatchString(stmt) {
			return fmt.Errorf("pluginmigrate: %s: statement kind is forbidden in plugin migrations: %.60q", pluginID, stmt)
		}
		target := ""
		matched := false
		for _, re := range targetPatterns {
			if m := re.FindStringSubmatch(stmt); m != nil {
				target = normaliseIdent(m[1])
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("pluginmigrate: %s: unsupported statement in migration (allowed: CREATE TABLE/INDEX/TRIGGER/VIEW, ALTER TABLE, DROP, INSERT, UPDATE, DELETE): %.60q", pluginID, stmt)
		}
		if !strings.HasPrefix(target, prefix) {
			return fmt.Errorf("pluginmigrate: %s: table %q is outside the plugin's namespace (tables must be prefixed %q)", pluginID, target, prefix)
		}
		for _, core := range coreTables {
			if target == core {
				return fmt.Errorf("pluginmigrate: %s: table %q is a core system table — refused (namespace violation)", pluginID, target)
			}
		}
	}
	return nil
}

// splitStatements is a deliberately simple splitter: plugin migrations are
// plain DDL/DML, and anything exotic enough to confuse it (string literals
// containing semicolons feeding trigger bodies, etc.) belongs in multiple
// registered steps instead. Trigger bodies (BEGIN…END) are kept whole.
func splitStatements(sqlText string) []string {
	var out []string
	depth := 0 // BEGIN…END nesting for triggers
	var cur strings.Builder
	tokens := strings.Split(sqlText, ";")
	for _, tok := range tokens {
		if cur.Len() > 0 {
			cur.WriteString(";")
		}
		cur.WriteString(tok)
		up := strings.ToUpper(tok)
		depth += strings.Count(up, "BEGIN")
		depth -= strings.Count(up, " END") + boolToInt(strings.TrimSpace(up) == "END")
		if depth <= 0 {
			s := strings.TrimSpace(cur.String())
			if s != "" {
				out = append(out, s)
			}
			cur.Reset()
			depth = 0
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// normaliseIdent lowercases and strips quoting / schema qualification from
// an extracted identifier ("main"."Plugin_X_T" → plugin_x_t).
func normaliseIdent(ident string) string {
	ident = strings.Trim(ident, `"`)
	if i := strings.LastIndex(ident, "."); i >= 0 {
		ident = ident[i+1:]
	}
	ident = strings.Trim(ident, `"`)
	return strings.ToLower(ident)
}
