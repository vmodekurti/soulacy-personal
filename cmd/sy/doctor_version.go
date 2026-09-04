// doctor_version.go — the "workspace version / migration compatibility" doctor
// check (Epic 9). It stamps the binary version into the workspace on first run
// and, on subsequent runs, verifies the installed binary isn't OLDER than the
// version that last wrote the workspace — the case where a downgrade could meet
// a forward-only migrated database. A newer binary is fine (migrations move
// forward); an older binary is flagged so the operator can roll back the
// workspace/config alongside the binary.

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
)

const workspaceVersionFile = ".soulacy-version"

func checkWorkspaceVersion(runtimeDir string) doctorCheck {
	const name = "workspace version"
	binVer := strings.TrimSpace(config.Version)
	path := filepath.Join(runtimeDir, workspaceVersionFile)

	recorded := ""
	if data, err := os.ReadFile(path); err == nil {
		recorded = strings.TrimSpace(string(data))
	}

	// First run for this workspace (or a dev build): stamp and pass.
	if recorded == "" {
		_ = os.WriteFile(path, []byte(binVer+"\n"), 0o644)
		return doctorCheck{Name: name, Status: doctorOK, Detail: "recorded workspace version " + binVer}
	}

	cmp := compareSemver(binVer, recorded)
	switch {
	case cmp < 0:
		// Installed binary is OLDER than what last migrated the workspace.
		return doctorCheck{
			Name:   name,
			Status: doctorWarn,
			Detail: "installed binary " + binVer + " is older than the workspace (" + recorded + ") — a forward-only migration may already have run",
			Remedy: "upgrade back to " + recorded + ", or roll back the workspace with `install.sh --rollback` to match this binary",
		}
	case cmp > 0:
		// Upgrade: record the new version (migrations run forward at open time).
		_ = os.WriteFile(path, []byte(binVer+"\n"), 0o644)
		return doctorCheck{Name: name, Status: doctorOK, Detail: "upgraded workspace " + recorded + " → " + binVer}
	default:
		return doctorCheck{Name: name, Status: doctorOK, Detail: "workspace matches binary (" + binVer + ")"}
	}
}

// compareSemver compares two version strings loosely: it strips a leading "v",
// splits on ".", and compares the numeric components. Non-numeric/dev versions
// (e.g. "dev", "1.2.3-dirty") compare equal so they never raise a false alarm.
func compareSemver(a, b string) int {
	if a == b {
		return 0
	}
	pa, oka := semverParts(a)
	pb, okb := semverParts(b)
	if !oka || !okb {
		return 0 // can't compare (dev build) → treat as equal
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

func semverParts(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop any pre-release/build suffix.
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
