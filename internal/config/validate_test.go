package config

import (
	"strings"
	"testing"
)

// validConfig returns a Config that should pass Validate(), mirroring the
// defaults set in Load().
func validConfig() *Config {
	c := &Config{}
	c.Server.Port = 18789
	c.Runtime.MaxConcurrentSessions = 100
	c.Runtime.DefaultMaxTurns = 20
	c.Runtime.MaxTurnsCeiling = 50
	c.Runtime.MaxAgentCallDepth = 5
	c.Runtime.MaxSessions = 10000
	c.Runtime.MaxHistoryTurns = 100
	c.Runtime.ToolTimeout = "120s"
	c.Runtime.SessionTTL = "24h"
	c.Auth.JWTAccessTTL = "15m"
	c.Auth.JWTRefreshTTL = "168h"
	c.Queue.NATSAckWait = "30s"
	c.Executor.Backend = "process"
	c.Knowledge.ChunkSize = 1000
	c.Knowledge.ChunkOverlap = 200
	c.Knowledge.MaxDocumentBytes = 50 << 20
	return c
}

func TestValidate_DefaultsPass(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("default-shaped config should validate, got: %v", err)
	}
}

func TestValidate_BadDuration(t *testing.T) {
	c := validConfig()
	c.Runtime.ToolTimeout = "120" // the classic missing-unit typo
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for unit-less duration, got nil")
	}
	if !strings.Contains(err.Error(), "runtime.tool_timeout") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestValidate_RangeViolations(t *testing.T) {
	cases := map[string]func(*Config){
		"negative max_turns":     func(c *Config) { c.Runtime.DefaultMaxTurns = -1 },
		"negative agent depth":   func(c *Config) { c.Runtime.MaxAgentCallDepth = -1 },
		"port out of range":      func(c *Config) { c.Server.Port = 70000 },
		"overlap >= chunk size":  func(c *Config) { c.Knowledge.ChunkOverlap = 1000 },
		"negative max doc bytes": func(c *Config) { c.Knowledge.MaxDocumentBytes = -1 },
		"default above ceiling":  func(c *Config) { c.Runtime.DefaultMaxTurns = 200 },
		"negative max_sessions":  func(c *Config) { c.Runtime.MaxSessions = -5 },
		"pool workers below one": func(c *Config) { c.Executor.Backend = "pool"; c.Executor.Workers = 0 },
		"bad executor backend":   func(c *Config) { c.Executor.Backend = "telepathy" },
		"ssh missing host":       func(c *Config) { c.Executor.Backend = "ssh"; c.Executor.SSHHost = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatalf("%s: expected validation error, got nil", name)
			}
		})
	}
}

func TestValidate_AccumulatesAllErrors(t *testing.T) {
	c := validConfig()
	c.Runtime.ToolTimeout = "nope"
	c.Server.Port = -1
	err := c.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	// Both problems should be reported in one pass.
	if !strings.Contains(err.Error(), "tool_timeout") || !strings.Contains(err.Error(), "server.port") {
		t.Fatalf("expected both problems reported, got: %v", err)
	}
}
