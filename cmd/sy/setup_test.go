package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestWriteConfigDeploymentModesLoadSuccessfully(t *testing.T) {
	for _, mode := range []string{config.DeploymentModePersonal, config.DeploymentModeTeam, config.DeploymentModeScale} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			cfg := &setupConfig{
				DeploymentMode:      mode,
				QueueBackend:        "memory",
				ExecutorBackend:     "process",
				Host:                "127.0.0.1",
				Port:                18789,
				APIKey:              "sy_bootstrap",
				LLMProvider:         "ollama",
				LLMModel:            "llama3",
				OllamaURL:           "http://localhost:11434",
				PythonBin:           "python3",
				LogLevel:            "info",
				DataDir:             home,
				ConfigPath:          filepath.Join(home, "config.yaml"),
				JWTSecret:           strings.Repeat("j", 32),
				PostgresDSN:         "postgres://db/soulacy",
				NATSURL:             "nats://queue:4222",
				SharedArtifactStore: "s3://soulacy-artifacts/prod",
			}
			if mode != config.DeploymentModePersonal {
				cfg.ExecutorBackend = "docker"
			}
			if mode == config.DeploymentModeScale {
				cfg.QueueBackend = "nats"
			}
			if err := writeConfig(cfg); err != nil {
				t.Fatalf("writeConfig: %v", err)
			}
			loaded, _, err := config.Load(cfg.ConfigPath)
			if err != nil {
				t.Fatalf("generated config does not load: %v", err)
			}
			if got := loaded.DeploymentMode(); got != mode {
				t.Fatalf("deployment mode = %q, want %q", got, mode)
			}
		})
	}
}
