package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/netguard"
)

type PackageInstallRequest struct {
	Source          string `json:"source"`
	Kind            string `json:"kind"`
	AllowUnverified bool   `json:"allow_unverified"`
	AllowHostBuild  bool   `json:"allow_host_build"`
}

type packageInstallJob struct {
	ID       string    `json:"job_id"`
	Status   string    `json:"status"`
	Messages []string  `json:"messages"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started_at"`
	Finished time.Time `json:"finished_at,omitempty"`
	mu       sync.RWMutex
}

type packageInstallRunner func(context.Context, PackageInstallRequest, func(string)) error

const remotePackageInstallTimeout = 15 * time.Minute

func (j *packageInstallJob) snapshot() packageInstallJob {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return packageInstallJob{ID: j.ID, Status: j.Status, Messages: append([]string(nil), j.Messages...), Error: j.Error, Started: j.Started, Finished: j.Finished}
}

func (j *packageInstallJob) progress(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	j.mu.Lock()
	j.Messages = append(j.Messages, message)
	j.mu.Unlock()
}

func validatePackageInstallRequest(req PackageInstallRequest) error {
	u, err := url.ParseRequestURI(strings.TrimSpace(req.Source))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("source must be an HTTPS repository URL without embedded credentials")
	}
	if err := netguard.CheckPublic(req.Source); err != nil {
		return fmt.Errorf("source failed the public-network check: %w", err)
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = "auto"
	}
	if kind != "auto" && kind != "skill" && kind != "mcp" {
		return fmt.Errorf("kind must be auto, skill, or mcp")
	}
	if !req.AllowUnverified {
		return fmt.Errorf("raw Git sources require allow_unverified=true after operator review")
	}
	if kind != "skill" && !req.AllowHostBuild {
		return fmt.Errorf("remote auto/MCP provisioning requires allow_host_build=true")
	}
	return nil
}

func (s *Server) handleStartPackageInstall(c *fiber.Ctx) error {
	var req PackageInstallRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid JSON body")
	}
	req.Source = strings.TrimSpace(req.Source)
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if req.Kind == "" {
		req.Kind = "auto"
	}
	if err := validatePackageInstallRequest(req); err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, err.Error())
	}
	if s.packageInstallPolicy != nil {
		if err := s.packageInstallPolicy(req); err != nil {
			return s.errMsg(c, fiber.StatusForbidden, err.Error())
		}
	}

	job := &packageInstallJob{ID: uuid.NewString(), Status: "queued", Started: time.Now().UTC(), Messages: []string{"Queued package installation"}}
	s.packageInstallMu.Lock()
	if len(s.packageInstallJobs) >= 100 {
		for id, old := range s.packageInstallJobs {
			if snap := old.snapshot(); snap.Status == "succeeded" || snap.Status == "failed" {
				delete(s.packageInstallJobs, id)
				break
			}
		}
	}
	s.packageInstallJobs[job.ID] = job
	s.packageInstallMu.Unlock()

	runner := s.packageInstallRunner
	if runner == nil {
		runner = s.runPackageInstallCLI
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), remotePackageInstallTimeout)
		defer cancel()
		job.mu.Lock()
		job.Status = "running"
		job.mu.Unlock()
		job.progress("Validating and scanning package on the gateway")
		err := runner(ctx, req, job.progress)
		job.mu.Lock()
		defer job.mu.Unlock()
		job.Finished = time.Now().UTC()
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
			return
		}
		job.Status = "succeeded"
		job.Messages = append(job.Messages, "Package installed and registered")
	}()

	return c.Status(fiber.StatusAccepted).JSON(job.snapshot())
}

func (s *Server) handlePackageInstallStatus(c *fiber.Ctx) error {
	s.packageInstallMu.RLock()
	job := s.packageInstallJobs[c.Params("job")]
	s.packageInstallMu.RUnlock()
	if job == nil {
		return s.errMsg(c, fiber.StatusNotFound, "package installation job not found")
	}
	return c.JSON(job.snapshot())
}

func (s *Server) runPackageInstallCLI(ctx context.Context, req PackageInstallRequest, progress func(string)) error {
	syPath, err := exec.LookPath("sy")
	if err != nil {
		if current, currentErr := os.Executable(); currentErr == nil {
			candidate := filepath.Join(filepath.Dir(current), "sy")
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				syPath, err = candidate, nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("remote package installer is unavailable: sy executable not found")
	}
	port := 18789
	if s.cfg != nil && s.cfg.Server.Port > 0 {
		port = s.cfg.Server.Port
	}
	args := []string{"--gateway", fmt.Sprintf("http://127.0.0.1:%d", port), "package", "install", req.Source, "--kind", req.Kind, "--yes"}
	if req.AllowUnverified {
		args = append(args, "--allow-unverified")
	}
	cmd := exec.CommandContext(ctx, syPath, args...)
	cmd.Env = append(os.Environ(), "SOULACY_CONFIG_PATH="+s.cfgPath)
	if s.cfg != nil && s.cfg.Server.APIKey != "" {
		cmd.Env = append(cmd.Env, "SOULACY_API_KEY="+s.cfg.Server.APIKey)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, stream := range []io.Reader{stdout, stderr} {
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				progress(scanner.Text())
			}
		}(stream)
	}
	err = cmd.Wait()
	wg.Wait()
	if err != nil {
		return fmt.Errorf("package installer: %w", err)
	}
	return nil
}
