// Package buildinfo answers "which build of Soulacy is running right now?"
//
// config.Version (git describe) is not enough on its own: two builds of
// different uncommitted changes share "v1.2.0-45-gab12cd3-dirty", and a binary
// built without ldflags just says "dev". After a redeploy the operator could
// not tell whether the new code was live.
//
// The identity here is a hash of the running executable. Any code change yields
// a new BuildID; rebuilding identical source (Go builds are reproducible)
// keeps it. The BuildID is rendered as a short, memorable codename such as
// "amber-falcon" so it can be compared at a glance. A per-process InstanceID
// separates "same build, restarted" from "new build deployed".
package buildinfo

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/config"
)

// Info describes the running gateway process. Field names are the JSON
// contract read by the portal (gui/src/lib/buildwatch.js) and `sy version`.
type Info struct {
	// Label is the human-friendly codename derived from BuildID.
	Label string `json:"label"`
	// BuildID is the first 12 hex characters of the executable's SHA-256.
	BuildID string `json:"build_id"`
	// Version is config.Version (git describe at build time, or "dev").
	Version string `json:"version"`
	// Commit is the VCS revision Go embedded at build time, if any.
	Commit string `json:"commit,omitempty"`
	// Modified reports uncommitted changes in the build's working tree.
	Modified bool `json:"modified"`
	// BuiltAt is the executable's modification time, the closest record of
	// when this binary was produced or installed.
	BuiltAt time.Time `json:"built_at,omitempty"`
	// StartedAt is when this process started.
	StartedAt time.Time `json:"started_at"`
	// InstanceID changes on every process start, even for the same build.
	InstanceID string `json:"instance_id"`
	// GoVersion is the toolchain that compiled the binary.
	GoVersion string `json:"go_version"`
}

var (
	startedAt  = time.Now().UTC()
	instanceID = randomHex(4)

	once    sync.Once
	current Info
)

// Current returns the running build's identity. The executable is hashed on
// the first call only.
func Current() Info {
	once.Do(func() { current = compute() })
	return current
}

func compute() Info {
	info := Info{
		Version:    strings.TrimSpace(config.Version),
		StartedAt:  startedAt,
		InstanceID: instanceID,
		GoVersion:  runtime.Version(),
	}
	var vcsTime string
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Commit = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			case "vcs.time":
				vcsTime = s.Value
			}
		}
	}
	digest, builtAt := hashExecutable()
	if digest == "" {
		// The executable could not be read (unusual sandboxing). Fall back to
		// what the binary says about itself; still stable per build.
		sum := sha256.Sum256([]byte(info.Version + "|" + info.Commit + "|" + vcsTime + "|" + boolString(info.Modified)))
		digest = hex.EncodeToString(sum[:])
	}
	info.BuildID = digest[:12]
	info.Label = Codename(info.BuildID)
	info.BuiltAt = builtAt
	return info
}

func hashExecutable() (string, time.Time) {
	path, err := os.Executable()
	if err != nil {
		return "", time.Time{}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}
	}
	defer f.Close()
	var builtAt time.Time
	if st, err := f.Stat(); err == nil {
		builtAt = st.ModTime().UTC()
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", builtAt
	}
	return hex.EncodeToString(h.Sum(nil)), builtAt
}

// Codename maps a hex build ID to a stable "adjective-noun" label. The same ID
// always yields the same name. 64×64 combinations make consecutive builds
// nearly always look different; BuildID remains the exact identity.
func Codename(buildID string) string {
	b, err := hex.DecodeString(padHex(buildID))
	if err != nil || len(b) < 2 {
		return "unknown-build"
	}
	return adjectives[int(b[0])%len(adjectives)] + "-" + nouns[int(b[1])%len(nouns)]
}

func padHex(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s)%2 == 1 {
		s = s[:len(s)-1]
	}
	return s
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("150405.000"))[:n])
	}
	return hex.EncodeToString(b)
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// Word lists are short, distinct, and easy to say aloud. Both have exactly 64
// entries; changing either renames every existing build, so append-only edits
// are not possible — replace a word only if it is genuinely confusing.
var adjectives = [64]string{
	"amber", "azure", "bold", "brave", "brisk", "bright", "calm", "candid",
	"cedar", "clever", "coral", "cosmic", "crisp", "dapper", "daring", "dusky",
	"eager", "early", "ember", "fabled", "fleet", "frosty", "gentle", "gilded",
	"glad", "golden", "hardy", "hazel", "humble", "indigo", "ivory", "jade",
	"jolly", "keen", "lively", "lucid", "lunar", "maple", "mellow", "merry",
	"misty", "noble", "olive", "opal", "plucky", "polar", "proud", "quiet",
	"rapid", "rosy", "ruby", "rustic", "sage", "silver", "sly", "snowy",
	"solar", "steady", "sunny", "swift", "tidal", "velvet", "vivid", "witty",
}

var nouns = [64]string{
	"badger", "beacon", "bison", "breeze", "canyon", "cypress", "comet", "condor",
	"coyote", "crane", "delta", "dolphin", "eagle", "falcon", "fern", "finch",
	"fjord", "forest", "fox", "gecko", "glacier", "harbor", "hawk", "heron",
	"ibis", "island", "jaguar", "kestrel", "koala", "lagoon", "lantern", "lark",
	"lemur", "lynx", "magpie", "marlin", "meadow", "meteor", "moose", "nebula",
	"oasis", "orca", "osprey", "otter", "owl", "panda", "pebble", "pelican",
	"puffin", "quail", "raven", "reef", "river", "robin", "sparrow", "summit",
	"tiger", "tundra", "valley", "walrus", "willow", "wolf", "yak", "zephyr",
}
