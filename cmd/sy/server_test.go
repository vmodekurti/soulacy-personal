package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGatewayBinaryUsesExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soulacy-test")
	if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveGatewayBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolved binary = %q, want %q", got, path)
	}
}

func TestServerStartAcceptsGatewayBinary(t *testing.T) {
	server := buildServerCmd()
	start, _, err := server.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	if start.Flags().Lookup("binary") == nil {
		t.Fatal("server start does not expose --binary")
	}
}
