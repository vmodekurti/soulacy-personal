package runtime

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEngineFilesStayBelowDecompositionLimit(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Dir(here)
	files, err := filepath.Glob(filepath.Join(dir, "engine*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := 0
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			lines++
		}
		_ = f.Close()
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if lines >= 1500 {
			t.Errorf("%s has %d lines; engine responsibility files must stay below 1500", filepath.Base(path), lines)
		}
	}
}

func TestEverySystemToolHasExactlyOneSecurityClassification(t *testing.T) {
	e := newMinimalEngine(t)
	seen := make(map[string]int)
	for _, tool := range e.buildSystemTools() {
		seen[tool.Name]++
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("tool %q is defined %d times", name, count)
		}
		if _, ok := toolSecurityClasses[name]; !ok {
			t.Errorf("tool %q has no security classification", name)
		}
	}
	for name := range toolSecurityClasses {
		if seen[name] != 1 {
			t.Errorf("classification for unknown or duplicate tool %q", name)
		}
	}
}
