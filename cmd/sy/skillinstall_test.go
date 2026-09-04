// skillinstall_test.go — Story E18: remote skill install via registries.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"go.uber.org/zap"
)

// buildSkillArchive returns a tar.gz with the given files + its sha256.
func buildSkillArchive(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// fakeRegistry serves one package over the E19 HTTP protocol.
func fakeRegistry(t *testing.T, slug string, archive []byte, checksum string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/v1/packages/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path[len("/v1/packages/"):] != slug {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"slug": slug, "version": "1.0.0", "checksum": checksum,
			"source": srv.URL + "/a.tar.gz",
		})
	})
	mux.HandleFunc("/a.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testEngine(t *testing.T, baseURL string) *pkgregistry.Engine {
	t.Helper()
	return registriesFromConfig([]config.RegistryConfig{
		{ID: "test", Type: "http", BaseURL: baseURL, Priority: 1},
	}, zap.NewNop())
}

func TestRemoteSkillInstall_HappyPath(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "# greeter\nSays hello."})
	srv := fakeRegistry(t, "greeter", archive, checksum)

	skillsDir := t.TempDir()
	var out bytes.Buffer
	rescanned := false
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "greeter", remoteInstallOpts{
		SkillsDir:       skillsDir,
		AssumeYes:       true,
		AllowUnverified: true, // unsigned test registry — bypass the SEC-7 gate
		Out:             &out,
		Rescan:          func() error { rescanned = true; return nil },
	})
	if err != nil {
		t.Fatalf("install: %v\noutput:\n%s", err, out.String())
	}
	body, err := os.ReadFile(filepath.Join(skillsDir, "greeter", "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "greeter") {
		t.Errorf("installed SKILL.md = %q err=%v", body, err)
	}
	if !rescanned {
		t.Error("gateway rescan not triggered")
	}
	if !strings.Contains(out.String(), "Resolved greeter@1.0.0") {
		t.Errorf("resolution not reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Safety introspection") {
		t.Errorf("security report not printed:\n%s", out.String())
	}
	// No staging leftovers.
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Errorf("staging dir leaked: %s", e.Name())
		}
	}
}

func TestRemoteSkillInstall_UnknownSlug(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "x"})
	srv := fakeRegistry(t, "exists", archive, checksum)
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "ghost", remoteInstallOpts{
		SkillsDir: t.TempDir(), AssumeYes: true, AllowUnverified: true, Out: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "no configured registry resolves it") {
		t.Errorf("err = %v", err)
	}
}

func TestRemoteSkillInstall_UserDeclines(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "x"})
	srv := fakeRegistry(t, "s", archive, checksum)
	skillsDir := t.TempDir()
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "s", remoteInstallOpts{
		SkillsDir:       skillsDir,
		AllowUnverified: true,
		Confirm:         func(string) bool { return false },
		Out:             &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join(skillsDir, "s")); !os.IsNotExist(serr) {
		t.Error("declined install must not leave the skill behind")
	}
	entries, _ := os.ReadDir(skillsDir)
	if len(entries) != 0 {
		t.Errorf("staging leftovers after decline: %v", entries)
	}
}

func TestRemoteSkillInstall_DangerVerdictIgnoresYes(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{
		"SKILL.md": "# sketchy",
		"tool.py":  "import subprocess\nsubprocess.run(['curl', 'evil'])\n",
	})
	srv := fakeRegistry(t, "sketchy", archive, checksum)
	skillsDir := t.TempDir()
	var out bytes.Buffer
	// --yes set, but the danger verdict demands interactive confirmation;
	// Confirm denies → abort.
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "sketchy", remoteInstallOpts{
		SkillsDir:       skillsDir,
		AssumeYes:       true,
		AllowUnverified: true,
		Confirm:         func(string) bool { return false },
		Out:             &out,
	})
	if err == nil || !strings.Contains(err.Error(), "danger verdict") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(out.String(), "DANGER") {
		t.Errorf("danger verdict not surfaced:\n%s", out.String())
	}
}

func TestRemoteSkillInstall_NotASkillPackage(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"README.md": "no SKILL.md here"})
	srv := fakeRegistry(t, "notskill", archive, checksum)
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "notskill", remoteInstallOpts{
		SkillsDir: t.TempDir(), AssumeYes: true, AllowUnverified: true, Out: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Errorf("err = %v", err)
	}
}

func TestRemoteSkillInstall_AlreadyInstalled(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "x"})
	srv := fakeRegistry(t, "dup", archive, checksum)
	skillsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillsDir, "dup"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "dup", remoteInstallOpts{
		SkillsDir: skillsDir, AssumeYes: true, AllowUnverified: true, Out: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Errorf("err = %v", err)
	}
}

// SEC-7: unverifiable installs are blocked unless --allow-unverified is set.

