package plugins

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/soulacy/soulacy/internal/credentials"
	"github.com/soulacy/soulacy/pkg/plugin"
)

func testVault(t *testing.T) *credentials.SQLiteVault {
	t.Helper()
	kms, err := credentials.NewPassthroughKMS([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := credentials.NewSQLiteVault(filepath.Join(t.TempDir(), "vault.db"), kms)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func setSecret(t *testing.T, v credentials.Vault, pluginID, key, val string) {
	t.Helper()
	if err := v.Set(context.Background(), PluginVaultNamespace(pluginID), key, []byte(val)); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// ValidateCredentialRefs
// ---------------------------------------------------------------------------

func TestValidateCredentialRefs_Valid(t *testing.T) {
	refs := []plugin.CredentialRef{
		{Key: "MATRIX_TOKEN", From: "matrix-suite/token"},
		{Key: "MATRIX_USER", From: "matrix-suite/user"},
	}
	if err := ValidateCredentialRefs("matrix-suite", refs); err != nil {
		t.Fatalf("ValidateCredentialRefs: %v", err)
	}
}

func TestValidateCredentialRefs_ForeignNamespace_Error(t *testing.T) {
	refs := []plugin.CredentialRef{{Key: "X", From: "other-plugin/token"}}
	if err := ValidateCredentialRefs("matrix-suite", refs); err == nil {
		t.Fatal("foreign namespace accepted, want error")
	}
}

func TestValidateCredentialRefs_BadPath_Error(t *testing.T) {
	for _, from := range []string{"", "token", "matrix-suite/", "/token", "matrix-suite/a/b"} {
		refs := []plugin.CredentialRef{{Key: "X_Y", From: from}}
		if err := ValidateCredentialRefs("matrix-suite", refs); err == nil {
			t.Errorf("from=%q accepted, want error", from)
		}
	}
}

func TestValidateCredentialRefs_BadEnvName_Error(t *testing.T) {
	for _, key := range []string{"", "lower", "1ABC", "WITH-DASH", "WITH SPACE", "=X"} {
		refs := []plugin.CredentialRef{{Key: key, From: "p/x"}}
		if err := ValidateCredentialRefs("p", refs); err == nil {
			t.Errorf("key=%q accepted, want error", key)
		}
	}
}

func TestValidateCredentialRefs_DuplicateKey_Error(t *testing.T) {
	refs := []plugin.CredentialRef{
		{Key: "TOKEN", From: "p/a"},
		{Key: "TOKEN", From: "p/b"},
	}
	if err := ValidateCredentialRefs("p", refs); err == nil {
		t.Fatal("duplicate env key accepted, want error")
	}
}

// ---------------------------------------------------------------------------
// Delegator.Env
// ---------------------------------------------------------------------------

func TestDelegatorEnv_InjectsOnlyDeclared(t *testing.T) {
	v := testVault(t)
	setSecret(t, v, "matrix-suite", "token", "s3cret-token")
	setSecret(t, v, "matrix-suite", "undeclared", "must-not-leak")
	setSecret(t, v, "other-plugin", "token", "foreign-secret")

	d := NewDelegator(v, zap.NewNop())
	env, err := d.Env(context.Background(),
		"matrix-suite",
		[]plugin.CredentialRef{{Key: "MATRIX_TOKEN", From: "matrix-suite/token"}})
	if err != nil {
		t.Fatalf("Env: %v", err)
	}

	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "MATRIX_TOKEN=s3cret-token") {
		t.Fatalf("declared secret missing from env:\n%s", joined)
	}
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "foreign-secret") {
		t.Fatalf("undeclared secret leaked into env:\n%s", joined)
	}
	// Base environment stays minimal: PATH must be present for exec to work.
	if !strings.Contains(joined, "PATH=") {
		t.Fatalf("PATH missing from base env:\n%s", joined)
	}
}

func TestDelegatorEnv_HostEnvNotInherited(t *testing.T) {
	t.Setenv("SUPER_SECRET_HOST_VAR", "host-only-value")
	v := testVault(t)
	setSecret(t, v, "p", "x", "v")
	d := NewDelegator(v, zap.NewNop())
	env, err := d.Env(context.Background(), "p",
		[]plugin.CredentialRef{{Key: "X_TOKEN", From: "p/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(env, "\n"), "SUPER_SECRET_HOST_VAR") {
		t.Fatal("host environment variable leaked into sidecar env")
	}
}

func TestDelegatorEnv_MissingSecret_Error(t *testing.T) {
	v := testVault(t)
	d := NewDelegator(v, zap.NewNop())
	_, err := d.Env(context.Background(), "p",
		[]plugin.CredentialRef{{Key: "X_TOKEN", From: "p/missing"}})
	if err == nil {
		t.Fatal("missing secret resolved, want error")
	}
	if strings.Contains(err.Error(), "missing-value") {
		t.Fatal("error must not contain secret values")
	}
}

func TestDelegatorEnv_InvalidRefs_Error(t *testing.T) {
	v := testVault(t)
	d := NewDelegator(v, zap.NewNop())
	_, err := d.Env(context.Background(), "p",
		[]plugin.CredentialRef{{Key: "X", From: "other/x"}})
	if err == nil {
		t.Fatal("invalid ref resolved, want error")
	}
}

func TestDelegatorEnv_NeverLogsSecretValues(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	v := testVault(t)
	setSecret(t, v, "p", "tok", "the-secret-value")
	d := NewDelegator(v, zap.New(core))
	if _, err := d.Env(context.Background(), "p",
		[]plugin.CredentialRef{{Key: "TOK", From: "p/tok"}}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range logs.All() {
		line := entry.Message + " " + fmt.Sprint(entry.ContextMap())
		if strings.Contains(line, "the-secret-value") {
			t.Fatalf("secret value logged: %s", line)
		}
	}
}

// ---------------------------------------------------------------------------
// Watcher: restart on rotation
// ---------------------------------------------------------------------------

func TestWatchCredentials_FiresOnChange(t *testing.T) {
	v := testVault(t)
	setSecret(t, v, "p", "tok", "v1")
	refs := []plugin.CredentialRef{{Key: "TOK", From: "p/tok"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 4)
	WatchCredentials(ctx, v, "p", refs, 10*time.Millisecond, zap.NewNop(),
		func() { changed <- struct{}{} })

	// No change yet → no fire.
	select {
	case <-changed:
		t.Fatal("watcher fired without a change")
	case <-time.After(60 * time.Millisecond):
	}

	setSecret(t, v, "p", "tok", "v2") // value change (Set/rotation both alter it)

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not fire after credential change")
	}
}

func TestWatchCredentials_RotateFires(t *testing.T) {
	v := testVault(t)
	setSecret(t, v, "p", "tok", "v1")
	refs := []plugin.CredentialRef{{Key: "TOK", From: "p/tok"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 4)
	WatchCredentials(ctx, v, "p", refs, 10*time.Millisecond, zap.NewNop(),
		func() { changed <- struct{}{} })

	time.Sleep(30 * time.Millisecond) // let the watcher take its baseline
	// Set a NEW value (Rotate re-encrypts the same value; delegation watches
	// the decrypted value, so simulate rotation-to-new-value via Set).
	setSecret(t, v, "p", "tok", "v2-rotated")

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not fire after rotation")
	}
}

func TestWatchCredentials_StopsOnCancel(t *testing.T) {
	v := testVault(t)
	setSecret(t, v, "p", "tok", "v1")
	refs := []plugin.CredentialRef{{Key: "TOK", From: "p/tok"}}

	ctx, cancel := context.WithCancel(context.Background())
	changed := make(chan struct{}, 4)
	WatchCredentials(ctx, v, "p", refs, 10*time.Millisecond, zap.NewNop(),
		func() { changed <- struct{}{} })
	cancel()
	time.Sleep(30 * time.Millisecond)
	setSecret(t, v, "p", "tok", "v2")
	select {
	case <-changed:
		t.Fatal("watcher fired after cancel")
	case <-time.After(100 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// Loader integration
// ---------------------------------------------------------------------------

func TestLoader_InvalidCredentialRefs_PluginSkipped(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "bad-creds", `
id: bad-creds
credentials:
  - key: TOKEN
    from: other-namespace/token
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 0 {
		t.Fatalf("Count = %d, want 0 (foreign-namespace credential must skip plugin)", l.Count())
	}
}

func TestLoader_ValidCredentialRefs_Loaded(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "matrix-suite", `
id: matrix-suite
credentials:
  - key: MATRIX_TOKEN
    from: matrix-suite/token
`)
	l := New([]string{root}, zap.NewNop())
	if l.Count() != 1 {
		t.Fatalf("Count = %d, want 1", l.Count())
	}
	m := l.All()[0].Manifest
	if len(m.Credentials) != 1 || m.Credentials[0].Key != "MATRIX_TOKEN" {
		t.Fatalf("Credentials = %+v", m.Credentials)
	}
}
