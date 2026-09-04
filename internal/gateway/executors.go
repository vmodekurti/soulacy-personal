package gateway

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/executor/cloud"
)

type executorReadiness struct {
	DefaultBackend string                  `json:"default_backend"`
	ActiveBackend  string                  `json:"active_backend"`
	Ready          int                     `json:"ready"`
	Configured     int                     `json:"configured"`
	Backends       []executorBackendStatus `json:"backends"`
	NextActions    []executorAction        `json:"next_actions,omitempty"`
}

type executorBackendStatus struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Active     bool   `json:"active"`
	Selectable bool   `json:"selectable"`
	Configured bool   `json:"configured"`
	Detail     string `json:"detail"`
	Next       string `json:"next,omitempty"`
	Command    string `json:"command,omitempty"`
}

type executorAction struct {
	Label   string `json:"label"`
	Detail  string `json:"detail"`
	Href    string `json:"href,omitempty"`
	Command string `json:"command,omitempty"`
}

func (s *Server) handleExecutors(c *fiber.Ctx) error {
	return c.JSON(s.executorReadiness())
}

func (s *Server) executorReadiness() executorReadiness {
	cfg := config.ExecutorConfig{Backend: "process", Workers: 1, DockerImage: "python:3.12-slim", DockerNetwork: "none", SSHPythonBin: "python3"}
	pythonBin := "python3"
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Executor
		if strings.TrimSpace(s.cfg.Runtime.PythonBin) != "" {
			pythonBin = strings.TrimSpace(s.cfg.Runtime.PythonBin)
		}
	}
	backend := normalizeExecutorBackend(cfg.Backend)
	pythonOK := commandAvailable(pythonBin)

	backends := []executorBackendStatus{
		{
			Key:        "process",
			Label:      "Local process",
			Kind:       "local",
			Status:     statusIf(pythonOK, "ok", "fail"),
			Active:     backend == "process",
			Selectable: true,
			Configured: true,
			Detail:     fmt.Sprintf("Runs each Python tool in a fresh %s process.", pythonBin),
			Next:       nextIf(pythonOK, "Use this for simple local development.", fmt.Sprintf("Install or configure runtime.python_bin so %q is available.", pythonBin)),
			Command:    fmt.Sprintf("%s --version", pythonBin),
		},
		{
			Key:        "pool",
			Label:      "Local worker pool",
			Kind:       "local",
			Status:     statusIf(pythonOK && cfg.Workers > 0, "ok", "warn"),
			Active:     backend == "pool",
			Selectable: true,
			Configured: cfg.Workers > 0,
			Detail:     fmt.Sprintf("Keeps %d Python worker(s) warm for lower-latency tool calls.", maxInt(cfg.Workers, 0)),
			Next:       nextIf(pythonOK && cfg.Workers > 0, "Set executor.backend to pool for interactive agents with frequent Python calls.", "Set executor.workers >= 1 and confirm the configured Python binary works."),
			Command:    "executor.backend: pool",
		},
		dockerExecutorStatus(cfg, backend, commandAvailable("docker")),
		sshExecutorStatus(cfg, backend, commandAvailable("ssh")),
		cloudExecutorStatus(cfg, backend),
	}

	ready, configured := 0, 0
	actions := make([]executorAction, 0, 4)
	for _, b := range backends {
		if b.Status == "ok" {
			ready++
		}
		if b.Configured {
			configured++
		}
		if b.Status != "ok" && len(actions) < 4 {
			actions = append(actions, executorAction{Label: b.Label, Detail: b.Next, Href: "#config", Command: b.Command})
		}
	}
	return executorReadiness{
		DefaultBackend: backend,
		ActiveBackend:  activeExecutorLabel(backend, cfg.CloudPreset),
		Ready:          ready,
		Configured:     configured,
		Backends:       backends,
		NextActions:    actions,
	}
}

func dockerExecutorStatus(cfg config.ExecutorConfig, backend string, dockerOK bool) executorBackendStatus {
	image := strings.TrimSpace(cfg.DockerImage)
	configured := image != ""
	status := "warn"
	next := "Set executor.docker_image and install Docker to run tools in isolated containers."
	if configured && dockerOK {
		status = "ok"
		next = "Set executor.backend to docker when agents need container isolation."
	}
	if backend == "docker" && (!configured || !dockerOK) {
		status = "fail"
	}
	network := orDefault(cfg.DockerNetwork, "none")
	return executorBackendStatus{
		Key:        "docker",
		Label:      "Docker sandbox",
		Kind:       "container",
		Status:     status,
		Active:     backend == "docker",
		Selectable: configured,
		Configured: configured,
		Detail:     fmt.Sprintf("Runs Python tools in short-lived containers using image %q and network %q.", orDash(image), network),
		Next:       next,
		Command:    fmt.Sprintf("docker run --rm --network %s %s python3 --version", network, orDefault(image, "python:3.12-slim")),
	}
}

