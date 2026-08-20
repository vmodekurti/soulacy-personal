package gateway

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":   true,
		"::1":         true,
		"[::1]":       true,
		"localhost":   true,
		"LOCALHOST":   true,
		"0.0.0.0":     false,
		"":            false, // empty == all interfaces == unsafe
		"192.168.1.5": false,
		"example.com": false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func cfgWith(host, key string, allow bool) *config.Config {
	c := &config.Config{}
	c.Server.Host = host
	c.Server.APIKey = key
	c.Server.AllowUnauthenticated = allow
	return c
}

func TestCheckAuthBindSafety(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		authCfg auth.Config
		wantErr bool
	}{
		{"loopback no key", cfgWith("127.0.0.1", "", false), auth.Config{}, false},
		{"loopback empty-host treated unsafe", cfgWith("", "", false), auth.Config{}, true},
		{"public no key refuses", cfgWith("0.0.0.0", "", false), auth.Config{}, true},
		{"public with key ok", cfgWith("0.0.0.0", "secret", false), auth.Config{}, false},
		{"public no key but allowed", cfgWith("0.0.0.0", "", true), auth.Config{}, false},
		{"public no static key but jwt configured", cfgWith("0.0.0.0", "", false), auth.Config{Mode: "jwt"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authEngine, err := auth.New(tt.authCfg, tt.cfg.Server.APIKey, zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(authEngine.Close)
			s := withCfg(&Server{authEngine: authEngine, log: zap.NewNop()}, tt.cfg)
			err = s.checkAuthBindSafety()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Server.checkAuthBindSafety() err = %v, effective=%v, wantErr=%v", err, authEngine.Effective(), tt.wantErr)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "refusing to start") {
				t.Errorf("expected remediation message, got: %v", err)
			}
		})
	}
}

func TestAllowUnauthenticatedLogsErrorAtStartup(t *testing.T) {
	cfg := cfgWith("127.0.0.1", "secret", true)
	engine, err := auth.New(auth.Config{}, cfg.Server.APIKey, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zap.ErrorLevel)
	s := withCfg(&Server{authEngine: engine, log: zap.New(core)}, cfg)
	if err := s.checkAuthBindSafety(); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 1 || !strings.Contains(logs.All()[0].Message, "unauthenticated startup override") {
		t.Fatalf("expected Error-level startup warning, got %#v", logs.All())
	}
}
