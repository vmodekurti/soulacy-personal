package config

import (
	"strings"
	"testing"
)

// validConfig returns a Config that should pass Validate(), mirroring the
// defaults set in Load().
func validConfig() *Config {
	c := &Config{}
	c.Deployment.Mode = DeploymentModePersonal
	c.Server.Port = 1947
	c.Runtime.MaxConcurrentSessions = 100
	c.Runtime.DefaultMaxTurns = 20
	c.Runtime.MaxTurnsCeiling = 50
	c.Runtime.MaxAgentCallDepth = 5
	c.Runtime.MaxSessions = 10000
	c.Runtime.MaxHistoryTurns = 100
	c.Runtime.ToolTimeout = "120s"
	c.Runtime.SessionTTL = "24h"
	c.Auth.JWTAccessTTL = "15m"
	c.Auth.JWTRefreshTTL = "168h"
	c.Queue.NATSAckWait = "30s"
	c.Executor.Backend = "process"
	c.Knowledge.ChunkSize = 1000
	c.Knowledge.ChunkOverlap = 200
	c.Knowledge.MaxDocumentBytes = 50 << 20
	return c
}

func validTeamConfig() *Config {
	c := validConfig()
	c.Deployment.Mode = DeploymentModeTeam
	c.Auth.Mode = "jwt"
	c.Auth.JWTSecret = strings.Repeat("a", 32)
	c.Server.APIKey = "sy_bootstrap"
	c.Storage.Backend = "postgres"
	c.Storage.PostgresDSN = "postgres://localhost/soulacy"
	c.Queue.Backend = "nats"
	c.Queue.NATSUrl = "tls://nats.internal:4222"
	c.Queue.NATSCredentials = "/var/run/secrets/nats/soulacy.creds"
	c.Credentials.KMSProvider = "awskms"
	c.Credentials.AWSKMSKeyID = "alias/soulacy-test"
	c.Executor.Backend = "worker"
	c.Executor.DockerImage = "registry.example/soulacy-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	c.Executor.DockerRuntime = "runsc"
	c.Executor.RequireSignedImage = true
	c.Runtime.Sandbox.Enabled = true
	c.Runtime.Sandbox.Mode = "docker"
	c.Runtime.Sandbox.Image = c.Executor.DockerImage
	c.Runtime.Sandbox.ContainerRuntime = "runsc"
	c.Runtime.Sandbox.RequireSignedImage = true
	c.RateLimit.Enabled = true
	c.RateLimit.PerUserRPM = 60
	c.RateLimit.Backend = "redis"
	c.RateLimit.RedisURL = "rediss://redis.internal:6379"
	return c
}

func TestValidate_DefaultsPass(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("default-shaped config should validate, got: %v", err)
	}
}

func TestDeploymentMode_DefaultsToPersonal(t *testing.T) {
	c := validConfig()
	c.Deployment.Mode = ""
	if got := c.DeploymentMode(); got != DeploymentModePersonal {
		t.Fatalf("DeploymentMode() = %q, want personal", got)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("legacy empty mode should validate as personal: %v", err)
	}
}

func TestValidate_TeamRequirements(t *testing.T) {
	if err := validTeamConfig().Validate(); err != nil {
		t.Fatalf("valid team config rejected: %v", err)
	}
	c := validConfig()
	c.Deployment.Mode = DeploymentModeTeam
	err := c.Validate()
	if err == nil {
		t.Fatal("unsafe team config unexpectedly passed")
	}
	for _, field := range []string{"auth.mode", "auth.jwt_secret", "server.api_key", "storage", "queue.backend", "credentials.kms_provider", "executor.backend", "runtime.sandbox"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("team validation error missing %q: %v", field, err)
		}
	}
}

func TestValidate_ScaleRequirements(t *testing.T) {
	c := validTeamConfig()
	c.Deployment.Mode = DeploymentModeScale
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "shared_artifact_store") {
		t.Fatalf("scale requirements not enforced: %v", err)
	}
	c.Queue.Backend = "nats"
	c.Deployment.SharedArtifactStore = "s3://soulacy-artifacts/prod"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid scale config rejected: %v", err)
	}
}

