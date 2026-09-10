package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShippedTimeoutHierarchyIsStrictlyOrdered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, _, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	raw := []string{cfg.Runtime.Timeouts.Tool, cfg.Runtime.Timeouts.LLM, cfg.Runtime.Timeouts.Step, cfg.Runtime.Timeouts.Run, cfg.Runtime.Timeouts.HTTP}
	var previous time.Duration
	for i, value := range raw {
		got, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("timeout[%d] %q: %v", i, value, err)
		}
		if i > 0 && got <= previous {
			t.Fatalf("timeout hierarchy is not strictly ordered: %v", raw)
		}
		previous = got
	}
}

func TestLoadRejectsInvertedTimeoutHierarchy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("runtime:\n  timeouts:\n    tool: 4m\n    llm: 3m\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "tool < llm") {
		t.Fatalf("expected named hierarchy error, got %v", err)
	}
}

func TestLegacyToolTimeoutPopulatesHierarchy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("runtime:\n  tool_timeout: 90s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.Timeouts.Tool != "90s" || cfg.Runtime.ToolTimeout != "90s" {
		t.Fatalf("legacy alias was not reconciled: %+v", cfg.Runtime.Timeouts)
	}
}

func TestLoadExplicitConfigKeepsHomeBackedDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
server:
  api_key: test-key
memory:
  max_history: 7
`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, resolved, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if resolved != cfgPath {
		t.Fatalf("resolved path = %q, want %q", resolved, cfgPath)
	}

	wantMemoryDir := filepath.Join(home, ".soulacy", "soulspace", "memory")
	if cfg.Memory.Dir != wantMemoryDir {
		t.Fatalf("memory.dir = %q, want %q", cfg.Memory.Dir, wantMemoryDir)
	}
	wantArchive := filepath.Join(home, ".soulacy", "soulspace", "data", "archive.db")
	if cfg.Memory.SQLitePath != wantArchive {
		t.Fatalf("memory.sqlite_path = %q, want %q", cfg.Memory.SQLitePath, wantArchive)
	}
	wantKB := filepath.Join(home, ".soulacy", "soulspace", "data", "knowledge.db")
	if cfg.Knowledge.DBPath != wantKB {
		t.Fatalf("knowledge.db_path = %q, want %q", cfg.Knowledge.DBPath, wantKB)
	}
	if len(cfg.AgentDirs) != 1 || cfg.AgentDirs[0] != filepath.Join(home, ".soulacy", "soulspace", "agents") {
		t.Fatalf("agent_dirs = %#v", cfg.AgentDirs)
	}
	if cfg.Memory.MaxHistory != 7 {
		t.Fatalf("memory.max_history = %d, want 7", cfg.Memory.MaxHistory)
	}
}

func TestLoadCostsPricingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
costs:
  daily_budget_usd: 12.5
  monthly_budget_usd: 250
  alert_threshold: 0.75
  pricing:
    openai/gpt-test:
      input_per_mtok: 1.5
      output_per_mtok: 6
    omniroute/*:
      input_per_mtok: 0.25
      output_per_mtok: 0.75
ops:
  slo_window: 12h
  max_failure_rate: 0.2
  max_incomplete_rate: 0.03
  max_p95_run_duration: 2m
  min_runs_for_signal: 5
deployment:
  profile: production
  owner: platform
  region: us-central
  notes: customer workspace
`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Costs.Pricing["openai/gpt-test"]; got.InputPerMTok != 1.5 || got.OutputPerMTok != 6 {
		t.Fatalf("openai pricing = %+v", got)
	}
	if got := cfg.Costs.Pricing["omniroute/*"]; got.InputPerMTok != 0.25 || got.OutputPerMTok != 0.75 {
		t.Fatalf("omniroute pricing = %+v", got)
	}
	if cfg.Costs.DailyBudgetUSD != 12.5 || cfg.Costs.MonthlyBudgetUSD != 250 || cfg.Costs.AlertThreshold != 0.75 {
		t.Fatalf("cost budgets = daily %.2f monthly %.2f threshold %.2f", cfg.Costs.DailyBudgetUSD, cfg.Costs.MonthlyBudgetUSD, cfg.Costs.AlertThreshold)
	}
	if cfg.Ops.SLOWindow != "12h" || cfg.Ops.MaxFailureRate != 0.2 || cfg.Ops.MaxIncompleteRate != 0.03 || cfg.Ops.MaxP95RunDuration != "2m" || cfg.Ops.MinRunsForSignal != 5 {
		t.Fatalf("ops slo config = %+v", cfg.Ops)
	}
	if cfg.Deployment.Profile != "production" || cfg.Deployment.Owner != "platform" || cfg.Deployment.Region != "us-central" || cfg.Deployment.Notes != "customer workspace" {
		t.Fatalf("deployment config = %+v", cfg.Deployment)
	}
	if !cfg.Runtime.SSRFProtection {
		t.Fatal("production profile should default runtime.ssrf_protection to true")
	}
}

