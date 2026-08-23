package main

import (
	"os"
	"testing"
)

func TestWorkerConfigIsNarrowAndFailClosed(t *testing.T) {
	for _, name := range []string{"SOULACY_WORKER_NATS_URL", "SOULACY_WORKER_NATS_CREDENTIALS", "SOULACY_WORKER_IMAGE", "SOULACY_WORKER_RUNTIME", "SOULACY_WORKER_COSIGN_KEY", "SOULACY_EXECUTION_ROOT"} {
		t.Setenv(name, "")
	}
	// A gateway config path must not satisfy worker configuration. This is the
	// boundary that keeps OIDC/database/billing secrets out of worker memory.
	t.Setenv("SOULACY_CONFIG_PATH", "/etc/soulacy/config.yaml")
	if _, err := loadWorkerConfig(); err == nil {
		t.Fatal("worker accepted gateway configuration")
	}
	t.Setenv("SOULACY_WORKER_NATS_URL", "tls://nats.internal:4222")
	t.Setenv("SOULACY_WORKER_NATS_CREDENTIALS", "/run/secrets/nats.creds")
	t.Setenv("SOULACY_WORKER_IMAGE", "registry/execution@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("SOULACY_WORKER_RUNTIME", "runsc")
	t.Setenv("SOULACY_WORKER_COSIGN_KEY", "/run/keys/execution.pub")
	t.Setenv("SOULACY_EXECUTION_ROOT", t.TempDir())
	cfg, err := loadWorkerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.executor.Network != "none" || !cfg.executor.RequireSignedImage || cfg.runtime != "runsc" {
		t.Fatalf("unsafe worker config: %#v", cfg)
	}
	if os.Getenv("SOULACY_CONFIG_PATH") == "" {
		t.Fatal("fixture error")
	}
}
