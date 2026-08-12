package gateway

import (
	"os"
	"strings"
	"testing"
)

// readGatewaySource reads a file from this package for rules that are about
// where a guard is INSTALLED rather than what a function computes. A route
// table is exactly that: the middleware on the line is the enforcement.
func readGatewaySource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func findLine(t *testing.T, src, needle string) string {
	t.Helper()
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("%q is gone — this rule now guards nothing", needle)
	return ""
}
