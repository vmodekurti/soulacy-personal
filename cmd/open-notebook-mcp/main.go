// open-notebook-mcp is an optional standalone MCP adapter for Open Notebook.
// It is deliberately separate from the Soulacy gateway deployment and can be
// installed, upgraded, and removed on its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/opennotebook"
	"github.com/soulacy/soulacy/internal/opennotebookmcp"
)

func main() {
	baseURL := flag.String("base-url", envDefault("OPEN_NOTEBOOK_URL", opennotebook.DefaultBaseURL), "local Open Notebook API URL")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *showVersion {
		fmt.Println(config.Version)
		return
	}
	srv, err := opennotebookmcp.New(*baseURL, os.Getenv("OPEN_NOTEBOOK_TOKEN"), config.Version, nil)
	if err == nil {
		err = srv.Serve(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "open-notebook-mcp: %v\n", err)
		os.Exit(1)
	}
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
