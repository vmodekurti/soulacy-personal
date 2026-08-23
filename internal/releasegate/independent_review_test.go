package releasegate

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type reviewAttestation struct {
	Reviewer       string    `json:"reviewer"`
	ReportURL      string    `json:"report_url"`
	CompletedAt    time.Time `json:"completed_at"`
	CriticalOpen   int       `json:"critical_open"`
	HighOpen       int       `json:"high_open"`
	ReviewedCommit string    `json:"reviewed_commit"`
}

// The ordinary suite skips without an attestation. The release workflow sets
// SOULACY_SECURITY_REVIEW_ATTESTATION and explicitly fails a skip, making the
// human review a real publication gate without hard-coding reviewer identity.
func TestIndependentSecurityReviewAttestation(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("SOULACY_SECURITY_REVIEW_ATTESTATION"))
	if raw == "" {
		t.Skip("SOULACY_SECURITY_REVIEW_ATTESTATION is required by the release workflow")
	}
	var attestation reviewAttestation
	if err := json.Unmarshal([]byte(raw), &attestation); err != nil {
		t.Fatalf("invalid independent-review attestation: %v", err)
	}
	report, err := url.Parse(attestation.ReportURL)
	if err != nil || report.Scheme != "https" || report.Host == "" {
		t.Fatalf("independent review report_url must be an absolute HTTPS URL")
	}
	if strings.TrimSpace(attestation.Reviewer) == "" || strings.TrimSpace(attestation.ReviewedCommit) == "" {
		t.Fatal("independent review must name the reviewer and reviewed commit")
	}
	if attestation.CompletedAt.IsZero() || time.Since(attestation.CompletedAt) > 90*24*time.Hour || time.Until(attestation.CompletedAt) > 5*time.Minute {
		t.Fatalf("independent review is missing, future-dated, or older than 90 days: %v", attestation.CompletedAt)
	}
	if attestation.CriticalOpen != 0 || attestation.HighOpen != 0 {
		t.Fatalf("independent review has unresolved findings: critical=%d high=%d", attestation.CriticalOpen, attestation.HighOpen)
	}
	if expected := strings.TrimSpace(os.Getenv("SOULACY_RELEASE_COMMIT")); expected != "" && attestation.ReviewedCommit != expected {
		t.Fatalf("independent review covers commit %s, release is %s", attestation.ReviewedCommit, expected)
	}
	t.Log("SOULACY_INDEPENDENT_REVIEW_GATE=passed report=" + attestation.ReportURL)
}

func TestProductionReleaseHasNoDeclaredBlockers(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SOULACY_ENFORCE_RELEASE_GATE")) != "1" {
		t.Skip("SOULACY_ENFORCE_RELEASE_GATE=1 is set by the publication workflow")
	}
	if blockers := Blockers(); len(blockers) != 0 {
		t.Fatalf("production release has unresolved gates: %s", strings.Join(blockers, "; "))
	}
	t.Log("SOULACY_RELEASE_BLOCKERS=none")
}
