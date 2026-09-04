package storage

import "testing"

func TestRegisterMigration(t *testing.T) {
	if err := RegisterMigration("demo-plug", "001_create", "CREATE TABLE plugin_demo_plug_items (id TEXT)"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := RegisterMigration("demo-plug", "001_create", "CREATE TABLE x (id TEXT)"); err == nil {
		t.Fatal("duplicate (plugin, name) must error")
	}
	if err := RegisterMigration("", "001", "CREATE ..."); err == nil {
		t.Fatal("empty plugin id must error")
	}
	if err := RegisterMigration("p", "", "CREATE ..."); err == nil {
		t.Fatal("empty name must error")
	}
	if err := RegisterMigration("p", "001", ""); err == nil {
		t.Fatal("empty SQL must error")
	}
}

func TestRegisteredMigrationsOrderAndFilter(t *testing.T) {
	MustRegisterMigration("order-plug", "001_first", "CREATE TABLE plugin_order_plug_a (id TEXT)")
	MustRegisterMigration("order-plug", "002_second", "CREATE TABLE plugin_order_plug_b (id TEXT)")
	MustRegisterMigration("other-plug", "001_first", "CREATE TABLE plugin_other_plug_a (id TEXT)")

	got := PluginMigrations("order-plug")
	if len(got) != 2 || got[0].Name != "001_first" || got[1].Name != "002_second" {
		t.Fatalf("PluginMigrations order = %+v", got)
	}

	all := RegisteredMigrations()
	count := 0
	for _, m := range all {
		if m.PluginID == "order-plug" || m.PluginID == "other-plug" {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("RegisteredMigrations missing entries, count = %d", count)
	}
	// returned slices are copies — mutating them must not corrupt the registry
	all[0].Name = "tampered"
	if RegisteredMigrations()[0].Name == "tampered" {
		t.Fatal("RegisteredMigrations must return a copy")
	}
}
