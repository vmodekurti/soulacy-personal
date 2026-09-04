// Command soulacybuild is the standalone wrapper around the flavored-binary
// build tool (Story E12). The same logic ships inside the gateway binary as
// `soulacy build` (Story 19b) — this wrapper remains for environments that
// build from source without a soulacy binary on hand.
//
//	go run ./scripts/soulacybuild --with github.com/acme/soulacy-matrix@v1.2.0 -o bin/soulacy-matrix
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/soulacy/soulacy/internal/buildtool"
)

// withFlags collects repeated --with values.
type withFlags []string

func (w *withFlags) String() string     { return strings.Join(*w, ",") }
func (w *withFlags) Set(v string) error { *w = append(*w, v); return nil }

func main() {
	var with withFlags
	out := flag.String("o", "bin/soulacy", "output binary path")
	skipVerify := flag.Bool("skip-verify", false, "skip the conformance/registry test gates before building")
	keep := flag.Bool("keep", true, "keep the generated builtins_extra.go after the build (required for rebuilds)")
	flag.Var(&with, "with", "extra driver module to compile in, module[@version] (repeatable)")
	flag.Parse()

	if err := buildtool.Run(with, *out, *skipVerify, *keep); err != nil {
		fmt.Fprintf(os.Stderr, "soulacybuild: %v\n", err)
		os.Exit(1)
	}
}