func TestProductionProfileCanExplicitlyDisableSSRFProtection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("deployment:\n  profile: production\nruntime:\n  ssrf_protection: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.SSRFProtection {
		t.Fatal("explicit false was overridden by the production default")
	}
}

// ---------------------------------------------------------------------------
// Load — pure-defaults path (no config file, no env overrides)
// ---------------------------------------------------------------------------

// TestLoadNoConfigFileUsesDefaults verifies that Load succeeds with no config
// file and that all critical default values are applied.
func TestLoadNoConfigFileUsesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Pass an empty cfgPath so Load searches for a file that doesn't exist.
	cfg, resolvedPath, err := Load("")
	if err != nil {
		t.Fatalf("Load with no config file: %v", err)
	}

	// resolvedPath should fall back to the workspace config.yaml.
	wantResolvedPath := filepath.Join(home, ".soulacy", "soulspace", "config.yaml")
	if resolvedPath != wantResolvedPath {
		t.Errorf("resolvedPath = %q, want %q", resolvedPath, wantResolvedPath)
	}

	// Server defaults.
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("server.host = %q, want 127.0.0.1", cfg.Server.Host)
	}
	if cfg.Server.Port != 18789 {
		t.Errorf("server.port = %d, want 18789", cfg.Server.Port)
	}
	if !cfg.Server.GUIEnabled {
		t.Error("server.gui_enabled should default to true")
	}

	// Runtime defaults.
	if cfg.Runtime.MaxConcurrentSessions != 100 {
		t.Errorf("runtime.max_concurrent_sessions = %d, want 100", cfg.Runtime.MaxConcurrentSessions)
	}
	if cfg.Runtime.DefaultMaxTurns != 20 {
		t.Errorf("runtime.default_max_turns = %d, want 20", cfg.Runtime.DefaultMaxTurns)
	}
	if cfg.Runtime.MaxAgentCallDepth != 5 {
		t.Errorf("runtime.max_agent_call_depth = %d, want 5", cfg.Runtime.MaxAgentCallDepth)
	}
	if cfg.Runtime.PythonBin != "python3" {
		t.Errorf("runtime.python_bin = %q, want python3", cfg.Runtime.PythonBin)
	}
	if cfg.Runtime.ToolTimeout != "120s" {
		t.Errorf("runtime.tool_timeout = %q, want 120s", cfg.Runtime.ToolTimeout)
	}
	if len(cfg.Runtime.AllowSystemAgents) != 0 {
		t.Errorf("runtime.allow_system_agents should default empty, got %v", cfg.Runtime.AllowSystemAgents)
	}
	if cfg.Runtime.SSRFProtection {
		t.Error("runtime.ssrf_protection should default to false")
	}

	// Sandbox defaults.
	if !cfg.Runtime.Sandbox.Enabled {
		t.Error("runtime.sandbox.enabled should default to true")
	}
	if cfg.Runtime.Sandbox.CPUSeconds != 30 {
		t.Errorf("runtime.sandbox.cpu_seconds = %d, want 30", cfg.Runtime.Sandbox.CPUSeconds)
	}
	if cfg.Runtime.Sandbox.MemoryMB != 512 {
		t.Errorf("runtime.sandbox.memory_mb = %d, want 512", cfg.Runtime.Sandbox.MemoryMB)
	}
	if cfg.Runtime.Sandbox.OpenFiles != 256 {
		t.Errorf("runtime.sandbox.open_files = %d, want 256", cfg.Runtime.Sandbox.OpenFiles)
	}
	if cfg.Runtime.Sandbox.FileSizeMB != 64 {
		t.Errorf("runtime.sandbox.file_size_mb = %d, want 64", cfg.Runtime.Sandbox.FileSizeMB)
	}

	// Memory defaults.
	if cfg.Memory.MaxHistory != 50 {
		t.Errorf("memory.max_history = %d, want 50", cfg.Memory.MaxHistory)
	}
	if cfg.Memory.VectorDB != "" {
		t.Errorf("memory.vector_db = %q, want ''", cfg.Memory.VectorDB)
	}
	if cfg.Memory.VectorDims != 768 {
		t.Errorf("memory.vector_dims = %d, want 768", cfg.Memory.VectorDims)
	}
	wantMemoryDir := filepath.Join(home, ".soulacy", "soulspace", "memory")
	if cfg.Memory.Dir != wantMemoryDir {
		t.Errorf("memory.dir = %q, want %q", cfg.Memory.Dir, wantMemoryDir)
	}
	wantArchivePath := filepath.Join(home, ".soulacy", "soulspace", "data", "archive.db")
	if cfg.Memory.SQLitePath != wantArchivePath {
		t.Errorf("memory.sqlite_path = %q, want %q", cfg.Memory.SQLitePath, wantArchivePath)
	}

	// Storage defaults.
	if cfg.Storage.Backend != "sqlite" {
		t.Errorf("storage.backend = %q, want sqlite", cfg.Storage.Backend)
	}

	// Executor defaults.
	if cfg.Executor.Backend != "process" {
		t.Errorf("executor.backend = %q, want process", cfg.Executor.Backend)
	}
	if cfg.Executor.Workers != 4 {
		t.Errorf("executor.workers = %d, want 4", cfg.Executor.Workers)
	}
	if cfg.Executor.DockerImage != "python:3.12-slim" {
		t.Errorf("executor.docker_image = %q, want python:3.12-slim", cfg.Executor.DockerImage)
	}
	if cfg.Executor.DockerNetwork != "none" {
		t.Errorf("executor.docker_network = %q, want none", cfg.Executor.DockerNetwork)
	}
	if cfg.Executor.SSHPythonBin != "python3" {
		t.Errorf("executor.ssh_python_bin = %q, want python3", cfg.Executor.SSHPythonBin)
	}

	// Queue defaults.
	if cfg.Queue.Backend != "memory" {
		t.Errorf("queue.backend = %q, want memory", cfg.Queue.Backend)
	}
	if cfg.Queue.NATSUrl != "nats://localhost:4222" {
		t.Errorf("queue.nats_url = %q, want nats://localhost:4222", cfg.Queue.NATSUrl)
	}
	if cfg.Queue.NATSStream != "soulacy" {
		t.Errorf("queue.nats_stream = %q, want soulacy", cfg.Queue.NATSStream)
	}
	if cfg.Queue.NATSAckWait != "30s" {
		t.Errorf("queue.nats_ack_wait = %q, want 30s", cfg.Queue.NATSAckWait)
	}
	if cfg.Queue.NATSMaxDeliver != 0 {
		t.Errorf("queue.nats_max_deliver = %d, want 0", cfg.Queue.NATSMaxDeliver)
	}

	// Auth defaults.
	if cfg.Auth.Mode != "apikey" {
		t.Errorf("auth.mode = %q, want apikey", cfg.Auth.Mode)
	}
	if cfg.Auth.JWTAccessTTL != "15m" {
		t.Errorf("auth.jwt_access_ttl = %q, want 15m", cfg.Auth.JWTAccessTTL)
	}
	if cfg.Auth.JWTRefreshTTL != "168h" {
		t.Errorf("auth.jwt_refresh_ttl = %q, want 168h", cfg.Auth.JWTRefreshTTL)
	}

	// LLM defaults.
	if cfg.LLM.DefaultProvider != "ollama" {
		t.Errorf("llm.default_provider = %q, want ollama", cfg.LLM.DefaultProvider)
	}

	// Knowledge defaults.
	if cfg.Knowledge.EmbeddingProvider != "ollama" {
		t.Errorf("knowledge.embedding_provider = %q, want ollama", cfg.Knowledge.EmbeddingProvider)
	}
	if cfg.Knowledge.EmbeddingModel != "nomic-embed-text" {
		t.Errorf("knowledge.embedding_model = %q, want nomic-embed-text", cfg.Knowledge.EmbeddingModel)
	}
	if cfg.Knowledge.ChunkSize != 1000 {
		t.Errorf("knowledge.chunk_size = %d, want 1000", cfg.Knowledge.ChunkSize)
	}
	if cfg.Knowledge.ChunkOverlap != 200 {
		t.Errorf("knowledge.chunk_overlap = %d, want 200", cfg.Knowledge.ChunkOverlap)
	}
	if cfg.Knowledge.MaxDocumentBytes != 50<<20 {
		t.Errorf("knowledge.max_document_bytes = %d, want %d", cfg.Knowledge.MaxDocumentBytes, int64(50<<20))
	}

	// Log defaults.
	if cfg.Log.Level != "info" {
		t.Errorf("log.level = %q, want info", cfg.Log.Level)
	}
	if cfg.Log.Format != "console" {
		t.Errorf("log.format = %q, want console", cfg.Log.Format)
	}
}

