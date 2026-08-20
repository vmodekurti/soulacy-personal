package updates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/safearchive"
)

type UpdateManifest struct {
	Product   string           `json:"product"`
	Version   string           `json:"version"`
	Artifacts []UpdateArtifact `json:"artifacts"`
}

type UpdateArtifact struct {
	Name      string `json:"name"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	URL       string `json:"url,omitempty"`
	BundleURL string `json:"bundle_url,omitempty"`
}

type UpdateCheckResult struct {
	CurrentVersion  string          `json:"current_version"`
	LatestVersion   string          `json:"latest_version,omitempty"`
	UpdateAvailable bool            `json:"update_available"`
	Comparable      bool            `json:"comparable"`
	ManifestSource  string          `json:"manifest_source,omitempty"`
	Artifact        *UpdateArtifact `json:"artifact,omitempty"`
	Message         string          `json:"message"`
}

type UpdateInstallOptions struct {
	ManifestSource string
	CurrentVersion string
	InstallDir     string
	DryRun         bool
	Yes            bool
}

type UpdateInstallResult struct {
	UpdateCheckResult
	Installed  bool     `json:"installed"`
	DryRun     bool     `json:"dry_run,omitempty"`
	InstallDir string   `json:"install_dir,omitempty"`
	Backups    []string `json:"backups,omitempty"`
}

const defaultGitHubRepo = "vmodekurti/soulacy"

const (
	maxUpdateManifestBytes  = 2 << 20
	maxUpdateArtifactBytes  = 1 << 30
	maxSigstoreBundleBytes  = 8 << 20
	maxCosignBinaryBytes    = 256 << 20
	githubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"
	cosignBootstrapVersion  = "v3.1.3"
)

var HTTPClient = http.DefaultClient
var renameUpdateFile = os.Rename
var VerifySigstore = verifySigstoreBundle
var findCosign = exec.LookPath
var cosignReleaseBaseURL = "https://github.com/sigstore/cosign/releases/download/" + cosignBootstrapVersion
var cosignCacheRoot = defaultCosignCacheRoot

var cosignBootstrapSHA256 = map[string]string{
	"darwin/amd64": "2347488e5d5b25336644024dfeca5601b190e91197a71a917bda44744aff106c",
	"darwin/arm64": "5cf948c2f4dfe59687bdd0b8523709067383e03982cc543475c8a7dc70e92a76",
	"linux/amd64":  "4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71",
	"linux/arm64":  "c5d324e091826b0d7a78eb16fef316450b4eb9aaec045611c08ba06f5e73220a",
}

func CheckForUpdate(ctx context.Context, manifestSource, currentVersion string) (UpdateCheckResult, error) {
	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		currentVersion = strings.TrimSpace(config.Version)
	}

	res := UpdateCheckResult{
		CurrentVersion: currentVersion,
		ManifestSource: manifestSource,
	}

	var manifest UpdateManifest
	var err error

	if manifestSource == "" {
		manifestSource = fmt.Sprintf("https://github.com/%s/releases/latest/download/release-manifest.json", defaultGitHubRepo)
		res.ManifestSource = manifestSource
	}
	manifest, err = readUpdateManifest(ctx, manifestSource)
	if err != nil {
		return res, err
	}

	res.LatestVersion = strings.TrimSpace(manifest.Version)
	res.Artifact = artifactForCurrentPlatform(manifest.Artifacts)
	cmp := CompareSemver(res.LatestVersion, currentVersion)
	res.Comparable = semverComparable(res.LatestVersion, currentVersion)
	res.UpdateAvailable = res.Comparable && cmp > 0

	switch {
	case !res.Comparable:
		res.Message = fmt.Sprintf("Soulacy %s is installed. Latest release version is %s, but versions are not comparable.", currentVersion, res.LatestVersion)
	case res.UpdateAvailable:
		res.Message = fmt.Sprintf("Update available: Soulacy %s -> %s.", currentVersion, res.LatestVersion)
	case cmp < 0:
		res.Message = fmt.Sprintf("Soulacy %s is newer than release version %s.", currentVersion, res.LatestVersion)
	default:
		res.Message = fmt.Sprintf("Soulacy %s is current.", currentVersion)
	}

	if res.UpdateAvailable && res.Artifact == nil {
		res.Message += fmt.Sprintf(" No %s/%s artifact was found in the release.", runtime.GOOS, runtime.GOARCH)
	}

	return res, nil
}

func InstallUpdate(ctx context.Context, opts UpdateInstallOptions) (UpdateInstallResult, error) {
	check, err := CheckForUpdate(ctx, opts.ManifestSource, opts.CurrentVersion)
	if err != nil {
		return UpdateInstallResult{}, err
	}
	res := UpdateInstallResult{UpdateCheckResult: check, DryRun: opts.DryRun}
	if !check.UpdateAvailable {
		res.Message = check.Message
		return res, nil
	}
	if check.Artifact == nil {
		return res, fmt.Errorf("update install: no %s/%s artifact found", runtime.GOOS, runtime.GOARCH)
	}
	if !opts.DryRun && !opts.Yes {
		return res, fmt.Errorf("update install: pass --yes to confirm or --dry-run to verify only")
	}
	installDir, err := resolveUpdateInstallDir(opts.InstallDir)
	if err != nil {
		return res, err
	}
	res.InstallDir = installDir

	if err := validateArtifactIdentity(check.LatestVersion, *check.Artifact); err != nil {
		return res, err
	}
	if err := verifySignedSource(ctx, check.ManifestSource, check.LatestVersion, maxUpdateManifestBytes); err != nil {
		return res, fmt.Errorf("update manifest signature: %w", err)
	}
	artifactPath, source, cleanup, err := downloadUpdateArtifact(ctx, check.ManifestSource, *check.Artifact)
	if err != nil {
		return res, err
	}
	defer cleanup()
	if err := verifyUpdateArtifactFile(*check.Artifact, artifactPath); err != nil {
		return res, err
	}
	if err := verifySignedFile(ctx, source, artifactPath, check.LatestVersion); err != nil {
		return res, fmt.Errorf("update artifact signature: %w", err)
	}
	files, err := unpackUpdateArchiveFile(artifactPath)
	if err != nil {
		return res, err
	}
	if _, ok := files["soulacy"]; !ok {
		return res, fmt.Errorf("update install: archive %s does not contain soulacy binary", check.Artifact.Name)
	}
	if _, ok := files["sy"]; !ok {
		return res, fmt.Errorf("update install: archive %s does not contain sy binary", check.Artifact.Name)
	}
	if opts.DryRun {
		res.Message = fmt.Sprintf("Update verified: Soulacy %s -> %s from %s. Dry run only; no files replaced.", check.CurrentVersion, check.LatestVersion, source)
		return res, nil
	}
	backups, err := installUpdateFiles(installDir, files)
	if err != nil {
		return res, err
	}
	res.Backups = backups
	res.Installed = true
	res.Message = fmt.Sprintf("Updated Soulacy %s -> %s in %s.", check.CurrentVersion, check.LatestVersion, installDir)
	return res, nil
}

func readUpdateManifest(ctx context.Context, source string) (UpdateManifest, error) {
	data, err := readUpdateManifestBytes(ctx, source)
	if err != nil {
		return UpdateManifest{}, err
	}
	var manifest UpdateManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return UpdateManifest{}, fmt.Errorf("update manifest: invalid JSON: %w", err)
	}
	if strings.TrimSpace(manifest.Product) != "" && !strings.EqualFold(manifest.Product, "soulacy") {
		return UpdateManifest{}, fmt.Errorf("update manifest: unexpected product %q", manifest.Product)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return UpdateManifest{}, fmt.Errorf("update manifest: version is required")
	}
	return manifest, nil
}

func readUpdateManifestBytes(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") {
		return nil, fmt.Errorf("update manifest: remote source must use HTTPS")
	}
	if strings.HasPrefix(source, "https://") {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "soulacy-updater")
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("update manifest: HTTP %d from %s", resp.StatusCode, source)
		}
		return readLimited(resp.Body, maxUpdateManifestBytes, "update manifest")
	}
	return os.ReadFile(source)
}

func readLimited(r io.Reader, limit int64, label string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d byte limit", label, limit)
	}
	return data, nil
}

func validateArtifactIdentity(version string, artifact UpdateArtifact) error {
	want := fmt.Sprintf("soulacy_%s_%s_%s.tar.gz", version, artifact.OS, artifact.Arch)
	if artifact.Name != want {
		return fmt.Errorf("update artifact: name %q does not bind product/version/platform (want %q)", artifact.Name, want)
	}
	return nil
}

func expectedWorkflowIdentity(version string) string {
	tag := strings.TrimSpace(version)
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return "https://github.com/" + defaultGitHubRepo + "/.github/workflows/release.yml@refs/tags/" + tag
}

func verifySigstoreBundle(ctx context.Context, artifactPath, bundlePath, identity string) error {
	cosign, cleanup, err := resolveCosignVerifier(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, cosign, "verify-blob",
		"--bundle", bundlePath,
		"--certificate-identity", identity,
		"--certificate-oidc-issuer", githubActionsOIDCIssuer,
		artifactPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cosign verification failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func resolveCosignVerifier(ctx context.Context) (string, func(), error) {
	if path, err := findCosign("cosign"); err == nil {
		return path, func() {}, nil
	}

	platform := runtime.GOOS + "/" + runtime.GOARCH
	want, ok := cosignBootstrapSHA256[platform]
	if !ok {
		return "", func() {}, fmt.Errorf(
			"cosign is not installed and automatic verifier bootstrap is unsupported on %s; install cosign and retry",
			platform,
		)
	}
	name := "cosign-" + runtime.GOOS + "-" + runtime.GOARCH
	cacheRoot, err := cosignCacheRoot()
	if err == nil {
		cached := filepath.Join(cacheRoot, cosignBootstrapVersion, name)
		if verifyFileSHA256(cached, want) == nil {
			if chmodErr := os.Chmod(cached, 0o700); chmodErr == nil {
				return cached, func() {}, nil
			}
		}
	}
	source := strings.TrimRight(cosignReleaseBaseURL, "/") + "/" + name
	path, cleanup, err := downloadUpdateSource(ctx, source, maxCosignBinaryBytes)
	if err != nil {
		return "", func() {}, fmt.Errorf("bootstrap cosign %s: %w", cosignBootstrapVersion, err)
	}
	if err := verifyFileSHA256(path, want); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("bootstrap cosign %s: %w", cosignBootstrapVersion, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("bootstrap cosign %s: make verifier executable: %w", cosignBootstrapVersion, err)
	}
	if cacheRoot != "" {
		cached, cacheErr := cacheCosignVerifier(cacheRoot, name, path)
		if cacheErr == nil {
			cleanup()
			return cached, func() {}, nil
		}
	}
	return path, cleanup, nil
}

func defaultCosignCacheRoot() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "soulacy", "verification"), nil
}

func cacheCosignVerifier(cacheRoot, name, source string) (string, error) {
	dir := filepath.Join(cacheRoot, cosignBootstrapVersion)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	staged, err := os.CreateTemp(dir, ".cosign-*")
	if err != nil {
		return "", err
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()
	input, err := os.Open(source)
	if err != nil {
		_ = staged.Close()
		return "", err
	}
	_, copyErr := io.Copy(staged, input)
	closeInputErr := input.Close()
	closeErr := staged.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeInputErr != nil {
		return "", closeInputErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Chmod(stagedPath, 0o700); err != nil {
		return "", err
	}
	destination := filepath.Join(dir, name)
	if err := os.Rename(stagedPath, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func verifyFileSHA256(path, want string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(want)) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}

func verifySignedSource(ctx context.Context, source, version string, maxSourceBytes int64) error {
	path, cleanup, err := downloadUpdateSource(ctx, source, maxSourceBytes)
	if err != nil {
		return err
	}
	defer cleanup()
	return verifySignedFile(ctx, source, path, version)
}

func verifySignedFile(ctx context.Context, source, path, version string) error {
	bundleSource := source + ".cosign.bundle"
	bundlePath, cleanup, err := downloadUpdateSource(ctx, bundleSource, maxSigstoreBundleBytes)
	if err != nil {
		return fmt.Errorf("load Sigstore bundle: %w", err)
	}
	defer cleanup()
	return VerifySigstore(ctx, path, bundlePath, expectedWorkflowIdentity(version))
}

func downloadUpdateSource(ctx context.Context, source string, limit int64) (string, func(), error) {
	if strings.HasPrefix(source, "http://") {
		return "", func() {}, fmt.Errorf("remote update source must use HTTPS: %s", source)
	}
	if !strings.HasPrefix(source, "https://") {
		if !filepath.IsAbs(source) {
			abs, err := filepath.Abs(source)
			if err != nil {
				return "", func() {}, err
			}
			source = abs
		}
		return source, func() {}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", func() {}, err
	}
	req.Header.Set("User-Agent", "soulacy-updater")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return "", func() {}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", func() {}, fmt.Errorf("HTTP %d from %s", resp.StatusCode, source)
	}
	if resp.ContentLength > limit {
		return "", func() {}, fmt.Errorf("update source exceeds %d byte limit", limit)
	}
	tmp, err := os.CreateTemp("", "soulacy-update-verify-*")
	if err != nil {
		return "", func() {}, err
	}
	path := tmp.Name()
	cleanup := func() { _ = os.Remove(path) }
	n, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, limit+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || n > limit {
		cleanup()
		if copyErr != nil {
			return "", func() {}, copyErr
		}
		if closeErr != nil {
			return "", func() {}, closeErr
		}
		return "", func() {}, fmt.Errorf("update source exceeds %d byte limit", limit)
	}
	return path, cleanup, nil
}

func downloadUpdateArtifact(ctx context.Context, manifestSource string, artifact UpdateArtifact) (string, string, func(), error) {
	source, err := resolveUpdateArtifactSource(manifestSource, artifact)
	if err != nil {
		return "", "", func() {}, err
	}
	path, cleanup, err := downloadUpdateSource(ctx, source, maxUpdateArtifactBytes)
	return path, source, cleanup, err
}

func resolveUpdateArtifactSource(manifestSource string, artifact UpdateArtifact) (string, error) {
	raw := strings.TrimSpace(artifact.URL)
	if raw == "" {
		raw = strings.TrimSpace(artifact.Name)
	}
	if raw == "" {
		return "", fmt.Errorf("update artifact: url or name is required")
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") || filepath.IsAbs(raw) {
		return raw, nil
	}
	if strings.HasPrefix(manifestSource, "http://") || strings.HasPrefix(manifestSource, "https://") {
		base, err := url.Parse(manifestSource)
		if err != nil {
			return "", err
		}
		ref, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		return base.ResolveReference(ref).String(), nil
	}
	if manifestSource == "" || manifestSource == "github-releases" {
		return raw, nil
	}
	return filepath.Join(filepath.Dir(manifestSource), raw), nil
}

func verifyUpdateArtifact(artifact UpdateArtifact, data []byte) error {
	if artifact.Bytes > 0 && int64(len(data)) != artifact.Bytes {
		return fmt.Errorf("update artifact: byte size mismatch for %s: got %d want %d", artifact.Name, len(data), artifact.Bytes)
	}
	want := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("update artifact: valid sha256 is required for %s", artifact.Name)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("update artifact: malformed sha256 for %s: %w", artifact.Name, err)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("update artifact: sha256 mismatch for %s: got %s want %s", artifact.Name, got, want)
	}
	return nil
}

func verifyUpdateArtifactFile(artifact UpdateArtifact, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if artifact.Bytes > 0 && info.Size() != artifact.Bytes {
		return fmt.Errorf("update artifact: byte size mismatch for %s: got %d want %d", artifact.Name, info.Size(), artifact.Bytes)
	}
	want := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("update artifact: valid sha256 is required for %s", artifact.Name)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("update artifact: malformed sha256 for %s: %w", artifact.Name, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != want {
		return fmt.Errorf("update artifact: sha256 mismatch for %s: got %s want %s", artifact.Name, got, want)
	}
	return nil
}

func unpackUpdateArchiveFile(path string) (map[string][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return unpackUpdateArchiveReader(file)
}

// unpackUpdateArchiveReader pulls the two release binaries out of an update
// tarball.
//
// The selection and the bounds moved to internal/safearchive, which is now the
// only place in this repo that opens somebody else's archive. What this file
// had was a per-entry cap of 512 MiB and nothing else: no cumulative bound, so
// an archive of ten thousand entries all named `soulacy` allocated half a
// gigabyte ten thousand times in sequence, and no entry-count bound at all.
//
// The archive's checksum is verified against the signed manifest BEFORE this
// runs, so an attacker who reaches here has already replaced the release. That
// is exactly the case worth bounding: the guard that only matters when
// something upstream has already failed is the one nobody notices is missing.
func unpackUpdateArchiveReader(reader io.Reader) (map[string][]byte, error) {
	files, err := safearchive.SelectTarGz(reader,
		map[string]bool{"soulacy": true, "sy": true},
		safearchive.Limits{
			// A release binary is tens of megabytes; 512 MiB is the historic
			// ceiling, kept so a legitimately large future build is not
			// refused by a number nobody revisited.
			MaxEntryBytes: 512 << 20,
			MaxTotalBytes: 1 << 30,
			MaxEntries:    10_000,
		})
	if err != nil {
		return nil, fmt.Errorf("update archive: %w", err)
	}
	return files, nil
}

func installUpdateFiles(installDir string, files map[string][]byte) ([]string, error) {
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return nil, err
	}
	names := []string{"soulacy", "sy"}
	staged := make(map[string]string, len(names))
	cleanupStaged := func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}
	// Stage and verify every binary before moving either live destination.
	for _, name := range names {
		data, ok := files[name]
		if !ok || len(data) == 0 {
			cleanupStaged()
			return nil, fmt.Errorf("update install: missing or empty %s binary", name)
		}
		tmp, err := os.CreateTemp(installDir, "."+name+".update-*")
		if err != nil {
			cleanupStaged()
			return nil, err
		}
		path := tmp.Name()
		staged[name] = path
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			cleanupStaged()
			return nil, err
		}
		if err := tmp.Chmod(0o755); err != nil {
			_ = tmp.Close()
			cleanupStaged()
			return nil, err
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			cleanupStaged()
			return nil, err
		}
		if err := tmp.Close(); err != nil {
			cleanupStaged()
			return nil, err
		}
		stagedData, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(stagedData, data) {
			cleanupStaged()
			return nil, fmt.Errorf("update install: staged %s verification failed", name)
		}
	}

	stamp := time.Now().UTC().Format("20060102T150405Z")
	backups := []string{}
	backupByName := make(map[string]string, len(names))
	installed := make(map[string]bool, len(names))
	restore := func() {
		for _, name := range names {
			dest := filepath.Join(installDir, name)
			if installed[name] {
				_ = os.Remove(dest)
			}
			if backup := backupByName[name]; backup != "" {
				_ = os.Remove(dest)
				_ = renameUpdateFile(backup, dest)
			}
		}
	}
	for _, name := range names {
		dest := filepath.Join(installDir, name)
		if st, err := os.Stat(dest); err == nil && st.Mode().IsRegular() {
			backup := fmt.Sprintf("%s.bak-%s", dest, stamp)
			if err := renameUpdateFile(dest, backup); err != nil {
				restore()
				cleanupStaged()
				return backups, err
			}
			backupByName[name] = backup
			backups = append(backups, backup)
		}
	}
	for _, name := range names {
		dest := filepath.Join(installDir, name)
		if err := renameUpdateFile(staged[name], dest); err != nil {
			restore()
			cleanupStaged()
			return nil, fmt.Errorf("update install: replace %s: %w", name, err)
		}
		installed[name] = true
		delete(staged, name)
	}
	return backups, nil
}

func artifactForCurrentPlatform(artifacts []UpdateArtifact) *UpdateArtifact {
	for i := range artifacts {
		if artifacts[i].OS == runtime.GOOS && artifacts[i].Arch == runtime.GOARCH {
			return &artifacts[i]
		}
	}
	return nil
}

func semverComparable(a, b string) bool {
	_, oka := SemverParts(a)
	_, okb := SemverParts(b)
	return oka && okb
}

func CompareSemver(a, b string) int {
	if a == b {
		return 0
	}
	pa, oka := SemverParts(a)
	pb, okb := SemverParts(b)
	if !oka || !okb {
		return 0
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func SemverParts(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	segs := strings.Split(v, ".")
	var out [3]int
	if len(segs) == 0 || segs[0] == "" {
		return out, false
	}
	for i := 0; i < 3 && i < len(segs); i++ {
		n, err := strconv.Atoi(segs[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func resolveUpdateInstallDir(explicit string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return filepath.Abs(explicit)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update install: resolve current executable: %w", err)
	}
	return filepath.Dir(exe), nil
}

// DefaultSourceDescription names where updates come from when no manifest URL is
// configured. Exported so the gateway's readiness and deployment checks describe
// the SAME source the CLI actually uses, instead of reporting the default as an
// unconfigured gap.
func DefaultSourceDescription() string {
	return "GitHub releases for " + defaultGitHubRepo
}
