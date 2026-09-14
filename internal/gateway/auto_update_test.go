package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/updates"
)

func boolp(b bool) *bool { return &b }

func TestDecideAutoUpdateModes(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.UpdateConfig
		env  autoUpdateEnv
		want string
	}{
		{"default installs", config.UpdateConfig{}, autoUpdateEnv{Writable: true}, updateModeInstall},
		{"disabled notifies", config.UpdateConfig{Auto: boolp(false)}, autoUpdateEnv{Writable: true}, updateModeOff},
		{"container notifies", config.UpdateConfig{}, autoUpdateEnv{InContainer: true, Writable: true}, updateModeNotify},
		{"read-only notifies", config.UpdateConfig{}, autoUpdateEnv{Writable: false}, updateModeNotify},
	}
	for _, c := range cases {
		mode, reason := decideAutoUpdate(c.cfg, c.env)
		if mode != c.want || reason == "" {
			t.Fatalf("%s: mode=%s reason=%q want %s", c.name, mode, reason, c.want)
		}
	}
}

// autoUpdateFixture wires fakes into the seams and a manifest server that
// advertises a newer release for this platform.
type autoUpdateFixture struct {
	installs, verifies, restores, restarts, exits atomic.Int32
	verifyErr                                     error
	installDir                                    string
}

func (f *autoUpdateFixture) counts() (installs, verifies, restores, restarts, exits int) {
	return int(f.installs.Load()), int(f.verifies.Load()), int(f.restores.Load()), int(f.restarts.Load()), int(f.exits.Load())
}

func setupAutoUpdate(t *testing.T, s *Server, env autoUpdateEnv) *autoUpdateFixture {
	t.Helper()
	f := &autoUpdateFixture{installDir: t.TempDir()}
	oldInstall, oldVerify, oldRestore, oldRestart, oldExit := autoInstall, autoVerifyBinary, autoRestoreBackups, autoRestart, autoExit
	oldContainer, oldWritable, oldDir := autoInContainer, autoInstallDirWritable, autoInstallDir
	autoInstall = func(_ context.Context, opts updates.UpdateInstallOptions) (updates.UpdateInstallResult, error) {
		f.installs.Add(1)
		check, err := updates.CheckForUpdate(context.Background(), opts.ManifestSource, "")
		if err != nil {
			return updates.UpdateInstallResult{}, err
		}
		return updates.UpdateInstallResult{UpdateCheckResult: check, Installed: true, InstallDir: f.installDir, Backups: []string{f.installDir + "/soulacy.bak-1"}}, nil
	}
	autoVerifyBinary = func(_ context.Context, _, _ string) error { f.verifies.Add(1); return f.verifyErr }
	autoRestoreBackups = func(_ []string) error { f.restores.Add(1); return nil }
	autoRestart = func() error { f.restarts.Add(1); return nil }
	autoExit = func() { f.exits.Add(1) }
	autoInContainer = func() bool { return env.InContainer }
	autoInstallDirWritable = func(string) bool { return env.Writable }
	autoInstallDir = func(string) (string, error) { return f.installDir, nil }
	t.Cleanup(func() {
		autoInstall, autoVerifyBinary, autoRestoreBackups, autoRestart, autoExit = oldInstall, oldVerify, oldRestore, oldRestart, oldExit
		autoInContainer, autoInstallDirWritable, autoInstallDir = oldContainer, oldWritable, oldDir
		globalUpdates = newUpdatesManager()
	})
	globalUpdates = newUpdatesManager()
	ts := newerReleaseServer(t)
	oldClient := updates.HTTPClient
	updates.HTTPClient = ts.Client()
	t.Cleanup(func() { updates.HTTPClient = oldClient })
	s.cfg.Updates.ManifestURL = ts.URL
	s.cfg.Updates.CheckInterval = "1h"
	return f
}

func newerReleaseServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"product":"soulacy","version":"99.0.0","artifacts":[` +
			`{"name":"soulacy_99.0.0_darwin_arm64.tar.gz","os":"darwin","arch":"arm64","sha256":"aa"},` +
			`{"name":"soulacy_99.0.0_darwin_amd64.tar.gz","os":"darwin","arch":"amd64","sha256":"aa"},` +
			`{"name":"soulacy_99.0.0_linux_amd64.tar.gz","os":"linux","arch":"amd64","sha256":"aa"},` +
			`{"name":"soulacy_99.0.0_linux_arm64.tar.gz","os":"linux","arch":"arm64","sha256":"aa"}]}`))
	}))
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := config.Version
	config.Version = v
	t.Cleanup(func() { config.Version = old })
}

func TestAutoUpdateInstallsVerifiesAndRestartsWhenIdle(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	withVersion(t, "1.0.0")
	f := setupAutoUpdate(t, s, autoUpdateEnv{Writable: true})
	next := s.runUpdateCycle(context.Background())
	if i, v, rs, r, _ := f.counts(); i != 1 || v != 1 || r != 1 || rs != 0 {
		t.Fatalf("installs=%d verifies=%d restarts=%d restores=%d", i, v, r, rs)
	}
	if next != time.Hour {
		t.Fatalf("next cycle should be the configured interval, got %s", next)
	}
	time.Sleep(400 * time.Millisecond)
	if _, _, _, _, e := f.counts(); e != 1 {
		t.Fatal("process should exit after spawning the replacement")
	}
	globalUpdates.RLock()
	defer globalUpdates.RUnlock()
	if globalUpdates.state.LastAppliedVersion != "99.0.0" || globalUpdates.pendingRestart != "" {
		t.Fatalf("state not recorded: %+v pending=%q", globalUpdates.state, globalUpdates.pendingRestart)
	}
}

func TestAutoUpdateDefersRestartWhileRunsAreActive(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	withVersion(t, "1.0.0")
	f := setupAutoUpdate(t, s, autoUpdateEnv{Writable: true})
	s.runReg.Register("run-1", func() {})
	next := s.runUpdateCycle(context.Background())
	if i, _, _, r, _ := f.counts(); i != 1 || r != 0 {
		t.Fatalf("expected install without restart, installs=%d restarts=%d", i, r)
	}
	if next != pendingRestartRetry {
		t.Fatalf("should retry restart soon, got %s", next)
	}
	// Second cycle: still busy, still deferred, and no second download.
	if s.runUpdateCycle(context.Background()) != pendingRestartRetry || f.installs.Load() != 1 {
		t.Fatal("busy gateway must keep deferring without reinstalling")
	}
	// Runs finish: restart proceeds.
	s.runReg.Done("run-1")
	if s.runUpdateCycle(context.Background()) != time.Hour || f.restarts.Load() != 1 {
		t.Fatalf("restart should proceed once idle, restarts=%d", f.restarts.Load())
	}
	// Idle-wait exhaustion forces the restart even when busy.
	globalUpdates = newUpdatesManager()
	f.restarts.Store(0)
	s.cfg.Updates.IdleWait = "0s"
	s.runReg.Register("run-2", func() {})
	s.runUpdateCycle(context.Background())
	if f.restarts.Load() != 1 {
		t.Fatal("zero idle wait should restart immediately")
	}
}

func TestAutoUpdateOnlyNotifiesInContainerOrWhenDisabled(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	withVersion(t, "1.0.0")
	f := setupAutoUpdate(t, s, autoUpdateEnv{InContainer: true, Writable: true})
	s.runUpdateCycle(context.Background())
	if f.installs.Load() != 0 || f.restarts.Load() != 0 {
		t.Fatal("container must never install in place")
	}
	globalUpdates.RLock()
	avail, mode := globalUpdates.status.UpdateAvailable, globalUpdates.mode
	globalUpdates.RUnlock()
	if !avail || mode != updateModeNotify {
		t.Fatalf("should still report the release: available=%v mode=%s", avail, mode)
	}
	s.cfg.Updates.Auto = boolp(false)
	autoInContainer = func() bool { return false }
	s.runUpdateCycle(context.Background())
	if f.installs.Load() != 0 {
		t.Fatal("auto=false must never install")
	}
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/system/updates/status", "secret", "")
	if status != 200 || body["mode"] != updateModeOff || body["auto_enabled"] != false || body["latest_version"] != "99.0.0" {
		t.Fatalf("status: %d %+v", status, body)
	}
}

func TestAutoUpdateRollsBackWhenNewBinaryFailsVerification(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	withVersion(t, "1.0.0")
	f := setupAutoUpdate(t, s, autoUpdateEnv{Writable: true})
	f.verifyErr = errors.New("exec format error")
	s.runUpdateCycle(context.Background())
	if i, _, rs, r, _ := f.counts(); i != 1 || rs != 1 || r != 0 {
		t.Fatalf("expected rollback and no restart: installs=%d restores=%d restarts=%d", i, rs, r)
	}
	globalUpdates.RLock()
	defer globalUpdates.RUnlock()
	if globalUpdates.state.LastError == "" || globalUpdates.state.LastAppliedVersion != "" {
		t.Fatalf("failure must be recorded and nothing marked applied: %+v", globalUpdates.state)
	}
}

func TestAutoUpdateSkipsWhenCurrent(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	withVersion(t, "99.0.0")
	f := setupAutoUpdate(t, s, autoUpdateEnv{Writable: true})
	s.runUpdateCycle(context.Background())
	if f.installs.Load() != 0 {
		t.Fatal("no install when already current")
	}
}

func TestConfigPatchUpdatesPolicyIsHotApplied(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.cfgPath = t.TempDir() + "/config.yaml"
	globalUpdates = newUpdatesManager()
	status, _ := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "secret", `{"updates":{"auto":false,"check_interval":"30m"}}`)
	if status != 200 {
		t.Fatalf("patch: %d", status)
	}
	if s.cfg.Updates.AutoOn() || s.cfg.Updates.CheckIntervalDuration() != 30*time.Minute {
		t.Fatalf("policy not applied: %+v", s.cfg.Updates)
	}
	select {
	case <-globalUpdates.wake:
	default:
		t.Fatal("checker should have been woken")
	}
}
