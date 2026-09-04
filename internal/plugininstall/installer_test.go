package plugininstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/plugin"
)

const demoManifest = `
id: demo-plug
name: Demo Plug
description: test fixture
permissions:
  - cap: vector.search
    agents: [assistant]
credentials:
  - key: API_TOKEN
    from: demo-plug/api_token
`

func writeFixturePlugin(t *testing.T, manifest string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src-plugin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools.py"), []byte("# tools"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newInstaller(t *testing.T) *Installer {
	t.Helper()
	ins, err := New(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	return ins
}

func TestStageApproveLifecycle_LocalDir(t *testing.T) {
	src := writeFixturePlugin(t, demoManifest)
	ins := newInstaller(t)

	pv, err := ins.Stage(context.Background(), src, "")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if pv.PluginID != "demo-plug" || len(pv.Permissions) != 1 || len(pv.Credentials) != 1 {
		t.Fatalf("preview = %+v", pv)
	}
	if pv.Permissions[0].Cap != "vector.search" {
		t.Fatalf("preview caps = %+v", pv.Permissions)
	}

	id, err := ins.Approve(pv.StagedID, pv.Source, pv.Checksum)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if id != "demo-plug" {
		t.Fatalf("installed id = %q", id)
	}

	list, err := ins.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	got := list[0]
	if !got.Enabled || got.NeedsReapproval || got.ID != "demo-plug" {
		t.Fatalf("installed = %+v", got)
	}

	// double-install refused
	pv2, err := ins.Stage(context.Background(), src, "")
	if err != nil {
		t.Fatalf("restage: %v", err)
	}
	if _, err := ins.Approve(pv2.StagedID, src, ""); err == nil {
		t.Fatal("approving over an existing install must error")
	}
	_ = ins.Discard(pv2.StagedID)
}

func TestStagedPluginIsNotActive(t *testing.T) {
	src := writeFixturePlugin(t, demoManifest)
	ins := newInstaller(t)
	if _, err := ins.Stage(context.Background(), src, ""); err != nil {
		t.Fatalf("stage: %v", err)
	}
	// nothing installed until approval
	list, err := ins.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("staged plugin leaked into installed list: %v %+v", err, list)
	}
}