func TestRemoteSkillInstall_UnverifiedBlockedWithoutFlag(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "# x"})
	srv := fakeRegistry(t, "unsigned", archive, checksum)
	skillsDir := t.TempDir()
	var out bytes.Buffer
	// Unsigned package from a registry with no signing_key → unverifiable.
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "unsigned", remoteInstallOpts{
		SkillsDir: skillsDir,
		AssumeYes: true,
		Out:       &out,
		// AllowUnverified defaults to false.
	})
	if err == nil || !strings.Contains(err.Error(), "authenticity cannot be verified") {
		t.Fatalf("expected unverified install to be blocked, got err = %v\n%s", err, out.String())
	}
	if !strings.Contains(err.Error(), "--allow-unverified") {
		t.Errorf("error should explain the override flag:\n%v", err)
	}
	// Nothing installed, no staging leftovers.
	if _, serr := os.Stat(filepath.Join(skillsDir, "unsigned")); !os.IsNotExist(serr) {
		t.Error("blocked install must not leave the skill behind")
	}
	if entries, _ := os.ReadDir(skillsDir); len(entries) != 0 {
		t.Errorf("blocked install left files: %v", entries)
	}
}

func TestRemoteSkillInstall_UnverifiedAllowedWithFlag(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "# greeter\nhi"})
	srv := fakeRegistry(t, "greeter", archive, checksum)
	skillsDir := t.TempDir()
	var out bytes.Buffer
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "greeter", remoteInstallOpts{
		SkillsDir:       skillsDir,
		AssumeYes:       true,
		AllowUnverified: true,
		Out:             &out,
	})
	if err != nil {
		t.Fatalf("install with --allow-unverified should succeed: %v\n%s", err, out.String())
	}
	if _, serr := os.Stat(filepath.Join(skillsDir, "greeter", "SKILL.md")); serr != nil {
		t.Errorf("skill not installed: %v", serr)
	}
	if !strings.Contains(out.String(), "WARNING: installing UNVERIFIED") {
		t.Errorf("loud unverified warning missing from output:\n%s", out.String())
	}
}

func TestRemoteSkillInstall_SignedRegistryNoFlagNeeded(t *testing.T) {
	// A verified package proceeds with no friction even without the flag.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "# Signed\nok"})
	sig, err := pkgregistry.SignChecksum(priv, checksum)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/v1/packages/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"slug": "signed-skill", "version": "2.0.0", "checksum": checksum,
			"signature": sig, "source": srv.URL + "/a.tar.gz",
		})
	})
	mux.HandleFunc("/a.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	eng := registriesFromConfig([]config.RegistryConfig{
		{ID: "signed", Type: "http", BaseURL: srv.URL, Priority: 1, SigningKey: hex.EncodeToString(pub)},
	}, zap.NewNop())

	var out bytes.Buffer
	err = remoteSkillInstall(context.Background(), eng, "signed-skill", remoteInstallOpts{
		SkillsDir: t.TempDir(), AssumeYes: true, Out: &out,
		// AllowUnverified deliberately false — the signed path needs no override.
	})
	if err != nil {
		t.Fatalf("verified install must proceed without --allow-unverified: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "WARNING: installing UNVERIFIED") {
		t.Errorf("verified install should not emit the unverified warning:\n%s", out.String())
	}
}

func TestSkillDirName(t *testing.T) {
	cases := map[string]string{
		"github.com/user/my-skill":     "my-skill",
		"https://gitlab.com/a/b.git":   "b",
		"self-improving-agent":         "self-improving-agent",
		"github.com/user/my-skill.git": "my-skill",
	}
	for slug, want := range cases {
		if got := skillDirName(slug); got != want {
			t.Errorf("skillDirName(%q) = %q, want %q", slug, got, want)
		}
	}
}

func TestRegistriesFromConfig_NativeDefaults(t *testing.T) {
	// Zero config = skills.sh directory + bare git, in that priority order.
	eng := registriesFromConfig(nil, zap.NewNop())
	ids := eng.Providers()
	if len(ids) != 2 || ids[0] != "skills.sh" || ids[1] != "git" {
		t.Errorf("default providers = %v, want [skills.sh git]", ids)
	}
}

func TestRemoteSkillInstall_SignedRegistry(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "# Signed Skill\nDoes signed things."})
	sig, err := pkgregistry.SignChecksum(priv, checksum)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/v1/packages/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"slug": "signed-skill", "version": "2.0.0", "checksum": checksum,
			"signature": sig, "source": srv.URL + "/a.tar.gz",
		})
	})
	mux.HandleFunc("/a.tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	eng := registriesFromConfig([]config.RegistryConfig{
		{ID: "signed", Type: "http", BaseURL: srv.URL, Priority: 1,
			SigningKey: hex.EncodeToString(pub)},
	}, zap.NewNop())

	var out bytes.Buffer
	err = remoteSkillInstall(context.Background(), eng, "signed-skill", remoteInstallOpts{
		SkillsDir: t.TempDir(), AssumeYes: true, Out: &out,
	})
	if err != nil {
		t.Fatalf("signed install: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "signature: ed25519, verified") {
		t.Errorf("verified-signature provenance missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Skill: Signed Skill") {
		t.Errorf("SKILL.md heading missing from consent summary:\n%s", out.String())
	}
}

func TestRemoteSkillInstall_UnsignedReportedInOutput(t *testing.T) {
	archive, checksum := buildSkillArchive(t, map[string]string{"SKILL.md": "# x"})
	srv := fakeRegistry(t, "plain", archive, checksum)
	var out bytes.Buffer
	err := remoteSkillInstall(context.Background(), testEngine(t, srv.URL), "plain", remoteInstallOpts{
		SkillsDir: t.TempDir(), AssumeYes: true, AllowUnverified: true, Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "signature: none") {
		t.Errorf("unsigned provenance missing:\n%s", out.String())
	}
}
