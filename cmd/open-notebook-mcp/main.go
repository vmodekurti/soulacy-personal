// open-notebook-mcp is an optional standalone MCP adapter for Open Notebook.
// It is deliberately separate from the Soulacy gateway deployment and can be
// installed, upgraded, and removed on its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/opennotebook"
	"github.com/soulacy/soulacy/internal/opennotebookmcp"
)

func main() {
	baseURL := flag.String("base-url", envDefault("OPEN_NOTEBOOK_URL", opennotebook.DefaultBaseURL), "local Open Notebook API URL")
	audioBaseURL := flag.String("audio-base-url", envDefault("OPEN_NOTEBOOK_AUDIO_URL", ""), "client-facing podcast audio base URL")
	audioListen := flag.String("audio-listen", envDefault("OPEN_NOTEBOOK_AUDIO_LISTEN", ""), "loopback address for the podcast audio proxy")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *showVersion {
		fmt.Println(config.Version)
		return
	}
	srv, err := opennotebookmcp.NewWithAudioBaseURL(*baseURL, *audioBaseURL, os.Getenv("OPEN_NOTEBOOK_TOKEN"), config.Version, nil)
	var listener net.Listener
	if err == nil && strings.TrimSpace(*audioListen) != "" {
		if strings.TrimSpace(*audioBaseURL) == "" {
			err = fmt.Errorf("--audio-listen requires --audio-base-url")
		} else if err = opennotebookmcp.ValidateAudioListenAddress(*audioListen); err == nil {
			listener, err = net.Listen("tcp", *audioListen)
		}
		if err == nil {
			server := &http.Server{Handler: srv.AudioHandler(), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
					fmt.Fprintf(os.Stderr, "open-notebook-mcp audio proxy: %v\n", serveErr)
				}
			}()
			defer server.Close()
		}
	}
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