func TestEnableDisableRemove(t *testing.T) {
	src := writeFixturePlugin(t, demoManifest)
	ins := newInstaller(t)
	pv, _ := ins.Stage(context.Background(), src, "")
	if _, err := ins.Approve(pv.StagedID, src, ""); err != nil {
		t.Fatal(err)
	}

	if err := ins.SetEnabled("demo-plug", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	list, _ := ins.List()
	if list[0].Enabled {
		t.Fatal("still enabled after disable")
	}
	dir := filepath.Join(ins.root, "demo-plug")
	if v := Gate(dir, list[0].Permissions, list[0].Credentials); v.Load {
		t.Fatal("gate must refuse a disabled plugin")
	}

	if err := ins.SetEnabled("demo-plug", true); err != nil {
		t.Fatal(err)
	}
	if v := Gate(dir, list[0].Permissions, list[0].Credentials); !v.Load {
		t.Fatalf("gate must allow enabled+approved plugin: %s", v.Reason)
	}

	if err := ins.Remove("demo-plug"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if list, _ := ins.List(); len(list) != 0 {
		t.Fatalf("still listed after remove: %+v", list)
	}
}

func TestPermissionChangeRequiresReapproval(t *testing.T) {
	src := writeFixturePlugin(t, demoManifest)
	ins := newInstaller(t)
	pv, _ := ins.Stage(context.Background(), src, "")
	if _, err := ins.Approve(pv.StagedID, src, ""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ins.root, "demo-plug")

	// simulate an update that widens permissions
	widened := strings.Replace(demoManifest, "agents: [assistant]", "agents: ['*']", 1)
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(widened), 0644); err != nil {
		t.Fatal(err)
	}

	list, _ := ins.List()
	if !list[0].NeedsReapproval {
		t.Fatal("widened permissions must flag needs_reapproval")
	}
	if v := Gate(dir, list[0].Permissions, list[0].Credentials); v.Load {
		t.Fatal("gate must refuse until re-approved")
	}

	if err := ins.Reapprove("demo-plug"); err != nil {
		t.Fatalf("reapprove: %v", err)
	}
	list, _ = ins.List()
	if list[0].NeedsReapproval {
		t.Fatal("still flagged after reapproval")
	}
	if v := Gate(dir, list[0].Permissions, list[0].Credentials); !v.Load {
		t.Fatalf("gate must allow after reapproval: %s", v.Reason)
	}
}

func TestFingerprintOrderInsensitive(t *testing.T) {
	a := []plugin.Permission{{Cap: "a.b", Agents: []string{"y", "x"}}, {Cap: "c.d"}}
	b := []plugin.Permission{{Cap: "c.d"}, {Cap: "a.b", Agents: []string{"x", "y"}}}
	if Fingerprint(a, nil) != Fingerprint(b, nil) {
		t.Fatal("reordering must not change the fingerprint")
	}
	c := []plugin.Permission{{Cap: "a.b", Agents: []string{"x", "y", "z"}}, {Cap: "c.d"}}
	if Fingerprint(a, nil) == Fingerprint(c, nil) {
		t.Fatal("widening a scope must change the fingerprint")
	}
}

func TestGateHandInstalledAlwaysLoads(t *testing.T) {
	dir := t.TempDir() // no metadata file
	if v := Gate(dir, nil, nil); !v.Load {
		t.Fatal("hand-installed plugins must load unconditionally")
	}
}

func TestArchiveInstallVerifiesChecksum(t *testing.T) {
	src := writeFixturePlugin(t, demoManifest)
	arch := filepath.Join(t.TempDir(), "plug.tar.gz")
	makeTarGz(t, src, arch)
	sum := fileSHA256(t, arch)

	ins := newInstaller(t)
	// wrong checksum refused
	if _, err := ins.Stage(context.Background(), arch, strings.Repeat("0", 64)); err == nil {
		t.Fatal("bad checksum must refuse install")
	}
	// missing checksum refused
	if _, err := ins.Stage(context.Background(), arch, ""); err == nil {
		t.Fatal("archive without checksum must refuse install")
	}
	// correct checksum stages
	pv, err := ins.Stage(context.Background(), arch, sum)
	if err != nil {
		t.Fatalf("stage archive: %v", err)
	}
	if pv.PluginID != "demo-plug" {
		t.Fatalf("preview = %+v", pv)
	}
}

func TestArchivePathTraversalRefused(t *testing.T) {
	arch := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ := os.Create(arch)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("evil")
	_ = tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	tw.Close()
	gz.Close()
	f.Close()

	if err := extractTarGz(arch, t.TempDir()); err == nil {
		t.Fatal("path traversal must be refused")
	}
}

func TestGitInstall(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := writeFixturePlugin(t, demoManifest)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", ".")
	run("commit", "-q", "-m", "fixture")

	ins := newInstaller(t)
	pv, err := ins.Stage(context.Background(), src+"/.git", "")
	if err != nil {
		t.Fatalf("stage from git: %v", err)
	}
	if pv.PluginID != "demo-plug" {
		t.Fatalf("preview = %+v", pv)
	}
	// .git history stripped from the staged tree
	if _, err := os.Stat(filepath.Join(ins.stagePath(pv.StagedID), ".git")); !os.IsNotExist(err) {
		t.Fatal(".git must be stripped from the staged tree")
	}
}

// ── fixture helpers ─────────────────────────────────────────────────────────

func makeTarGz(t *testing.T, srcDir, out string) {
	t.Helper()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	err = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Story 17: the approval preview shows manifest-declared migrations so the
// operator approves schema alongside permissions.
func TestStagePreviewShowsDeclaredMigrations(t *testing.T) {
	root := t.TempDir()
	ins, err := New(filepath.Join(root, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `id: schema-plugin
name: Schema Plugin
migrations:
  - name: 001_items
    up_sql: CREATE TABLE plugin_schema_plugin_items (id INTEGER PRIMARY KEY)
`
	if err := os.WriteFile(filepath.Join(src, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	pv, err := ins.Stage(context.Background(), src, "")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(pv.Migrations) != 1 || pv.Migrations[0].Name != "001_items" {
		t.Errorf("preview migrations = %+v", pv.Migrations)
	}
	if !strings.Contains(pv.Migrations[0].UpSQL, "CREATE TABLE") {
		t.Errorf("up_sql missing: %+v", pv.Migrations[0])
	}
}
