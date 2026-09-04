// Package webui embeds the compiled Svelte GUI so the gateway can serve it
// as static files without requiring a separate web server process.
//
// Workflow:
//
//	make gui        # builds Svelte → writes files to internal/webui/dist/
//	make build      # compiles Go, embedding dist/ into the binary
//	make all        # both of the above in order
//
// When the GUI has not been built, the placeholder index.html is served instead.
package webui

import "embed"

// FS contains the compiled GUI static files (JS, CSS, HTML, assets).
// Populated by 'make gui'. The all: prefix includes hidden files and empty dirs.
//
//go:embed all:dist
var FS embed.FS
