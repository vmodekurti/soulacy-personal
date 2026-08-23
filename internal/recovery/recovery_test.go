package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/soulacy/soulacy/internal/artifactstore"
	"github.com/soulacy/soulacy/internal/credentials"
)

func TestProductionRecoveryTargetsAreComplete(t *testing.T) {
	if err := ValidateTargets(ProductionTargets()); err != nil {
		t.Fatal(err)
	}
}

// TestProductionLikeRestoreDrill is mandatory in the release workflow. Local
// unit runs skip when no live PostgreSQL DSN is supplied; CI treats a skip as
// failure by checking the emitted evidence marker.
func TestProductionLikeRestoreDrill(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("SOULACY_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("SOULACY_TEST_POSTGRES_DSN is required for the production restore drill")
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Fatal("pg_dump is required for the production restore drill")
	}
	if _, err := exec.LookPath("pg_restore"); err != nil {
		t.Fatal("pg_restore is required for the production restore drill")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	schema := "recovery_" + strings.ToLower(strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", ""))
	quoted := `"` + schema + `"`
	defer db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoted+` CASCADE`) //nolint:errcheck
	for _, statement := range []string{
		`CREATE SCHEMA ` + quoted,
		`CREATE TABLE ` + quoted + `.schema_versions(component text primary key, version integer not null)`,
		`CREATE TABLE ` + quoted + `.tenant_records(workspace_id text not null, id text not null, body text not null, primary key(workspace_id,id))`,
		`CREATE TABLE ` + quoted + `.artifact_refs(workspace_id text not null, object_key text not null, vector_id text not null, primary key(workspace_id,object_key))`,
		`INSERT INTO ` + quoted + `.schema_versions VALUES ('recovery-fixture',7)`,
		`INSERT INTO ` + quoted + `.tenant_records VALUES ('ws_restore','rec_1','relational-payload'),('ws_neighbour','rec_2','neighbour-payload')`,
		`INSERT INTO ` + quoted + `.artifact_refs VALUES ('ws_restore','workspaces/ws_restore/runs/run_1/result.txt','vec_restore_1')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	dump := filepath.Join(t.TempDir(), "postgres.dump")
	if output, err := exec.CommandContext(ctx, "pg_dump", "--dbname", dsn, "--format=custom", "--no-owner", "--no-privileges", "--schema", schema, "--file", dump).CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %v: %s", err, output)
	}
	if _, err := db.ExecContext(ctx, `DROP SCHEMA `+quoted+` CASCADE`); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(ctx, "pg_restore", "--dbname", dsn, "--no-owner", "--no-privileges", dump).CombinedOutput(); err != nil {
		t.Fatalf("pg_restore: %v: %s", err, output)
	}
	var body, objectKey, vectorID string
	var version int
	if err := db.QueryRowContext(ctx, `SELECT body FROM `+quoted+`.tenant_records WHERE workspace_id='ws_restore' AND id='rec_1'`).Scan(&body); err != nil || body != "relational-payload" {
		t.Fatalf("restored relational row = %q, %v", body, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT object_key,vector_id FROM `+quoted+`.artifact_refs WHERE workspace_id='ws_restore'`).Scan(&objectKey, &vectorID); err != nil || objectKey == "" || vectorID != "vec_restore_1" {
		t.Fatalf("restored object/vector references = %q/%q, %v", objectKey, vectorID, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM `+quoted+`.schema_versions WHERE component='recovery-fixture'`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("restored schema version = %d, %v", version, err)
	}

	verifyObjectRestore(t, ctx, objectKey)
	verifyEncryptedVaultRestore(t, ctx)
	t.Log("SOULACY_RECOVERY_DRILL=passed relational=ok objects=ok vector_references=ok encrypted_secrets=ok schema_versions=ok")
}

func verifyObjectRestore(t *testing.T, ctx context.Context, key string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "shared-objects")
	store, err := artifactstore.OpenStore(ctx, "file://"+root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload := []byte("artifact-body-for-restore")
	if err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	object, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := io.ReadAll(object.Body)
	object.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeletePrefix(ctx, artifactstore.WorkspacePrefix("ws_restore")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, key, bytes.NewReader(backup), int64(len(backup)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(restored.Body)
	restored.Body.Close()
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatal("restored object checksum differs from source")
	}
}

func verifyEncryptedVaultRestore(t *testing.T, ctx context.Context) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "vault.db")
	backup := filepath.Join(dir, "vault.backup")
	kms, err := credentials.NewLocalKMSWithStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewSQLiteVault(source, kms)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(ctx, "ws_restore", "agent", "provider_key", []byte("secret-after-restore")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := credentials.NewSQLiteVault(source, kms)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.Get(ctx, "ws_restore", "agent", "provider_key")
	if err != nil || string(got) != "secret-after-restore" {
		t.Fatalf("restored encrypted secret = %q, %v", got, err)
	}
	if _, err := restored.Get(ctx, "ws_neighbour", "agent", "provider_key"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("restored vault crossed tenants: %v", err)
	}
}
