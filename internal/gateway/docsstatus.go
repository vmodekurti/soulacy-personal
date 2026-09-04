package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type publicDocsReadiness struct {
	Status                string   `json:"status"`
	Score                 int      `json:"score"`
	SiteURL               string   `json:"site_url,omitempty"`
	Checks                []string `json:"checks"`
	Missing               []string `json:"missing,omitempty"`
	Detail                string   `json:"detail"`
	ScreenshotGeneratedAt string   `json:"screenshot_generated_at,omitempty"`
	ScreenshotAgeHours    int      `json:"screenshot_age_hours,omitempty"`
	NextActions           []string `json:"next_actions,omitempty"`
}

func (s *Server) publicDocsReadiness() publicDocsReadiness {
	root, ok := findRepoRootForDocs()
	if !ok {
		return publicDocsReadiness{
			Status: "warn",
			Score:  65,
			Detail: "Source docs were not found from this runtime. Verify the docs workflow from the source repository before launch.",
			NextActions: []string{
				"Run `make docs-build` from the source checkout.",
				"Confirm `.github/workflows/docs.yml` deploys the MkDocs site.",
			},
		}
	}

	var checks, missing []string
	checkFile := func(rel, label string) {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			checks = append(checks, label)
		} else {
			missing = append(missing, label)
		}
	}
	checkFile("mkdocs.yml", "MkDocs configuration")
	checkFile("docs/index.md", "public docs index")
	checkFile("docs/install.sh", "published installer copy")
	checkFile(".github/workflows/docs.yml", "GitHub Pages deploy workflow")
	checkFile("docs/assets/screenshots/manifest.json", "GUI screenshot manifest")
	checkFile("docs/assets/screenshots/index.md", "GUI screenshot gallery")

	mkdocs := readText(filepath.Join(root, "mkdocs.yml"))
	workflow := readText(filepath.Join(root, ".github/workflows/docs.yml"))
	makefile := readText(filepath.Join(root, "Makefile"))
	installer := readText(filepath.Join(root, "docs", "install.sh"))
	manifest := readText(filepath.Join(root, "docs", "assets", "screenshots", "manifest.json"))
	screenshotGeneratedAt, screenshotAgeHours, screenshotFresh := screenshotManifestFreshness(manifest, 14*24*time.Hour)

	siteURL := docsSetting(mkdocs, "site_url:")
	if siteURL != "" {
		checks = append(checks, "public site URL")
	} else {
		missing = append(missing, "public site URL")
	}
	if strings.Contains(workflow, "make docs-build") && strings.Contains(workflow, "gh-deploy") {
		checks = append(checks, "strict docs deploy")
	} else {
		missing = append(missing, "strict docs deploy")
	}
	if strings.Contains(makefile, "mkdocs build --strict") {
		checks = append(checks, "strict local docs build")
	} else {
		missing = append(missing, "strict local docs build")
	}
	if strings.Contains(makefile, "cp install.sh docs/install.sh") && strings.Contains(installer, "Soulacy installer") {
		checks = append(checks, "published installer uses canonical installer")
	} else {
		missing = append(missing, "published installer uses canonical installer")
	}
	if strings.Contains(makefile, "docs-screenshots") && strings.Contains(manifest, `"routes"`) {
		checks = append(checks, "repeatable GUI screenshot evidence")
	} else {
		missing = append(missing, "repeatable GUI screenshot evidence")
	}
	if screenshotFresh {
		checks = append(checks, "fresh GUI screenshot evidence")
	} else {
		missing = append(missing, "fresh GUI screenshot evidence")
	}

	status, score := "ok", 96
	detail := "Public docs build strictly, deploy through GitHub Pages with the canonical installer, and include repeatable GUI screenshot evidence."
	var next []string
	if len(missing) > 0 {
		status = "warn"
		score = 70
		detail = "Public docs are partially wired, but launch publishing still has gaps."
		next = append(next, "Fix missing docs checks: "+strings.Join(missing, ", ")+".")
	}

	return publicDocsReadiness{
		Status:                status,
		Score:                 score,
		SiteURL:               siteURL,
		Checks:                checks,
		Missing:               missing,
		Detail:                detail,
		ScreenshotGeneratedAt: screenshotGeneratedAt,
		ScreenshotAgeHours:    screenshotAgeHours,
		NextActions:           next,
	}
}

func parityDocsPublishing(docs publicDocsReadiness) parityArea {
	if docs.Status == "ok" {
		return parityArea{
			Key:       "docs",
			Label:     "Docs & Launch Guidance",
			Status:    "ok",
			Score:     maxInt(docs.Score, 88),
			Detail:    "Public docs, setup guides, troubleshooting, verified GitHub Pages publishing, and GUI screenshot evidence are wired.",
			Next:      "Run `make docs-screenshots` before launch tags and keep real-world troubleshooting signatures current.",
			Benchmark: "Commercial launch",
			Href:      "https://vmodekurti.github.io/soulacy",
		}
	}
	next := "Run `make docs-build`, fix docs deploy checks, and publish the GitHub Pages site before launch."
	if len(docs.NextActions) > 0 {
		next = docs.NextActions[0]
	}
	return parityArea{
		Key:       "docs",
		Label:     "Docs & Launch Guidance",
		Status:    "warn",
		Score:     docs.Score,
		Detail:    docs.Detail,
		Next:      next,
		Benchmark: "Commercial launch",
		Href:      "#dashboard",
	}
}

func findRepoRootForDocs() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if fileExists(filepath.Join(wd, "mkdocs.yml")) && fileExists(filepath.Join(wd, ".github", "workflows", "docs.yml")) {
			return wd, true
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", false
		}
		wd = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func docsSetting(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, key)), `"'`)
		}
	}
	return ""
}

func screenshotManifestFreshness(manifest string, maxAge time.Duration) (string, int, bool) {
	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Routes      []any  `json:"routes"`
	}
	if err := json.Unmarshal([]byte(manifest), &payload); err != nil || payload.GeneratedAt == "" || len(payload.Routes) == 0 {
		return "", 0, false
	}
	generated, err := time.Parse(time.RFC3339, payload.GeneratedAt)
	if err != nil {
		return payload.GeneratedAt, 0, false
	}
	age := time.Since(generated)
	if age < 0 {
		age = 0
	}
	return payload.GeneratedAt, int(age.Hours()), age <= maxAge
}