// ---------------------------------------------------------------------------
// Load — environment variable overrides
// ---------------------------------------------------------------------------

// TestLoadEnvVarOverridesServerPort verifies that the SOULACY_SERVER_PORT
// environment variable overrides the default port.
func TestLoadEnvVarOverridesServerPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_SERVER_PORT", "9999")
	defer os.Unsetenv("SOULACY_SERVER_PORT")

	cfg, _, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("server.port = %d, want 9999 (from env)", cfg.Server.Port)
	}
}

// TestLoadEnvVarOverridesServerHost verifies SOULACY_SERVER_HOST override.
func TestLoadEnvVarOverridesServerHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_SERVER_HOST", "0.0.0.0")
	defer os.Unsetenv("SOULACY_SERVER_HOST")

	cfg, _, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("server.host = %q, want 0.0.0.0 (from env)", cfg.Server.Host)
	}
}

// TestLoadEnvVarOverridesAuthMode verifies SOULACY_AUTH_MODE override.
func TestLoadEnvVarOverridesAuthMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_AUTH_MODE", "jwt")
	defer os.Unsetenv("SOULACY_AUTH_MODE")

	cfg, _, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Mode != "jwt" {
		t.Errorf("auth.mode = %q, want jwt (from env)", cfg.Auth.Mode)
	}
}

