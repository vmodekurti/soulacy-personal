package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoveryConfigurationIsOptInAndEnvironmentBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 18789\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Discovery.Enabled {
		t.Fatal("discovery enabled by default")
	}
	t.Setenv("SOULACY_SERVER_DISCOVERY_ENABLED", "true")
	t.Setenv("SOULACY_SERVER_DISCOVERY_INTERFACE", "lan-test")
	t.Setenv("SOULACY_SERVER_DISCOVERY_NAME", "Office")
	t.Setenv("SOULACY_SERVER_DISCOVERY_HOSTNAME", "office.local")
	cfg, _, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Discovery != (DiscoveryConfig{Enabled: true, Interface: "lan-test", Name: "Office", Hostname: "office.local"}) {
		t.Fatalf("discovery environment not bound: %+v", cfg.Server.Discovery)
	}
}

func TestDiscoverySchemaAndMissingInterface(t *testing.T) {
	if err := ValidateSchema([]byte("server:\n  discovery:\n    enabled: true\n    interface: en0\n    name: Office\n    hostname: office.local\n")); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Server: ServerConfig{Port: 18789, Discovery: DiscoveryConfig{Enabled: true}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "server.discovery.interface") {
		t.Fatalf("missing interface accepted: %v", err)
	}
	cfg.Server.Discovery.Interface = "en0"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "server.discovery.hostname") {
		t.Fatalf("unstable hostname accepted: %v", err)
	}
}