func sshExecutorStatus(cfg config.ExecutorConfig, backend string, sshOK bool) executorBackendStatus {
	host := strings.TrimSpace(cfg.SSHHost)
	py := orDefault(cfg.SSHPythonBin, "python3")
	configured := host != ""
	status := "warn"
	next := "Set executor.ssh_host, executor.ssh_user, and executor.ssh_identity_credential for remote Python execution."
	if configured && sshOK {
		status = "ok"
		next = "Set executor.backend to ssh when tools should run on this remote worker."
	}
	if backend == "ssh" && (!configured || !sshOK) {
		status = "fail"
	}
	identity := "config path"
	if strings.TrimSpace(cfg.SSHIdentityCredential) != "" {
		identity = "vault credential"
	}
	return executorBackendStatus{
		Key:        "ssh",
		Label:      "SSH remote worker",
		Kind:       "remote",
		Status:     status,
		Active:     backend == "ssh",
		Selectable: configured,
		Configured: configured,
		Detail:     fmt.Sprintf("Runs Python on %s using %s and %s.", orDash(host), py, identity),
		Next:       next,
		Command:    fmt.Sprintf("ssh %s %s --version", orDefault(host, "user@host"), py),
	}
}

func cloudExecutorStatus(cfg config.ExecutorConfig, backend string) executorBackendStatus {
	preset := strings.ToLower(strings.TrimSpace(cfg.CloudPreset))
	target := strings.TrimSpace(cfg.CloudTarget)
	runner, known := cloud.Preset(preset, target, cfg.CloudCLI)
	configured := preset != ""
	status := "warn"
	next := "Set executor.cloud_preset to modal, runpod, or daytona, then add executor.cloud_target."
	command := "executor.cloud_preset: modal|runpod|daytona"
	if configured && !known {
		status = "fail"
		next = "Choose one of the supported cloud presets: " + strings.Join(cloud.Names(), ", ") + "."
	} else if configured {
		cliOK := len(runner) > 0 && commandAvailable(runner[0])
		command = strings.Join(runner, " ") + " python3 --version"
		if cliOK && target != "" {
			status = "ok"
			next = "Cloud preset is configured; agents can select it through execution.backend."
		} else if backend == "cloud" || backend == preset {
			status = "fail"
			next = "Install/authenticate the provider CLI and set executor.cloud_target."
		} else {
			next = "Install/authenticate the provider CLI and set executor.cloud_target before using this backend."
		}
	}
	return executorBackendStatus{
		Key:        "cloud",
		Label:      "Cloud sandbox preset",
		Kind:       "cloud",
		Status:     status,
		Active:     backend == "cloud" || (preset != "" && backend == preset),
		Selectable: configured && known,
		Configured: configured && known && target != "",
		Detail:     fmt.Sprintf("Cloud execution via %s targeting %s.", orDash(preset), orDash(target)),
		Next:       next,
		Command:    command,
	}
}

func parityRemoteExecution(exec executorReadiness) parityArea {
	localReady, remoteReady, configuredRemote := false, false, false
	for _, b := range exec.Backends {
		if b.Kind == "local" && b.Status == "ok" {
			localReady = true
		}
		if b.Kind != "local" && b.Configured {
			configuredRemote = true
		}
		if b.Kind != "local" && b.Status == "ok" {
			remoteReady = true
		}
	}
	switch {
	case localReady && remoteReady:
		return parityArea{Key: "remote_execution", Label: "Remote Execution", Status: "ok", Score: 84, Detail: "Local execution plus at least one container, SSH, or cloud backend is configured and visible in readiness.", Next: "Add remote worker enrollment and artifact return to make this feel fully managed.", Benchmark: "Hermes", Href: "#config"}
	case localReady && configuredRemote:
		return parityArea{Key: "remote_execution", Label: "Remote Execution", Status: "warn", Score: 72, Detail: "Remote execution is configured but still needs one readiness issue resolved before production use.", Next: "Open Config > Executors and complete the highlighted backend action.", Benchmark: "Hermes", Href: "#config"}
	case localReady:
		return parityArea{Key: "remote_execution", Label: "Remote Execution", Status: "warn", Score: 62, Detail: "Local process and worker-pool execution are ready; Docker, SSH, or cloud execution still need setup.", Next: "Configure Docker, SSH, or a cloud preset in Config > Executors.", Benchmark: "Hermes", Href: "#config"}
	default:
		return parityArea{Key: "remote_execution", Label: "Remote Execution", Status: "fail", Score: 35, Detail: "Python execution is not ready, so tool-heavy agents cannot run dependably.", Next: "Install Python or set runtime.python_bin to a working interpreter.", Benchmark: "Hermes", Href: "#config"}
	}
}

func normalizeExecutorBackend(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || v == "local" {
		return "process"
	}
	return v
}

func activeExecutorLabel(backend, preset string) string {
	if normalizeExecutorBackend(backend) == "cloud" && strings.TrimSpace(preset) != "" {
		return strings.ToLower(strings.TrimSpace(preset))
	}
	return normalizeExecutorBackend(backend)
}

func commandAvailable(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func statusIf(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func nextIf(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func orDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "not set"
	}
	return strings.TrimSpace(v)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