func TestValidate_PublicDemoRequiresBoundedOIDCWorkspace(t *testing.T) {
	c := validTeamConfig()
	c.Auth.OIDCIssuer = "https://accounts.google.com"
	c.Auth.OIDCClientID = "demo-client"
	c.PublicDemo = PublicDemoConfig{
		Enabled:          true,
		WorkspaceID:      "wrk_demo",
		MembershipTTL:    "24h",
		DraftTTL:         "12h",
		MaxActiveMembers: 25,
		AllowedProviders: []string{"nvidia"},
		AllowedModels:    []string{"meta/llama-3.3-70b-instruct"},
		AllowedTools:     []string{"web_search"},
	}
	c.RateLimit.PerUserTokensDay = 100000
	c.Costs.EnforcementMode = "hard"
	if err := c.Validate(); err != nil {
		t.Fatalf("bounded public demo rejected: %v", err)
	}

	c.RateLimit.Backend = "memory"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "Redis-backed") {
		t.Fatalf("unsafe public demo should require distributed quotas: %v", err)
	}
}

func TestValidate_UnsafeDeploymentAcknowledgementAllowsStartup(t *testing.T) {
	c := validTeamConfig()
	c.Storage.Backend = "sqlite"
	c.Storage.PostgresDSN = ""
	c.Deployment.Acknowledgements = []string{UnsafeDeploymentPrerequisitesAcknowledgement}
	if err := c.Validate(); err != nil {
		t.Fatalf("explicit unsafe acknowledgement should permit startup: %v", err)
	}
}

func TestValidate_UnsafeAcknowledgementCannotBypassExecutionIsolation(t *testing.T) {
	c := validTeamConfig()
	c.Deployment.Profile = "production"
	c.Executor.Backend = "process"
	c.Runtime.Sandbox.Enabled = false
	c.Deployment.Acknowledgements = []string{UnsafeDeploymentPrerequisitesAcknowledgement}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "executor.backend") || !strings.Contains(err.Error(), "runtime.sandbox") {
		t.Fatalf("unsafe acknowledgement bypassed the tenant execution boundary: %v", err)
	}
}

func TestValidate_LocalTeamAcknowledgementAllowsLegacyExecution(t *testing.T) {
	c := validTeamConfig()
	c.Deployment.Profile = "local"
	c.Executor.Backend = "process"
	c.Executor.DockerRuntime = ""
	c.Executor.DockerImage = ""
	c.Executor.RequireSignedImage = false
	c.Runtime.Sandbox.Enabled = false
	c.Runtime.Sandbox.ContainerRuntime = ""
	c.Runtime.Sandbox.Image = ""
	c.Runtime.Sandbox.RequireSignedImage = false
	c.Deployment.Acknowledgements = []string{UnsafeDeploymentPrerequisitesAcknowledgement}
	if err := c.Validate(); err != nil {
		t.Fatalf("acknowledged local Team development install should remain bootable: %v", err)
	}
}

func TestValidate_UnsafeAcknowledgementCannotBypassAuthentication(t *testing.T) {
	c := validConfig()
	c.Deployment.Mode = DeploymentModeTeam
	c.Deployment.Acknowledgements = []string{UnsafeDeploymentPrerequisitesAcknowledgement}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "auth.mode") || !strings.Contains(err.Error(), "auth.jwt_secret") {
		t.Fatalf("unsafe acknowledgement bypassed authentication: %v", err)
	}
}

func TestValidate_UnknownDeploymentMode(t *testing.T) {
	c := validConfig()
	c.Deployment.Mode = "communal"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "deployment.mode") {
		t.Fatalf("expected deployment.mode error, got %v", err)
	}
}

func TestValidate_BadDuration(t *testing.T) {
	c := validConfig()
	c.Runtime.ToolTimeout = "120" // the classic missing-unit typo
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for unit-less duration, got nil")
	}
	if !strings.Contains(err.Error(), "runtime.tool_timeout") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestValidate_RangeViolations(t *testing.T) {
	cases := map[string]func(*Config){
		"negative max_turns":     func(c *Config) { c.Runtime.DefaultMaxTurns = -1 },
		"negative agent depth":   func(c *Config) { c.Runtime.MaxAgentCallDepth = -1 },
		"port out of range":      func(c *Config) { c.Server.Port = 70000 },
		"overlap >= chunk size":  func(c *Config) { c.Knowledge.ChunkOverlap = 1000 },
		"negative max doc bytes": func(c *Config) { c.Knowledge.MaxDocumentBytes = -1 },
		"default above ceiling":  func(c *Config) { c.Runtime.DefaultMaxTurns = 200 },
		"negative max_sessions":  func(c *Config) { c.Runtime.MaxSessions = -5 },
		"pool workers below one": func(c *Config) { c.Executor.Backend = "pool"; c.Executor.Workers = 0 },
		"bad executor backend":   func(c *Config) { c.Executor.Backend = "telepathy" },
		"ssh missing host":       func(c *Config) { c.Executor.Backend = "ssh"; c.Executor.SSHHost = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatalf("%s: expected validation error, got nil", name)
			}
		})
	}
}

