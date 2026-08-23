// soulacy-worker is the disposable execution-plane process. It does not open
// HTTP ports, load tenant credentials, or connect to the control-plane
// database; its only inputs are signed execution images and queue jobs.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	executordocker "github.com/soulacy/soulacy/internal/executor/docker"
	"github.com/soulacy/soulacy/internal/executor/remote"
	queuenats "github.com/soulacy/soulacy/internal/queue/nats"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/sandbox"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "soulacy-worker:", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := loadWorkerConfig()
	if err != nil {
		return err
	}
	q, err := queuenats.New(cfg.queue)
	if err != nil {
		return err
	}
	defer q.Close()
	backend := executordocker.NewHardened(cfg.executor)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := backend.Ready(ctx); err != nil {
		return fmt.Errorf("execution boundary readiness: %w", err)
	}
	w := remote.NewWorker(q, backend, "execution-workers", cfg.concurrency)
	privileged := runtime.DockerPrivilegedRunner{Workspace: cfg.executionRoot, Root: cfg.executionRoot, Image: cfg.sandboxImage, ContainerRuntime: cfg.runtime, RequireSignedImage: true, CosignKey: cfg.cosignKey, EgressProxy: cfg.egressProxy, EgressNetwork: cfg.egressNetwork, AllowedEgressHosts: cfg.allowedEgressHosts, PIDs: cfg.pids, Limits: cfg.limits}
	if err := privileged.Ready(ctx); err != nil {
		return fmt.Errorf("privileged execution boundary readiness: %w", err)
	}
	w.SetPrivilegedRunner(privileged)
	if err := w.Start(ctx); err != nil {
		return err
	}
	defer w.Close()
	<-ctx.Done()
	return nil
}

// workerConfig is intentionally not the gateway config.Config. Loading the
// gateway YAML here would place database, OIDC, Stripe, and API-key secrets in
// the execution plane even if the worker never used them.
type workerConfig struct {
	queue              queuenats.Config
	executor           executordocker.Config
	executionRoot      string
	sandboxImage       string
	runtime            string
	cosignKey          string
	egressProxy        string
	egressNetwork      string
	allowedEgressHosts []string
	concurrency        int
	pids               int
	limits             sandbox.Limits
}

func loadWorkerConfig() (workerConfig, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}
	natsURL, err := required("SOULACY_WORKER_NATS_URL")
	if err != nil {
		return workerConfig{}, err
	}
	if !strings.HasPrefix(strings.ToLower(natsURL), "tls://") {
		return workerConfig{}, fmt.Errorf("SOULACY_WORKER_NATS_URL must use tls://")
	}
	creds := strings.TrimSpace(os.Getenv("SOULACY_WORKER_NATS_CREDENTIALS"))
	tlsCert, tlsKey := strings.TrimSpace(os.Getenv("SOULACY_WORKER_NATS_TLS_CERT")), strings.TrimSpace(os.Getenv("SOULACY_WORKER_NATS_TLS_KEY"))
	if creds == "" && (tlsCert == "" || tlsKey == "") {
		return workerConfig{}, fmt.Errorf("worker NATS requires SOULACY_WORKER_NATS_CREDENTIALS or both mTLS certificate and key")
	}
	image, err := required("SOULACY_WORKER_IMAGE")
	if err != nil {
		return workerConfig{}, err
	}
	runtimeName, err := required("SOULACY_WORKER_RUNTIME")
	if err != nil {
		return workerConfig{}, err
	}
	cosignKey, err := required("SOULACY_WORKER_COSIGN_KEY")
	if err != nil {
		return workerConfig{}, err
	}
	root, err := required("SOULACY_EXECUTION_ROOT")
	if err != nil {
		return workerConfig{}, err
	}
	ackWait := envDuration("SOULACY_WORKER_NATS_ACK_WAIT", 30*time.Second)
	return workerConfig{
		queue:         queuenats.Config{URL: natsURL, StreamName: envString("SOULACY_WORKER_NATS_STREAM", "soulacy"), SubjectPrefix: os.Getenv("SOULACY_WORKER_NATS_SUBJECT_PREFIX"), AckWait: ackWait, MaxDeliver: envInt("SOULACY_WORKER_NATS_MAX_DELIVER", 5), Credentials: creds, TLSCA: os.Getenv("SOULACY_WORKER_NATS_TLS_CA"), TLSCert: tlsCert, TLSKey: tlsKey, TLSServerName: os.Getenv("SOULACY_WORKER_NATS_TLS_SERVER_NAME")},
		executor:      executordocker.Config{Image: image, PythonBin: envString("SOULACY_WORKER_PYTHON", "python3"), Network: "none", Runtime: runtimeName, RequireSignedImage: true, CosignKey: cosignKey},
		executionRoot: root, sandboxImage: envString("SOULACY_WORKER_SANDBOX_IMAGE", image), runtime: runtimeName, cosignKey: cosignKey,
		egressProxy: os.Getenv("SOULACY_WORKER_EGRESS_PROXY"), egressNetwork: os.Getenv("SOULACY_WORKER_EGRESS_NETWORK"), allowedEgressHosts: splitCSV(os.Getenv("SOULACY_WORKER_ALLOWED_EGRESS_HOSTS")),
		concurrency: envInt("SOULACY_WORKER_CONCURRENCY", 4), pids: envInt("SOULACY_WORKER_PIDS", 128),
		limits: sandbox.Limits{Enabled: true, CPUSeconds: envInt("SOULACY_WORKER_CPU_SECONDS", 30), MemoryMB: envInt("SOULACY_WORKER_MEMORY_MB", 512), OpenFiles: envInt("SOULACY_WORKER_OPEN_FILES", 256), FileSizeMB: envInt("SOULACY_WORKER_FILE_SIZE_MB", 64)},
	}, nil
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
func splitCSV(raw string) []string {
	var out []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