// TestLoadEnvVarOverridesLogLevel verifies SOULACY_LOG_LEVEL override.
func TestLoadEnvVarOverridesLogLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_LOG_LEVEL", "debug")
	defer os.Unsetenv("SOULACY_LOG_LEVEL")

	cfg, _, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("log.level = %q, want debug (from env)", cfg.Log.Level)
	}
}

// ---------------------------------------------------------------------------
// Load — malformed YAML returns error
// ---------------------------------------------------------------------------

// TestLoadMalformedYAMLReturnsError verifies that a config file with invalid
// YAML syntax causes Load to return a non-nil error.
func TestLoadMalformedYAMLReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(cfgPath, []byte(":\tbad: yaml: [unclosed"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err := Load(cfgPath)
	if err == nil {
		t.Fatal("Load should return error for malformed YAML")
	}
}

// ---------------------------------------------------------------------------
// Load — config file overrides several fields simultaneously
// ---------------------------------------------------------------------------

// TestLoadConfigFileOverridesMultipleFields verifies that multiple YAML
// sections are unmarshalled correctly when explicitly set.
func TestLoadConfigFileOverridesMultipleFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	yaml := `
server:
  host: "0.0.0.0"
  port: 8080
  gui_enabled: false
runtime:
  default_max_turns: 50
  python_bin: "/usr/bin/python3"
auth:
  mode: "jwt"
  jwt_secret: "supersecret"
log:
  level: "warn"
  format: "json"
queue:
  backend: "nats"
  nats_url: "nats://mycluster:4222"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("server.host = %q, want 0.0.0.0", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("server.port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.GUIEnabled {
		t.Error("server.gui_enabled should be false")
	}
	if cfg.Runtime.DefaultMaxTurns != 50 {
		t.Errorf("runtime.default_max_turns = %d, want 50", cfg.Runtime.DefaultMaxTurns)
	}
	if cfg.Runtime.PythonBin != "/usr/bin/python3" {
		t.Errorf("runtime.python_bin = %q, want /usr/bin/python3", cfg.Runtime.PythonBin)
	}
	if cfg.Auth.Mode != "jwt" {
		t.Errorf("auth.mode = %q, want jwt", cfg.Auth.Mode)
	}
	if cfg.Auth.JWTSecret != "supersecret" {
		t.Errorf("auth.jwt_secret = %q, want supersecret", cfg.Auth.JWTSecret)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("log.level = %q, want warn", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("log.format = %q, want json", cfg.Log.Format)
	}
	if cfg.Queue.Backend != "nats" {
		t.Errorf("queue.backend = %q, want nats", cfg.Queue.Backend)
	}
	if cfg.Queue.NATSUrl != "nats://mycluster:4222" {
		t.Errorf("queue.nats_url = %q, want nats://mycluster:4222", cfg.Queue.NATSUrl)
	}
}

// ---------------------------------------------------------------------------
// DataDir
// ---------------------------------------------------------------------------

// TestDataDir verifies that DataDir returns a path ending in ".soulacy"
// rooted under the user's home directory.
func TestDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	want := filepath.Join(home, ".soulacy", "soulspace")
	if dir != want {
		t.Errorf("DataDir = %q, want %q (fresh installs are soulspace)", dir, want)
	}

	// Legacy installation → flat ~/.soulacy.
	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	if err := os.MkdirAll(filepath.Join(home2, ".soulacy", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err = DataDir()
	if err != nil {
		t.Fatalf("DataDir legacy: %v", err)
	}
	if dir != filepath.Join(home2, ".soulacy") {
		t.Errorf("legacy DataDir = %q", dir)
	}
}

// ---------------------------------------------------------------------------
// EnsureDirs
// ---------------------------------------------------------------------------

// TestEnsureDirsCreatesRequiredDirectories verifies that EnsureDirs creates
// all directories referenced by a Config, including Memory.Dir,
// SQLitePath parent, AgentDirs, PluginDirs, and AuditDir.
func TestEnsureDirsCreatesRequiredDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	base := t.TempDir()
	cfg := &Config{
		Memory: MemoryConfig{
			Dir:        filepath.Join(base, "memory"),
			SQLitePath: filepath.Join(base, "db", "archive.db"),
		},
		Knowledge: KnowledgeConfig{
			DBPath: filepath.Join(base, "kb", "knowledge.db"),
		},
		AgentDirs:  []string{filepath.Join(base, "agents")},
		PluginDirs: []string{filepath.Join(base, "plugins")},
		SkillDirs:  []string{filepath.Join(base, "skills")},
		Runtime: RuntimeConfig{
			AuditDir: filepath.Join(base, "audit"),
		},
	}

	if err := EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	expected := []string{
		cfg.Memory.Dir,
		filepath.Dir(cfg.Memory.SQLitePath),
		filepath.Dir(cfg.Knowledge.DBPath),
		cfg.AgentDirs[0],
		cfg.PluginDirs[0],
		cfg.SkillDirs[0],
		cfg.Runtime.AuditDir,
	}
	for _, d := range expected {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("EnsureDirs did not create %q: %v", d, err)
		}
	}
}

// TestEnsureDirsSkipsEmptyPaths verifies that EnsureDirs ignores empty-string
// directory entries rather than attempting to create "".
func TestEnsureDirsSkipsEmptyPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	base := t.TempDir()
	cfg := &Config{
		Memory: MemoryConfig{
			Dir:        filepath.Join(base, "memory"),
			SQLitePath: filepath.Join(base, "archive.db"),
		},
		Knowledge: KnowledgeConfig{
			DBPath: "", // empty — should be skipped
		},
		Runtime: RuntimeConfig{
			AuditDir: "", // empty — should be skipped
		},
	}
	if err := EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs with empty paths: %v", err)
	}
}

// TestEnsureDirsCreatesHomeSkillsDir verifies the workspace skills directory
// is created when HOME is set: fresh installs get the soulspace layout,
// legacy installations keep ~/.soulacy/skills.
func TestEnsureDirsCreatesHomeSkillsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SOULACY_WORKSPACE", "")

	base := t.TempDir()
	cfg := &Config{
		Memory: MemoryConfig{
			Dir:        filepath.Join(base, "memory"),
			SQLitePath: filepath.Join(base, "archive.db"),
		},
	}
	if err := EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	skillsDir := filepath.Join(home, ".soulacy", "soulspace", "skills")
	if _, err := os.Stat(skillsDir); err != nil {
		t.Errorf("EnsureDirs did not create soulspace skills dir %q: %v", skillsDir, err)
	}

	// Legacy installation → skills stay at ~/.soulacy/skills.
	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	if err := os.MkdirAll(filepath.Join(home2, ".soulacy", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirs(cfg); err != nil {
		t.Fatalf("EnsureDirs legacy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home2, ".soulacy", "skills")); err != nil {
		t.Errorf("legacy skills dir missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Version variable is exported and non-empty by default
// ---------------------------------------------------------------------------

func TestVersionDefaultIsNonEmpty(t *testing.T) {
	if Version == "" {
		t.Error("Version should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Load — resolvedPath uses the explicit cfgPath when file exists
// ---------------------------------------------------------------------------

// TestLoadResolvedPathMatchesExplicitPath is a regression guard: when Load is
// given an explicit cfgPath, the returned resolved path must equal that path.
func TestLoadResolvedPathMatchesExplicitPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  port: 1234\n"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, resolved, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if resolved != cfgPath {
		t.Errorf("resolvedPath = %q, want %q", resolved, cfgPath)
	}
}

// ---------------------------------------------------------------------------
// Load — MCP and Channels config sections unmarshal correctly
// ---------------------------------------------------------------------------

// TestLoadMCPConfig verifies that MCP server configuration is unmarshalled.
func TestLoadMCPConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	yaml := `
mcp:
  servers:
    filesystem:
      transport: "stdio"
      command: "/usr/local/bin/mcp-fs"
      args:
        - "/tmp"
      env_secret_refs:
        SERPAPI_KEY: travel-serpapi
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svr, ok := cfg.MCP.Servers["filesystem"]
	if !ok {
		t.Fatal("mcp.servers.filesystem not found")
	}
	if svr.Transport != "stdio" {
		t.Errorf("mcp.servers.filesystem.transport = %q, want stdio", svr.Transport)
	}
	if svr.Command != "/usr/local/bin/mcp-fs" {
		t.Errorf("mcp.servers.filesystem.command = %q", svr.Command)
	}
	if len(svr.Args) != 1 || svr.Args[0] != "/tmp" {
		t.Errorf("mcp.servers.filesystem.args = %v, want [\"/tmp\"]", svr.Args)
	}
	if svr.EnvSecretRefs["SERPAPI_KEY"] != "travel-serpapi" {
		t.Errorf("mcp.servers.filesystem.env_secret_refs = %v", svr.EnvSecretRefs)
	}
}

