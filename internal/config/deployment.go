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

	// UnsafeTenantSystemAgentAcknowledgement restores the built-in System
	// agent in a multi-user deployment.
	//
	// It is off by default there because the System agent's tools have no
	// tenant: shell, file writes, package installation and edits to the
	// deployment's own config file cannot be scoped to a workspace, so a
	// member of any workspace who could reach it held the host. Meanwhile the
	// operator could NOT reach it — the static server key is refused by the
	// workspace APIs in multi-user mode — so it was available to exactly the
	// wrong people.
	//
	// A named waiver rather than a silent refusal, because a single-tenant
	// Team install run by the same person who owns the machine is a real
	// configuration, and an operator with no path forward edits the binary.
	// Writing this down is the whole point: `sy doctor` reports it, and
	// somebody had to decide.
	UnsafeTenantSystemAgentAcknowledgement = "unsafe_tenant_system_agent"
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
	return c.hasAcknowledgement(UnsafeDeploymentPrerequisitesAcknowledgement)
}

// HasTenantSystemAgentAcknowledgement reports whether the operator has
// explicitly restored the built-in System agent in a multi-user deployment.
func (c *Config) HasTenantSystemAgentAcknowledgement() bool {
	return c.hasAcknowledgement(UnsafeTenantSystemAgentAcknowledgement)
}

func (c *Config) hasAcknowledgement(name string) bool {
	if c == nil {
		return false
	}
	for _, acknowledgement := range c.Deployment.Acknowledgements {
		if strings.EqualFold(strings.TrimSpace(acknowledgement), name) {
			return true
		}
	}
	return false
}

// PlatformAgentsEnabled reports whether the built-in System agent exists in
// this deployment.
//
// Personal is unconditionally yes: there is one tenant and they own the
// machine, so "the agent can reach the host" and "the user can reach the host"
// are the same sentence. Team and Scale are no unless explicitly waived — see
// UnsafeTenantSystemAgentAcknowledgement.
func (c *Config) PlatformAgentsEnabled() bool {
	if c == nil {
		return true
	}
	if !IsMultiUserMode(c.DeploymentMode()) {
		return true
	}
	return c.HasTenantSystemAgentAcknowledgement()
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
			// Still required, and the message now says what setting it does
			// and does not buy. No runtime code reads this value yet — it is
			// recorded intent, not a wired dependency — and an operator who
			// configured it had every reason to believe artifacts had become
			// shared. See ScaleReplicationBlockers.
			infrastructureIssues = append(infrastructureIssues,
				"deployment.shared_artifact_store: required for scale deployments. Note that it is "+
					"currently RECORDED and not yet used: artifacts still live on each replica's own "+
					"disk. See the startup report of scale replication blockers")
		}
	}
	return hardIssues, infrastructureIssues
}
