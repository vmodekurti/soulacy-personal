// registry.go — `soulacy registry` subcommands: the reference E19 package
// registry server (serve) and signing-key generation (keygen).
//
//	soulacy registry keygen --out ~/.soulacy/registry-signing.key
//	soulacy registry serve --dir ./packages --addr 127.0.0.1:18790 \
//	    --signing-key-file ~/.soulacy/registry-signing.key
//
// Key file format: hex-encoded ed25519 seed (32 bytes) or full private key
// (64 bytes). keygen prints the hex PUBLIC key — that's what consumers put
// in their registries: entry's signing_key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/registryserver"
)

func runRegistry(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: soulacy registry <serve|keygen> [flags]")
	}
	switch args[0] {
	case "serve":
		return runRegistryServe(args[1:])
	case "keygen":
		return runRegistryKeygen(args[1:])
	default:
		return fmt.Errorf("unknown registry subcommand %q (want serve or keygen)", args[0])
	}
}

func runRegistryServe(args []string) error {
	fs := flag.NewFlagSet("registry serve", flag.ContinueOnError)
	dir := fs.String("dir", "packages", "directory of <slug>-<version>.tar.gz package archives")
	addr := fs.String("addr", "127.0.0.1:18790", "listen address")
	keyFile := fs.String("signing-key-file", "", "hex ed25519 private key file; when set, every package is signed")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var priv ed25519.PrivateKey
	if *keyFile != "" {
		k, err := loadSigningKey(*keyFile)
		if err != nil {
			return err
		}
		priv = k
	}

	srv, err := registryserver.New(*dir, priv)
	if err != nil {
		return err
	}
	fmt.Printf("soulacy registry: serving %d package(s) from %s on http://%s\n", srv.Count(), *dir, *addr)
	if priv != nil {
		pub := priv.Public().(ed25519.PublicKey)
		fmt.Printf("  signing: ed25519 enabled — consumers set signing_key: %s\n", hex.EncodeToString(pub))
	} else {
		fmt.Println("  signing: DISABLED — run with --signing-key-file to sign packages")
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return httpSrv.ListenAndServe()
}

func runRegistryKeygen(args []string) error {
	fs := flag.NewFlagSet("registry keygen", flag.ContinueOnError)
	out := fs.String("out", "registry-signing.key", "output path for the hex private key (0600)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s already exists — refusing to overwrite a signing key", *out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("✓ private key written to %s (keep it secret)\n", *out)
	fmt.Printf("public key (consumers' registries: signing_key):\n%s\n", hex.EncodeToString(pub))
	return nil
}

// loadSigningKey reads a hex ed25519 seed (32 bytes) or full private key
// (64 bytes) from path.
func loadSigningKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("signing key %s: not hex: %w", path, err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, fmt.Errorf("signing key %s: want %d-byte seed or %d-byte private key, got %d bytes",
			path, ed25519.SeedSize, ed25519.PrivateKeySize, len(raw))
	}
}
