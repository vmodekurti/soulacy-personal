package costs

import (
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

func TestStoreRecordsSchemaVersion(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	v, err := sqlitex.SchemaVersion(s.db, "costs")
	if err != nil || v != 5 {
		t.Errorf("costs schema version = %d err=%v, want 5", v, err)
	}
}
