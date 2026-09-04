package workboard

// E22 adoption: every store records its schema version at boot so future
// MigrateSchema evolutions know where each database stands.

import (
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

func TestStoreRecordsSchemaVersion(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "wb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	v, err := sqlitex.SchemaVersion(s.db, "workboard")
	if err != nil || v != 1 {
		t.Errorf("workboard schema version = %d err=%v, want 1", v, err)
	}
}
