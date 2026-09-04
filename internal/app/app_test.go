package app

import (
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
)

func TestNewRequiresConfig(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil config must error")
	}
}

func TestNewBuildsLoggerFromConfig(t *testing.T) {
	a, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Logger() == nil {
		t.Fatal("logger not built")
	}
}

func TestNewOptionsApply(t *testing.T) {
	log := zap.NewNop()
	a, err := New(&config.Config{}, WithConfigPath("/tmp/x.yaml"), WithLogger(log))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.cfgPath != "/tmp/x.yaml" {
		t.Fatalf("cfgPath = %q", a.cfgPath)
	}
	if a.Logger() != log {
		t.Fatal("WithLogger not applied")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"", "localhost", "127.0.0.1", "127.9.9.9", "::1"} {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "192.168.1.10", "example.com"} {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true", h)
		}
	}
}
