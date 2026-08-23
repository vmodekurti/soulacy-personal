package main

import (
	"os"
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
				Port:                1947,
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
				NATSURL:             "tls://queue:4222",
				NATSCredentials:     "/run/secrets/nats.creds",
				RedisURL:            "rediss://redis.internal:6379",
				SharedArtifactStore: "s3://soulacy-artifacts/prod",
			}
			if mode != config.DeploymentModePersonal {
				cfg.ExecutorBackend = "worker"
				cfg.QueueBackend = "nats"
				cfg.KMSProvider = "awskms"
				cfg.AWSKMSKeyID = "alias/soulacy-test"
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

func TestWriteConfigNVIDIADefaultDoesNotRequireOllama(t *testing.T) {
	home := t.TempDir()
	cfg := &setupConfig{
		DeploymentMode:  config.DeploymentModePersonal,
		QueueBackend:    "memory",
		ExecutorBackend: "process",
		Host:            "127.0.0.1",
		Port:            1947,
		APIKey:          "sy_bootstrap",
		LLMProvider:     "nvidia",
		LLMModel:        "meta/llama-3.3-70b-instruct",
		NVIDIAKey:       "nvapi-test",
		PythonBin:       "python3",
		LogLevel:        "info",
		DataDir:         home,
		ConfigPath:      filepath.Join(home, "config.yaml"),
	}
	if err := writeConfig(cfg); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	loaded, _, err := config.Load(cfg.ConfigPath)
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if loaded.LLM.DefaultProvider != "nvidia" {
		t.Fatalf("default provider = %q, want nvidia", loaded.LLM.DefaultProvider)
	}
	if got := loaded.LLM.Providers["nvidia"]; got.BaseURL != "https://integrate.api.nvidia.com/v1" || got.Model != "meta/llama-3.3-70b-instruct" || got.APIKey != "nvapi-test" {
		t.Fatalf("unexpected NVIDIA provider: %#v", got)
	}
	data, err := os.ReadFile(cfg.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "    ollama:") {
		t.Fatalf("NVIDIA default config unexpectedly includes Ollama:\n%s", data)
	}
}
