package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestEngineTimeoutHierarchy(t *testing.T) {
	e := &Engine{}
	e.SetTimeoutHierarchy(2*time.Minute, 3*time.Minute, 4*time.Minute, 15*time.Minute)
	if e.effectiveLLMTimeout() != 3*time.Minute || e.effectiveStepTimeout() != 4*time.Minute || e.effectiveRunTimeout() != 15*time.Minute {
		t.Fatalf("timeout hierarchy not retained: llm=%s step=%s run=%s", e.effectiveLLMTimeout(), e.effectiveStepTimeout(), e.effectiveRunTimeout())
	}
}

// This guard makes adding another literal deadline in request-path code a
// deliberate review decision. Existing literals are legacy debt; the central
// hierarchy must be used for all new LLM, step, run, tool, and HTTP deadlines.
func TestNoNewHardcodedRequestTimeouts(t *testing.T) {
	repo := filepath.Clean(filepath.Join("..", ".."))
	re := regexp.MustCompile(`context\.WithTimeout\([^\n]*[0-9]+\s*\*\s*time\.(Second|Minute|Millisecond)`)
	count := 0
	for _, dir := range []string{"internal/runtime", "internal/reasoning", "internal/gateway"} {
		err := filepath.WalkDir(filepath.Join(repo, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".go" || filepath.Ext(path[:len(path)-3]) == ".test" {
				return err
			}
			if len(path) >= 8 && path[len(path)-8:] == "_test.go" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			count += len(re.FindAll(data, -1))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	const legacyBaseline = 23
	if count > legacyBaseline {
		t.Fatalf("found %d hardcoded request timeouts, baseline is %d; use the configured timeout hierarchy", count, legacyBaseline)
	}
}
