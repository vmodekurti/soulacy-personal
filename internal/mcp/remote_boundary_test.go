package mcp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/netguard"
	"go.uber.org/zap"
)

func TestPublicOnlyHTTPUsesGuardedTransport(t *testing.T) {
	tx := newHTTP(ServerConfig{URL: "https://1.1.1.1/mcp", PublicOnly: true}, nil)
	if _, ok := tx.client.Transport.(*netguard.GuardedTransport); !ok {
		t.Fatalf("transport = %T, want *netguard.GuardedTransport", tx.client.Transport)
	}
	plain := newHTTP(ServerConfig{URL: "http://127.0.0.1/mcp"}, nil)
	if plain.client.Transport != nil && plain.client.Transport != http.DefaultTransport {
		t.Fatalf("ordinary configured transport unexpectedly changed: %T", plain.client.Transport)
	}
}

func TestManagedStdioRechecksSymlinkEscapeAtProcessStart(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "server")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err := newStdio(ServerConfig{Command: link, ManagedRoot: root}, nil, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "escaped managed root") {
		t.Fatalf("error = %v, want managed-root refusal", err)
	}
}