func TestValidate_AccumulatesAllErrors(t *testing.T) {
	c := validConfig()
	c.Runtime.ToolTimeout = "nope"
	c.Server.Port = -1
	err := c.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	// Both problems should be reported in one pass.
	if !strings.Contains(err.Error(), "tool_timeout") || !strings.Contains(err.Error(), "server.port") {
		t.Fatalf("expected both problems reported, got: %v", err)
	}
}

func TestValidateOIDCInteractiveRequirements(t *testing.T) {
	c := validConfig()
	c.Auth.OIDCIssuer = "https://issuer.example"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "oidc_client_id") || !strings.Contains(err.Error(), "auth.mode=jwt") {
		t.Fatalf("missing OIDC requirements: %v", err)
	}
	c.Auth.Mode = "jwt"
	c.Auth.JWTSecret = strings.Repeat("a", 32)
	c.Auth.OIDCClientID = "soulacy"
	if err := c.Validate(); err != nil {
		t.Fatalf("request-derived callback should be valid without an override: %v", err)
	}
	c.Auth.OIDCRedirectURL = "http://127.0.0.1:1947/api/v1/auth/oidc/callback"
	if err := c.Validate(); err != nil {
		t.Fatalf("loopback development callback should be valid: %v", err)
	}
	c.Auth.OIDCRedirectURL = "http://agents.example/api/v1/auth/oidc/callback"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("public HTTP callback accepted: %v", err)
	}
}

func TestValidateSelfServiceSignupRequiresMultiUserGlobalOIDC(t *testing.T) {
	c := validConfig()
	c.Signup.Enabled = true
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "deployment.mode=team or scale") || !strings.Contains(err.Error(), "global OIDC") {
		t.Fatalf("unsafe self-service signup accepted: %v", err)
	}
	c = validTeamConfig()
	c.Signup.Enabled = true
	c.Auth.OIDCIssuer = "https://issuer.example"
	c.Auth.OIDCClientID = "soulacy"
	c.RateLimit.Enabled = true
	c.RateLimit.PerUserRPM = 60
	c.RateLimit.Backend = "redis"
	c.RateLimit.RedisURL = "rediss://redis.example:6379"
	c.Auth.OIDCScopes = []string{"openid", "profile"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "email scope") {
		t.Fatalf("signup without verified-email scope accepted: %v", err)
	}
	c.Auth.OIDCScopes = []string{"openid", "profile", "email"}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid self-service signup rejected: %v", err)
	}
}

func TestValidateRejectsUnknownRateLimitBackend(t *testing.T) {
	c := validConfig()
	c.RateLimit.Backend = "shared-ish"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "rate_limit.backend") {
		t.Fatalf("unknown rate limit backend error = %v", err)
	}
}

func TestValidateJWTSessionTTLBounds(t *testing.T) {
	c := validConfig()
	c.Auth.JWTAccessTTL = "2h"
	c.Auth.JWTRefreshTTL = "1h"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "1h interactive-session maximum") || !strings.Contains(err.Error(), "must be greater") {
		t.Fatalf("unsafe TTLs accepted: %v", err)
	}
}

func TestValidateStrictStripeBilling(t *testing.T) {
	c := validConfig()
	c.Billing.Provider = "stripe"
	c.Billing.Enforcement = "strict"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "stripe_secret_key") || !strings.Contains(err.Error(), "default_plan") {
		t.Fatalf("incomplete strict billing was accepted: %v", err)
	}
	c.Billing.StripeSecretKey = "sk_test"
	c.Billing.StripeWebhookSecret = "whsec_test"
	c.Billing.DefaultPlan = "team"
	c.Billing.StripePrices = map[string]string{"team": "price_team"}
	c.Billing.CheckoutSuccessURL = "https://app.example/#workspace-admin"
	c.Billing.CheckoutCancelURL = "https://app.example/#workspace-admin"
	c.Billing.PortalReturnURL = "https://app.example/#workspace-admin"
	if err := c.Validate(); err != nil {
		t.Fatalf("complete strict billing rejected: %v", err)
	}
}
