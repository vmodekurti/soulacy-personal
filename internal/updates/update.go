package updates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

const defaultGitHubRepo = "vmodekurti/soulacy"

const (
	maxUpdateManifestBytes  = 2 << 20
	maxUpdateArtifactBytes  = 1 << 30
	maxSigstoreBundleBytes  = 8 << 20
	githubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"
)

var HTTPClient = http.DefaultClient
var renameUpdateFile = os.Rename
var VerifySigstore = verifySigstoreBundle

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

func fetchLatestGitHubReleaseManifest(ctx context.Context, repo string) (UpdateManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	urlStr := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return UpdateManifest{}, err
	}
	// Add user-agent header as required by GitHub API
	req.Header.Set("User-Agent", "soulacy-updater")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return UpdateManifest{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No GitHub release has been published yet. Return a dummy manifest matching dev version.
		return UpdateManifest{
			Product: "soulacy",
			Version: "dev",
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return UpdateManifest{}, fmt.Errorf("github API: HTTP %d from %s", resp.StatusCode, urlStr)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return UpdateManifest{}, err
	}

	version := strings.TrimPrefix(rel.TagName, "v")
	manifest := UpdateManifest{
		Product: "soulacy",
		Version: version,
	}

	// Try to locate the checksums file first
	var checksumsURL string
	for _, asset := range rel.Assets {
		if asset.Name == "checksums.sha256" {
			checksumsURL = asset.BrowserDownloadURL
			break
		}
	}

	checksums := make(map[string]string)
	if checksumsURL != "" {
		if m, err := fetchAndParseChecksums(ctx, checksumsURL); err == nil {
			checksums = m
		}
	}

	for _, asset := range rel.Assets {
		if strings.HasSuffix(asset.Name, ".tar.gz") {
			// Extract OS/Arch from filename: soulacy_<version>_<os>_<arch>.tar.gz
			parts := strings.Split(strings.TrimSuffix(asset.Name, ".tar.gz"), "_")
			if len(parts) >= 4 {
				osName := parts[len(parts)-2]
				archName := parts[len(parts)-1]
				sha := checksums[asset.Name]
				manifest.Artifacts = append(manifest.Artifacts, UpdateArtifact{
					Name:   asset.Name,
					OS:     osName,
					Arch:   archName,
					Bytes:  asset.Size,
					SHA256: sha,
					URL:    asset.BrowserDownloadURL,
				})
			}
		}
	}

	return manifest, nil
}

func fetchAndParseChecksums(ctx context.Context, urlStr string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "soulacy-updater")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	checksums := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			checksums[parts[1]] = parts[0]
		}
	}
	return checksums, nil
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
	cosign, err := exec.LookPath("cosign")
	if err != nil {
		return fmt.Errorf("cosign is required for update verification: %w", err)
	}
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

func readUpdateArtifact(ctx context.Context, manifestSource string, artifact UpdateArtifact) ([]byte, string, error) {
	source, err := resolveUpdateArtifactSource(manifestSource, artifact)
	if err != nil {
		return nil, "", err
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, source, err
		}
		req.Header.Set("User-Agent", "soulacy-updater")
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return nil, source, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, source, fmt.Errorf("update artifact: HTTP %d from %s", resp.StatusCode, source)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
		return data, source, err
	}
	data, err := os.ReadFile(source)
	return data, source, err
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

func unpackUpdateArchive(data []byte) (map[string][]byte, error) {
	return unpackUpdateArchiveReader(bytes.NewReader(data))
}

func unpackUpdateArchiveReader(reader io.Reader) (map[string][]byte, error) {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("update archive: open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("update archive: read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size < 0 || hdr.Size > 512<<20 {
			return nil, fmt.Errorf("update archive: %s exceeds binary size limit", hdr.Name)
		}
		name := strings.TrimPrefix(filepath.Clean(hdr.Name), string(filepath.Separator))
		base := filepath.Base(name)
		if name == "." || strings.Contains(name, "..") || (base != "soulacy" && base != "sy") {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if err != nil {
			return nil, fmt.Errorf("update archive: read %s: %w", hdr.Name, err)
		}
		if int64(len(body)) != hdr.Size {
			return nil, fmt.Errorf("update archive: size mismatch for %s", hdr.Name)
		}
		files[base] = body
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