// ---------------------------------------------------------------------------
// Load — VectorConfig and StorageConfig
// ---------------------------------------------------------------------------

func TestLoadVectorAndStorageConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	yaml := `
vector:
  backend: "qdrant"
  url: "http://qdrant:6333"
  collection: "my_memories"
  api_key: "secret"
  dims: 1536
storage:
  backend: "postgres"
  postgres_dsn: "postgres://localhost/soulacy"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, _, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Vector.Backend != "qdrant" {
		t.Errorf("vector.backend = %q, want qdrant", cfg.Vector.Backend)
	}
	if cfg.Vector.URL != "http://qdrant:6333" {
		t.Errorf("vector.url = %q", cfg.Vector.URL)
	}
	if cfg.Vector.Collection != "my_memories" {
		t.Errorf("vector.collection = %q", cfg.Vector.Collection)
	}
	if cfg.Vector.Dims != 1536 {
		t.Errorf("vector.dims = %d, want 1536", cfg.Vector.Dims)
	}
	if cfg.Storage.Backend != "postgres" {
		t.Errorf("storage.backend = %q, want postgres", cfg.Storage.Backend)
	}
	if cfg.Storage.PostgresDSN != "postgres://localhost/soulacy" {
		t.Errorf("storage.postgres_dsn = %q", cfg.Storage.PostgresDSN)
	}
}
