package config

import (
	"fmt"
	"strings"
)

const (
	DeploymentModePersonal = "personal"
	DeploymentModeTeam     = "team"
	DeploymentModeScale    = "scale"

	// UnsafeDeploymentPrerequisitesAcknowledgement is intentionally verbose:
	// operators must opt in explicitly and `sy doctor` keeps the risk visible.
	UnsafeDeploymentPrerequisitesAcknowledgement = "unsafe_multi_user_prerequisites"
)

// DeploymentMode returns the normalized configured operating mode. Empty is
// intentionally Personal so old installations retain their zero-dependency
// behavior after upgrading.
func (c *Config) DeploymentMode() string {
	if c == nil {
		return DeploymentModePersonal
	}
	mode := strings.ToLower(strings.TrimSpace(c.Deployment.Mode))
	if mode == "" {
		return DeploymentModePersonal
	}
	return mode
}

func IsMultiUserMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case DeploymentModeTeam, DeploymentModeScale:
		return true
	default:
		return false
	}
}

func (c *Config) HasUnsafeDeploymentAcknowledgement() bool {
	if c == nil {
		return false
	}
	for _, acknowledgement := range c.Deployment.Acknowledgements {
		if strings.EqualFold(strings.TrimSpace(acknowledgement), UnsafeDeploymentPrerequisitesAcknowledgement) {
			return true
		}
	}
	return false
}

// DeploymentReadinessIssues returns only operating-mode safety failures. It is
// shared by startup validation and `sy doctor`, keeping diagnostics identical
// to the actual boot gate.
func (c *Config) DeploymentReadinessIssues() []string {
	hardIssues, infrastructureIssues := c.deploymentIssueGroups()
	if c != nil && c.HasUnsafeDeploymentAcknowledgement() {
		return hardIssues
	}
	return append(hardIssues, infrastructureIssues...)
}

// AcknowledgedDeploymentIssues returns infrastructure failures currently
// waived by the named unsafe acknowledgement. Authentication failures are
// never included because they cannot be waived.
func (c *Config) AcknowledgedDeploymentIssues() []string {
	if c == nil || !c.HasUnsafeDeploymentAcknowledgement() {
		return nil
	}
	_, infrastructureIssues := c.deploymentIssueGroups()
	return infrastructureIssues
}

func (c *Config) deploymentIssueGroups() ([]string, []string) {
	if c == nil {
		return []string{"configuration is unavailable"}, nil
	}
	mode := c.DeploymentMode()
	var hardIssues []string
	var infrastructureIssues []string
	switch mode {
	case DeploymentModePersonal, DeploymentModeTeam, DeploymentModeScale:
	default:
		return []string{fmt.Sprintf("deployment.mode: unsupported value %q (must be personal, team, or scale)", c.Deployment.Mode)}, nil
	}
	if IsMultiUserMode(mode) {
		if strings.ToLower(strings.TrimSpace(c.Auth.Mode)) != "jwt" {
			hardIssues = append(hardIssues, fmt.Sprintf("auth.mode: %s deployments require \"jwt\"", mode))
		}
		if len(strings.TrimSpace(c.Auth.JWTSecret)) < 32 {
			hardIssues = append(hardIssues, fmt.Sprintf("auth.jwt_secret: %s deployments require a stable secret of at least 32 characters", mode))
		}
		if strings.TrimSpace(c.Server.APIKey) == "" {
			hardIssues = append(hardIssues, fmt.Sprintf("server.api_key: %s deployments require a bootstrap administration key", mode))
		}
		if c.Server.AllowUnauthenticated {
			hardIssues = append(hardIssues, fmt.Sprintf("server.allow_unauthenticated: cannot be enabled for %s deployments", mode))
		}
		if strings.ToLower(strings.TrimSpace(c.Storage.Backend)) != "postgres" || strings.TrimSpace(c.Storage.PostgresDSN) == "" {
			infrastructureIssues = append(infrastructureIssues, fmt.Sprintf("storage: %s deployments require backend \"postgres\" and a non-empty postgres_dsn", mode))
		}
		if strings.ToLower(strings.TrimSpace(c.Executor.Backend)) != "docker" {
			infrastructureIssues = append(infrastructureIssues, fmt.Sprintf("executor.backend: %s deployments require the isolated \"docker\" backend", mode))
		}
		if !c.Runtime.Sandbox.Enabled || strings.ToLower(strings.TrimSpace(c.Runtime.Sandbox.Mode)) != "docker" {
			infrastructureIssues = append(infrastructureIssues, fmt.Sprintf("runtime.sandbox: %s deployments require enabled=true and mode \"docker\"", mode))
		}
	}
	if mode == DeploymentModeScale {
		queueBackend := strings.ToLower(strings.TrimSpace(c.Queue.Backend))
		if queueBackend != "nats" && queueBackend != "external" {
			infrastructureIssues = append(infrastructureIssues, "queue.backend: scale deployments require \"nats\" or \"external\" durable distributed jobs")
		}
		if strings.TrimSpace(c.Deployment.SharedArtifactStore) == "" {
			infrastructureIssues = append(infrastructureIssues, "deployment.shared_artifact_store: required for scale deployments")
		}
	}
	return hardIssues, infrastructureIssues
}
