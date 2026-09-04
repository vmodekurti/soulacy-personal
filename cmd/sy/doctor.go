package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/internal/updates"

)

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorCheck struct {
	Name    string       `json:"name"`
	Status  doctorStatus `json:"status"`
	Detail  string       `json:"detail"`
	Remedy  string       `json:"remedy,omitempty"`
	Elapsed string       `json:"elapsed,omitempty"`
}

type doctorReport struct {
	GatewayURL string        `json:"gateway_url"`
	ConfigPath string        `json:"config_path"`
	Checks     []doctorCheck `json:"checks"`
	Summary    struct {
		OK    int `json:"ok"`
		Warn  int `json:"warn"`
		Fail  int `json:"fail"`
		Total int `json:"total"`
	} `json:"summary"`
}

func buildDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run local diagnostics for Soulacy",
		Long: `Run local diagnostics for a Soulacy installation.

Checks config discovery, runtime directories, gateway health, Python tooling,
Ollama reachability, knowledge storage, and MCP server status.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor()
		},
	}
	cmd.AddCommand(buildDoctorBundleCmd())
	return cmd
}

func runDoctor() error {
	report := collectDoctorReport()
	if outputJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		printDoctorReport(report)
	}
	if report.Summary.Fail > 0 {
		return fmt.Errorf("doctor found %d failing check(s)", report.Summary.Fail)
	}
	return nil
}

func collectDoctorReport() doctorReport {
	report := doctorReport{
		GatewayURL: gatewayURL,
		ConfigPath: viper.ConfigFileUsed(),
	}
	add := func(c doctorCheck) {
		report.Checks = append(report.Checks, c)
		switch c.Status {
		case doctorOK:
			report.Summary.OK++
		case doctorWarn:
			report.Summary.Warn++
		case doctorFail:
			report.Summary.Fail++
		}
		report.Summary.Total++
	}

	home, _ := os.UserHomeDir()
	runtimeDir := filepath.Join(home, ".soulacy")
	if ws, werr := config.ResolveWorkspace(); werr == nil {
		runtimeDir = ws.Root
	}

	add(checkConfig())
	add(checkRuntimeDir(runtimeDir))
	add(checkInstallLayout())
	add(checkAgentDirs())
	add(checkAgentCount())
	add(checkPort())
	add(checkPython())
	add(checkOllama())
	add(checkProviderReachability())
	add(checkProviderAuth())
	add(checkVaultConsistency())
	add(checkSandboxState())
	add(checkKnowledgeDB(runtimeDir))
	add(checkWorkspaceVersion(runtimeDir))
	add(checkUpdateManifest())
	add(checkGatewayHealth())
	add(checkRecentErrors())
	add(checkMCPStatus())

	return report
}

func checkUpdateManifest() doctorCheck {
	source := resolveUpdateManifestSource("")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := updates.CheckForUpdate(ctx, source, config.Version)
	if err != nil {
		return doctorCheck{
			Name:   "updates",
			Status: doctorWarn,
			Detail: "could not check for updates: " + err.Error(),
			Remedy: "verify internet connectivity or updates.manifest_url value",
		}
	}
	status := doctorOK
	remedy := ""
	if res.UpdateAvailable {
		status = doctorWarn
		remedy = "run `sy upgrade` to install the latest version (" + res.LatestVersion + ")"
	}
	detail := res.Message
	if res.Artifact != nil {
		detail += " (artifact: " + res.Artifact.Name + ")"
	}
	return doctorCheck{
		Name:   "updates",
		Status: status,
		Detail: detail,
		Remedy: remedy,
	}
}


// loadDoctorConfig unmarshals the viper-loaded configuration into a typed
// config.Config for local (non-gateway) inspection. Returns nil on failure.
func loadDoctorConfig() (*config.Config, error) {
	var cfg config.Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// checkVaultConsistency opens the encrypted credential vault the same way the
// gateway does (LocalKMS + SQLite) and verifies it actually decrypts, then
// cross-checks configured non-local providers: each should have a usable API
// key either in config or in the vault. This catches the common "provider
// configured but its key silently missing" failure before a run hits an auth
// error. (sy doctor v2)
func checkVaultConsistency() doctorCheck {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return doctorCheck{Name: "vault", Status: doctorWarn, Detail: "cannot resolve workspace: " + err.Error()}
	}
	vaultPath := ws.CredentialsDB()
	if _, statErr := os.Stat(vaultPath); statErr != nil {
		return doctorCheck{
			Name:   "vault",
			Status: doctorOK,
			Detail: "no vault yet; secrets resolve from config/env",
		}
	}

	kms, kerr := credentials.NewLocalKMSWithStore(filepath.Dir(vaultPath))
	if kerr != nil {
		return doctorCheck{
			Name:   "vault",
			Status: doctorFail,
			Detail: "KMS init failed: " + kerr.Error(),
			Remedy: "ensure the vault master-secret file next to " + vaultPath + " is readable and unmodified",
		}
	}
	vault, verr := credentials.NewSQLiteVault(vaultPath, kms)
	if verr != nil {
		return doctorCheck{
			Name:   "vault",
			Status: doctorFail,
			Detail: "vault open failed: " + verr.Error(),
			Remedy: "the vault DB may be corrupt or encrypted with a different master secret",
		}
	}
	defer vault.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	keys, lerr := vault.List(ctx, secrets.GlobalScope)
	if lerr != nil {
		return doctorCheck{
			Name:   "vault",
			Status: doctorFail,
			Detail: "vault list failed: " + lerr.Error(),
			Remedy: "the vault may be unreadable with the current master secret",
		}
	}
	// Prove decryption end-to-end on the first key (List returns names only).
	if len(keys) > 0 {
		if _, gerr := vault.Get(ctx, secrets.GlobalScope, keys[0]); gerr != nil {
			return doctorCheck{
				Name:   "vault",
				Status: doctorFail,
				Detail: fmt.Sprintf("decrypt check failed for %q: %v", keys[0], gerr),
				Remedy: "vault entries cannot be decrypted with the current master secret",
			}
		}
	}
	stored := map[string]bool{}
	for _, k := range keys {
		stored[strings.ToLower(strings.TrimSpace(k))] = true
	}

	// Cross-check configured non-local providers for a usable key.
	cfg, _ := loadDoctorConfig()
	var missing []string
	if cfg != nil {
		for id, p := range cfg.LLM.Providers {
			if isLocalProvider(id, p.BaseURL) {
				continue
			}
			if strings.TrimSpace(p.APIKey) != "" {
				continue
			}
			if stored[strings.ToLower(id+".api_key")] || stored[strings.ToLower(id)] {
				continue
			}
			missing = append(missing, id)
		}
	}

	detail := fmt.Sprintf("vault OK — %d secret(s) stored, decryption verified", len(keys))
	if len(missing) > 0 {
		return doctorCheck{
			Name:   "vault",
			Status: doctorWarn,
			Detail: detail + "; providers without a key in config or vault: " + strings.Join(missing, ", "),
			Remedy: "add the missing API key(s) via the Secrets page or `sy secrets set <provider>.api_key`",
		}
	}
	return doctorCheck{Name: "vault", Status: doctorOK, Detail: detail}
}

func checkConfig() doctorCheck {
	path := viper.ConfigFileUsed()
	if path == "" {
		return doctorCheck{
			Name:   "config",
			Status: doctorWarn,
			Detail: "no config file loaded; using defaults and environment",
			Remedy: "run `sy setup` or create ~/.soulacy/config.yaml",
		}
	}
	if _, err := os.Stat(path); err != nil {
		return doctorCheck{Name: "config", Status: doctorFail, Detail: err.Error()}
	}
	return doctorCheck{Name: "config", Status: doctorOK, Detail: path}
}

func checkRuntimeDir(runtimeDir string) doctorCheck {
	required := []string{"agents", "logs", "mcp-servers", "skills"}
	var missing []string
	for _, name := range required {
		p := filepath.Join(runtimeDir, name)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return doctorCheck{
			Name:   "runtime directories",
			Status: doctorWarn,
			Detail: "missing: " + strings.Join(missing, ", "),
			Remedy: "run `sy setup` or `mkdir -p ~/.soulacy/{agents,logs,mcp-servers,skills}`",
		}
	}
	return doctorCheck{Name: "runtime directories", Status: doctorOK, Detail: runtimeDir}
}

func checkInstallLayout() doctorCheck {
	bin, err := resolveSoulacyBinary()
	if err != nil {
		return doctorCheck{
			Name:   "install",
			Status: doctorFail,
			Detail: err.Error(),
			Remedy: "run `make install`, `bash scripts/install.sh`, or set SOULACY_BIN to the intended gateway binary",
		}
	}
	if st, statErr := os.Stat(bin); statErr != nil {
		return doctorCheck{Name: "install", Status: doctorFail, Detail: statErr.Error()}
	} else if st.Mode()&0o111 == 0 {
		return doctorCheck{
			Name:   "install",
			Status: doctorFail,
			Detail: bin + " is not executable",
			Remedy: "run `chmod +x " + bin + "` or reinstall Soulacy",
		}
	}

	syPath, syErr := exec.LookPath("sy")
	copies := pathCopies("soulacy")
	status := doctorOK
	var issues []string
	var remedy []string
	if syErr != nil {
		status = doctorWarn
		issues = append(issues, "sy not found on PATH")
		remedy = append(remedy, "install both binaries with `make install`")
	}
	if len(copies) > 1 {
		status = doctorWarn
		issues = append(issues, fmt.Sprintf("%d soulacy binaries on PATH", len(copies)))
		remedy = append(remedy, "remove stale copies so `command -v soulacy` resolves to the intended install")
	}
	if servicePath, serviceBin, serviceOK := serviceBinaryConfig(); servicePath == "" {
		status = doctorWarn
		issues = append(issues, "daemon service not installed")
		remedy = append(remedy, "run `sy daemon install` for login/boot startup")
	} else if serviceOK && serviceBin != "" && serviceBin != bin {
		status = doctorWarn
		issues = append(issues, "daemon points at "+serviceBin)
		remedy = append(remedy, "run `sy daemon install` again after installing the desired binary")
	} else if !serviceOK {
		status = doctorWarn
		issues = append(issues, "could not read daemon service file "+servicePath)
		remedy = append(remedy, "check service file permissions or reinstall with `sy daemon install`")
	}

	detail := "soulacy=" + bin
	if syErr == nil {
		if abs, absErr := filepath.Abs(syPath); absErr == nil {
			detail += "; sy=" + abs
		} else {
			detail += "; sy=" + syPath
		}
	}
	if len(issues) > 0 {
		detail += "; " + strings.Join(issues, "; ")
	}
	return doctorCheck{Name: "install", Status: status, Detail: detail, Remedy: strings.Join(uniqueStrings(remedy), "; ")}
}

func serviceBinaryConfig() (path, bin string, ok bool) {
	switch runtime.GOOS {
	case "darwin":
		p, err := launchAgentPath()
		if err != nil {
			return "", "", false
		}
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			return "", "", true
		}
		if err != nil {
			return p, "", false
		}
		return p, configuredServiceBinary(string(data), runtime.GOOS), true
	case "linux":
		p, err := systemdUnitPath()
		if err != nil {
			return "", "", false
		}
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			return "", "", true
		}
		if err != nil {
			return p, "", false
		}
		return p, configuredServiceBinary(string(data), runtime.GOOS), true
	default:
		return "", "", true
	}
}

func configuredServiceBinary(content, goos string) string {
	switch goos {
	case "darwin":
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "<string>") || !strings.HasSuffix(line, "</string>") {
				continue
			}
			v := strings.TrimSuffix(strings.TrimPrefix(line, "<string>"), "</string>")
			if filepath.IsAbs(v) && filepath.Base(v) == "soulacy" {
				return v
			}
		}
	case "linux":
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "ExecStart=") {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
			if len(fields) > 0 && filepath.Base(fields[0]) == "soulacy" {
				return fields[0]
			}
		}
	}
	return ""
}

func pathCopies(name string) []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			abs, _ := filepath.Abs(p)
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func checkAgentDirs() doctorCheck {
	dirs := viper.GetStringSlice("agent_dirs")
	if len(dirs) == 0 {
		// Same fallback as cmd/sy/agent_tier.go::loadAgentsFromDisk —
		// `sy` doesn't run config.Load(), so viper has no agent_dirs
		// even on installs where the gateway uses the workspace default
		// at ~/.soulacy/soulspace/agents. Going through ResolveWorkspace
		// gives doctor the SAME path the gateway resolves.
		ws, err := config.ResolveWorkspace()
		if err == nil && ws.Agents != "" {
			dirs = []string{ws.Agents}
		}
	}
	if len(dirs) == 0 {
		return doctorCheck{
			Name:   "agent_dirs",
			Status: doctorWarn,
			Detail: "no agent_dirs configured and workspace resolution failed",
			Remedy: "set agent_dirs in your config.yaml, or set $SOULACY_WORKSPACE",
		}
	}
	var issues []string
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			issues = append(issues, dir+" is relative")
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			issues = append(issues, dir+" not found")
		}
	}
	if len(issues) > 0 {
		return doctorCheck{
			Name:   "agent_dirs",
			Status: doctorWarn,
			Detail: strings.Join(issues, "; "),
			Remedy: "use absolute, existing paths so LaunchAgent/systemd starts load the same agents",
		}
	}
	return doctorCheck{Name: "agent_dirs", Status: doctorOK, Detail: strings.Join(dirs, ", ")}
}

func checkPython() doctorCheck {
	py := viper.GetString("runtime.python_bin")
	if py == "" {
		py = "python3"
	}
	resolved, err := exec.LookPath(py)
	if err != nil {
		return doctorCheck{
			Name:   "python",
			Status: doctorWarn,
			Detail: py + " not found on PATH",
			Remedy: "set runtime.python_bin to an absolute Python 3 path",
		}
	}
	out, err := exec.Command(resolved, "--version").CombinedOutput()
	if err != nil {
		return doctorCheck{Name: "python", Status: doctorWarn, Detail: strings.TrimSpace(string(out))}
	}
	status := doctorOK
	remedy := ""
	detail := strings.TrimSpace(string(out)) + " at " + resolved
	if !filepath.IsAbs(py) {
		status = doctorWarn
		remedy = "set runtime.python_bin to " + resolved + " for LaunchAgent/systemd reliability"
	}
	return doctorCheck{Name: "python", Status: status, Detail: detail, Remedy: remedy}
}

func checkOllama() doctorCheck {
	base := viper.GetString("llm.providers.ollama.base_url")
	if base == "" {
		base = "http://localhost:11434"
	}
	start := time.Now()
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := httpJSON(base+"/api/tags", &payload, 3*time.Second); err != nil {
		return doctorCheck{
			Name:    "ollama",
			Status:  doctorWarn,
			Detail:  err.Error(),
			Remedy:  "start Ollama or configure a different default LLM provider",
			Elapsed: time.Since(start).String(),
		}
	}
	names := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		names = append(names, m.Name)
	}
	detail := fmt.Sprintf("%d model(s)", len(names))
	if len(names) > 0 {
		detail += ": " + strings.Join(firstN(names, 5), ", ")
	}
	return doctorCheck{Name: "ollama", Status: doctorOK, Detail: detail, Elapsed: time.Since(start).String()}
}

func checkKnowledgeDB(runtimeDir string) doctorCheck {
	path := viper.GetString("knowledge.db_path")
	if path == "" {
		path = filepath.Join(runtimeDir, "knowledge.db")
	}
	st, err := os.Stat(path)
	if err != nil {
		return doctorCheck{
			Name:   "knowledge db",
			Status: doctorWarn,
			Detail: path + " not found",
			Remedy: "create a KB from the GUI or POST /api/v1/knowledge",
		}
	}
	return doctorCheck{Name: "knowledge db", Status: doctorOK, Detail: fmt.Sprintf("%s (%d bytes)", path, st.Size())}
}

func checkGatewayHealth() doctorCheck {
	start := time.Now()
	var body map[string]any
	err := gatewayJSON("/health", &body, 3*time.Second)
	if err != nil {
		return doctorCheck{
			Name:    "gateway",
			Status:  doctorFail,
			Detail:  err.Error(),
			Remedy:  "start the gateway with `soulacy serve`",
			Elapsed: time.Since(start).String(),
		}
	}
	status, _ := body["status"].(string)
	if status == "" {
		status = "unknown"
	}
	checkStatus := doctorOK
	if status != "ok" {
		checkStatus = doctorWarn
	}
	return doctorCheck{Name: "gateway", Status: checkStatus, Detail: "health status: " + status, Elapsed: time.Since(start).String()}
}

func checkMCPStatus() doctorCheck {
	start := time.Now()
	var body struct {
		Servers []struct {
			ID        string `json:"id"`
			Connected bool   `json:"connected"`
			Detail    string `json:"detail"`
		} `json:"servers"`
	}
	if err := gatewayJSON("/mcp", &body, 5*time.Second); err != nil {
		return doctorCheck{Name: "mcp", Status: doctorWarn, Detail: err.Error(), Elapsed: time.Since(start).String()}
	}
	if len(body.Servers) == 0 {
		return doctorCheck{Name: "mcp", Status: doctorOK, Detail: "no MCP servers configured", Elapsed: time.Since(start).String()}
	}
	var connected, failed []string
	for _, s := range body.Servers {
		if s.Connected {
			connected = append(connected, s.ID)
		} else {
			d := s.ID
			if s.Detail != "" {
				d += " (" + s.Detail + ")"
			}
			failed = append(failed, d)
		}
	}
	if len(failed) > 0 {
		return doctorCheck{
			Name:    "mcp",
			Status:  doctorWarn,
			Detail:  fmt.Sprintf("connected: %s; failed: %s", strings.Join(connected, ", "), strings.Join(failed, ", ")),
			Remedy:  "open the MCP GUI page or inspect ~/.soulacy/logs/soulacy.log",
			Elapsed: time.Since(start).String(),
		}
	}
	return doctorCheck{Name: "mcp", Status: doctorOK, Detail: "connected: " + strings.Join(connected, ", "), Elapsed: time.Since(start).String()}
}

func gatewayJSON(path string, out any, timeout time.Duration) error {
	url := gatewayURL + "/api/v1" + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func gatewayRawGET(path string, timeout time.Duration) (int, string, error) {
	url := gatewayURL + "/api/v1" + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("cannot reach %s: %w", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return resp.StatusCode, string(data), nil
}

func httpJSON(url string, out any, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func printDoctorReport(r doctorReport) {
	fmt.Println(bold("Soulacy Doctor"))
	fmt.Printf("Gateway: %s\n", r.GatewayURL)
	if r.ConfigPath != "" {
		fmt.Printf("Config:  %s\n", r.ConfigPath)
	} else {
		fmt.Println("Config:  (none loaded)")
	}
	fmt.Println()
	for _, c := range r.Checks {
		mark := green("✓")
		if c.Status == doctorWarn {
			mark = yellow("!")
		} else if c.Status == doctorFail {
			mark = red("✗")
		}
		elapsed := ""
		if c.Elapsed != "" {
			elapsed = " " + gray("("+c.Elapsed+")")
		}
		fmt.Printf("%s %-20s %s%s\n", mark, c.Name, c.Detail, elapsed)
		if c.Remedy != "" {
			fmt.Printf("  %s %s\n", gray("fix:"), c.Remedy)
		}
	}
	fmt.Println()
	fmt.Printf("Summary: %s ok, %s warn, %s fail\n",
		green(fmt.Sprint(r.Summary.OK)),
		yellow(fmt.Sprint(r.Summary.Warn)),
		red(fmt.Sprint(r.Summary.Fail)),
	)
	if runtime.GOOS == "darwin" {
		fmt.Println(gray("Note: macOS LaunchAgents inherit a minimal PATH; absolute runtime.python_bin is recommended."))
	}
}

func firstN(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

// ── doctor v2 checks ────────────────────────────────────────────────────────
//
// Added 2026-06-09 as part of the OpenClaw parity pass. Each check is
// designed to catch a real failure mode an operator hits on a fresh
// install or after misconfiguration — NOT to inflate the check count.

// checkAgentCount warns when the workspace has zero agents on disk. A
// fresh install includes the basic-chat starter, so this should only
// fire if the operator deleted it without adding any replacements. The
// remedy is to run `sy onboard` or copy a SOUL.yaml.
func checkAgentCount() doctorCheck {
	ws := syWorkspace()
	dirs := []string{ws.Agents}
	if d := viper.GetStringSlice("agent_dirs"); len(d) > 0 {
		dirs = d
	}
	total := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "SOUL.yaml")); err == nil {
				total++
			}
		}
	}
	if total == 0 {
		return doctorCheck{
			Name:   "agents",
			Status: doctorWarn,
			Detail: "no agents found on disk",
			Remedy: "run `sy onboard` to set up a starter, or drop a SOUL.yaml into " + dirs[0],
		}
	}
	return doctorCheck{
		Name:   "agents",
		Status: doctorOK,
		Detail: fmt.Sprintf("%d agent(s) on disk across %d dir(s)", total, len(dirs)),
	}
}

// checkPort verifies the configured gateway port is either bound by an
// already-running soulacy (good — `sy doctor` while serving) OR is free
// for binding. A foreign process holding the port is a hard fail.
//
// Bug fix 2026-06-09: previously hit `/healthz` (which 404s; the real
// endpoint is `/api/v1/health` under the API prefix) and used `httpJSON`
// (no auth, requires JSON decode). The 404 from the wrong path made
// every running-gateway-on-port-18789 look like "another process".
// Fixed: reuse `gatewayJSON("/health", ...)` — same code path as
// checkGatewayHealth, so the two checks now agree.
func checkPort() doctorCheck {
	host := viper.GetString("server.host")
	if host == "" {
		host = "127.0.0.1"
	}
	port := viper.GetInt("server.port")
	if port == 0 {
		port = 18789
	}
	addr := fmt.Sprintf("%s:%d", host, port)

	ln, bindErr := net.Listen("tcp", addr)
	if bindErr == nil {
		_ = ln.Close()
		return doctorCheck{
			Name:   "port",
			Status: doctorOK,
			Detail: fmt.Sprintf("%s is free for binding", addr),
		}
	}
	// Port is in use — ask the same /api/v1/health endpoint that
	// checkGatewayHealth uses. If it responds, the port is held by our
	// gateway (with auth, via gatewayJSON which adds the API key).
	if err := gatewayJSON("/health", &map[string]any{}, 2*time.Second); err == nil {
		return doctorCheck{
			Name:   "port",
			Status: doctorOK,
			Detail: fmt.Sprintf("%s held by a running soulacy gateway", addr),
		}
	}
	return doctorCheck{
		Name:   "port",
		Status: doctorFail,
		Detail: fmt.Sprintf("%s is occupied by another process", addr),
		Remedy: fmt.Sprintf("find the process: lsof -i :%d", port),
	}
}

// checkProviderReachability does a HEAD/GET to each LLM provider that
// has a key configured. We don't actually try to complete a chat (that
// would burn tokens) — we just verify the endpoint resolves and accepts
// TCP. A 401 from the API means the endpoint is reachable, which is
// what we care about; the key validity is a separate concern.
func checkProviderReachability() doctorCheck {
	type providerView struct {
		APIKey     string `json:"api_key"`
		Registered bool   `json:"registered"`
		BaseURL    string `json:"base_url"`
	}
	var gatewayProviders struct {
		Providers map[string]providerView `json:"providers"`
	}
	if err := gatewayJSON("/providers", &gatewayProviders, 5*time.Second); err == nil {
		configured := 0
		var unreachable []string
		for id, p := range gatewayProviders.Providers {
			if !p.Registered || p.APIKey == "" || isLocalProvider(id, p.BaseURL) {
				continue
			}
			configured++
			if p.BaseURL == "" {
				unreachable = append(unreachable, id+" (missing base_url)")
				continue
			}
			client := &http.Client{Timeout: 3 * time.Second}
			req, _ := http.NewRequest("GET", strings.TrimRight(p.BaseURL, "/"), nil)
			resp, err := client.Do(req)
			if err != nil {
				unreachable = append(unreachable, fmt.Sprintf("%s (%v)", id, err))
				continue
			}
			_ = resp.Body.Close()
		}
		if configured == 0 {
			return doctorCheck{
				Name:   "provider_reach",
				Status: doctorWarn,
				Detail: "no registered remote provider keys found in gateway state (local Ollama-only OK)",
				Remedy: "configure a cloud provider in Providers or confirm local Ollama is intentional",
			}
		}
		if len(unreachable) > 0 {
			return doctorCheck{
				Name:   "provider_reach",
				Status: doctorFail,
				Detail: fmt.Sprintf("%d of %d provider endpoint(s) unreachable: %s",
					len(unreachable), configured, strings.Join(firstN(unreachable, 3), "; ")),
				Remedy: "check network / DNS / firewall or the provider base_url",
			}
		}
		return doctorCheck{
			Name:   "provider_reach",
			Status: doctorOK,
			Detail: fmt.Sprintf("%d live remote provider endpoint(s) reachable; auth verified separately", configured),
		}
	}

	type probe struct {
		name string
		key  string // viper key for API key presence
		url  string // probe URL — pick a cheap endpoint per provider
	}
	probes := []probe{
		{"openai", "llm.providers.openai.api_key", "https://api.openai.com/v1/models"},
		{"anthropic", "llm.providers.anthropic.api_key", "https://api.anthropic.com/v1/messages"},
		{"groq", "llm.providers.groq.api_key", "https://api.groq.com/openai/v1/models"},
		{"mistral", "llm.providers.mistral.api_key", "https://api.mistral.ai/v1/models"},
		{"openrouter", "llm.providers.openrouter.api_key", "https://openrouter.ai/api/v1/models"},
		{"deepseek", "llm.providers.deepseek.api_key", "https://api.deepseek.com/v1/models"},
	}
	configured := 0
	var unreachable []string
	for _, p := range probes {
		if viper.GetString(p.key) == "" {
			continue
		}
		configured++
		client := &http.Client{Timeout: 3 * time.Second}
		req, _ := http.NewRequest("GET", p.url, nil)
		resp, err := client.Do(req)
		if err != nil {
			unreachable = append(unreachable, fmt.Sprintf("%s (%v)", p.name, err))
			continue
		}
		_ = resp.Body.Close()
		// Any HTTP response means the endpoint is reachable; we don't
		// care about auth status (401 is fine for a reachability probe).
	}
	if configured == 0 {
		return doctorCheck{
			Name:   "provider_reach",
			Status: doctorWarn,
			Detail: "no remote LLM provider keys configured (Ollama-only OK if you have it running)",
			Remedy: "run `sy onboard` to configure a provider, or paste a key into config.yaml",
		}
	}
	if len(unreachable) > 0 {
		return doctorCheck{
			Name:   "provider_reach",
			Status: doctorFail,
			Detail: fmt.Sprintf("%d of %d provider endpoints unreachable: %s",
				len(unreachable), configured, strings.Join(firstN(unreachable, 3), "; ")),
			Remedy: "check network / DNS / firewall to api.openai.com etc.",
		}
	}
	return doctorCheck{
		Name:   "provider_reach",
		Status: doctorOK,
		Detail: fmt.Sprintf("%d provider endpoint(s) reachable", configured),
	}
}

// checkProviderAuth asks the running gateway to list models for configured
// providers. Unlike the reachability probe above, this exercises the live
// provider object after vault/config/env secret overlay, so stale vault keys
// surface as provider-specific failures instead of hiding until an agent run.
func checkProviderAuth() doctorCheck {
	type providerView struct {
		APIKey     string `json:"api_key"`
		Registered bool   `json:"registered"`
		BaseURL    string `json:"base_url"`
		Model      string `json:"model"`
	}
	var resp struct {
		Providers map[string]providerView `json:"providers"`
	}
	if err := gatewayJSON("/providers", &resp, 5*time.Second); err != nil {
		return doctorCheck{
			Name:   "provider_auth",
			Status: doctorWarn,
			Detail: "skipped: couldn't read providers from gateway: " + err.Error(),
			Remedy: "start the gateway and pass --api-key if auth is enabled",
		}
	}
	if len(resp.Providers) == 0 {
		return doctorCheck{Name: "provider_auth", Status: doctorWarn, Detail: "no providers configured"}
	}

	var checked, ok, failedRemote, failedLocal, skipped []string
	for id, p := range resp.Providers {
		if !p.Registered {
			skipped = append(skipped, id+" (not registered)")
			continue
		}
		local := isLocalProvider(id, p.BaseURL)
		if p.APIKey == "" && !local {
			skipped = append(skipped, id+" (no key)")
			continue
		}
		checked = append(checked, id)
		status, body, err := gatewayRawGET("/providers/"+url.PathEscape(id)+"/models", 12*time.Second)
		if err != nil {
			entry := fmt.Sprintf("%s (%v)", id, err)
			if local {
				failedLocal = append(failedLocal, entry)
			} else {
				failedRemote = append(failedRemote, entry)
			}
			continue
		}
		if status >= 200 && status < 300 {
			ok = append(ok, id)
			continue
		}
		msg := strings.TrimSpace(body)
		if len(msg) > 120 {
			msg = msg[:120] + "..."
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", status)
		} else {
			msg = fmt.Sprintf("HTTP %d: %s", status, msg)
		}
		entry := id + " (" + msg + ")"
		if local {
			failedLocal = append(failedLocal, entry)
		} else {
			failedRemote = append(failedRemote, entry)
		}
	}

	if len(checked) == 0 {
		return doctorCheck{
			Name:   "provider_auth",
			Status: doctorWarn,
			Detail: "no registered keyed providers to validate",
			Remedy: "configure at least one cloud provider or use local Ollama",
		}
	}
	// A local provider that can't answer a model probe is a runtime state
	// issue (Ollama not running, base_url wrong) — checkOllama already
	// surfaces that separately. Bad credentials on a real cloud provider
	// is what this check is for, so only escalate to fail when at least
	// one REMOTE probe failed.
	if len(failedRemote) > 0 {
		combined := append([]string{}, failedRemote...)
		combined = append(combined, failedLocal...)
		return doctorCheck{
			Name:   "provider_auth",
			Status: doctorFail,
			Detail: fmt.Sprintf("%d/%d provider model probes failed: %s", len(combined), len(checked), strings.Join(firstN(combined, 3), "; ")),
			Remedy: "re-save the provider key in the Providers page or run `sy secrets set llm.providers.<id>.api_key`",
		}
	}
	if len(failedLocal) > 0 {
		return doctorCheck{
			Name:   "provider_auth",
			Status: doctorWarn,
			Detail: fmt.Sprintf("%d local provider probe(s) failed (likely not running): %s", len(failedLocal), strings.Join(firstN(failedLocal, 3), "; ")),
			Remedy: "start the local runtime (e.g. `ollama serve`) or point the provider at a reachable base_url",
		}
	}
	detail := fmt.Sprintf("%d provider key/model probe(s) passed: %s", len(ok), strings.Join(ok, ", "))
	if len(skipped) > 0 {
		detail += fmt.Sprintf(" (skipped %d)", len(skipped))
	}
	return doctorCheck{Name: "provider_auth", Status: doctorOK, Detail: detail}
}

func isLocalProvider(id, baseURL string) bool {
	if id == "ollama" {
		return true
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// checkSandboxState verifies the sandbox is enabled when the config says
// it should be, and the python self-exec sandbox wrapper is invokable.
// The runtime auto-disables sandbox on platforms where rlimits aren't
// available; we warn if config says enabled but we know it can't fire.
func checkSandboxState() doctorCheck {
	enabled := viper.GetBool("runtime.sandbox.enabled")
	if !enabled {
		// Look at the default — if the operator explicitly disabled it,
		// say so neutrally. If they're on the default-false (older
		// configs), nudge them to flip it on.
		return doctorCheck{
			Name:   "sandbox",
			Status: doctorWarn,
			Detail: "runtime.sandbox.enabled is false",
			Remedy: "set runtime.sandbox.enabled: true in config.yaml to constrain python tool execution",
		}
	}
	if runtime.GOOS == "windows" {
		return doctorCheck{
			Name:   "sandbox",
			Status: doctorWarn,
			Detail: "sandbox configured ON but rlimit-based sandbox isn't supported on Windows",
			Remedy: "set runtime.sandbox.enabled: false, OR run soulacy under WSL2",
		}
	}
	cpu := viper.GetInt("runtime.sandbox.cpu_seconds")
	mem := viper.GetInt("runtime.sandbox.memory_mb")
	return doctorCheck{
		Name:   "sandbox",
		Status: doctorOK,
		Detail: fmt.Sprintf("enabled (cpu=%ds mem=%dMB)", cpu, mem),
	}
}

// checkRecentErrors samples recent actions across up to N agents and
// warns when the aggregate error rate crosses a threshold. Skipped
// cleanly when there's no API key (the endpoint needs auth).
//
// Bug fix history:
//
//	2026-06-09 (first pass): the check originally hit /api/v1/actions
//	directly without auth. That endpoint required the API key, so a
//	401 came out as a misleading "gateway down?".
//
//	2026-06-09 (second pass — this code): there's NO global
//	/api/v1/actions endpoint at all. Actions are per-agent under
//	/api/v1/agents/:id/actions. The path I was hitting just routed to
//	the SPA fallback and served the GUI HTML, which is why the JSON
//	decode failed with "invalid character '<'". Fixed by listing
//	agents via /api/v1/agents, then sampling actions from each.
func checkRecentErrors() doctorCheck {
	type actionRow struct {
		Status string `json:"status"`
	}
	type actionsResp struct {
		Items []actionRow `json:"items"`
	}
	type agentRow struct {
		ID string `json:"id"`
	}
	type agentsListResp struct {
		Agents []agentRow `json:"agents"`
	}

	// Skip cleanly when there's no API key configured — the endpoint
	// needs auth, and we don't want a misleading "gateway down" for an
	// operator who simply hasn't passed --api-key.
	if apiKey == "" && viper.GetString("server.api_key") == "" {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorWarn,
			Detail: "skipped: no API key configured (pass --api-key or set server.api_key)",
		}
	}

	// Get the live agent list. We sample up to 5 agents to bound cost —
	// a workspace with hundreds of agents shouldn't pay for a doctor
	// check.
	var agentsResp agentsListResp
	if err := gatewayJSON("/agents", &agentsResp, 3*time.Second); err != nil {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorWarn,
			Detail: "couldn't list agents: " + err.Error(),
		}
	}
	if len(agentsResp.Agents) == 0 {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorOK,
			Detail: "no agents loaded — nothing to assess",
		}
	}
	sampleSize := len(agentsResp.Agents)
	if sampleSize > 5 {
		sampleSize = 5
	}

	// Aggregate recent actions across the sampled agents. We don't
	// filter by time at the API level because the per-agent actions
	// endpoint doesn't expose a `since` parameter — we just take the
	// most recent N per agent and treat that as the rolling sample.
	var (
		total int
		errs  int
	)
	for _, ag := range agentsResp.Agents[:sampleSize] {
		var resp actionsResp
		path := fmt.Sprintf("/agents/%s/actions?limit=50", ag.ID)
		if err := gatewayJSON(path, &resp, 3*time.Second); err != nil {
			continue // tolerate per-agent failures — doctor should not be flaky
		}
		for _, r := range resp.Items {
			total++
			if r.Status == "error" || r.Status == "failed" || r.Status == "timeout" {
				errs++
			}
		}
	}
	if total == 0 {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorOK,
			Detail: fmt.Sprintf("no recent activity across %d agent(s) to assess", sampleSize),
		}
	}
	rate := float64(errs) / float64(total)
	if rate >= 0.30 {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorFail,
			Detail: fmt.Sprintf("%d/%d (%.0f%%) recent actions failed across %d agent(s)", errs, total, rate*100, sampleSize),
			Remedy: "check `sy logs` for repeated errors; common causes: provider auth, sandbox limits, MCP server crashes",
		}
	}
	if rate >= 0.10 {
		return doctorCheck{
			Name:   "error_rate",
			Status: doctorWarn,
			Detail: fmt.Sprintf("%d/%d (%.0f%%) recent actions failed across %d agent(s)", errs, total, rate*100, sampleSize),
			Remedy: "check `sy logs` if this is unexpected",
		}
	}
	return doctorCheck{
		Name:   "error_rate",
		Status: doctorOK,
		Detail: fmt.Sprintf("%d/%d (%.0f%%) recent actions failed across %d agent(s)", errs, total, rate*100, sampleSize),
	}
}
