package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/buildinfo"
)

// renderVersionReport formats `sy version`. The gateway's build label is the
// line to compare after a redeploy: it changes whenever the running binary
// changes, unlike the version string, which repeats across dirty builds.
func renderVersionReport(cliVersion, gateway string, healthBody []byte, healthErr error, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Soulacy CLI    %s\n", orUnknown(cliVersion))
	fmt.Fprintf(&b, "Gateway        %s\n", gateway)
	if healthErr != nil {
		fmt.Fprintf(&b, "\nCould not read the running build: %v\n", healthErr)
		return b.String()
	}
	var health struct {
		Build *buildinfo.Info `json:"build"`
	}
	if err := json.Unmarshal(healthBody, &health); err != nil || health.Build == nil || health.Build.Label == "" {
		b.WriteString("\nThe gateway did not report a build label; it predates build labels. Upgrade or restart it.\n")
		return b.String()
	}
	info := health.Build
	fmt.Fprintf(&b, "\nRunning build  %s  (%s)\n", info.Label, info.BuildID)
	fmt.Fprintf(&b, "  Version      %s\n", orUnknown(info.Version))
	if info.Commit != "" {
		commit := info.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if info.Modified {
			commit += " (with uncommitted changes)"
		}
		fmt.Fprintf(&b, "  Commit       %s\n", commit)
	}
	if !info.BuiltAt.IsZero() {
		fmt.Fprintf(&b, "  Built        %s (%s)\n", info.BuiltAt.Local().Format("2006-01-02 15:04"), ago(now, info.BuiltAt))
	}
	fmt.Fprintf(&b, "  Started      %s (%s)\n", info.StartedAt.Local().Format("2006-01-02 15:04"), ago(now, info.StartedAt))
	fmt.Fprintf(&b, "  Instance     %s\n", info.InstanceID)
	return b.String()
}

func ago(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
