package pluginmigrate

import (
	"strings"
	"testing"
)

func TestTablePrefix(t *testing.T) {
	if got := TablePrefix("weather-bot"); got != "plugin_weather_bot_" {
		t.Fatalf("prefix = %q", got)
	}
	if got := TablePrefix("A.B"); got != "plugin_a_b_" {
		t.Fatalf("prefix = %q", got)
	}
}

func TestValidateAllowsOwnNamespace(t *testing.T) {
	cases := []string{
		"CREATE TABLE plugin_demo_items (id TEXT PRIMARY KEY, body TEXT)",
		"CREATE TABLE IF NOT EXISTS plugin_demo_items (id TEXT)",
		"CREATE INDEX idx_plugin_demo_items_body ON plugin_demo_items(body)",
		"CREATE UNIQUE INDEX IF NOT EXISTS plugin_demo_uq ON plugin_demo_items(id)",
		"ALTER TABLE plugin_demo_items ADD COLUMN note TEXT",
		"INSERT INTO plugin_demo_items (id) VALUES ('seed')",
		"UPDATE plugin_demo_items SET body = '' WHERE id = 'seed'",
		"DELETE FROM plugin_demo_items WHERE id = 'seed'",
		"DROP TABLE IF EXISTS plugin_demo_items",
		"CREATE TABLE plugin_demo_a (id TEXT);\nCREATE TABLE plugin_demo_b (id TEXT);",
	}
	for _, sql := range cases {
		if err := Validate("demo", sql); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", sql, err)
		}
	}
}

func TestValidateRefusesForeignAndCoreTables(t *testing.T) {
	cases := []struct {
		sql  string
		frag string // expected error fragment
	}{
		{"CREATE TABLE plugin_other_items (id TEXT)", "namespace"},
		{"CREATE TABLE items (id TEXT)", "namespace"},
		{"DROP TABLE workboard_tasks", "namespace"},
		{"ALTER TABLE token_usage ADD COLUMN x TEXT", "namespace"},
		{"DELETE FROM agent_events", "namespace"},
		{"INSERT INTO conversation_history (id) VALUES ('x')", "namespace"},
		{"CREATE INDEX plugin_demo_idx ON credentials(name)", "namespace"},
		// index name in own namespace but target table foreign
		{"CREATE TRIGGER plugin_demo_trg AFTER INSERT ON workboard_runs BEGIN SELECT 1; END", "namespace"},
	}
	for _, c := range cases {
		err := Validate("demo", c.sql)
		if err == nil || !strings.Contains(err.Error(), c.frag) {
			t.Errorf("Validate(%q) = %v, want %q error", c.sql, err, c.frag)
		}
	}
}

func TestValidateRefusesDangerousStatements(t *testing.T) {
	cases := []string{
		"ATTACH DATABASE '/tmp/x.db' AS evil",
		"DETACH DATABASE evil",
		"PRAGMA writable_schema = ON",
		"VACUUM",
		"SELECT load_extension('evil')",
		"CREATE TABLE plugin_demo_x (id TEXT); ATTACH DATABASE '/x' AS y",
	}
	for _, sql := range cases {
		if err := Validate("demo", sql); err == nil {
			t.Errorf("Validate(%q) = nil, want error", sql)
		}
	}
}

func TestValidateRefusesEmptyAndGarbage(t *testing.T) {
	if err := Validate("demo", "   ;  ; "); err == nil {
		t.Fatal("empty statements must error")
	}
	if err := Validate("demo", "FROBNICATE THE DATABASE"); err == nil {
		t.Fatal("unknown statement kind must error")
	}
}
