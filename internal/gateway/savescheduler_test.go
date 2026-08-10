package gateway

// Two Save buttons write the same agent file. They must agree about the
// scheduler.
//
// Studio has two save paths: the wizard's Save (POST /studio/save) and the Code
// view's Save (POST /studio/save-yaml). Both call loader.Upsert on the same
// SOUL.yaml. Only the Code view told the scheduler about it.
//
// So editing a live scheduled workflow in the wizard wrote enabled: false to
// disk and left the cron entry registered, pointing at an agent that read as OFF
// in the list, in the file, and over the API — and that kept firing. Editing the
// same workflow in the Code view behaved correctly. Same user, same agent, two
// buttons, opposite outcomes.
//
// The engine now refuses a disabled agent at fire time (internal/scheduler), so
// the damage is contained wherever this rule is forgotten. This test guards the
// other half: that the cron table itself is kept honest by every writer, and
// that these two handlers do not drift apart again.
//
// It reads the handler source rather than a hand-kept list, because a list is
// the thing that goes stale — that is how the divergence arose in the first
// place.

import (
	"os"
	"strings"
	"testing"
)

// saveHandlers are the handlers that persist a Studio workflow to disk.
var saveHandlers = []string{"handleStudioSave", "handleStudioSaveYAML"}

func TestBothStudioSavePathsSyncTheScheduler(t *testing.T) {
	src, err := os.ReadFile("studio.go")
	if err != nil {
		t.Fatalf("read studio.go: %v", err)
	}
	for _, name := range saveHandlers {
		body, ok := funcBodySource(string(src), name)
		if !ok {
			t.Errorf("%s no longer exists in studio.go — if it was renamed, rename it here too", name)
			continue
		}
		if !strings.Contains(body, "loader.Upsert(") {
			t.Errorf("%s no longer writes the agent — this test is guarding the wrong function", name)
			continue
		}
		if !strings.Contains(body, "scheduler.DeregisterAgent(") {
			t.Errorf("%s writes the agent file but never drops its cron entry — "+
				"a schedule edited here keeps firing on the old expression", name)
		}
		if !strings.Contains(body, "scheduler.RegisterAgent(") {
			t.Errorf("%s writes the agent file but never re-registers it — "+
				"a schedule saved here will not run until the gateway restarts", name)
		}
	}
}

// funcBodySource returns the source of the named top-level func, from its
// signature to the closing brace in column 1. Crude on purpose: gofmt
// guarantees the shape, and a real parser would be more machinery than the
// question needs.
func funcBodySource(src, name string) (string, bool) {
	marker := ") " + name + "("
	i := strings.Index(src, marker)
	if i < 0 {
		if i = strings.Index(src, "func "+name+"("); i < 0 {
			return "", false
		}
	} else {
		i = strings.LastIndex(src[:i], "\nfunc ")
		if i < 0 {
			return "", false
		}
	}
	rest := src[i:]
	if end := strings.Index(rest[1:], "\n}\n"); end >= 0 {
		return rest[:end+4], true
	}
	return rest, true
}
