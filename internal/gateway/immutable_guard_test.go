// immutable_guard_test.go — every Fiber app in this package, production or
// test, must set Immutable.
//
// WHAT GOES WRONG WITHOUT IT is not a crash. Fiber returns c.Params/c.Query
// as views into fasthttp's REUSABLE request buffer, and this package retains
// those strings in long-lived structures: agent IDs become loader map keys,
// scheduler entries, and cron closures. A later request refills the buffer and
// silently rewrites them in place. server.go's own config comment records the
// production symptom — an agent id "daily-briefing" becoming "e/statusiefing".
//
// It bit the TESTS, which is why this guard exists rather than a comment.
// Every workspace-scoped test helper built its app with
// fiber.New(fiber.Config{DisableStartupMessage: true}) — the same shape as
// production minus the one setting production needs. Under that config a test
// that created agent "canvas", updated it, then posted to /studio/save found
// the stored agent's id had become "saveas": six bytes of a later request's
// path, sitting under a string the loader was still using as a key.
//
// The failure that produced was a save being told 422 (contract blocked)
// instead of 409 (stale) — a test failing with a plausible, entirely wrong
// diagnosis, about a guard that was working correctly. That is the specific
// cost of a test harness that differs from production in one setting: the test
// does not report the difference, it reports a bug in the code under test.
package gateway

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fiberConstruction matches a fiber.New(...) call and captures its arguments,
// across line breaks. Deliberately textual rather than an AST walk: the
// question is about one literal field in one call, and a parser here would be
// more machinery than the question needs.
var fiberConstruction = regexp.MustCompile(`(?s)fiber\.New\((.*?)\)\n`)

func TestEveryFiberAppInThisPackageRetainsItsStringsSafely(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || name == "immutable_guard_test.go" {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range fiberConstruction.FindAllStringSubmatch(string(source), -1) {
			args := match[1]
			if strings.TrimSpace(args) == "" {
				// A construction with no config takes the zero value, which
				// has Immutable false.
				t.Errorf("%s constructs a Fiber app with no config, so Immutable is false — "+
					"retained c.Params strings will be rewritten by a later request", name)
				continue
			}
			checked++
			if !strings.Contains(args, "Immutable:") || strings.Contains(args, "Immutable: false") {
				t.Errorf("%s constructs a Fiber app without Immutable: true — "+
					"this package retains c.Params strings as map keys, and fasthttp reuses "+
					"the buffer they point into", name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Fiber apps found; this guard is matching nothing and would pass for any code")
	}
}
